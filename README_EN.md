# MeBox

<p align="center">
  <img src="web/public/brand/logo-192.png" width="96" height="96" alt="MeBox Logo" />
</p>

<h3 align="center">A self-hosted media center for NAS and home theater</h3>

<p align="center">
  <strong>Libraries · Metadata · Cloud STRM · Emby/Jellyfin client compatible · Remote Emby mounts · Multi-user · Docker-first</strong>
</p>

<p align="center">
  <a href="README.md">中文</a> ·
  <a href="#overview">Overview</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#deployment-tiers">Deployment</a> ·
  <a href="#acknowledgements">Acknowledgements</a> ·
  <a href="#development">Development</a> ·
  <a href="https://t.me/MeBoxGroup">Telegram</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go&logoColor=white" />
  <img alt="React" src="https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react&logoColor=111827" />
  <img alt="Docker" src="https://img.shields.io/badge/Docker-ready-2496ED?style=flat-square&logo=docker&logoColor=white" />
  <img alt="License" src="https://img.shields.io/badge/License-GPL--3.0-blue?style=flat-square" />
</p>

---

## Overview

**MeBox** is a self-hosted private media management system for NAS, mini PCs, family sharing, and multi-device playback. This repository is a maintained fork of [MediaStationGo](https://github.com/ShukeBta/MediaStationGo), extended with stronger cloud playback, task queues, remote mounts, and permission controls.

In practice, MeBox gives you:

- A modern **web media library**
- An **Emby/Jellyfin-compatible protocol gateway** for third-party players
- A single panel for **local disks, download folders, and cloud storage**

### Key capabilities

| Area | Highlights |
| --- | --- |
| **Libraries** | Movies, TV, anime, variety, music, custom libraries; multi-root scanning; poster wall; continue watching |
| **Metadata** | TMDb, Bangumi, Douban, TheTVDB, Fanart, NFO import, manual matching, scrape queue |
| **Playback** | Web player, HLS transcoding, danmaku, subtitles, play profiles, history and favourites |
| **Emby/Jellyfin client compatible** | Full Emby server protocol implementation: Infuse, SenPlayer, Fileball, and official Emby/Jellyfin clients can add MeBox as an Emby server and sign in with MeBox accounts — poster walls, watch progress, and multi-user work out of the box |
| **Remote Emby mounts** | Browse remote Emby libraries inside MeBox without a separate Emby client |
| **Cloud & STRM** | OpenList, CloudDrive2, 115, WebDAV; STRM sync; upload/download queues; direct or 302 playback |
| **Downloads & organize** | Scheduled download-folder organization (smart classification, auto library registration), file manager (copy/move/hardlink/symlink) |
| **Users & permissions** | Admin/regular users, expiry, NSFW toggle, play-profile PIN, granular permissions |
| **Operations** | Unified task queue, storage stats, DLNA casting, settings and logs |

### Tech stack

- **Backend**: Go, Gin, GORM, SQLite or PostgreSQL, optional Redis and OpenSearch
- **Frontend**: React 18, Vite, TypeScript, Tailwind CSS, Zustand
- **Deployment**: Standalone Docker Compose templates, amd64/arm64 images, single-binary releases

---

## Quick Start

Docker Compose is the recommended path. The repo ships four **standalone** templates; no `.env` is required.

```bash
mkdir -p MeBox && cd MeBox

# Simplest: one container with built-in SQLite
curl -fsSL https://raw.githubusercontent.com/truewhile/MeBox/main/docker-compose.simple.yml -o docker-compose.yml

# Or PostgreSQL tier for multi-user setups
# curl -fsSL https://raw.githubusercontent.com/truewhile/MeBox/main/docker-compose.yml -o docker-compose.yml

docker compose up -d
```

Open:

```text
http://SERVER_IP:18080
```

Default login: `admin` / `admin123` — change the password immediately.

> 💡 **Seamless for Emby users**: MeBox fully implements the Emby/Jellyfin client protocol. Infuse, SenPlayer, Fileball, and official Emby/Jellyfin apps on phones, TVs, and tablets can add it as an Emby server at `http://SERVER_IP:18080` and sign in with MeBox accounts — no change to your existing workflow.

Image:

```text
ghcr.io/truewhile/mebox:latest
```

---

## Deployment tiers

Pick one compose file. Do **not** stack multiple `-f` files.

| Tier | File | Stack | Best for |
| --- | --- | --- | --- |
| Single image | `docker-compose.simple.yml` | MeBox + SQLite | Beginners, single-user, low-resource NAS |
| Tier 1 | `docker-compose.yml` | MeBox + PostgreSQL | Most home NAS deployments |
| Tier 2 | `docker-compose.standard.yml` | + Redis | Multi-user, frequent Emby client refreshes |
| Tier 3 | `docker-compose.search.yml` | + OpenSearch | Very large libraries, advanced full-text search |

### Single-image notes

- Only one MeBox container; database lives in `./data/mebox.db`
- Do **not** set `MEBOX_DATABASE_DSN` or it switches to PostgreSQL
- Back up `./data`; `./cache` can be rebuilt

### PostgreSQL notes

- Primary DB: `./postgres`; secrets and runtime files: `./data`
- Existing `./data/mebox.db` migrates automatically on first start
- After migration, point `MEBOX_DATABASE_DB_PATH` at a non-existent file to disable re-checks

### Backup

| Path | Notes |
| --- | --- |
| `./data` | JWT secret, config, SQLite DB or migration source |
| `./postgres` | PostgreSQL primary DB |
| `./cache`, `./redis`, `./opensearch` | Rebuildable |

### Update

```bash
docker compose pull mebox
docker compose up -d --no-deps mebox
```

---

## Path mapping

The most common Docker mistake is mixing host paths with container paths.

- Left side of `volumes` = real host/NAS path
- Right side = container path; use `/media/...` in the web UI
- Keep `MEBOX_MEDIA_DIR` / `MEBOX_DOWNLOAD_DIR` aligned with mounts when organizing or ingesting downloads

Example:

```yaml
volumes:
  - /vol1/1000/Media:/media
  - /vol1/1000/Downloads:/downloads
environment:
  MEBOX_MEDIA_DIR: /vol1/1000/Media
  MEBOX_MEDIA_CONTAINER_DIR: /media
  MEBOX_DOWNLOAD_DIR: /vol1/1000/Downloads
  MEBOX_DOWNLOAD_CONTAINER_DIR: /downloads
```

---

## First-time setup

1. Create a library with a container path such as `/media/Movies`, then scan
2. Add metadata providers (TMDb, Bangumi, etc.) in system settings
3. Optionally set up download-folder auto-organization under file management so finished downloads land in the right library
4. Optionally configure cloud accounts under STRM management
5. Add the server in Emby-compatible players at `http://SERVER_IP:18080` using MeBox credentials

---

## FAQ

**Library scan is slow**  
Check path mapping and DB tier. Cloud scans also depend on API limits and folder size.

**Downloaded files are not organized**  
Ensure the download directory is mounted into the container and env vars match. MeBox handles folder organization; run qBittorrent or any downloader yourself as a regular app.

**Hardlink fails with cross-device link**  
Hardlinks require the same filesystem/subvolume; use copy or symlink across disks or cloud mounts.

**External player cannot connect**  
Use `http://IP:18080` and a MeBox user account; reverse proxies need correct external URL and HTTPS headers.

---

## Development

The backend embeds `web/dist` via `go:embed`. Build the frontend first.

```bash
npm --prefix web ci
npm --prefix web run build

go test ./...
go run ./cmd/server
npm --prefix web run dev
```

Release builds ship single-file binaries for Windows, Linux, and macOS on amd64 and arm64.

Build the Windows executable locally:

```powershell
.\scripts\build-windows.ps1 -Version dev
```

The Windows executable uses the project logo and runs without a console window. It stays in the notification area, with menu actions for opening MeBox, toggling auto-start, viewing logs, restarting, and exiting.

---

## Acknowledgements

MeBox is forked from and continues to evolve [MediaStationGo](https://github.com/ShukeBta/MediaStationGo). Thank you to the upstream project for the media-library architecture, Emby-protocol compatibility, and self-hosted foundation.

Many cloud sync, STRM, and media-organization ideas in this project were also informed by [qmediasync](https://github.com/qicfan/qmediasync). Thank you for the reference implementation and design patterns.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md) before opening issues or pull requests.

---

## Star History

<a href="https://www.star-history.com/?repos=truewhile%2FMeBox&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&legend=top-left" />
 </picture>
</a>

---

## License

This project is licensed under [GPL-3.0](LICENSE).

---

## Support & Donate

If MeBox makes your home theater life easier, feel free to buy the maintainer a coffee ☕

<p align="center">
  <img src="docs/images/donation-qr.png" width="320" alt="WhileTrue donation QR" />
</p>

<p align="center">
  <strong>Telegram group</strong>: <a href="https://t.me/MeBoxGroup">https://t.me/MeBoxGroup</a><br/>
  Questions, feature requests, and release news — come chat with us
</p>
