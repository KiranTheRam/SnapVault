#!/usr/bin/env python3
"""
SnapVault - Professional Camera Photo Organizer

Detects an SD card, collects metadata, and streams photos directly to NAS SMB
shares organized by date with Discord notifications and detailed logging.
"""

import argparse
import json
import logging
import os
import shutil
import subprocess
import tempfile
from collections import defaultdict
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path

import requests
from dotenv import load_dotenv
from PIL import Image
from PIL.ExifTags import TAGS
from tqdm import tqdm


class DiscordNotifier:
    """Handle Discord webhook notifications."""

    def __init__(self, webhook_url):
        self.webhook_url = webhook_url
        self.enabled = bool(webhook_url)

    def send_message(self, title, description, color=0x5865F2, fields=None):
        """Send an embed message to Discord."""
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
        except Exception as exc:  # pragma: no cover - network failures logged
            logging.error("Failed to send Discord notification: %s", exc)

    def send_start(self, folder_name, source_path):
        """Send job start notification."""
        self.send_message(
            title="📸 SnapVault Started",
            description=f"Processing photoshoot: **{folder_name}**",
            color=0x5865F2,
            fields=[{"name": "Source", "value": str(source_path), "inline": False}]
        )

    def send_success(self, folder_name, stats):
        """Send job completion notification."""
        fields = [
            {"name": "📊 Total Photos", "value": str(stats['total_photos']), "inline": True},
            {"name": "📅 Date Folders", "value": str(stats['date_folders']), "inline": True},
            {"name": "⏱️ Duration", "value": stats['duration'], "inline": True}
        ]

        if stats.get('date_breakdown'):
            breakdown = "\n".join(
                f"{date}: {count} photos"
                for date, count in sorted(stats['date_breakdown'].items())
            )
            fields.append({"name": "Date Breakdown", "value": f"```{breakdown}```", "inline": False})

        self.send_message(
            title="✅ SnapVault Completed",
            description=f"Successfully processed: **{folder_name}**",
            color=0x57F287,
            fields=fields
        )

    def send_error(self, folder_name, error_msg, traceback=None):
        """Send error notification."""
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
    """Configure logging to both file and console."""
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
    """Load configuration from environment variables."""
    load_dotenv()

    config = {
        'nas_ip': os.getenv('NAS_IP'),
        'nas_username': os.getenv('NAS_USERNAME'),
        'nas_password': os.getenv('NAS_PASSWORD'),
        'nas_domain': os.getenv('NAS_DOMAIN'),
        'nas_storage_share': os.getenv('NAS_STORAGE_SHARE'),
        'nas_storage_path': os.getenv('NAS_STORAGE_PATH', ''),
        'nas_editing_share': os.getenv('NAS_EDITING_SHARE'),
        'nas_editing_path': os.getenv('NAS_EDITING_PATH', ''),
        'log_dir': os.getenv('LOG_DIR', 'logs'),
        'discord_webhook': os.getenv('DISCORD_WEBHOOK_URL')
    }

    required = ['nas_ip', 'nas_username', 'nas_password',
                'nas_storage_share', 'nas_editing_share']
    missing = [key for key in required if not config[key]]
    if missing:
        raise ValueError(f"Missing required environment variables: {', '.join(missing)}")

    return config


def ensure_smbclient_available():
    """Ensure smbclient is present for user-space SMB transfers."""
    if shutil.which("smbclient") is None:
        raise RuntimeError(
            "smbclient command not found. Install Samba client tools (e.g., 'sudo apt install smbclient')."
        )


def escape_for_smb(value):
    """Escape a filesystem path for smbclient command usage."""
    return str(value).replace("\\", "/").replace('"', r'\"')


def join_remote_path(*parts):
    """Join path segments for SMB operations without introducing duplicate separators."""
    cleaned = [str(part).strip("/\\") for part in parts if part]
    return "/".join(cleaned)


