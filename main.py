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
import urllib.parse
import time

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
        'log_dir': os.getenv('LOG_DIR', 'logs'),
        'discord_webhook': os.getenv('DISCORD_WEBHOOK_URL')
    }

    # Validate required fields
    required = ['nas_ip', 'nas_username', 'nas_password',
                'nas_storage_share', 'nas_editing_share']

    missing = [k for k in required if not config[k]]
    if missing:
        raise ValueError(f"Missing required environment variables: {', '.join(missing)}")

    return config


def copy_folder_to_smb(source_folder, nas_ip, username, password, share_name, remote_path):
    """
    Copy a folder to an SMB share by temporarily mounting it (Linux version) and using rsync.
    """
    source_path = Path(source_folder)
    folder_name = source_path.name
    mount_point = Path("/tmp") / f"snapvault_mount_{share_name.replace(' ', '_')}"

    logging.info(f"Preparing to mount SMB share {share_name} at {mount_point}")

    # Check if mount point already exists and unmount if necessary
    if mount_point.exists():
        mount_check = subprocess.run(['mount'], capture_output=True, text=True)
        if str(mount_point) in mount_check.stdout:
            logging.info(f"Mount point {mount_point} is already mounted, unmounting first...")
            try:
                subprocess.run(['umount', str(mount_point)], check=True, capture_output=True)
                logging.info("Successfully unmounted existing mount")
            except subprocess.CalledProcessError:
                logging.warning("Regular unmount failed, trying lazy unmount...")
                try:
                    subprocess.run(['umount', '-l', str(mount_point)], check=True, capture_output=True)
                    logging.info("Lazy unmount successful")
                except subprocess.CalledProcessError as e:
                    logging.error(f"Could not unmount {mount_point}: {e}")
                    return False

        # Try to remove the directory if it exists
        try:
            mount_point.rmdir()
        except OSError:
            pass

    # Wait for unmount to finalize
    for _ in range(5):
        mount_check = subprocess.run(['mount'], capture_output=True, text=True)
        if str(mount_point) not in mount_check.stdout:
            break
        logging.info(f"Waiting for {mount_point} to fully unmount...")
        time.sleep(1)
    else:
        logging.error(f"Mount point {mount_point} still appears mounted after retries.")
        return False

    if mount_point.exists():
        try:
            shutil.rmtree(mount_point)
        except Exception as e:
            logging.warning(f"Could not remove stale mount point: {e}")

    mount_point.mkdir(parents=True, exist_ok=True)

    print(f"  Mounting {share_name}...")

    # Mount command for Linux using CIFS
    share_path = f"//{nas_ip}/{share_name}"

    mount_cmd = [
        "mount", "-t", "cifs", share_path, str(mount_point),
        "-o", f"username={username},password={password},rw,vers=3.0,nounix,noserverino,iocharset=utf8"
    ]

    logging.info(f"Mounting: //{username}:***@{nas_ip}/{share_name}")

    try:
        subprocess.run(mount_cmd, capture_output=True, text=True, check=True)
        logging.info("SMB share mounted successfully")
    except subprocess.CalledProcessError as e:
        logging.error(f"Failed to mount SMB share: {e.stderr}")
        print(f"  ❌ Failed to mount SMB share")
        return False

    try:
        # Create destination path inside mounted share
        dest_path = mount_point
        if remote_path:
            dest_path = dest_path / remote_path
            dest_path.mkdir(parents=True, exist_ok=True)

        final_dest = dest_path / folder_name

        if final_dest.exists():
            logging.warning(f"Destination already exists: {final_dest}")
            print(f"  ⚠️  Destination folder already exists, skipping...")
            return True

        final_dest.mkdir(exist_ok=True)

        # Rsync for copying
        rsync_cmd = [
            "rsync", "-avh", "--progress",
            str(source_path) + "/", str(final_dest) + "/"
        ]

        logging.info(f"Running rsync: rsync -avh --progress <source> <dest>")
        print(f"  Copying files...")

        process = subprocess.Popen(
            rsync_cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1
        )

        for line in iter(process.stdout.readline, ''):
            if not line:
                break
            line = line.strip()
            if line:
                if 'to-check' in line or '%' in line:
                    print(f"  {line}", end='\r')

        process.wait()
        stderr = process.stderr.read()

        if process.returncode == 0:
            logging.info("Rsync completed successfully")
            print(f"  ✓ Transfer complete" + " " * 50)
            return True
        else:
            logging.error(f"Rsync failed: {stderr}")
            print(f"  ❌ Transfer failed: {stderr}")
            return False

    except Exception as e:
        logging.error(f"Copy operation failed: {e}")
        print(f"  ❌ Copy failed: {e}")
        return False

    finally:
        print(f"  Unmounting {share_name}...")
        try:
            subprocess.run(["umount", str(mount_point)], check=True, capture_output=True)
            logging.info(f"Unmounted {mount_point}")
            mount_point.rmdir()
        except subprocess.CalledProcessError as e:
            logging.warning(f"Could not unmount {share_name}: {e}")
            try:
                subprocess.run(["umount", "-l", str(mount_point)], capture_output=True)
                mount_point.rmdir()
            except Exception:
                pass
        except Exception as e:
            logging.warning(f"Cleanup error: {e}")



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

                # IMPORTANT: Copy files, never move or delete from source (SD card)
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

        # Construct destination path
        if dest['path']:
            remote_path = dest['path']
        else:
            remote_path = ""

        dest_display = f"//{config['nas_ip']}/{dest['share']}/{remote_path}/{folder_name}".replace("//", "/").replace(
            ":/", "://")
        print(f"  Copying to: {dest_display}")

        try:
            success = copy_folder_to_smb(
                source_folder,
                config['nas_ip'],
                config['nas_username'],
                config['nas_password'],
                dest['share'],
                remote_path
            )

            if success:
                logging.info(f"Successfully copied to {dest['share']}")
                print(f"  ✓ Copied successfully")
            else:
                error_msg = f"Failed to copy to {dest['share']}"
                logging.error(error_msg)
                raise Exception(error_msg)

        except Exception as e:
            logging.error(f"Copy failed: {e}")
            raise

    print(f"\n✅ All copies completed!")


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

    # Create temporary directory in project folder
    script_dir = Path(__file__).parent
    temp_dir = script_dir / 'temp_organize'
    temp_dir.mkdir(exist_ok=True)

    organized_folder = None

    try:
        # Organize photos in local temp directory
        organized_folder, stats = organize_photos(args.source, temp_dir, folder_name)

        if not organized_folder:
            raise Exception("Failed to organize photos - no images found")

        # Copy to NAS locations
        copy_to_destinations(organized_folder, config, args.destination)

        # Calculate duration
        duration = datetime.now() - start_time
        duration_str = str(duration).split('.')[0]  # Remove microseconds
        stats['duration'] = duration_str

        logging.info(f"Job completed in {duration_str}")

        # Send success notification
        discord.send_success(folder_name, stats)

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

    finally:
        # ALWAYS clean up temporary files (but NEVER touch the SD card source)
        if organized_folder and organized_folder.exists():
            try:
                print(f"\n🗑️  Cleaning up temporary files...")
                shutil.rmtree(organized_folder)
                logging.info(f"Cleaned up temporary files from {organized_folder}")
                print(f"  ✓ Temporary files removed")
            except Exception as e:
                logging.warning(f"Could not remove temporary files: {e}")
                print(f"  ⚠️  Could not remove temporary files: {e}")

        # Remove temp directory if empty
        try:
            if temp_dir.exists() and not any(temp_dir.iterdir()):
                temp_dir.rmdir()
        except Exception:
            pass


if __name__ == '__main__':
    main()