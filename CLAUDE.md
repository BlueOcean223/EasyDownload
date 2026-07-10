# CLAUDE.md

Read and follow `AGENTS.md` before making changes. `AGENTS.md` is the authoritative source for repository-wide commands, generated-file rules, validation gates, coding conventions, documentation expectations, and pull-request guidance. Read `CONTEXT.md` before changing task lifecycle, persistence, settings, detection, or Wails-boundary semantics.

## Current Architecture

EasyDownload is a Wails v2.11.0 desktop application using Go 1.23.x and a Vue 3 + TypeScript frontend on Node.js 20.

### Application Boundary

- `main.go` configures and starts Wails.
- `app.go` composes backend services and exposes the Wails-bound application API.
- Frontend RPCs are generated into `frontend/wailsjs/`. Never edit those files manually.
- `frontend/src/stores/app.ts` is the frontend integration boundary. It normalizes generated DTOs into frontend domain types and applies revision/generation ordering rules.
- `frontend/src/settings-bindings.test.ts` locks reviewed Wails fields, types, optionality, and important RPC signatures.

### Download Runtime

- `internal/download/downloader.go` coordinates task creation and execution; lifecycle, task storage, output allocation, publication, recovery, and runtime configuration are kept in focused files within the same package.
- `internal/download/task/` defines `PlatformAdapter`, `TaskExecutionContext`, task snapshots, progress updates, artifacts, checkpoints, errors, and stop reasons.
- `internal/download/fetch/` owns shared HTTP transfer behavior, including sequential transfer, resumable sidecars, identity validation, retries, limits, and explicitly enabled multipart transfer.
- Platform packages (`bilibili/`, `douyin/`, `wechat/`, `xiaohongshu/`) own parsing, credentials, metadata, candidate selection, fallback, post-processing, and cleanup. Each platform enters the runtime through an adapter.
- The download manager owns lifecycle and persistence but does not parse platform payloads or platform error strings.

### Settings and Detection

- `internal/settings/` provides transactional settings updates. It validates the complete candidate snapshot, runs critical effects before commit, reports best-effort warnings, and returns restart requirements.
- `internal/detection/` owns stable detected-media identity, merge rules, revision ordering, bounded session state, and public snapshots.
- `internal/detection/wechatadapter/` translates WeChat proxy callbacks into the detection model.
- Frontend code consumes authoritative settings and detection snapshots; it must not duplicate backend merge, validation, or ordering policy.

### Supporting Services

- `internal/proxy/` contains MITM, CA certificate, injection, and system-proxy behavior for WeChat detection.
- `internal/api/` receives internal callbacks and serves runtime media/image proxy endpoints.
- `internal/config/` persists configuration atomically.
- `internal/infra/` contains logging, FFmpeg, and credential-storage integrations.
- `internal/tray/`, `internal/platformfix/`, and `internal/utils/` contain desktop and platform support.

## Non-Negotiable Invariants

1. Platform adapters use `TaskExecutionContext.Fetcher()` and the recorded output policy. They do not allocate a different final path or create an unmanaged fetch client.
2. Final publication is atomic and no-replace. Existing user files must never be overwritten by a race between allocation and commit.
3. After a final artifact becomes visible, sync or temporary-cleanup failures are persisted as cleanup diagnostics; they do not regress the task from completed to failed.
4. Task instance, generation, revision, and lifecycle-operation identifiers fence stale commands and events. Preserve these checks across backend, Wails DTOs, and the frontend store.
5. Platform data and checkpoints remain versioned and platform-owned. The manager may persist them but must not infer their schema.
6. Settings changes use the unified patch transaction; do not reintroduce field-by-field Wails setters.
7. Detection snapshots are authoritative and revisioned; do not add frontend source-specific deduplication.
8. Do not use broad TypeScript assertions to force generated Wails results into local domain types. Add or update an explicit normalizer and its binding-contract coverage.

## Working Practices

- Use the pinned Wails v2.11.0 generation command from `AGENTS.md`; never install or generate with `@latest` for repository changes.
- Use `npm ci` for clean installs and keep `go.sum` plus `frontend/package-lock.json` committed.
- Run focused tests while iterating, then complete Go tests/vet/module verification and frontend tests/typecheck/build.
- Run the download race suite on a CGO-capable system; CI provides the Linux race gate.
- Prefer deterministic synchronization in concurrency tests. Do not rely on arbitrary sleeps to prove event or retry ordering.
- Update platform principle docs, package docs, the reliability document, and `CONTEXT.md` when their contracts change.
