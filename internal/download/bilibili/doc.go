// Package bilibili provides video downloading functionality for Bilibili.
//
// This package implements the Bilibili video parser and downloader, which supports:
//   - Parsing Bilibili video URLs (BV/AV format) and PGC/bangumi URLs (ep/ss/md)
//   - Fetching video metadata including multi-part (分P) information and bangumi episode lists
//   - QR code login authentication for accessing higher quality streams
//   - Selecting DASH video streams by codec priority and bandwidth
//   - Downloading DASH format videos with separate video/audio streams
//   - Byte-equivalent backup CDN mirrors with validator-aware resume
//   - Resumable downloads with progress tracking
//   - Context-aware FFmpeg integration for merging video and audio streams
//
// Bilibili API Overview:
// The downloader interacts with several Bilibili APIs:
//   - Video info API: /x/web-interface/view - fetches video metadata
//   - Play URL API: /x/player/playurl - fetches stream URLs with quality options
//   - Bangumi season API: /pgc/view/web/season - fetches PGC season/episode metadata
//   - Bangumi play URL API: /pgc/player/web/playurl - fetches PGC DASH streams
//   - QR Login API: /x/passport-login/web/qrcode/* - handles QR code authentication
//   - User info API: /x/web-interface/nav - fetches logged-in user information
//
// Stream selection and download behavior:
// For each available quality, the downloader selects one DASH video stream using codec
// priority (AV1 > HEVC > H.264) and then bandwidth as a tie-breaker. It selects the
// highest-bandwidth DASH audio stream and records byte-equivalent backup CDN URLs.
// Fetch may try those mirrors while preserving one resource identity. Content length
// probing also tries backup URLs so size and progress reporting remain accurate.
// Task data persists stable BV/CID metadata and the requested quality, but RunTask
// resolves fresh playurl URLs for every execution, including ordinary tasks whose
// legacy part index is -1.
// FFmpeg merge and part/playurl resolution use the task context, so pause/cancel
// can interrupt both API and external-process work. Media CDN requests intentionally
// omit SESSDATA; the cookie is restricted to injected API/auth HTTPDoer requests.
// The default API request timeout is 30 seconds. Playurl HTTP/business failures
// are mapped to stable auth_required, risk_control, or resource_expired errors.
// The adapter rejects an unknown PlatformDataVersion before decoding TaskData.
// RunTask publishes verified temporary output only through manager-owned PublishFinal.
//
// Authentication:
// Higher quality streams (1080P+, 4K, etc.) require user authentication via SESSDATA cookie.
// QR login stores SESSDATA internally but omits it from the public status DTO.
//
// See docs/bilibili-link-download-principle.md for the full parsing and download flow.
package bilibili
