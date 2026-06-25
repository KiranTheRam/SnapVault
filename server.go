package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webAssets embed.FS

// webServer holds the shared state for the local web UI.
type webServer struct {
	configPath string
	timeout    time.Duration
	workers    int

	mu     sync.Mutex
	config *Config
	job    *transferJob
}

func runWebServer(configPath, addr string, timeout time.Duration, workers int, openBrowser bool) error {
	configData, err := loadConfigRaw(configPath)
	if err != nil {
		// Missing config is fine; the user can add shares in the UI.
		configData = &Config{}
	}

	srv := &webServer{
		configPath: configPath,
		timeout:    timeout,
		workers:    workers,
		config:     configData,
	}

	mux := http.NewServeMux()

	// Static assets (the web/ directory embedded at build time).
	staticFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		return fmt.Errorf("preparing embedded assets: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	mux.HandleFunc("/api/shares", srv.handleShares)
	mux.HandleFunc("/api/shares/test", srv.handleTestShare)
	mux.HandleFunc("/api/shares/delete", srv.handleDeleteShare)
	mux.HandleFunc("/api/mounts", srv.handleMounts)
	mux.HandleFunc("/api/browse", srv.handleBrowse)
	mux.HandleFunc("/api/scan", srv.handleScan)
	mux.HandleFunc("/api/transfer", srv.handleStartTransfer)
	mux.HandleFunc("/api/transfer/cancel", srv.handleCancelTransfer)
	mux.HandleFunc("/api/transfer/events", srv.handleEvents)
	mux.HandleFunc("/api/settings", srv.handleSettings)
	mux.HandleFunc("/api/settings/ntfy/test", srv.handleNtfyTest)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	url := fmt.Sprintf("http://%s/", addr)
	slog.Info("SnapVault web UI listening", "url", url)
	fmt.Printf("\n  📸 SnapVault is running at %s\n  Press Ctrl+C to stop.\n\n", url)

	if openBrowser {
		go openInBrowser(url)
	}

	return httpServer.ListenAndServe()
}

// ---- Shares ----

// shareDTO is the share representation sent to the browser. It never includes a password.
type shareDTO struct {
	Key      string `json:"key"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Share    string `json:"share"`
	BasePath string `json:"basePath"`
	Username string `json:"username"`
	Display  string `json:"display"`
}

func toShareDTO(c SMBConfig) shareDTO {
	port := c.Port
	if port == 0 {
		port = 445
	}
	return shareDTO{
		Key:      smbShareKey(c),
		Host:     c.Host,
		Port:     port,
		Share:    c.Share,
		BasePath: c.BasePath,
		Username: c.Username,
		Display:  formatShareForDisplay(c),
	}
}

func (s *webServer) handleShares(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		dtos := make([]shareDTO, 0, len(s.config.SMBShares))
		for _, c := range s.config.SMBShares {
			dtos = append(dtos, toShareDTO(c))
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, dtos)

	case http.MethodPost:
		// Save a share. The connection is tested before saving.
		var in SMBConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := validateSMBConnection(expandShare(in), 15*time.Second); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("connection failed: %v", err))
			return
		}

		s.mu.Lock()
		s.config.SMBShares = upsertShareConfig(s.config.SMBShares, in)
		saveErr := saveConfig(s.configPath, s.config)
		dto := toShareDTO(in)
		s.mu.Unlock()

		if saveErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("connected but failed to save config: %v", saveErr))
			return
		}
		writeJSON(w, http.StatusOK, dto)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *webServer) handleTestShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in SMBConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateSMBConnection(expandShare(in), 15*time.Second); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "display": formatShareForDisplay(in)})
}

func (s *webServer) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	s.mu.Lock()
	kept := make([]SMBConfig, 0, len(s.config.SMBShares))
	for _, c := range s.config.SMBShares {
		if smbShareKey(c) != body.Key {
			kept = append(kept, c)
		}
	}
	s.config.SMBShares = kept
	saveErr := saveConfig(s.configPath, s.config)
	s.mu.Unlock()

	if saveErr != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", saveErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Settings (ntfy) ----

func (s *webServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		ntfy := s.config.Ntfy
		s.mu.Unlock()
		out := map[string]any{"server": "", "topic": "", "username": "", "hasToken": false, "hasPassword": false}
		if ntfy != nil {
			out["server"] = ntfy.Server
			out["topic"] = ntfy.Topic
			out["username"] = ntfy.Username
			out["hasToken"] = strings.TrimSpace(ntfy.Token) != ""
			out["hasPassword"] = ntfy.Password != ""
		}
		writeJSON(w, http.StatusOK, map[string]any{"ntfy": out})

	case http.MethodPost:
		var body NtfyConfig
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Server = strings.TrimSpace(body.Server)
		body.Topic = strings.TrimSpace(body.Topic)

		s.mu.Lock()
		// Preserve existing secrets when the form leaves them blank, so saving
		// general settings doesn't wipe a stored token/password.
		if prev := s.config.Ntfy; prev != nil {
			if strings.TrimSpace(body.Token) == "" {
				body.Token = prev.Token
			}
			if body.Password == "" {
				body.Password = prev.Password
			}
		}
		if body.Server == "" && body.Topic == "" {
			s.config.Ntfy = nil // clearing disables notifications
		} else {
			s.config.Ntfy = &body
		}
		saveErr := saveConfig(s.configPath, s.config)
		s.mu.Unlock()

		if saveErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", saveErr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *webServer) handleNtfyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body NtfyConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Server) == "" || strings.TrimSpace(body.Topic) == "" {
		writeError(w, http.StatusBadRequest, "server and topic are required")
		return
	}
	// Fall back to stored secrets if the form left them blank (token isn't sent to the browser).
	s.mu.Lock()
	if prev := s.config.Ntfy; prev != nil {
		if strings.TrimSpace(body.Token) == "" {
			body.Token = prev.Token
		}
		if body.Password == "" {
			body.Password = prev.Password
		}
	}
	s.mu.Unlock()

	err := publishNtfy(r.Context(), &body, "SnapVault test", "Notifications are configured correctly.", "wave", "default")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Mounts & scan ----

func (s *webServer) handleMounts(w http.ResponseWriter, r *http.Request) {
	candidates := detectMountCandidates()
	type mountDTO struct {
		Path   string `json:"path"`
		Source string `json:"source"`
		FSType string `json:"fsType"`
		Label  string `json:"label"`
	}
	dtos := make([]mountDTO, 0, len(candidates))
	for _, c := range candidates {
		dtos = append(dtos, mountDTO{Path: c.Path, Source: c.Source, FSType: c.FSType, Label: formatMountCandidate(c)})
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleBrowse opens a native OS folder picker on the machine running the server
// and returns the chosen absolute path. Only meaningful for a local server.
func (s *webServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path, canceled, err := pickFolder()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	if canceled {
		writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

// pickFolder shows a native folder chooser. Returns (path, canceled, error).
func pickFolder() (string, bool, error) {
	if runtime.GOOS != "darwin" {
		return "", false, fmt.Errorf("native folder picker is only available on macOS; type the path instead")
	}
	const script = `POSIX path of (choose folder with prompt "Select the folder of photos to import")`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		// osascript exits 1 with "User canceled. (-128)" when the dialog is dismissed.
		if strings.Contains(strings.ToLower(string(exitStderr(err))), "cancel") {
			return "", true, nil
		}
		return "", true, nil // treat any picker failure as a cancel rather than erroring the UI
	}
	return strings.TrimRight(strings.TrimSpace(string(out)), "/"), false, nil
}

func exitStderr(err error) []byte {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.Stderr
	}
	return nil
}

func (s *webServer) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Mount string `json:"mount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	mount := strings.TrimSpace(body.Mount)
	if mount == "" {
		writeError(w, http.StatusBadRequest, "mount is required")
		return
	}
	if err := validateMountPath(mount); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid mount path: %v", err))
		return
	}

	// Generous safety bound; the scan no longer opens files so this is rarely hit,
	// but very large cards over a slow reader can still take a while to walk.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	summary, err := scanMedia(ctx, mount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// ---- Transfer ----

func (s *webServer) handleStartTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Mount     string   `json:"mount"`
		Name      string   `json:"name"`
		ShareKeys []string `json:"shareKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	body.Mount = strings.TrimSpace(body.Mount)
	body.Name = strings.TrimSpace(body.Name)
	if body.Mount == "" || body.Name == "" || len(body.ShareKeys) == 0 {
		writeError(w, http.StatusBadRequest, "mount, name and at least one share are required")
		return
	}
	if err := validateMountPath(body.Mount); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid mount path: %v", err))
		return
	}

	s.mu.Lock()
	if s.job != nil && !s.job.isDone() {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "a transfer is already in progress")
		return
	}

	// Resolve selected shares from the saved config.
	wanted := make(map[string]bool, len(body.ShareKeys))
	for _, k := range body.ShareKeys {
		wanted[k] = true
	}
	var shares []SMBConfig
	for _, c := range s.config.SMBShares {
		if wanted[smbShareKey(c)] {
			shares = append(shares, expandShare(c))
		}
	}
	s.mu.Unlock()

	if len(shares) == 0 {
		writeError(w, http.StatusBadRequest, "none of the selected shares were found in config")
		return
	}

	folderName := fmt.Sprintf("%d - %s", time.Now().Year(), body.Name)
	job := newTransferJob(folderName)

	s.mu.Lock()
	s.job = job
	s.mu.Unlock()

	go s.runJob(job, body.Mount, folderName, shares)

	writeJSON(w, http.StatusOK, map[string]any{"jobId": job.id, "folderName": folderName})
}

func (s *webServer) runJob(job *transferJob, mount, folderName string, shares []SMBConfig) {
	ctx, cancel := context.WithCancel(context.Background())
	job.setCancel(cancel)
	defer cancel()

	job.broadcast(jobEvent{Type: "started", FolderName: folderName})

	config := &Config{SMBShares: shares}
	connections, err := establishConnections(ctx, config, s.timeout)
	if err != nil {
		job.finish(fmt.Errorf("establishing SMB connections: %w", err), nil)
		return
	}
	defer closeConnections(connections)

	hook := &TransferProgressHook{
		OnStart: func(total int) {
			job.setTotal(total)
			job.broadcast(jobEvent{Type: "progress", Total: total, Completed: 0})
		},
		OnProgress: func(total, completed int, filePath string) {
			job.setProgress(total, completed, filePath)
			job.broadcast(jobEvent{Type: "progress", Total: total, Completed: completed, File: baseName(filePath)})
		},
	}

	transferErrors, err := processPhotos(ctx, mount, folderName, connections, s.workers, hook)

	total, completed := job.progress()
	notifyTransferResult(s.ntfyConfig(), folderName, total, completed, time.Since(job.startedAt), err, transferErrors)

	job.finish(err, transferErrors)
}

func (s *webServer) ntfyConfig() *NtfyConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.Ntfy == nil {
		return nil
	}
	c := *s.config.Ntfy
	return &c
}

func (s *webServer) handleCancelTransfer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job := s.job
	s.mu.Unlock()
	if job == nil || job.isDone() {
		writeError(w, http.StatusConflict, "no transfer is in progress")
		return
	}
	job.requestCancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEvents streams the current job's progress as Server-Sent Events.
func (s *webServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	s.mu.Lock()
	job := s.job
	s.mu.Unlock()

	if job == nil {
		writeSSE(w, flusher, jobEvent{Type: "idle"})
		return
	}

	ch, snapshot := job.subscribe()
	defer job.unsubscribe(ch)

	// Send the current state immediately so a late-joining client is in sync.
	writeSSE(w, flusher, snapshot)

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			writeSSE(w, flusher, ev)
			if ev.Type == "finished" {
				return
			}
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ---- helpers ----

func expandShare(c SMBConfig) SMBConfig {
	c.Password = os.ExpandEnv(c.Password)
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, ev jobEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func openInBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	_ = exec.Command(cmd, args...).Start()
}
