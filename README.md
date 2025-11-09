# 📸 SnapVault

**Professional Camera Photo Organizer with NAS Integration**

SnapVault is a lightweight Golang CLI tool designed for photographers who need an automated workflow to organize and backup photos directly from SD cards to network storage. It automatically organizes photos by date using EXIF metadata and transfers them directly to SMB shares without using local storage.

---

## ✨ Features

- **📅 Automatic Date Organization**: Extracts EXIF date data from photos and organizes them into dated subfolders (YYYY-MM-DD)
- **🏗️ Year-Prefixed Folders**: Automatically prepends the current year to your photoshoot folder names (e.g., "2025 - Wedding")
- **🚀 Direct Transfer**: Photos go straight from SD card to NAS without copying to local storage
- **🌐 Multiple NAS Support**: Transfer to multiple SMB shares simultaneously for redundancy
- **🔐 SMB Authentication**: Supports username/password authentication for secure shares
- **📸 Wide Format Support**: Handles common photo formats including JPG, PNG, CR2, NEF, ARW, DNG, ORF, RW2, RAW
- **📝 Structured Logging**: Progress and error logging using Go's `log/slog`

---

## 📋 Requirements

- Go 1.25.1 or higher
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

Create a `config.yaml` file (see `config.example.yaml` for reference) with your SMB share details:

```yaml
smb_shares:
  - host: "nas.local"
    port: 445
    share: "photos"
    username: "photographer"
    password: "your-password"
    base_path: "Photoshoots"
  
  - host: "backup-nas.local"
    port: 445
    share: "backup"
    username: "backup-user"
    password: "backup-password"
    base_path: "PhotoBackups"
```

### Configuration Fields

- **host**: NAS hostname or IP address
- **port**: SMB port (default: 445)
- **share**: SMB share name
- **username**: Authentication username
- **password**: Authentication password
- **base_path**: Base directory within the share where photoshoot folders will be created

---

## 📖 Usage

```bash
./snapvault -mount <SD_CARD_MOUNT_POINT> -name <PHOTOSHOOT_NAME> [-config <CONFIG_FILE>]
```

### Flags

- `-mount`: Path to SD card mount point (required)
- `-name`: Photoshoot name (required, will be prefixed with current year)
- `-config`: Path to YAML config file (default: `config.yaml`)

### Examples

```bash
# Transfer photos from SD card to NAS with photoshoot name "Wedding"
./snapvault -mount /media/sdcard -name "Wedding"

# Use a custom config file
./snapvault -mount /media/sdcard -name "Birthday Party" -config /etc/snapvault/config.yaml

# Mount point on macOS
./snapvault -mount /Volumes/SDCARD -name "Concert"
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

## 📝 Logging

The tool uses structured logging via `log/slog`. Logs include:

- Photo processing progress
- Successful transfers
- Connection status
- Errors and warnings

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

- Store `config.yaml` securely with appropriate file permissions
- Avoid committing `config.yaml` with credentials to version control
- Use `.gitignore` to exclude config files with sensitive data

---

## 📄 License

See [LICENSE](LICENSE) file for details.