@contextmanager
def smb_credentials_file(username, password, domain=None):
    """Write credentials to a temporary file for smbclient and remove afterwards."""
    fd, path = tempfile.mkstemp(prefix="snapvault_", suffix=".cred")
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as cred_file:
            cred_file.write(f"username={username}\n")
            cred_file.write(f"password={password}\n")
            if domain:
                cred_file.write(f"domain={domain}\n")
        os.chmod(path, 0o600)
        yield path
    finally:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass


def run_smbclient(nas_ip, share_name, creds_path, commands, context=""):
    """Execute an smbclient command against a share and surface failures."""
    command_list = [
        "smbclient",
        f"//{nas_ip}/{share_name}",
        "-A",
        creds_path,
        "-m",
        "SMB3",
        "-c",
        commands
    ]
    logging.debug("Running smbclient (%s): %s", context or "cmd", commands)
    result = subprocess.run(command_list, capture_output=True, text=True)

    if result.stdout:
        logging.debug("smbclient stdout (%s): %s", context or "cmd", result.stdout.strip())
    if result.stderr:
        logging.debug("smbclient stderr (%s): %s", context or "cmd", result.stderr.strip())

    if result.returncode != 0:
        error_text = result.stderr.strip() or result.stdout.strip() or "Unknown SMB error"
        raise RuntimeError(f"smbclient command failed for {share_name}: {error_text}")


def ensure_remote_directory(nas_ip, share_name, creds_path, remote_path, created_cache):
    """Create nested remote directories as needed, caching successes."""
    if not remote_path:
        return

    parts = [segment for segment in remote_path.split('/') if segment]
    current_segments = []

    for segment in parts:
        current_segments.append(segment)
        current_path = "/".join(current_segments)
        cache_key = (share_name, current_path)
        if cache_key in created_cache:
            continue

        try:
            run_smbclient(
                nas_ip,
                share_name,
                creds_path,
                f'mkdir "{escape_for_smb(current_path)}"',
                context=f"mkdir {current_path}"
            )
        except RuntimeError as exc:
            collision_markers = ("OBJECT_NAME_COLLISION", "FILE_EXISTS", "OBJECT_NAME_EXISTS")
            if not any(marker in str(exc) for marker in collision_markers):
                logging.error("Failed to create directory %s on %s: %s", current_path, share_name, exc)
                raise
        finally:
            created_cache.add(cache_key)


def resolve_remote_filename(registry, share_name, remote_dir, original_name):
    """Generate a unique filename within a remote directory for this session."""
    key = (share_name, remote_dir)
    used_names = registry.setdefault(key, set())

    candidate = original_name
    stem = Path(original_name).stem
    suffix = Path(original_name).suffix
    counter = 1

    while candidate in used_names:
        candidate = f"{stem}_{counter}{suffix}"
        counter += 1

    used_names.add(candidate)
    return candidate


def upload_file_to_share(nas_ip, share_name, creds_path, local_path, remote_path):
    """Upload a single file from the SD card to the NAS share."""
    sanitized_remote = escape_for_smb(remote_path)
    sanitized_local = escape_for_smb(local_path)
    command = f'prompt OFF; put "{sanitized_local}" "{sanitized_remote}"'
    run_smbclient(
        nas_ip,
        share_name,
        creds_path,
        command,
        context=f"put {remote_path}"
    )


def list_removable_mounts():
    """Return mounted paths for removable devices detected by lsblk."""
    try:
        result = subprocess.run(
            ["lsblk", "-J", "-o", "NAME,RM,MOUNTPOINT,MOUNTPOINTS,TYPE,TRAN"],
            capture_output=True,
            text=True,
            check=True
        )
    except subprocess.CalledProcessError as exc:
        logging.warning("lsblk failed to enumerate removable devices: %s", exc.stderr)
        return []

    mounts = set()
    payload = json.loads(result.stdout)

    def walk(node, removable=False):
        node_rm = removable or bool(node.get('rm'))
        mountpoints = []
        mountpoint = node.get('mountpoint')
        if isinstance(mountpoint, str):
            mountpoints.append(mountpoint)
        mountpoint_list = node.get('mountpoints')
        if isinstance(mountpoint_list, list):
            mountpoints.extend([mp for mp in mountpoint_list if mp])

        if node_rm:
            for mp in mountpoints:
                if mp and mp not in ('/', '[SWAP]'):
                    mounts.add(mp)

        for child in node.get('children', []):
            walk(child, node_rm)

    for device in payload.get('blockdevices', []):
        walk(device)

    return sorted(mounts)


