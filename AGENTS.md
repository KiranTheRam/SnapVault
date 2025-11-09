# Repository Guidelines

## Project Structure & Module Organization
SnapVault is a Python CLI that mounts SMB targets, organizes photos, and copies them to NAS shares. Work from the repository root. Key paths:
- `main.py` holds the end-to-end workflow, Discord notifier, EXIF helpers, and SMB transfer logic.
- `logs/` captures timestamped runtime logs created by `main.py`; keep the directory writeable and review logs when debugging.
- `requirements.txt` lists runtime dependencies (Pillow, python-dotenv, requests, tqdm). Add development-only tools to a comment block or a separate file rather than mixing them with runtime needs.
- `.env` (create locally) stores NAS credentials and webhook secrets; never commit this file.

## Build, Test, and Development Commands
- `python3 -m venv .venv && source .venv/bin/activate` creates and activates a project virtual environment.
- `python3 -m pip install -r requirements.txt` installs runtime dependencies; re-run after updating requirements.
- `python3 main.py /Volumes/SD_CARD --destination both` runs the importer; swap the source path or `--destination` flag (`storage`, `editing`, or `both`) for targeted runs.
- `python3 main.py --help` surfaces CLI arguments; update the parser documentation when adding flags.

## Coding Style & Naming Conventions
- Follow PEP 8 with 4-space indentation. Keep functions and variables in `snake_case`, classes in `CapWords`, constants in `UPPER_SNAKE_CASE`.
- Docstrings use triple quotes; keep them action-oriented (e.g., “Copy a folder to an SMB share…”).
- Prefer structured logging via the existing `logging` module over ad-hoc prints; if console feedback is needed, pair it with a log entry.
- When adding modules, colocate utilities by responsibility (e.g., `notifications.py`, `nas.py`) and import them in `main.py` to preserve a clear CLI entry point.

## Testing Guidelines
- No automated suite exists yet; new contributions should add regression coverage under `tests/` using `pytest` or `unittest`.
- Mock network mounts, Discord calls, and filesystem writes to keep tests hermetic. Use fixtures to simulate `.env` content.
- Run `pytest` (after adding it to dev requirements) before opening a PR and document any gaps or manual steps in the PR description.

## Commit & Pull Request Guidelines
- Write concise, present-tense commit subjects (“Add SMB retry backoff”) followed by optional detail in the body. Group related changes together.
- Reference GitHub issues in the commit body or PR description (`Closes #12`) and list configuration or environment updates.
- PRs should include: summary of changes, testing evidence (commands plus results), screenshots for UX-facing tweaks, and risk/rollback notes for deployment-impacting changes.
- Keep PRs focused; open follow-up issues for deferred work instead of bundling unrelated fixes.
