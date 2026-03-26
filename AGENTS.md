# Repository Guidelines

## Project Structure & Module Organization
`EasyDownload` is a Wails desktop app with a Go backend and a Vue 3 + TypeScript frontend. Root files `main.go` and `app.go` bootstrap the app and expose Wails-bound methods. Backend code lives in `internal/`: `download/` contains the core downloader and platform modules (`bilibili/`, `douyin/`, `wechat/`, `xiaohongshu/`), `proxy/` handles MITM/certificate logic, `api/` receives internal callbacks, and `config/`, `infra/`, `tray/`, and `utils/` hold support code. Frontend code lives in `frontend/src/` with `views/`, `components/`, `stores/`, `router/`, `types/`, and `composables/`. Treat `frontend/wailsjs/` and `frontend/dist/` as generated output; do not hand-edit them.

## Build, Test, and Development Commands
Use `wails dev` to run the full app with Go + Vite hot reload. Use `wails build` to create production binaries in `build/bin/`. Run backend tests with `go test ./...` or scope to a package such as `go test ./internal/download/douyin/...`. In `frontend/`, use `npm run build` for the Vite bundle, `npm run typecheck` for strict TypeScript validation, `npm run test` for Vitest, and `npm run test:watch` while iterating.

## Coding Style & Naming Conventions
Format Go code with `gofmt`; keep packages lowercase and follow existing platform suffixes like `_windows.go`, `_darwin.go`, and `_other.go`. Keep Vue/TypeScript formatting consistent with the current codebase: 2-space indentation, no semicolons, and `@/` imports for `frontend/src`. Name Vue SFCs and reusable UI pieces in PascalCase (`WelcomeWizard.vue`), keep route/view names aligned with feature pages, and keep Pinia stores and utility modules concise and feature-focused.

## Testing Guidelines
Backend tests are colocated with source and use Go `testing` plus `testify`; name them `*_test.go`. Frontend test support is wired through Vitest and `@vue/test-utils`; place new specs beside the component or in the relevant feature folder using `*.test.ts`. Run the narrowest affected test package before opening a PR, then finish with `go test ./...` and `cd frontend && npm run test && npm run typecheck`.

## Commit & Pull Request Guidelines
Recent history follows Conventional Commits, including optional scopes: `feat:`, `fix:`, `refactor:`, `docs:`, and forms like `feat(frontend): ...`. Keep subjects imperative and specific. PRs should include a short summary, linked issue when applicable, the commands you ran, and screenshots or recordings for UI changes. Call out platform-specific impact when touching proxy, certificate, tray, or FFmpeg code.
