# granola-sync

Granola is an AI meeting assistant that records, transcribes, and summarizes meetings. It stores all your notes in its own app — but doesn't offer a way to export or back them up automatically.

This tool syncs your Granola meeting notes to Google Drive as plain Markdown files, so they're searchable, portable, and yours. It runs as a lightweight Docker container on a server and re-syncs on a configurable interval.

## File structure

Notes are saved as `FolderName/YYYY/MM/YYYY-MM-DD_Title.md` inside the configured Drive folder. Notes without a Granola folder go to `Unclassified/`.

### `.index.json`

A hidden file stored in the root of your Drive folder. It maps each Granola note ID to its file path and last-updated timestamp. This is how the tool knows:
- which notes are already synced (skip)
- which notes have been updated (re-upload)
- where the old file is when a note gets renamed or moved (delete old, create new)

**Do not delete `.index.json`.** If it's lost, the tool loses all state — it will re-upload all 268+ notes as duplicates alongside any existing files.

## Setup

### 1. Google Cloud credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com/) → **APIs & Services → Enable APIs** → enable **Google Drive API**
2. **Credentials → Create Credentials → OAuth 2.0 Client ID → Desktop app** — download the JSON
3. Copy `client_id` and `client_secret` into `.env`

### 2. Get a refresh token (one-time, run locally)

```bash
go build -o granola-sync .
export $(grep -v '^#' .env | xargs)
./granola-sync auth
```

A browser window will open. After authorizing, copy the printed `GOOGLE_REFRESH_TOKEN` into `.env`.

### 3. Configure `.env`

```env
GRANOLA_API_KEY=grn_...

GOOGLE_CLIENT_ID=...
GOOGLE_CLIENT_SECRET=...
GOOGLE_REFRESH_TOKEN=...

# ID from the Drive folder URL
GOOGLE_DRIVE_FOLDER_ID=...

# Sync interval (e.g. 1h, 30m, 6h)
SYNC_INTERVAL=1h
```

`GOOGLE_DRIVE_FOLDER_ID` is the last segment of the folder URL:
```
https://drive.google.com/drive/folders/<FOLDER_ID>
```

### 4. Run

```bash
docker compose up -d
docker compose logs -f
```

## Commands

| Command | Description |
|---|---|
| `./granola-sync` | Sync once and exit |
| `./granola-sync auth` | One-time OAuth2 authorization |
| `./granola-sync --since 2025-01-01` | Only fetch notes created after a date |
| `./granola-sync --interval 1h` | Run continuously on an interval |

## Markdown format

Each note is saved with the following structure:

```markdown
# Meeting Title

**Date:** 2026-04-03 10:00 UTC (45 min)
**Organiser:** organiser@example.com
**Attendees:** Name (email), ...
**Folders:** FolderName

## Summary

...AI-generated summary...

## Transcript

**You:** [00:00] Hello everyone...
**Them:** [00:05] Let's get started...
```