def detect_sd_card_path(manual_override=None):
    """Detect or prompt for the SD card mount path."""
    if manual_override:
        candidate = Path(manual_override).expanduser()
        if candidate.is_dir():
            return candidate
        raise FileNotFoundError(f"Specified source path does not exist: {manual_override}")

    mounts = list_removable_mounts()

    if mounts:
        if len(mounts) == 1:
            print(f"Detected SD card mount: {mounts[0]}")
            return Path(mounts[0])

        print("Detected removable volumes:")
        for idx, mount in enumerate(mounts, start=1):
            print(f"  {idx}. {mount}")

        while True:
            choice = input("Select a mount number or enter a path: ").strip()
            if choice.isdigit():
                index = int(choice)
                if 1 <= index <= len(mounts):
                    return Path(mounts[index - 1])
            if choice:
                candidate = Path(choice).expanduser()
                if candidate.is_dir():
                    return candidate
            print("Invalid selection. Please try again.")

    while True:
        manual = input("No SD card auto-detected. Enter the mount path manually: ").strip()
        if manual:
            candidate = Path(manual).expanduser()
            if candidate.is_dir():
                return candidate
        print("Path not found. Please try again.")


def get_date_taken(file_path):
    """Extract the date the image was captured using EXIF or fallback to modification time."""
    try:
        image = Image.open(file_path)
        exif_data = image._getexif()
        if exif_data:
            for tag_id, value in exif_data.items():
                tag = TAGS.get(tag_id, tag_id)
                if tag == 'DateTimeOriginal':
                    date_obj = datetime.strptime(value, '%Y:%m:%d %H:%M:%S')
                    return date_obj.strftime('%Y-%m-%d')
    except Exception as exc:
        logging.debug("Could not read EXIF from %s: %s", file_path, exc)

    mod_time = os.path.getmtime(file_path)
    return datetime.fromtimestamp(mod_time).strftime('%Y-%m-%d')


def get_image_files(source_dir):
    """Gather all supported image files from the SD card."""
    image_extensions = {'.jpg', '.jpeg', '.png', '.raw', '.cr2', '.nef',
                        '.arw', '.dng', '.orf', '.rw2', '.raf', '.heic',
                        '.tif', '.tiff', '.gif', '.bmp'}

    files = []
    source_path = Path(source_dir)
    logging.info("Scanning for images in %s", source_dir)

    for file in source_path.rglob('*'):
        if file.is_file() and file.suffix.lower() in image_extensions:
            files.append(file)

    logging.info("Found %d image files", len(files))
    return sorted(files)


def transfer_photos(source_root, folder_name, config, destination_filter):
    """Copy photos directly from the SD card to NAS destinations."""
    image_files = get_image_files(source_root)
    if not image_files:
        raise ValueError("No supported image files found on the SD card.")

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

    if destination_filter == 'storage':
        destinations = [d for d in all_destinations if d['filter'] == 'storage']
    elif destination_filter == 'editing':
        destinations = [d for d in all_destinations if d['filter'] == 'editing']
    else:
        destinations = all_destinations

    if not destinations:
        raise ValueError("No NAS destinations selected for transfer.")

    print("\n📤 Transferring photos directly to NAS shares...")
    for dest in destinations:
        remote_display = join_remote_path(dest['share'], dest['path'])
        print(f"  • {dest['name']}: //{config['nas_ip']}/{remote_display}")

    created_dirs = set()
    name_registry = defaultdict(set)
    date_counts = defaultdict(int)

    total_copies = len(image_files) * len(destinations)

    with smb_credentials_file(
        config['nas_username'],
        config['nas_password'],
        config.get('nas_domain')
    ) as creds_path:
        progress = tqdm(total=total_copies, desc="Transferring", unit="copy")

        for file_path in image_files:
            date_str = get_date_taken(file_path)
            date_counts[date_str] += 1

            for dest in destinations:
                remote_dir = join_remote_path(dest['path'], folder_name, date_str)
                ensure_remote_directory(
                    config['nas_ip'],
                    dest['share'],
                    creds_path,
                    remote_dir,
                    created_dirs
                )

                remote_name = resolve_remote_filename(
                    name_registry,
                    dest['share'],
                    remote_dir,
                    file_path.name
                )
                remote_path = join_remote_path(remote_dir, remote_name)

                upload_file_to_share(
                    config['nas_ip'],
                    dest['share'],
                    creds_path,
                    str(file_path),
                    remote_path
                )
                progress.update(1)

        progress.close()

    stats = {
        'total_photos': len(image_files),
        'date_folders': len(date_counts),
        'date_breakdown': dict(date_counts)
    }

    logging.info("Transfer summary: %s", stats)
    return stats


