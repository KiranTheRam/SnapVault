#!/usr/bin/env python3
"""
SnapVault - Professional Camera Photo Organizer
Organizes photos by date and copies to NAS via SMB with Discord notifications
"""

import os
import shutil
from pathlib import Path
from datetime import datetime
from PIL import Image
from PIL.ExifTags import TAGS
import argparse
from dotenv import load_dotenv
import subprocess
import requests
import json
import logging
from tqdm import tqdm


class DiscordNotifier:
    """Handle Discord webhook notifications"""

    def __init__(self, webhook_url):
        self.webhook_url = webhook_url
        self.enabled = bool(webhook_url)

    def send_message(self, title, description, color=0x5865F2, fields=None):
        """Send an embed message to Discord"""
        if not self.enabled:
            return

        embed = {
            "title": title,
            "description": description,
            "color": color,
            "timestamp": datetime.utcnow().isoformat(),
            "footer": {"text": "SnapVault"}
        }

        if fields:
            embed["fields"] = fields

        payload = {"embeds": [embed]}

        try:
            requests.post(self.webhook_url, json=payload, timeout=10)
        except Exception as e:
            logging.error(f"Failed to send Discord notification: {e}")

    def send_start(self, folder_name, source_path):
        """Send job start notification"""
        self.send_message(
            title="📸 SnapVault Started",
            description=f"Processing photoshoot: **{folder_name}**",
            color=0x5865F2,
            fields=[
                {"name": "Source", "value": source_path, "inline": False}
            ]
        )

    def send_success(self, folder_name, stats):
        """Send job completion notification"""
        fields = [
            {"name": "📊 Total Photos", "value": str(stats['total_photos']), "inline": True},
            {"name": "📅 Date Folders", "value": str(stats['date_folders']), "inline": True},
            {"name": "⏱️ Duration", "value": stats['duration'], "inline": True}
        ]

        if stats.get('date_breakdown'):
            breakdown = "\n".join([f"{date}: {count} photos"
                                   for date, count in sorted(stats['date_breakdown'].items())])
            fields.append({"name": "Date Breakdown", "value": f"```{breakdown}```", "inline": False})

        self.send_message(
            title="✅ SnapVault Completed",
            description=f"Successfully processed: **{folder_name}**",
            color=0x57F287,
            fields=fields
        )

    def send_error(self, folder_name, error_msg, traceback=None):
        """Send error notification"""
        description = f"Failed while processing: **{folder_name}**\n\n**Error:**\n```{error_msg}```"

        fields = []
        if traceback:
            fields.append({
                "name": "Traceback",
                "value": f"```{traceback[:1000]}```",
                "inline": False
            })

        self.send_message(
            title="❌ SnapVault Error",
            description=description,
            color=0xED4245,
            fields=fields
        )


def setup_logging(log_dir='logs'):
    """Setup logging to file"""
    log_path = Path(log_dir)
    log_path.mkdir(exist_ok=True)

    log_file = log_path / f"snapvault_{datetime.now().strftime('%Y%m%d_%H%M%S')}.log"

    logging.basicConfig(
        level=logging.INFO,
        format='%(asctime)s - %(levelname)s - %(message)s',
        handlers=[
            logging.FileHandler(log_file),
            logging.StreamHandler()
        ]
    )

    return log_file


def load_config():
    """Load configuration from .env file"""
    load_dotenv()

    config = {
        'nas_ip': os.getenv('NAS_IP'),
        'nas_username': os.getenv('NAS_USERNAME'),
        'nas_password': os.getenv('NAS_PASSWORD'),
        'nas_storage_share': os.getenv('NAS_STORAGE_SHARE'),
        'nas_storage_path': os.getenv('NAS_STORAGE_PATH', ''),
        'nas_editing_share': os.getenv('NAS_EDITING_SHARE'),
        'nas_editing_path': os.getenv('NAS_EDITING_PATH', ''),
        'temp_dir': os.getenv('TEMP_DIR', '/tmp/photo_import'),
        'mount_base': os.getenv('MOUNT_BASE', '/Volumes'),
        'discord_webhook': os.getenv('DISCORD_WEBHOOK_URL'),
        'log_dir': os.getenv('LOG_DIR', 'logs')
    }

    # Validate required fields
    required = ['nas_ip', 'nas_username', 'nas_password',
                'nas_storage_share', 'nas_editing_share']

    missing = [k for k in required if not config[k]]
    if missing:
        raise ValueError(f"Missing required environment variables: {', '.join(missing)}")

    return config


def mount_smb_share(nas_ip, username, password, share_name, mount_point):
    """Mount an SMB share on macOS"""
    mount_path = Path(mount_point)

    if mount_path.exists() and mount_path.is_mount():
        logging.info(f"Share already mounted at {mount_path}")
        return True

    mount_path.mkdir(parents=True, exist_ok=True)
    smb_url = f"smb://{username}:{password}@{nas_ip}/{share_name}"

    logging.info(f"Mounting {share_name} at {mount_path}")

    try:
        result = subprocess.run(
            ['mount', '-t', 'smbfs', smb_url, str(mount_path)],
            capture_output=True,
            text=True,
            timeout=30
        )

        if result.returncode == 0:
            logging.info(f"Successfully mounted {share_name}")
            return True
        else:
            logging.error(f"Mount failed: {result.stderr}")
            return False

    except subprocess.TimeoutExpired:
        logging.error(f"Mount timed out for {share_name}")
        return False
    except Exception as e:
        logging.error(f"Mount error: {e}")
        return False


