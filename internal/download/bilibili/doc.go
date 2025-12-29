// Package bilibili provides video downloading functionality for Bilibili.
//
// This package implements the Bilibili video parser and downloader, which supports:
//   - Parsing Bilibili video URLs (BV/AV format)
//   - Fetching video metadata including multi-part (分P) information
//   - QR code login authentication for accessing higher quality streams
//   - Downloading DASH format videos with separate video/audio streams
//   - Resumable downloads with progress tracking
//   - FFmpeg integration for merging video and audio streams
//
// Bilibili API Overview:
// The downloader interacts with several Bilibili APIs:
//   - Video info API: /x/web-interface/view - fetches video metadata
//   - Play URL API: /x/player/playurl - fetches stream URLs with quality options
//   - QR Login API: /x/passport-login/web/qrcode/* - handles QR code authentication
//   - User info API: /x/web-interface/nav - fetches logged-in user information
//
// Authentication:
// Higher quality streams (1080P+, 4K, etc.) require user authentication via SESSDATA cookie.
// The downloader supports QR code login flow and securely stores credentials.
package bilibili
