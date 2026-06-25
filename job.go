package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// jobEvent is a single Server-Sent Event describing transfer progress or state.
type jobEvent struct {
	Type       string          `json:"type"` // idle | started | progress | finished
	FolderName string          `json:"folderName,omitempty"`
	Total      int             `json:"total"`
	Completed  int             `json:"completed"`
	File       string          `json:"file,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
	FatalError string          `json:"fatalError,omitempty"`
	Errors     []transferErrDTO `json:"errors,omitempty"`
}

type transferErrDTO struct {
	File  string `json:"file"`
	Share string `json:"share"`
	Error string `json:"error"`
}

// transferJob tracks a single in-flight (or just-finished) transfer and fans out
// progress events to any number of SSE subscribers.
type transferJob struct {
	id         string
	folderName string
	startedAt  time.Time

	mu          sync.Mutex
	cancel      context.CancelFunc
	done        bool
	total       int
	completed   int
	currentFile string
	endedAt     time.Time
	fatalErr    error
	errors      []TransferError

	subscribers map[chan jobEvent]struct{}
}

func newTransferJob(folderName string) *transferJob {
	return &transferJob{
		id:          fmt.Sprintf("%d", time.Now().UnixNano()),
		folderName:  folderName,
		startedAt:   time.Now(),
		subscribers: make(map[chan jobEvent]struct{}),
	}
}

func (j *transferJob) setCancel(c context.CancelFunc) {
	j.mu.Lock()
	j.cancel = c
	j.mu.Unlock()
}

func (j *transferJob) requestCancel() {
	j.mu.Lock()
	c := j.cancel
	j.mu.Unlock()
	if c != nil {
		c()
	}
}

func (j *transferJob) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

func (j *transferJob) setTotal(total int) {
	j.mu.Lock()
	j.total = total
	j.mu.Unlock()
}

func (j *transferJob) setProgress(total, completed int, file string) {
	j.mu.Lock()
	j.total = total
	j.completed = completed
	j.currentFile = filepath.Base(file)
	j.mu.Unlock()
}

func (j *transferJob) progress() (total, completed int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.total, j.completed
}

func (j *transferJob) finish(fatal error, errs []TransferError) {
	j.mu.Lock()
	j.done = true
	j.endedAt = time.Now()
	j.fatalErr = fatal
	j.errors = errs
	j.mu.Unlock()

	j.broadcast(j.finishedEvent())

	// Close all subscriber channels so streams terminate cleanly.
	j.mu.Lock()
	for ch := range j.subscribers {
		close(ch)
	}
	j.subscribers = make(map[chan jobEvent]struct{})
	j.mu.Unlock()
}

func (j *transferJob) finishedEvent() jobEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	ev := jobEvent{
		Type:       "finished",
		FolderName: j.folderName,
		Total:      j.total,
		Completed:  j.completed,
		DurationMs: j.endedAt.Sub(j.startedAt).Milliseconds(),
	}
	if j.fatalErr != nil {
		ev.FatalError = j.fatalErr.Error()
	}
	for _, e := range j.errors {
		ev.Errors = append(ev.Errors, transferErrDTO{
			File:  filepath.Base(e.FilePath),
			Share: e.Share,
			Error: e.Error.Error(),
		})
	}
	return ev
}

// snapshot builds the current state as an event for a newly connected client.
func (j *transferJob) snapshot() jobEvent {
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()
	if done {
		return j.finishedEvent()
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	return jobEvent{
		Type:       "progress",
		FolderName: j.folderName,
		Total:      j.total,
		Completed:  j.completed,
		File:       j.currentFile,
	}
}

// broadcast sends an event to every subscriber without blocking on slow clients.
func (j *transferJob) broadcast(ev jobEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for ch := range j.subscribers {
		select {
		case ch <- ev:
		default:
			// Drop for a slow consumer; it will catch up via subsequent events.
		}
	}
}

func (j *transferJob) subscribe() (chan jobEvent, jobEvent) {
	snap := j.snapshot()
	ch := make(chan jobEvent, 64)
	j.mu.Lock()
	// If the job already finished, hand back a closed channel so the stream ends.
	if j.done {
		j.mu.Unlock()
		close(ch)
		return ch, snap
	}
	j.subscribers[ch] = struct{}{}
	j.mu.Unlock()
	return ch, snap
}

func (j *transferJob) unsubscribe(ch chan jobEvent) {
	j.mu.Lock()
	if _, ok := j.subscribers[ch]; ok {
		delete(j.subscribers, ch)
		close(ch)
	}
	j.mu.Unlock()
}
