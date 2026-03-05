# 📸 SnapVault

**Professional Camera Photo Organizer with NAS Integration**

SnapVault is a high-performance Golang terminal app designed for photographers who need an automated workflow to organize and backup photos directly from SD cards to network storage. It provides an interactive TUI for selecting NAS shares and SD card mount points, then organizes photos by date using EXIF metadata and transfers them directly to SMB shares with parallel processing and optimized network operations.

---

## ✨ Features

### Core Functionality
- **🫧 Bubble Tea TUI Workflow**: Prompt-based terminal UI for selecting NAS shares and SD card mount points
- **🧭 App-Style Setup Layout**: A persistent setup panel shows chosen NAS connections, mount path, and photoshoot details as you move through steps
- **🔌 Live NAS Validation**: Manual NAS details are tested immediately; failures show detailed errors and let you correct input
- **💾 Saved Successful Connections**: Successfully validated NAS connections are persisted and offered next launch
- **✍️ Manual Fallback Input**: If auto-detected/configured options are not suitable, enter NAS targets and mount paths manually with format guidance
- **📅 Automatic Date Organization**: Extracts EXIF date data from photos and organizes them into dated subfolders (YYYY-MM-DD)
- **🏗️ Year-Prefixed Folders**: Automatically prepends the current year to your photoshoot folder names (e.g., "2025 - Wedding")
- **🚀 Direct Transfer**: Photos go straight from SD card to NAS without copying to local storage
- **🌐 Multiple NAS Support**: Transfer to multiple SMB shares simultaneously for redundancy
- **📸 Wide Format Support**: Handles common photo formats including JPG, PNG, CR2, NEF, ARW, DNG, ORF, RW2, RAW

### Performance & Optimization
- **⚡ Parallel Processing**: Configurable worker pool (default: 4) for concurrent file transfers
- **🔄 Connection Reuse**: SMB connections established once and reused across all transfers
- **💾 Directory Caching**: Smart caching prevents redundant directory creation across workers
- **🎯 Optimized Network I/O**: Eliminates unnecessary Stat calls, reducing network round trips by 50%
- **📈 Live Transfer Progress Bar**: Interactive mode displays real-time per-photo transfer progress and current file

### Reliability & Safety
- **🛑 Graceful Shutdown**: Proper signal handling (SIGINT/SIGTERM) with context propagation
- **⏱️ Configurable Timeout**: Connection timeout to prevent indefinite hangs (default: 30s)
- **📊 Error Reporting**: Comprehensive error summary showing which files failed and why
- **✅ Context Cancellation**: Operations can be cancelled mid-flight, including during large file transfers

### Security & Configuration
- **🔐 SMB Authentication**: Username/password authentication for secure shares
- **🔑 Environment Variables**: Password expansion using environment variables (e.g., `${NAS_PASSWORD}`)
- **📝 Structured Logging**: Progress and error logging using Go's `log/slog`
- **⚙️ YAML Configuration**: Clean, declarative configuration for multiple SMB shares

---

## 📋 Requirements

- Go 1.24.2 or higher
- Network access to SMB shares
- SD card reader and mount point

---

## 🚀 Installation

Clone the repository and build the binary:

```bash
git clone https://github.com/KiranTheRam/SnapVault.git
cd SnapVault
go build -o snapvault
```

---

## ⚙️ Configuration

Create a `config.yaml` file (see `config.example.yaml` for reference) with your SMB share details.
In interactive mode, SnapVault can start without an existing config and save validated NAS targets to this file:

```yaml
smb_shares:
  - host: "192.168.1.33"
    port: 445
    share: "share_name"
    username: "photographer"
    password: "${NAS_PASSWORD}"  # Environment variable expansion supported
    base_path: "folder_in_share" # optional: path inside the share
  
  - host: "backup-nas.local"
    port: 445
    share: "backup_share"
    username: "backup-user"
    password: "${BACKUP_NAS_PASSWORD}"  # Or use direct value: "backup-password"
    base_path: "PhotoBackups"
```

### Configuration Fields

- **host**: NAS hostname or IP address
- **port**: SMB port (default: 445)
- **share**: SMB share name
- **username**: Authentication username
- **password**: Authentication password (supports environment variable expansion with `${VAR}` syntax)
- **base_path**: Base directory within the share where photoshoot folders will be created

### NAS Target Format in TUI

When adding a NAS connection in the TUI, enter:

1. `NAS URL/IP`: `host` or `host:port` (example: `192.168.1.33`)
2. `Share path`: `share[/path_inside_share]` (example: `general/snapvault`)

The TUI also accepts a combined value pasted into the host field:

- `host[:port]/share[/path_inside_share]`
- Example: `192.168.1.33/share_name/folder_in_share`

This maps to:

- `host`: `192.168.1.33`
- `port`: `445` (default if omitted)
- `share`: `share_name`
- `base_path`: `folder_in_share` (optional)

### Environment Variables

Set environment variables for secure password management:

```bash
export NAS_PASSWORD="your-secure-password"
export BACKUP_NAS_PASSWORD="your-backup-password"
./snapvault
```

---

## 📖 Usage

```bash
./snapvault [OPTIONS]
```

### Flags

