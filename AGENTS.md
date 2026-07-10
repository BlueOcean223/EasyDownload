# Repository Guidelines

## Project Structure & Module Organization

`EasyDownload` is a Wails v2 desktop app with a Go backend and a Vue 3 + TypeScript frontend. Root files `main.go` and `app.go` bootstrap the app and expose the Wails-bound application surface.

Backend code lives in `internal/`:

- `download/` owns durable task lifecycle, persistence, adapter registration, output allocation, and final artifact publication.
- `download/task/` defines platform adapter, execution-context, task snapshot, progress, artifact, and error contracts.
- `download/fetch/` is the shared HTTP transport layer for sequential, resumable, and explicitly enabled multipart transfers.
- `download/bilibili/`, `douyin/`, `wechat/`, and `xiaohongshu/` contain platform parsing, metadata, selection, and adapter logic.
- `detection/` owns the bounded, revisioned in-memory store for detected media and source adapters.
- `settings/` validates complete candidate settings and coordinates critical and best-effort runtime effects.
- `proxy/` handles MITM, certificate, and system-proxy behavior; `api/` receives internal detection callbacks.
- `config/`, `infra/`, `tray/`, `platformfix/`, and `utils/` contain persistence and platform support code.

Frontend code lives in `frontend/src/` with `views/`, `components/`, `stores/`, `router/`, `types/`, and `composables/`. Treat `frontend/wailsjs/` and `frontend/dist/` as generated output; do not hand-edit them. Use `CONTEXT.md` as the canonical glossary for task lifecycle, settings, detection, and Wails-boundary terminology.

## Toolchain, Build, and Development Commands

The supported toolchain is Go 1.23.x, Node.js 20, and Wails v2.11.0. Pin the Wails CLI rather than installing `@latest`:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0
```

Use `wails dev` for Go + Vite hot reload and `wails build` for production binaries in `build/bin/`. Keep dependency locks reproducible: use `npm ci` for clean frontend installs and commit both `go.sum` and `frontend/package-lock.json`.

Primary validation commands:

```bash
go test -count=1 ./...
go vet ./...
go mod verify

cd frontend
npm run test -- --run
npm run typecheck
npm run build
```

Run `go test -race ./internal/download/...` on a CGO-capable environment. The GitHub quality workflow provides the Linux race gate and the full Windows desktop test/vet gate.

## Architecture Invariants

- Platform packages implement the contracts in `internal/download/task/`; the download manager must not interpret platform-specific payloads or error strings.
- Platform adapters use the recorded output policy and the fetcher supplied by `TaskExecutionContext`. They must not independently choose final paths or instantiate an unmanaged transport client.
- Final artifacts are published atomically without replacing an existing file. Once the final artifact is visible, directory-sync or temporary-cleanup failures are diagnostics and must not turn a successful download into a failed task.
- Task instance, generation, and revision fences protect lifecycle commands and frontend event ordering. Do not replace them with status-only checks.
- Settings changes go through the unified settings patch transaction. Validation considers the complete candidate snapshot before persistence or runtime effects.
- Detection snapshots are revisioned and authoritative. The frontend should not recreate source-specific merge or deduplication rules.
- Wails-generated DTOs are normalized at the frontend store boundary. Do not bypass generated contracts with `as unknown as`, `as any`, or broad RPC-result assertions.

## Generated Bindings and Dependency Locks

When an exported Wails method or DTO changes, regenerate bindings with the pinned CLI and commit the resulting `frontend/wailsjs/` files plus `frontend/package.json.md5`:

```bash
go run github.com/wailsapp/wails/v2/cmd/wails@v2.11.0 generate module -nocolour
```

Do not manually format or patch generated binding files. Keep `frontend/src/settings-bindings.test.ts` aligned with intentionally exposed fields, field types, optionality, and RPC signatures.

## Coding Style & Naming Conventions

Format Go code with `gofmt`; keep packages lowercase and use build-tag/platform suffixes such as `_windows.go`, `_linux.go`, `_darwin.go`, and `_other.go`. Keep Vue/TypeScript formatting consistent with the current codebase: 2-space indentation, no semicolons, and `@/` imports for `frontend/src`. Name Vue SFCs and reusable UI pieces in PascalCase and keep route/view names aligned with feature pages.

Prefer cohesive feature modules over one-function wrapper files. Small OS-specific files are appropriate when build tags or platform APIs require them. Before adding a new abstraction, confirm it has a production caller and owns a meaningful boundary.

## Documentation Guidelines

When changing platform parsing, metadata fetching, stream selection, or download behavior, update the corresponding `docs/*-link-download-principle.md` file and any affected `internal/download/<platform>/doc.go`. Update `docs/security-and-download-reliability.md` for lifecycle, persistence, fetch, publication, or security-boundary changes. Update `CONTEXT.md` when introducing or changing domain terminology, and keep README usage text synchronized with user-visible behavior.

## Testing Guidelines

Backend tests are colocated with source and use Go `testing`, `testify`, and where appropriate `gopter`; name them `*_test.go`. Frontend tests use Vitest, `@vue/test-utils`, and `fast-check`; place specs beside the relevant feature using `*.test.ts`.

Run the narrowest affected package first, then the full validation set above. Concurrency and timeout tests should use deterministic barriers or channel handshakes rather than fixed sleeps. For Wails-surface changes, also regenerate bindings and run the binding contract tests.

## Commit & Pull Request Guidelines

Use Conventional Commits with optional scopes, for example `feat:`, `fix:`, `refactor:`, `docs:`, and `feat(frontend):`. Keep subjects imperative and specific. PRs should summarize behavior and architecture impact, list validation commands, link relevant issues, and include screenshots or recordings for UI changes. Call out platform-specific impact when touching proxy, certificate, tray, filesystem publication, or FFmpeg code.