def unmount_share(mount_point):
    """Unmount an SMB share"""
    mount_path = Path(mount_point)

    if not mount_path.exists() or not mount_path.is_mount():
        return True

    try:
        result = subprocess.run(
            ['umount', str(mount_path)],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            logging.info(f"Unmounted {mount_path}")
        return result.returncode == 0
    except Exception as e:
        logging.warning(f"Could not unmount {mount_path}: {e}")
        return False


def get_date_taken(file_path):
    """Extract the date taken from image EXIF data"""
    try:
        image = Image.open(file_path)
        exif_data = image._getexif()

        if exif_data:
            for tag_id, value in exif_data.items():
                tag = TAGS.get(tag_id, tag_id)
                if tag == 'DateTimeOriginal':
                    date_obj = datetime.strptime(value, '%Y:%m:%d %H:%M:%S')
                    return date_obj.strftime('%Y-%m-%d')
    except Exception as e:
        logging.debug(f"Could not read EXIF from {file_path.name}: {e}")

    # Fallback to file modification date
    mod_time = os.path.getmtime(file_path)
    return datetime.fromtimestamp(mod_time).strftime('%Y-%m-%d')


def get_image_files(source_dir):
    """Get all image files from the source directory"""
    image_extensions = {'.jpg', '.jpeg', '.png', '.raw', '.cr2', '.nef',
                        '.arw', '.dng', '.orf', '.rw2', '.raf', '.heic',
                        '.tif', '.tiff', '.gif', '.bmp'}

    files = []
    source_path = Path(source_dir)

    logging.info(f"Scanning for images in {source_dir}")

    for file in source_path.rglob('*'):
        if file.is_file() and file.suffix.lower() in image_extensions:
            files.append(file)

    logging.info(f"Found {len(files)} image files")
    return files


def organize_photos(source_dir, dest_dir, folder_name):
    """Organize photos into dated subfolders"""
    top_level = Path(dest_dir) / folder_name
    top_level.mkdir(parents=True, exist_ok=True)

    logging.info(f"Organizing photos into {top_level}")
    print(f"\n📁 Organizing photos into: {top_level}")

    image_files = get_image_files(source_dir)

    if not image_files:
        logging.warning("No image files found")
        print("⚠️  No image files found in source directory!")
        return None, {}

    date_counts = {}

    # Progress bar for organizing
    with tqdm(total=len(image_files), desc="Organizing", unit="photo") as pbar:
        for img_file in image_files:
            try:
                date_str = get_date_taken(img_file)
                date_folder = top_level / date_str
                date_folder.mkdir(exist_ok=True)

                dest_file = date_folder / img_file.name

                # Handle duplicate filenames
                counter = 1
                while dest_file.exists():
                    stem = img_file.stem
                    suffix = img_file.suffix
                    dest_file = date_folder / f"{stem}_{counter}{suffix}"
                    counter += 1

                shutil.copy2(img_file, dest_file)
                date_counts[date_str] = date_counts.get(date_str, 0) + 1

                logging.debug(f"Copied {img_file.name} to {date_str}/")
                pbar.update(1)

            except Exception as e:
                logging.error(f"Failed to process {img_file.name}: {e}")
                pbar.update(1)

    # Print summary
    print(f"\n📊 Organization Summary:")
    print(f"  Total photos: {len(image_files)}")
    print(f"  Date folders created: {len(date_counts)}")
    for date, count in sorted(date_counts.items()):
        print(f"    {date}: {count} photos")

    stats = {
        'total_photos': len(image_files),
        'date_folders': len(date_counts),
        'date_breakdown': date_counts
    }

    return top_level, stats


def copy_to_destinations(source_folder, config, destination_filter='both'):
    """Copy the organized folder to NAS destinations via SMB"""
    folder_name = source_folder.name
    mount_base = Path(config['mount_base'])

    print(f"\n📤 Copying to NAS destinations...")
    logging.info("Starting NAS copy operations")

    all_destinations = [
        {
            'name': 'Long-term storage',
            'share': config['nas_storage_share'],
            'path': config['nas_storage_path'],
            'filter': 'storage'
        },
        {
            'name': 'Editing SSD',
            'share': config['nas_editing_share'],
            'path': config['nas_editing_path'],
            'filter': 'editing'
        }
    ]

    # Filter destinations based on argument
    if destination_filter == 'storage':
        destinations = [d for d in all_destinations if d['filter'] == 'storage']
    elif destination_filter == 'editing':
        destinations = [d for d in all_destinations if d['filter'] == 'editing']
    else:
        destinations = all_destinations

    for idx, dest in enumerate(destinations, 1):
        print(f"\n{idx}️⃣  {dest['name']} ({dest['share']}):")
        logging.info(f"Processing {dest['name']}")

        mount_point = mount_base / dest['share']

        if mount_smb_share(config['nas_ip'], config['nas_username'],
                           config['nas_password'], dest['share'], mount_point):

            dest_path = mount_point / dest['path'] / folder_name
            dest_path = dest_path.resolve()

            print(f"  Copying to: {dest_path}")

            if dest_path.exists():
                logging.warning(f"Destination exists: {dest_path}")
                print(f"  ⚠️  Destination exists, skipping...")
            else:
                try:
                    dest_path.parent.mkdir(parents=True, exist_ok=True)

                    # Progress bar for copying
                    print(f"  Copying files...")
                    shutil.copytree(source_folder, dest_path)

                    logging.info(f"Successfully copied to {dest_path}")
                    print(f"  ✓ Copied successfully")
                except Exception as e:
                    logging.error(f"Copy failed: {e}")
                    raise
        else:
            error_msg = f"Failed to mount {dest['share']}"
            logging.error(error_msg)
            raise Exception(error_msg)

    print(f"\n✅ All copies completed!")

    # Unmount shares
    print(f"\n🔌 Unmounting shares...")
    for dest in destinations:
        mount_point = mount_base / dest['share']
        unmount_share(mount_point)


def main():
    parser = argparse.ArgumentParser(
        description='SnapVault - Professional Camera Photo Organizer'
    )
    parser.add_argument('source', help='Source directory (SD card path)')
    parser.add_argument('--env-file', default='.env',
                        help='Path to .env file (default: .env)')
    parser.add_argument('--destination', choices=['both', 'storage', 'editing'],
                        default='both',
                        help='Copy destination: both (default), storage only, or editing only')

    args = parser.parse_args()

    # Load environment configuration
    if args.env_file != '.env':
        load_dotenv(args.env_file)

    try:
        config = load_config()
    except ValueError as e:
        print(f"❌ Configuration Error: {e}")
        print(f"\nPlease create a .env file with the required variables.")
        return

    # Setup logging
    log_file = setup_logging(config['log_dir'])
    logging.info("=" * 60)
    logging.info("SnapVault started")
    logging.info(f"Log file: {log_file}")

    # Initialize Discord notifier
    discord = DiscordNotifier(config['discord_webhook'])

    # Verify source exists
    if not os.path.exists(args.source):
        error_msg = f"Source directory does not exist: {args.source}"
        logging.error(error_msg)
        print(f"❌ Error: {error_msg}")
        discord.send_error("Unknown", error_msg)
        return

    print("=" * 60)
    print("📸 SnapVault - Professional Photo Organizer")
    print("=" * 60)
    print(f"NAS: {config['nas_ip']}")
    print(f"Storage: {config['nas_storage_share']}")
    print(f"Editing: {config['nas_editing_share']}")
    print(f"Destination: {args.destination}")
    print(f"Log: {log_file}")

    # Ask for folder name and prepend with current year
    folder_input = input("\n📝 Enter folder name for this photoshoot: ").strip()

    if not folder_input:
        error_msg = "Folder name cannot be empty"
        logging.error(error_msg)
        print(f"❌ Error: {error_msg}")
        discord.send_error("Unknown", error_msg)
        return

    # Prepend current year
    current_year = datetime.now().year
    folder_name = f"{current_year} - {folder_input}"
    print(f"  → Full folder name: {folder_name}")

    logging.info(f"Photoshoot name: {folder_name}")

    # Send start notification
    discord.send_start(folder_name, args.source)

    start_time = datetime.now()

    try:
        # Organize photos
        organized_folder, stats = organize_photos(args.source, config['temp_dir'], folder_name)

        if not organized_folder:
            raise Exception("Failed to organize photos - no images found")

        # Copy to both NAS locations via SMB
        copy_to_destinations(organized_folder, config, args.destination)

        # Calculate duration
        duration = datetime.now() - start_time
        duration_str = str(duration).split('.')[0]  # Remove microseconds
        stats['duration'] = duration_str

        logging.info(f"Job completed in {duration_str}")

        # Send success notification
        discord.send_success(folder_name, stats)

        # Ask if user wants to clean up temp folder
        cleanup = input(f"\n🗑️  Remove temporary files from {organized_folder}? (y/n): ")
        if cleanup.lower() == 'y':
            shutil.rmtree(organized_folder)
            logging.info("Temporary files removed")
            print("  ✓ Temporary files removed")
        else:
            logging.info(f"Temporary files kept at {organized_folder}")
            print(f"  ℹ️  Temporary files kept at: {organized_folder}")

        print("\n✨ Import complete!")
        logging.info("SnapVault completed successfully")

    except Exception as e:
        import traceback
        error_msg = str(e)
        tb = traceback.format_exc()

        logging.error(f"Fatal error: {error_msg}")
        logging.error(tb)

        print(f"\n❌ Error: {error_msg}")
        print(f"Check log file for details: {log_file}")

        # Send error notification
        discord.send_error(folder_name, error_msg, tb)


if __name__ == '__main__':
    main()