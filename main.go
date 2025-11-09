package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
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

type Config struct {
	SMBShares []SMBConfig `yaml:"smb_shares"`
}

var photoExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".cr2":  true,
	".nef":  true,
	".arw":  true,
	".dng":  true,
	".orf":  true,
	".rw2":  true,
	".raw":  true,
}

func main() {
	mountPoint := flag.String("mount", "", "SD card mount point")
	photoshootName := flag.String("name", "", "Photoshoot name")
	configPath := flag.String("config", "config.yaml", "Path to SMB config YAML file")
	flag.Parse()

	if *mountPoint == "" || *photoshootName == "" {
		slog.Error("Both -mount and -name flags are required")
		flag.Usage()
		os.Exit(1)
	}

	// Load config
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

	// Process photos
	if err := processPhotos(*mountPoint, folderName, config); err != nil {
		slog.Error("Failed to process photos", "error", err)
		os.Exit(1)
	}

	slog.Info("Photo transfer completed successfully")
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &config, nil
}

func processPhotos(mountPoint, folderName string, config *Config) error {
	slog.Info("Scanning mount point for photos", "path", mountPoint)
	// Walk through mount point and collect photo files
	return filepath.Walk(mountPoint, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil // Continue with other files
		}

		if info.IsDir() {
			return nil
		}

		// Check if file is a photo
		ext := strings.ToLower(filepath.Ext(path))
		if !photoExtensions[ext] {
			return nil
		}

		slog.Info("Processing photo", "file", path)

		// Get photo date
		photoDate, err := getPhotoDate(path, info)
		if err != nil {
			slog.Warn("Failed to get photo date, using file mod time", "file", path, "error", err)
			photoDate = info.ModTime()
		}

		// Transfer to all SMB shares
		for i, smbConfig := range config.SMBShares {
			if err := transferToSMB(path, folderName, photoDate, smbConfig); err != nil {
				slog.Error("Failed to transfer to SMB share", "share_index", i, "host", smbConfig.Host, "error", err)
			} else {
				slog.Info("Successfully transferred to SMB share", "share_index", i, "host", smbConfig.Host)
			}
		}

		return nil
	})
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

func transferToSMB(sourcePath, folderName string, photoDate time.Time, config SMBConfig) error {
	slog.Info("Connecting to SMB share", "host", config.Host, "share", config.Share)
	conn, err := connectSMB(config)
	if err != nil {
		return fmt.Errorf("connecting to SMB: %w", err)
	}
	defer conn.Logoff()

	slog.Info("Mounting share", "share", config.Share)
	fs, err := conn.Mount(config.Share)
	if err != nil {
		return fmt.Errorf("mounting share: %w", err)
	}
	defer fs.Umount()

	// Create folder structure: basePath/folderName/YYYY-MM-DD/
	dateFolder := photoDate.Format("2006-01-02")
	destDir := filepath.Join(config.BasePath, folderName, dateFolder)

	slog.Info("Creating destination directory", "path", destDir)
	if err := mkdirAllSMB(fs, destDir); err != nil {
		return fmt.Errorf("creating directories: %w", err)
	}

	// Copy file
	fileName := filepath.Base(sourcePath)
	destPath := filepath.Join(destDir, fileName)

	slog.Info("Copying file to SMB", "source", fileName, "destination", destPath)
	if err := copyFileToSMB(sourcePath, fs, destPath); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	return nil
}

func connectSMB(config SMBConfig) (*smb2.Session, error) {
	port := config.Port
	if port == 0 {
		port = 445
	}

	addr := net.JoinHostPort(config.Host, fmt.Sprintf("%d", port))
	conn, err := net.Dial("tcp", addr)
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
func mkdirAllSMB(fs *smb2.Share, path string) error {
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

		// Try to stat the directory
		_, err := fs.Stat(currentPath)
		if err != nil {
			// Directory doesn't exist, create it
			if err := fs.Mkdir(currentPath, 0755); err != nil {
				// Check if error is "already exists"
				if !os.IsExist(err) {
					return fmt.Errorf("creating directory %s: %w", currentPath, err)
				}
			}
		}
	}

	return nil
}

func copyFileToSMB(sourcePath string, fs *smb2.Share, destPath string) error {
	// Normalize path separators
	destPath = filepath.ToSlash(destPath)

	// Open source file
	src, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer src.Close()

	// Create destination file on SMB
	dst, err := fs.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dst.Close()

	// Copy data
	_, err = io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("copying data: %w", err)
	}

	return nil
}