- `-mount`: Path to SD card mount point (optional; if omitted, selected in TUI)
- `-name`: Photoshoot name (optional; if omitted, entered in TUI and prefixed with current year)
- `-config`: Path to YAML config file (default: `config.yaml`)
- `-workers`: Number of parallel workers for file transfers (default: `4`)
- `-timeout`: SMB connection timeout (default: `30s`)

For non-interactive runs (`-mount` and `-name` provided), the config file must exist and include at least one SMB share.

### Interactive TUI Flow

When `-mount` or `-name` is missing, SnapVault launches interactive mode:

1. Shows saved NAS connections from `config.yaml` and lets you select one or many
2. Lets you add a new NAS target in format `host[:port]/share[/path_inside_share]`
   - Or enter it as separate fields: host + share path (starting with share name)
3. Attempts a real SMB connection immediately and reports success/failure details
4. On success, saves the NAS connection so it appears next launch
5. Shows detected mounted device paths for SD cards with manual fallback
6. Prompts for photoshoot name
7. Shows a live progress bar while transferring and a final summary screen with results

### Examples

```bash
# Launch interactive TUI (recommended)
./snapvault

# Non-interactive run with all required values
./snapvault -mount /media/sdcard -name "Wedding"

# Use more workers for faster transfers
./snapvault -mount /media/sdcard -name "Concert" -workers 8

# Custom config file and timeout
./snapvault -mount /media/sdcard -name "Birthday Party" -config /etc/snapvault/config.yaml -timeout 60s

# Mount point on macOS
./snapvault -mount /Volumes/SDCARD -name "Portrait Session"

# Graceful shutdown with Ctrl+C
# Press Ctrl+C to cancel - operations stop immediately and connections are cleaned up
```

### Folder Structure

Given a photoshoot name of "Wedding" in 2025, photos will be organized as:

```
<base_path>/
└── 2025 - Wedding/
    ├── 2025-11-01/
    │   ├── IMG_001.jpg
    │   └── IMG_002.CR2
    ├── 2025-11-02/
    │   └── IMG_003.jpg
    └── 2025-11-03/
        └── IMG_004.NEF
```

---

## 📝 Logging & Error Reporting

The tool uses structured logging via `log/slog` for real-time feedback:

```
2025/11/09 17:00:00 INFO Starting photo transfer folder="2025 - Wedding" mount_point=/media/sdcard
2025/11/09 17:00:00 INFO Establishing SMB connection index=0 host=nas.local share=photos
2025/11/09 17:00:00 INFO Successfully connected to SMB share index=0 host=nas.local
2025/11/09 17:00:01 INFO Scanning mount point for photos path=/media/sdcard workers=4
2025/11/09 17:00:01 INFO Processing photo file=/media/sdcard/IMG_001.jpg
2025/11/09 17:00:01 INFO Creating destination directory path=Photoshoots/2025 - Wedding/2025-11-09
2025/11/09 17:00:02 INFO Copying file to SMB source=IMG_001.jpg destination=Photoshoots/2025 - Wedding/2025-11-09/IMG_001.jpg
2025/11/09 17:00:03 INFO Successfully transferred to SMB share file=IMG_001.jpg share_index=0 host=nas.local
```

### Error Summary

If any transfers fail, a comprehensive error summary is displayed at the end:

```
=== Transfer Error Summary ===
File: /media/sdcard/IMG_042.jpg
  Share: nas.local/photos
  Error: copying file: connection reset by peer

File: /media/sdcard/IMG_043.CR2
  Share: backup-nas.local/backup
  Error: creating directories: connection timed out
```

---

## 🔧 Supported Photo Formats

- JPEG: `.jpg`, `.jpeg`
- PNG: `.png`
- Canon RAW: `.cr2`
- Nikon RAW: `.nef`
- Sony RAW: `.arw`
- Adobe DNG: `.dng`
- Olympus RAW: `.orf`
- Panasonic RAW: `.rw2`
- Generic RAW: `.raw`

---

## 🛡️ Security Considerations

- **Secure Config Storage**: Store `config.yaml` with restricted file permissions (`chmod 600 config.yaml`)
- **Environment Variables**: Use environment variables for passwords instead of hardcoding them
- **Version Control**: Avoid committing `config.yaml` with credentials to version control
- **Git Ignore**: Add config files with sensitive data to `.gitignore`
- **Network Security**: Ensure SMB traffic is on a trusted network or use encrypted channels

## 🚀 Performance Tips

- **Worker Count**: Increase `-workers` for faster transfers with high-bandwidth networks (e.g., `-workers 8`)
- **Network Speed**: Performance is primarily limited by network speed between your machine and NAS
- **Multiple Shares**: Transfers to multiple SMB shares happen in parallel per file
- **Directory Caching**: Subsequent files to the same date folder benefit from cached directory creation
- **Connection Reuse**: All files use the same SMB connection, eliminating authentication overhead

## 🔧 Troubleshooting

### Connection Issues

```bash
# Test with verbose output and longer timeout
./snapvault -mount /media/sdcard -name "Test" -timeout 60s

# Verify SMB connectivity manually
smbclient //nas.local/photos -U photographer
```

### Performance Issues

- Reduce `-workers` if experiencing network congestion
- Check network bandwidth between machine and NAS
- Verify NAS isn't under heavy load from other operations

### Cancellation

- Press `Ctrl+C` once for graceful shutdown
- In-flight work is canceled via context and SMB sessions are cleaned up before exit

---

## 📄 License

See [LICENSE](LICENSE) file for details.
