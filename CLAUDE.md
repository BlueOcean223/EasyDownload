# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
# Prerequisites: Go 1.21+, Node.js 18+, Wails CLI v2
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Development (hot-reload for both Go and Vue)
wails dev

# Production build (output: build/bin/)
wails build

# Backend tests
go test ./...
go test ./internal/download/bilibili/...   # single package

# Frontend tests
cd frontend && npm run test               # single run
cd frontend && npm run test:watch          # watch mode

# Frontend type check
cd frontend && npm run typecheck
```

## Architecture

Wails v2 desktop app: Go backend + Vue 3 frontend, communicating via Wails bindings (auto-generated in `frontend/wailsjs/`).

### Backend (Go)

- **`app.go`** — Central `App` struct holding all managers; Wails-bound methods exposed to frontend. This is the main integration point.
- **`main.go`** — Wails bootstrap and window configuration.
- **`internal/proxy/`** — MITM proxy (goproxy) for WeChat video sniffing. Includes CA cert generation (`cert.go`), request/response injection (`injector.go`), and system proxy management.
- **`internal/download/`** — Download engine with platform-specific implementations:
  - `downloader.go` — Core download manager with concurrent task management, resumable downloads (`http_resumable.go`), multipart support (`multipart.go`).
  - `bilibili/` — Bilibili downloader (BV/av URL parsing, multi-quality, SESSDATA auth via keyring).
  - `douyin/` — Douyin downloader (Parser→Client→Downloader pattern, share-page SSR first with `aweme/detail` + `slidesinfo` fallbacks, ratio stream probing, album/mixed-media support).
  - `xiaohongshu/` — Xiaohongshu downloader (same Parser→Client→Downloader pattern, video+image notes).
  - `wechat/` — WeChat video channel (sniffed via proxy, detection callbacks through internal API).
- **`internal/api/`** — Internal HTTP API server receiving video detection callbacks from the proxy injector.
- **`internal/proxy/`** — MITM proxy with cert management and system proxy auto-configuration.
- **`internal/config/`** — JSON-based persistent configuration.
- **`internal/infra/`** — Infrastructure: `logger/` (custom logging), `ffmpeg/` (embedded FFmpeg binary, extracted at runtime), `credential/` (keyring-based auth storage).
- **`internal/tray/`** — System tray integration (getlantern/systray).
- **`internal/utils/`** — File operations, system helpers, ZIP, filename sanitization.

### Frontend (Vue 3 + TypeScript)

- **Tech**: Vue 3 + Naive UI + Tailwind CSS + Pinia + Vue Router
- **`frontend/src/views/`** — Page components per download platform.
- **`frontend/src/stores/`** — Pinia state management.
- **`frontend/src/types/`** — TypeScript type definitions.
- **`frontend/wailsjs/`** — Auto-generated Wails Go↔JS bindings. **Do not edit manually**; regenerated on `wails dev`/`wails build`.

### Key Patterns

- **Parser→Client→Downloader**: Douyin and Xiaohongshu modules follow this 3-layer pattern — URL parsing, API client, download execution. Douyin's client currently prefers share-page SSR, falls back to `aweme/detail` and `slidesinfo`, and uses ranged GET ratio probing when `bit_rate` streams are missing.
- **Platform-conditional compilation**: Windows-specific files use `_windows.go` suffix (UAC elevation, cert install).
- **Embedded assets**: FFmpeg binary is embedded via Go `embed` FS and extracted to AppData on first run.
- **Credential storage**: Bilibili SESSDATA stored in OS keychain via `go-keyring`.

## Testing

- **Backend**: `testing` + `testify` (assertions) + `gopter` (property-based testing). Tests colocated with source files.
- **Frontend**: Vitest + `@vue/test-utils` + `fast-check` (property-based). Config in `frontend/vitest.config.ts` or `vite.config.ts`.

## Documentation

- When changing platform parsing, metadata fetching, stream selection, or download behavior, update the corresponding `docs/*-link-download-principle.md` file and any affected package docs such as `internal/download/<platform>/doc.go`.
- Keep README usage text in sync for user-visible feature changes.
- Do not manually edit generated output in `frontend/wailsjs/` or `frontend/dist/`.

## Conventions

- Go module name: `EasyDownload` (used in import paths like `EasyDownload/internal/...`).
- i18n: Frontend supports `zh-CN` and `en-US`.
- Commit style: conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`).
