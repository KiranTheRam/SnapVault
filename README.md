# 📸 SnapVault

**Professional Camera Photo Organizer with NAS Integration**

SnapVault is a Python-based photo import and organization tool designed for photographers who need an automated workflow to organize and backup photos from SD cards to network storage. It automatically organizes photos by date, copies them to multiple NAS destinations via SMB, and sends Discord notifications throughout the process.

---

## ✨ Features

- **📅 Automatic Date Organization**: Extracts EXIF date data from photos and organizes them into dated subfolders (YYYY-MM-DD)
- **🏗️ Year-Prefixed Folders**: Automatically prepends the current year to your photoshoot folder names (e.g., "2025 - Wedding Smith")
- **🌐 Network Storage**: Copies photos to NAS shares via SMB protocol (macOS native support)
- **💾 Dual Backup**: Supports copying to two separate destinations:
  - Long-term archive storage
  - Fast SSD storage for active editing
- **🎯 Flexible Destinations**: Choose to copy to both locations, or just one
- **🔔 Discord Notifications**: Real-time status updates sent to Discord via webhooks
- **📊 Progress Tracking**: Visual progress bars showing copy and organization status
- **📝 Comprehensive Logging**: Detailed logs saved to timestamped files for troubleshooting
- **🖼️ Wide Format Support**: JPG, PNG, RAW files (CR2, NEF, ARW, DNG, RAF, etc.), HEIC, TIFF, and more

---

## 🚀 Quick Start

### Prerequisites

- **macOS** (uses native SMB mounting)
- **Python 3.7+**
- Network access to your NAS
- SMB shares configured on your NAS

### Installation

1. **Clone or download** the repository

2. **Install dependencies:**
```bash
pip install Pillow python-dotenv requests tqdm
```

3. **Create a `.env` file** in the same directory as `snapvault.py`:
```bash
# NAS Connection
NAS_IP=192.168.1.100
NAS_USERNAME=your_username
NAS_PASSWORD=your_password

# Storage Share (long-term archive)
NAS_STORAGE_SHARE=Photos
NAS_STORAGE_PATH=Archive

# Editing Share (SSD for active work)
NAS_EDITING_SHARE=PhotosSSD
NAS_EDITING_PATH=Current

# Local Settings
TEMP_DIR=/tmp/photo_import
MOUNT_BASE=/Volumes
LOG_DIR=logs

# Discord Webhook (optional)
DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_WEBHOOK_URL_HERE
```

4. **Make the script executable** (optional):
```bash
chmod +x snapvault.py
```

---

## 📖 Usage

### Basic Usage

Import photos from SD card to both NAS destinations:
```bash
python3 snapvault.py /Volumes/SD_CARD
```

### Command-Line Arguments

```
positional arguments:
  source                Source directory (SD card path)

optional arguments:
  -h, --help            Show help message and exit
  
  --env-file ENV_FILE   Path to .env file (default: .env)
  
  --destination {both,storage,editing}
                        Copy destination (default: both)
                        - both: Copy to both storage and editing shares
                        - storage: Copy only to long-term storage
                        - editing: Copy only to editing SSD
```

### Examples

**Copy to both destinations (default):**
```bash
python3 snapvault.py /Volumes/SD_CARD
```

**Copy only to long-term storage:**
```bash
python3 snapvault.py /Volumes/SD_CARD --destination storage
```

**Copy only to editing SSD:**
```bash
python3 snapvault.py /Volumes/SD_CARD --destination editing
```

**Use a custom environment file:**
```bash
python3 snapvault.py /Volumes/SD_CARD --env-file ~/.snapvault-home.env
```

---

## 🔧 Configuration

### Environment Variables

| Variable | Required | Description | Example |
|----------|----------|-------------|---------|
| `NAS_IP` | ✅ Yes | IP address of your NAS | `192.168.1.100` |
| `NAS_USERNAME` | ✅ Yes | NAS username for SMB authentication | `admin` |
| `NAS_PASSWORD` | ✅ Yes | NAS password for SMB authentication | `your_password` |
| `NAS_STORAGE_SHARE` | ✅ Yes | SMB share name for long-term storage | `Photos` |
| `NAS_STORAGE_PATH` | ❌ No | Subdirectory within storage share | `Archive` or `` (empty) |
| `NAS_EDITING_SHARE` | ✅ Yes | SMB share name for editing storage | `PhotosSSD` |
| `NAS_EDITING_PATH` | ❌ No | Subdirectory within editing share | `Current` or `` (empty) |
| `TEMP_DIR` | ❌ No | Local temporary directory for organization | `/tmp/photo_import` |
| `MOUNT_BASE` | ❌ No | Base directory for SMB mounts | `/Volumes` |
| `LOG_DIR` | ❌ No | Directory for log files | `logs` |
| `DISCORD_WEBHOOK_URL` | ❌ No | Discord webhook URL for notifications | `https://discord.com/api/webhooks/...` |

### Finding Your NAS Share Names

**Method 1: Finder**
1. Press `Cmd + K` in Finder
2. Enter `smb://YOUR_NAS_IP`
3. The shares you see are your share names

**Method 2: Terminal**
```bash
smbutil view //YOUR_NAS_IP
```

**Method 3: NAS Admin Panel**
- Log into your NAS web interface
- Navigate to Shared Folders or SMB/CIFS settings
- Share names are listed there

### Setting Up Discord Notifications

