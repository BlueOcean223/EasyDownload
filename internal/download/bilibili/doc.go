// Package bilibili provides video downloading functionality for Bilibili.
//
// This package implements the Bilibili video parser and downloader, which supports:
//   - Parsing Bilibili video URLs (BV/AV format) and PGC/bangumi URLs (ep/ss/md)
//   - Fetching video metadata including multi-part (分P) information and bangumi episode lists
//   - QR code login authentication for accessing higher quality streams
//   - Selecting DASH video streams by codec priority and bandwidth
//   - Downloading DASH format videos with separate video/audio streams
//   - Fallback downloads via backup CDN URLs when primary stream URLs fail
//   - Resumable downloads with progress tracking
//   - FFmpeg integration for merging video and audio streams
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
// highest-bandwidth DASH audio stream, records backup CDN URLs, and retries those URLs
// if the primary URL fails. Content length probing also tries backup URLs so size and
// progress reporting remain as accurate as possible.
//
// Authentication:
// Higher quality streams (1080P+, 4K, etc.) require user authentication via SESSDATA cookie.
// The downloader supports QR code login flow and securely stores credentials.
//
// See docs/bilibili-link-download-principle.md for the full parsing and download flow.
package bilibili
