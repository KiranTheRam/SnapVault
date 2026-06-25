# SnapVault

**Camera import and archiving tool for photographers with a NAS.**

SnapVault reads photos and videos from an SD card and streams them directly to one or more SMB shares — no intermediate copy to local storage. Files are organized by EXIF capture date under a named shoot folder. It ships with a local web UI for guided imports and a terminal TUI for keyboard-driven workflows.

---

## How it works

```
SD card (/Volumes/SDCARD)
        │
        │  reads EXIF, filters macOS metadata
        ▼
  SnapVault (runs locally on your Mac)
        │
        │  SMB over LAN (userspace — no OS mount required)
        ▼
  NAS share  (e.g. Unraid → "RAW Photos")
        └── 2026 - Wedding/
                ├── 2026-06-14/
                │       ├── DSC_0001.NEF
                │       └── DSC_0002.NEF
                └── 2026-06-15/
                        └── DSC_0003.ARW
```

Every transfer is size-verified after copy. macOS sidecar files (`._*`, `.DS_Store`) are silently skipped. Multiple NAS shares can be targeted simultaneously for redundancy.

---

## Requirements

- macOS (primary platform; Linux is supported for the CLI/TUI)
- Go 1.24.2 or later
- LAN access to an SMB share

---

## Installation

```bash
git clone https://github.com/KiranTheRam/SnapVault.git
cd SnapVault
go build -o snapvault .
```

### System-wide alias (macOS / zsh)

`start.sh` wraps the binary — it rebuilds automatically if any source or web asset has changed, and always uses the correct config regardless of which directory you launch from.

```bash
# Add once to ~/.zshrc
alias snapvault='/path/to/SnapVault/start.sh'
```

After `source ~/.zshrc` you can run `snapvault` from anywhere.

---

## Configuration

SnapVault stores everything in `config.yaml` (created automatically on first save, permissions `0600`). The file is intentionally excluded from version control to keep credentials out of git.

### NAS shares

```yaml
smb_shares:
  - host: "192.168.1.33"
    port: 445                       # default; omit if 445
    share: "RAW Photos"
    username: "kiran"
    password: "${NAS_PASSWORD}"     # supports ${ENV_VAR} expansion
    base_path: ""                   # optional subdirectory within the share
```

Shares are added and tested through the web UI or TUI. You can target multiple shares; files are transferred to all of them in parallel.

### ntfy notifications

```yaml
ntfy:
  server: "https://ntfy.sh"
  topic: "snapvault"
  # Protected servers — use one:
  username: "kiran"
  password: "${NTFY_PASSWORD}"
  # token: "${NTFY_TOKEN}"          # bearer token alternative
```

A push is sent on transfer completion (✅ folder name, file count, duration) or failure (🚨 high-priority, with error details). Configure via the ⚙ button in the web UI.

---

## Usage

### Web UI (recommended)

```bash
snapvault
# or directly:
./snapvault -serve
```

Opens `http://127.0.0.1:8080` in your browser. The guided flow:

1. **Destinations** — select saved NAS shares or add a new one (connection is tested live before saving)
2. **Source card** — choose a detected volume from `/Volumes`, click **Browse…** for a native folder picker, or type a path; the card is scanned immediately showing file count, total size, and a per-day breakdown
3. **Shoot details** — name the shoot; files land under `<year> - <name>/YYYY-MM-DD/`
4. **Transfer** — live progress bar with the current filename; a summary screen on completion

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-serve` | — | Launch the web UI |
| `-addr` | `127.0.0.1:8080` | Bind address |
| `-no-open` | false | Don't auto-open the browser |
| `-config` | `config.yaml` | Config file path |
| `-workers` | `4` | Parallel transfer workers |
| `-timeout` | `30s` | SMB connection timeout |

### Terminal TUI

```bash
./snapvault          # launches TUI when -mount or -name is omitted
```

The TUI mirrors the web UI workflow in the terminal using Bubble Tea. NAS connections tested and saved here are shared with the web UI (same `config.yaml`).

### Non-interactive CLI

```bash
./snapvault -mount /Volumes/SDCARD -name "Wedding"
./snapvault -mount /Volumes/SDCARD -name "Concert" -workers 8
```

Requires an existing `config.yaml` with at least one share. Useful for scripting.

---

## Folder structure

Given shoot name `"Wedding"` in 2026:

```
<base_path>/
└── 2026 - Wedding/
    ├── 2026-06-14/
    │   ├── DSC_0001.NEF
    │   └── DSC_0002.CR2
    └── 2026-06-15/
        └── DSC_0003.ARW
```

Dates come from the EXIF `DateTimeOriginal` field. Files without EXIF (videos, unsupported formats) fall back to the file modification time.

---

## Supported formats

**Stills**

| Format | Extensions |
|--------|------------|
| JPEG | `.jpg` `.jpeg` |
| PNG | `.png` |
| HEIF | `.heic` `.heif` |
| TIFF | `.tif` `.tiff` |
| Canon RAW | `.cr2` `.cr3` |
| Nikon RAW | `.nef` |
| Sony RAW | `.arw` |
| Adobe DNG | `.dng` |
| Olympus RAW | `.orf` |
| Panasonic RAW | `.rw2` |
| Fujifilm RAW | `.raf` |
| Pentax RAW | `.pef` |
| Samsung RAW | `.srw` |
| Generic RAW | `.raw` |

**Video**

`.mov` `.mp4` `.m4v` `.avi` `.mts` `.m2ts` `.mxf`

macOS metadata files (`._*`, `.DS_Store`, `__MACOSX`) are always skipped.

---

## Performance

- **Parallel workers** — configurable pool (default 4) transfers multiple files concurrently; increase with `-workers 8` on fast networks
- **Connection reuse** — one SMB session per share, reused across all files
- **Directory caching** — date folders are created once and cached; no redundant round-trips
- **Direct streaming** — files go card → NAS with no local staging
- **Size verification** — written byte count is compared against the source after every file

---

## Security

- `config.yaml` is written with `0600` permissions and is excluded from git
- Passwords support `${ENV_VAR}` expansion so plaintext secrets stay out of the file
- The web UI never returns passwords or tokens to the browser; stored secrets are preserved on save if fields are left blank
- SMB authentication uses NTLM; keep traffic on a trusted LAN or VPN

---

## Troubleshooting

**Connection refused / timeout**
```bash
# Verify SMB is reachable
nc -zv <nas-ip> 445
# Try a longer timeout
./snapvault -serve -timeout 60s
```

**Scan times out on large cards**
The scan preview uses file modification times (not EXIF) so it's fast. If it still times out, check that the mount path is correct and the card is fully mounted.

**Empty folders left on NAS**
This was caused by macOS `._*` sidecar files being treated as photos — now fixed. The date folder is created before the copy attempt; if the copy fails the folder may remain but will be reused correctly on the next successful transfer to the same date.

**Transfer errors**
A summary is shown in the web UI and printed to the terminal. Individual file errors don't abort the transfer; all other files continue. Re-running the transfer will re-copy everything (deduplication is not currently implemented).

---

## License

See [LICENSE](LICENSE).