1. Open your Discord server
2. Go to Server Settings → Integrations → Webhooks
3. Click "New Webhook"
4. Name it (e.g., "SnapVault")
5. Select the channel for notifications
6. Copy the webhook URL
7. Add it to your `.env` file as `DISCORD_WEBHOOK_URL`

---

## 📂 Folder Structure

SnapVault organizes your photos with this structure:

```
2025 - Wedding Smith/
├── 2025-01-15/
│   ├── IMG_0001.JPG
│   ├── IMG_0002.JPG
│   └── IMG_0003.CR2
├── 2025-01-16/
│   ├── IMG_0100.JPG
│   ├── IMG_0101.JPG
│   └── IMG_0102.NEF
└── 2025-01-17/
    ├── IMG_0200.JPG
    └── IMG_0201.ARW
```

**Folder Naming:**
- You enter: `Wedding Smith`
- SnapVault creates: `2025 - Wedding Smith`
- Photos are organized by date into subfolders

---

## 🔔 Discord Notifications

SnapVault sends three types of notifications:

### 1. Job Started 📸
Sent when the import begins:
- Photoshoot name
- Source directory

### 2. Job Completed ✅
Sent when the import finishes successfully:
- Total photo count
- Number of date folders created
- Duration
- Date breakdown (photos per day)

### 3. Error Notification ❌
Sent if an error occurs:
- Error message
- Full traceback (for debugging)
- Context about what failed

---

## 📝 Logging

Logs are automatically created in the `LOG_DIR` directory with timestamps:
```
logs/
├── snapvault_20250108_143022.log
├── snapvault_20250108_150315.log
└── snapvault_20250109_091234.log
```

Each log contains:
- Detailed operation steps
- File processing information
- Mount/unmount operations
- Any warnings or errors
- Timing information

---

## 🖼️ Supported File Formats

SnapVault supports all common camera file formats:

**Image Formats:**
- JPEG (`.jpg`, `.jpeg`)
- PNG (`.png`)
- TIFF (`.tif`, `.tiff`)
- GIF (`.gif`)
- BMP (`.bmp`)
- HEIC (`.heic`)

**RAW Formats:**
- Canon: `.cr2`
- Nikon: `.nef`
- Sony: `.arw`
- Adobe: `.dng`
- Olympus: `.orf`
- Panasonic: `.rw2`
- Fujifilm: `.raf`
- Generic: `.raw`

---

## 🔍 How It Works

1. **Scan SD Card**: SnapVault scans the source directory for all image files
2. **Extract Dates**: Reads EXIF data to determine when each photo was taken (falls back to file modification date if EXIF unavailable)
3. **Organize Locally**: Creates a temporary folder structure organized by date
4. **Mount NAS Shares**: Automatically mounts SMB shares on your Mac
5. **Copy to NAS**: Copies the organized folder to your specified NAS destination(s)
6. **Unmount Shares**: Cleans up by unmounting the SMB shares
7. **Notify**: Sends status updates to Discord throughout the process
8. **Log Everything**: Records all operations to a timestamped log file

---

## ⚠️ Troubleshooting

### "Mount failed" error
- Verify your NAS IP address is correct
- Check that your username and password are correct
- Ensure the share names exist on your NAS
- Try mounting manually: `mount -t smbfs //user:pass@ip/share /Volumes/test`

### "No image files found"
- Verify your SD card is properly mounted
- Check that the source path is correct
- Ensure the SD card contains supported image formats

### Discord notifications not working
- Verify your webhook URL is correct and complete
- Check that the webhook hasn't been deleted in Discord
- Test the webhook manually using curl or Postman

### Permission errors
- Ensure you have write permissions on the NAS shares
- Check that the NAS user account has proper access rights

### Files already exist at destination
- SnapVault skips copying if the destination folder already exists
- Either remove the existing folder or use a different photoshoot name

---

## 💡 Tips & Best Practices

1. **Naming Convention**: Use descriptive names like "Wedding Smith", "Portugal Trip", or "Product Shoot Nike"
2. **Verify Mounts**: Check `/Volumes` to see what's currently mounted before running
3. **Test First**: Run with `--destination editing` first to test without affecting your archive
4. **Keep Logs**: Don't delete log files immediately - they're useful for tracking imports over time
5. **SD Card Safety**: Don't remove the SD card until SnapVault completes and you see the success message
6. **Network Speed**: First-time copies to NAS can take a while depending on network speed and photo count
7. **Temp Files**: Always clean up temp files when prompted unless you need to troubleshoot

---

## 🛠️ Advanced Usage

### Multiple NAS Configurations

Create different `.env` files for different setups:

```bash
# Home studio
python3 snapvault.py /Volumes/SD_CARD --env-file ~/.snapvault-home.env

# Client location
python3 snapvault.py /Volumes/SD_CARD --env-file ~/.snapvault-client.env
```

### Batch Processing

Process multiple SD cards in sequence:
```bash
for card in /Volumes/SD_*; do
    python3 snapvault.py "$card"
done
```

---

## 📄 License

This project is provided as-is for personal and professional use.

---

## 🤝 Contributing

Found a bug or have a feature request? Feel free to:
- Open an issue
- Submit a pull request
- Suggest improvements

---

## 📧 Support

For issues, questions, or feature requests, please check:
1. This README for configuration help
2. Log files in the `logs/` directory
3. Discord error notifications for detailed error messages

---

**Happy Shooting! 📸**