def main():
    parser = argparse.ArgumentParser(
        description='SnapVault - Professional Camera Photo Organizer'
    )
    parser.add_argument('--source', help='Override the auto-detected SD card path')
    parser.add_argument('--env-file', default='.env',
                        help='Path to .env file (default: .env)')
    parser.add_argument('--destination', choices=['both', 'storage', 'editing'],
                        default='both',
                        help='Copy destination: both (default), storage only, or editing only')

    args = parser.parse_args()

    if args.env_file != '.env':
        load_dotenv(args.env_file)

    try:
        config = load_config()
    except ValueError as exc:
        print(f"❌ Configuration Error: {exc}")
        print("\nPlease create a .env file with the required variables.")
        return

    log_file = setup_logging(config['log_dir'])
    logging.info("=" * 60)
    logging.info("SnapVault started")
    logging.info("Log file: %s", log_file)

    try:
        ensure_smbclient_available()
    except RuntimeError as exc:
        logging.error(exc)
        print(f"❌ {exc}")
        return

    discord = DiscordNotifier(config['discord_webhook'])

    try:
        source_path = detect_sd_card_path(args.source)
    except Exception as exc:
        error_msg = f"Unable to locate SD card: {exc}"
        logging.error(error_msg)
        print(f"❌ Error: {error_msg}")
        discord.send_error("Unknown", error_msg)
        return

    if not source_path.is_dir():
        error_msg = f"Source directory does not exist: {source_path}"
        logging.error(error_msg)
        print(f"❌ Error: {error_msg}")
        discord.send_error("Unknown", error_msg)
        return

    print("=" * 60)
    print("📸 SnapVault - Professional Photo Organizer")
    print("=" * 60)
    print(f"NAS: {config['nas_ip']}")
    print(f"Storage Share: {config['nas_storage_share']}")
    print(f"Editing Share: {config['nas_editing_share']}")
    print(f"Source: {source_path}")
    print(f"Destination filter: {args.destination}")
    print(f"Log: {log_file}")

    folder_name = "Unknown"
    folder_input = input("\n📝 Enter folder name for this photoshoot: ").strip()

    if not folder_input:
        error_msg = "Folder name cannot be empty"
        logging.error(error_msg)
        print(f"❌ Error: {error_msg}")
        discord.send_error(folder_name, error_msg)
        return

    folder_name = f"{datetime.now().year} - {folder_input}"
    print(f"  → Full folder name: {folder_name}")
    logging.info("Photoshoot name: %s", folder_name)

    discord.send_start(folder_name, source_path)

    start_time = datetime.now()

    try:
        stats = transfer_photos(source_path, folder_name, config, args.destination)
        duration = datetime.now() - start_time
        stats['duration'] = str(duration).split('.')[0]

        logging.info("Job completed in %s", stats['duration'])

        discord.send_success(folder_name, stats)

        print("\n✨ Transfer complete!")
        logging.info("SnapVault completed successfully")

    except Exception as exc:
        import traceback

        error_msg = str(exc)
        tb = traceback.format_exc()

        logging.error("Fatal error: %s", error_msg)
        logging.error(tb)

        print(f"\n❌ Error: {error_msg}")
        print(f"Check log file for details: {log_file}")

        discord.send_error(folder_name, error_msg, tb)


if __name__ == '__main__':
    main()
