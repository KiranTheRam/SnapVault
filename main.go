package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hirochachacha/go-smb2"
	"github.com/rwcarlsen/goexif/exif"
	"gopkg.in/yaml.v3"
)

type SMBConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Share    string `yaml:"share"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	BasePath string `yaml:"base_path"` // Base path within the share
}

type NtfyConfig struct {
	Server string `yaml:"server" json:"server"`
	Topic  string `yaml:"topic" json:"topic"`
	// Optional auth for protected ntfy servers. Token takes precedence; otherwise
	// Username/Password are used for HTTP Basic auth. Token supports ${ENV} expansion.
	Token    string `yaml:"token,omitempty" json:"token"`
	Username string `yaml:"username,omitempty" json:"username"`
	Password string `yaml:"password,omitempty" json:"password"`
}

type Config struct {
	SMBShares []SMBConfig `yaml:"smb_shares"`
	Ntfy      *NtfyConfig `yaml:"ntfy,omitempty"`
}

type SMBConnection struct {
	Config      SMBConfig
	Session     *smb2.Session
	Share       *smb2.Share
	createdDirs sync.Map // Cache of created directory paths
}

type TransferJob struct {
	SourcePath string
	FolderName string
	PhotoDate  time.Time
}

type TransferError struct {
	FilePath string
	Share    string
	Error    error
}

type TransferProgressHook struct {
	OnStart    func(total int)
	OnProgress func(total, completed int, filePath string)
}

type MountCandidate struct {
	Source string
	Path   string
	FSType string
}

var photoExtensions = map[string]bool{
	// Stills
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".heic": true,
	".heif": true,
	".tif":  true,
	".tiff": true,
	".cr2":  true,
	".cr3":  true,
	".nef":  true,
	".arw":  true,
	".dng":  true,
	".orf":  true,
	".rw2":  true,
	".raf":  true,
	".pef":  true,
	".srw":  true,
	".raw":  true,
	// Video (cameras and action cams)
	".mov":  true,
	".mp4":  true,
	".m4v":  true,
	".avi":  true,
	".mts":  true,
	".m2ts": true,
	".mxf":  true,
}

func main() {
	mountPoint := flag.String("mount", "", "SD card mount point")
	photoshootName := flag.String("name", "", "Photoshoot name")
	configPath := flag.String("config", "config.yaml", "Path to SMB config YAML file")
	timeout := flag.Duration("timeout", 30*time.Second, "SMB connection timeout")
	workers := flag.Int("workers", 4, "Number of parallel workers for file transfers")
	serve := flag.Bool("serve", false, "Run the web UI server instead of the terminal app")
	addr := flag.String("addr", "127.0.0.1:8080", "Address to bind the web UI server")
	noOpen := flag.Bool("no-open", false, "Do not open the browser automatically in -serve mode")
	flag.Parse()

	if *serve {
		if err := runWebServer(*configPath, *addr, *timeout, *workers, !*noOpen); err != nil {
			slog.Error("Web server failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if *mountPoint == "" || *photoshootName == "" {
		err := runInteractiveTUI(*configPath, *mountPoint, *photoshootName, *timeout, *workers)
		if err != nil {
			slog.Error("Interactive session failed", "error", err)
			os.Exit(1)
		}
		return
	}

	config, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	if len(config.SMBShares) == 0 {
		slog.Error("No SMB shares configured")
		os.Exit(1)
	}

	// Create folder name with year prefix
	currentYear := time.Now().Year()
	folderName := fmt.Sprintf("%d - %s", currentYear, *photoshootName)
	slog.Info("Starting photo transfer", "folder", folderName, "mount_point", *mountPoint)
	startedAt := time.Now()

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		cancel()
	}()

	// Establish all SMB connections upfront
	connections, err := establishConnections(ctx, config, *timeout)
	if err != nil {
		slog.Error("Failed to establish SMB connections", "error", err)
		os.Exit(1)
	}
	defer closeConnections(connections)

	// Process photos, tracking counts so notifications can report them.
	var totalCount, completedCount int64
	countHook := &TransferProgressHook{
		OnStart: func(total int) { atomic.StoreInt64(&totalCount, int64(total)) },
		OnProgress: func(total, completed int, _ string) {
			atomic.StoreInt64(&totalCount, int64(total))
			atomic.StoreInt64(&completedCount, int64(completed))
		},
	}
	transferErrors, err := processPhotos(ctx, *mountPoint, folderName, connections, *workers, countHook)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("Photo transfer cancelled by user")
			os.Exit(130)
		}
		notifyTransferResult(config.Ntfy, folderName, int(totalCount), int(completedCount), time.Since(startedAt), err, transferErrors)
		slog.Error("Failed to process photos", "error", err)
		os.Exit(1)
	}

	notifyTransferResult(config.Ntfy, folderName, int(totalCount), int(completedCount), time.Since(startedAt), nil, transferErrors)

	// Print summary
	if len(transferErrors) > 0 {
		slog.Warn("Transfer completed with errors", "failed_count", len(transferErrors))
		fmt.Println("\n=== Transfer Error Summary ===")
		for _, te := range transferErrors {
			fmt.Printf("File: %s\n  Share: %s\n  Error: %v\n\n", te.FilePath, te.Share, te.Error)
		}
		os.Exit(1)
	}

	slog.Info("Photo transfer completed successfully")
}

func parseManualSMBTarget(value string) (string, int, string, string, error) {
	target := strings.TrimSpace(value)
	target = strings.TrimPrefix(target, "smb://")
	target = strings.TrimPrefix(target, "//")
	target = strings.Trim(target, "/")

	parts := strings.SplitN(target, "/", 3)
	if len(parts) < 2 {
		return "", 0, "", "", fmt.Errorf("expected host[:port]/share[/path] format")
	}

	hostPort := strings.TrimSpace(parts[0])
	sharePath := strings.Join(parts[1:], "/")
	if hostPort == "" || strings.TrimSpace(sharePath) == "" {
		return "", 0, "", "", fmt.Errorf("host and share are required")
	}

	host, port, err := parseSMBHostPort(hostPort)
	if err != nil {
		return "", 0, "", "", err
	}

	share, basePath, err := parseSMBSharePath(sharePath)
	if err != nil {
		return "", 0, "", "", err
	}

	return host, port, share, basePath, nil
}

func parseSMBHostPort(value string) (string, int, error) {
	hostPort := strings.TrimSpace(value)
	hostPort = strings.TrimPrefix(hostPort, "smb://")
	hostPort = strings.TrimPrefix(hostPort, "//")
	hostPort = strings.Trim(hostPort, "/")

	if hostPort == "" {
		return "", 0, fmt.Errorf("host is required")
	}
	if strings.Contains(hostPort, "/") {
		return "", 0, fmt.Errorf("host must not contain '/'; use separate share path field")
	}

	port := 445
	host := hostPort
	if idx := strings.LastIndex(hostPort, ":"); idx > 0 && idx < len(hostPort)-1 {
		parsedPort, err := strconv.Atoi(hostPort[idx+1:])
		if err != nil || parsedPort <= 0 || parsedPort > 65535 {
			return "", 0, fmt.Errorf("invalid port in %q", hostPort)
		}
		host = hostPort[:idx]
		port = parsedPort
	}

	if strings.TrimSpace(host) == "" {
		return "", 0, fmt.Errorf("host is required")
	}

	return host, port, nil
}

func parseSMBSharePath(value string) (string, string, error) {
	path := strings.TrimSpace(value)
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", fmt.Errorf("share path is required")
	}

	parts := strings.SplitN(path, "/", 2)
	share := strings.TrimSpace(parts[0])
	basePath := ""
	if len(parts) == 2 {
		basePath = strings.Trim(parts[1], "/")
	}

	if share == "" {
		return "", "", fmt.Errorf("share name is required")
	}

	return share, basePath, nil
}

func validateMountPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func detectMountCandidates() []MountCandidate {
	candidateMap := make(map[string]MountCandidate)

	// Linux mount discovery.
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}

			source := decodeProcMountField(fields[0])
			path := decodeProcMountField(fields[1])
			fsType := decodeProcMountField(fields[2])

			if !looksLikeSDMount(source, path, fsType) {
				continue
			}

			if _, err := os.Stat(path); err == nil {
				candidateMap[path] = MountCandidate{Source: source, Path: path, FSType: fsType}
			}
		}
	}

	// Directory scan fallback for common mount roots.
	scanRoots := []string{"/media", "/run/media/" + os.Getenv("USER"), "/Volumes", "/mnt"}
	for _, root := range scanRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if root == "/media" || strings.HasPrefix(root, "/run/media/") {
				subEntries, err := os.ReadDir(path)
				if err == nil {
					for _, sub := range subEntries {
						if !sub.IsDir() {
							continue
						}
						subPath := filepath.Join(path, sub.Name())
						candidateMap[subPath] = MountCandidate{Path: subPath}
					}
				}
			}
			candidateMap[path] = MountCandidate{Path: path}
		}
	}

	candidates := make([]MountCandidate, 0, len(candidateMap))
	for _, c := range candidateMap {
		candidates = append(candidates, c)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Path < candidates[j].Path
	})

	return candidates
}

func looksLikeSDMount(source, path, fsType string) bool {
	if path == "" {
		return false
	}

	if strings.HasPrefix(path, "/media/") || strings.HasPrefix(path, "/run/media/") || strings.HasPrefix(path, "/Volumes/") {
		return true
	}

	if strings.HasPrefix(path, "/mnt/") && strings.HasPrefix(source, "/dev/") {
		return true
	}

	if strings.HasPrefix(source, "/dev/sd") || strings.HasPrefix(source, "/dev/mmc") || strings.HasPrefix(source, "/dev/disk") {
		switch fsType {
		case "vfat", "exfat", "ntfs", "fuseblk", "ext4", "ext3", "ext2", "msdos", "apfs", "hfs", "hfsplus":
			return true
		}
	}

	return false
}

func decodeProcMountField(value string) string {
	replacer := strings.NewReplacer(
		`\\`, `\`,
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
	)
	return replacer.Replace(value)
}

func loadConfig(path string) (*Config, error) {
	return loadConfigFromFile(path, true)
}

func loadConfigRaw(path string) (*Config, error) {
	return loadConfigFromFile(path, false)
}

func loadConfigFromFile(path string, expandPasswords bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if expandPasswords {
		for i := range config.SMBShares {
			config.SMBShares[i].Password = os.ExpandEnv(config.SMBShares[i].Password)
		}
	}

	return &config, nil
}

func saveConfig(path string, config *Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

func establishConnections(ctx context.Context, config *Config, timeout time.Duration) ([]*SMBConnection, error) {
	connections := make([]*SMBConnection, 0, len(config.SMBShares))

	for i, smbConfig := range config.SMBShares {
		select {
		case <-ctx.Done():
			closeConnections(connections)
			return nil, ctx.Err()
		default:
		}

		slog.Info("Establishing SMB connection", "index", i, "host", smbConfig.Host, "share", smbConfig.Share)

		session, err := connectSMB(ctx, smbConfig, timeout)
		if err != nil {
			// Clean up already established connections
			closeConnections(connections)
			return nil, fmt.Errorf("connecting to share %d (%s): %w", i, smbConfig.Host, err)
		}

		share, err := session.Mount(smbConfig.Share)
		if err != nil {
			session.Logoff()
			// Clean up already established connections
			closeConnections(connections)
			return nil, fmt.Errorf("mounting share %d (%s/%s): %w", i, smbConfig.Host, smbConfig.Share, err)
		}

		conn := &SMBConnection{
			Config:  smbConfig,
			Session: session,
			Share:   share,
		}
		connections = append(connections, conn)
		slog.Info("Successfully connected to SMB share", "index", i, "host", smbConfig.Host)
	}

	return connections, nil
}

func closeConnections(connections []*SMBConnection) {
	for i, conn := range connections {
		if conn.Share != nil {
			slog.Info("Unmounting share", "index", i, "host", conn.Config.Host)
			conn.Share.Umount()
		}
		if conn.Session != nil {
			conn.Session.Logoff()
		}
	}
}

func processPhotos(
	ctx context.Context,
	mountPoint, folderName string,
	connections []*SMBConnection,
	workers int,
	hook *TransferProgressHook,
) ([]TransferError, error) {
	slog.Info("Scanning mount point for photos", "path", mountPoint, "workers", workers)

	// Create channels
	jobs := make(chan TransferJob)
	tfChan := make(chan TransferError, workers)
	var workerWG sync.WaitGroup
	var completedCount int64

	photoJobs, collectErr := collectTransferJobs(ctx, mountPoint, folderName)
	if collectErr != nil {
		return nil, collectErr
	}
	if hook != nil && hook.OnStart != nil {
		hook.OnStart(len(photoJobs))
	}

	// Start worker pool
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func(workerID int) {
			defer workerWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}

					// Transfer to all SMB shares
					for i, conn := range connections {
						// Check for cancellation between transfers
						select {
						case <-ctx.Done():
							return
						default:
						}

						if err := transferToSMB(ctx, job.SourcePath, job.FolderName, job.PhotoDate, conn); err != nil {
							slog.Error("Failed to transfer to SMB share", "file", job.SourcePath, "share_index", i, "host", conn.Config.Host, "error", err)
							tfChan <- TransferError{
								FilePath: job.SourcePath,
								Share:    fmt.Sprintf("%s/%s", conn.Config.Host, conn.Config.Share),
								Error:    err,
							}
						} else {
							slog.Info("Successfully transferred to SMB share", "file", filepath.Base(job.SourcePath), "share_index", i, "host", conn.Config.Host)
						}
					}

					processed := int(atomic.AddInt64(&completedCount, 1))
					if hook != nil && hook.OnProgress != nil {
						hook.OnProgress(len(photoJobs), processed, job.SourcePath)
					}
				}
			}
		}(i)
	}

	// Start error collector
	var transferErrors []TransferError
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)
	go func() {
		defer collectorWG.Done()
		for e := range tfChan {
			transferErrors = append(transferErrors, e)
		}
	}()

	// Queue jobs.
	for _, job := range photoJobs {
		select {
		case jobs <- job:
		case <-ctx.Done():
			close(jobs)
			workerWG.Wait()
			close(tfChan)
			collectorWG.Wait()
			return transferErrors, ctx.Err()
		}
	}

	// Close jobs channel.
	close(jobs)

	// Wait for workers before closing transfer error channel.
	workerWG.Wait()
	close(tfChan)

	// Wait for the error collector.
	collectorWG.Wait()

	return transferErrors, nil
}

func collectTransferJobs(ctx context.Context, mountPoint, folderName string) ([]TransferJob, error) {
	jobs := make([]TransferJob, 0, 1024)

	err := filepath.Walk(mountPoint, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil
		}
		if info.IsDir() {
			if isMacMetadata(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if isMacMetadata(info.Name()) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !photoExtensions[ext] {
			return nil
		}

		photoDate, dateErr := getPhotoDate(path, info)
		if dateErr != nil {
			slog.Warn("Failed to get photo date, using file mod time", "file", path, "error", dateErr)
			photoDate = info.ModTime()
		}

		jobs = append(jobs, TransferJob{
			SourcePath: path,
			FolderName: folderName,
			PhotoDate:  photoDate,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return jobs, nil
}

type ScanSummary struct {
	FileCount  int            `json:"fileCount"`
	TotalBytes int64          `json:"totalBytes"`
	ByDate     map[string]int `json:"byDate"`
}

// scanMedia walks a mount point and summarizes the media files that would be
// transferred, without copying anything. Used by the web UI to preview an SD card.
//
// For speed it does NOT open files to read EXIF (that can take minutes over a
// card reader); the per-day breakdown uses the file modification time, which is
// an approximation. The actual transfer still groups by precise EXIF capture date.
func scanMedia(ctx context.Context, mountPoint string) (ScanSummary, error) {
	summary := ScanSummary{ByDate: make(map[string]int)}

	err := filepath.Walk(mountPoint, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			return nil
		}
		if info.IsDir() {
			if isMacMetadata(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isMacMetadata(info.Name()) {
			return nil
		}
		if !photoExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		summary.FileCount++
		summary.TotalBytes += info.Size()
		summary.ByDate[info.ModTime().Format("2006-01-02")]++
		return nil
	})
	if err != nil {
		return ScanSummary{}, err
	}

	return summary, nil
}

// isMacMetadata reports whether a file is macOS bookkeeping noise that should
// never be transferred: AppleDouble resource-fork sidecars (._*), Finder
// metadata (.DS_Store), and the __MACOSX directory tree created by zip.
func isMacMetadata(name string) bool {
	return strings.HasPrefix(name, "._") ||
		name == ".DS_Store" ||
		name == "__MACOSX"
}

func getPhotoDate(path string, info os.FileInfo) (time.Time, error) {
	// Try to read EXIF data
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		// If EXIF decode fails, use file mod time
		return info.ModTime(), nil
	}

	// Try to get DateTime or DateTimeOriginal
	tm, err := x.DateTime()
	if err != nil {
		return info.ModTime(), nil
	}

	return tm, nil
}

func transferToSMB(ctx context.Context, sourcePath, folderName string, photoDate time.Time, conn *SMBConnection) error {
	// Create folder structure: basePath/folderName/YYYY-MM-DD/
	dateFolder := photoDate.Format("2006-01-02")
	destDir := filepath.Join(conn.Config.BasePath, folderName, dateFolder)

	// Check cache first
	if _, exists := conn.createdDirs.Load(destDir); !exists {
		slog.Info("Creating destination directory", "path", destDir)
		if err := mkdirAllSMB(ctx, conn.Share, destDir); err != nil {
			return fmt.Errorf("creating directories: %w", err)
		}
		// Cache the successfully created path
		conn.createdDirs.Store(destDir, struct{}{})
	}

	// Copy file
	fileName := filepath.Base(sourcePath)
	destPath := filepath.Join(destDir, fileName)

	slog.Info("Copying file to SMB", "source", fileName, "destination", destPath)
	written, err := copyFileToSMB(ctx, sourcePath, conn.Share, destPath)
	if err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	// Verify the destination size matches the source to catch truncated/partial writes.
	if srcInfo, statErr := os.Stat(sourcePath); statErr == nil {
		if written != srcInfo.Size() {
			return fmt.Errorf("size mismatch after copy: wrote %d bytes, source is %d bytes", written, srcInfo.Size())
		}
	}

	return nil
}

func connectSMB(ctx context.Context, config SMBConfig, timeout time.Duration) (*smb2.Session, error) {
	port := config.Port
	if port == 0 {
		port = 445
	}

	addr := net.JoinHostPort(config.Host, fmt.Sprintf("%d", port))

	// Create context with timeout
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing: %w", err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     config.Username,
			Password: config.Password,
		},
	}

	session, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMB dial: %w", err)
	}

	return session, nil
}

// mkdirAllSMB creates all directories in the path
func mkdirAllSMB(ctx context.Context, fs *smb2.Share, path string) error {
	// Use context-aware share
	fs = fs.WithContext(ctx)

	// Normalize path separators to forward slashes
	path = filepath.ToSlash(path)

	// Split path into components
	parts := strings.Split(path, "/")
	currentPath := ""

	for _, part := range parts {
		if part == "" {
			continue
		}

		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		// Try to create the directory (optimistic creation, no stat check)
		if err := fs.Mkdir(currentPath, 0755); err != nil {
			// Ignore "already exists" errors
			if !os.IsExist(err) {
				return fmt.Errorf("creating directory %s: %w", currentPath, err)
			}
		}
	}

	return nil
}

func copyFileToSMB(ctx context.Context, sourcePath string, fs *smb2.Share, destPath string) (int64, error) {
	// Use context-aware share
	fs = fs.WithContext(ctx)

	// Normalize path separators
	destPath = filepath.ToSlash(destPath)

	// Open source file
	src, err := os.Open(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("opening source file: %w", err)
	}
	defer src.Close()

	// Create destination file on SMB
	dst, err := fs.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("creating destination file: %w", err)
	}
	defer dst.Close()

	// Copy data
	written, err := io.Copy(dst, src)
	if err != nil {
		return written, fmt.Errorf("copying data: %w", err)
	}

	return written, nil
}
