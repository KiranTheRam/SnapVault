package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// publishNtfy sends a single notification to an ntfy topic. It is a no-op when
// ntfy is not configured. HTTP headers must be ASCII, so emoji are expressed via
// the Tags header (ntfy renders known tags as emoji) rather than in the title.
func publishNtfy(ctx context.Context, cfg *NtfyConfig, title, message, tags, priority string) error {
	if cfg == nil || strings.TrimSpace(cfg.Server) == "" || strings.TrimSpace(cfg.Topic) == "" {
		return nil
	}

	url := strings.TrimRight(strings.TrimSpace(cfg.Server), "/") + "/" + strings.Trim(strings.TrimSpace(cfg.Topic), "/")

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, strings.NewReader(message))
	if err != nil {
		return err
	}
	// Authentication for protected servers: bearer token wins, else HTTP Basic.
	if token := strings.TrimSpace(os.ExpandEnv(cfg.Token)); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if strings.TrimSpace(cfg.Username) != "" || cfg.Password != "" {
		req.SetBasicAuth(strings.TrimSpace(cfg.Username), os.ExpandEnv(cfg.Password))
	}
	if title != "" {
		req.Header.Set("Title", title)
	}
	if tags != "" {
		req.Header.Set("Tags", tags)
	}
	if priority != "" {
		req.Header.Set("Priority", priority)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}

// notifyTransferResult publishes a completion or failure notification for a
// finished transfer. Errors are logged but never block the caller.
func notifyTransferResult(cfg *NtfyConfig, folderName string, total, completed int, dur time.Duration, fatal error, errs []TransferError) {
	if cfg == nil || strings.TrimSpace(cfg.Topic) == "" {
		return
	}

	var (
		title, tags, priority string
		body                  strings.Builder
	)

	if fatal == nil && len(errs) == 0 {
		title = "SnapVault transfer complete"
		tags = "white_check_mark,camera"
		priority = "default"
		fmt.Fprintf(&body, "%s\n%d files in %s", folderName, completed, dur.Round(time.Second))
	} else {
		title = "SnapVault transfer failed"
		tags = "rotating_light"
		priority = "high"
		fmt.Fprintf(&body, "%s\n%d of %d files transferred", folderName, completed, total)
		if fatal != nil {
			fmt.Fprintf(&body, "\nError: %v", fatal)
		}
		if len(errs) > 0 {
			fmt.Fprintf(&body, "\n%d file error(s)", len(errs))
		}
	}

	if err := publishNtfy(context.Background(), cfg, title, body.String(), tags, priority); err != nil {
		slog.Warn("Failed to send ntfy notification", "error", err)
	}
}
