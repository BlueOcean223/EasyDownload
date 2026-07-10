# EasyDownload

English | [简体中文](README.md)

A simple and easy-to-use desktop video downloader that supports downloading content from multiple platforms including WeChat Channels, Bilibili, Xiaohongshu (RedNote), and Douyin (TikTok China).

## Features

- **WeChat Channels Sniffer**: Automatically detects videos played in WeChat PC client, one-click download
- **Bilibili Download**: Supports BV ID, av ID, ordinary video URLs, and bangumi/PGC ep, ss, md URLs with multiple quality options
- **Xiaohongshu Download**: Supports downloading videos and image notes from Xiaohongshu
- **Douyin Download**: Supports Douyin video download, including slideshow preview and download
- **Visual Interface**: Netflix-style video card display with clear download progress
- **Reliable Downloads**: Built-in queueing, resume support, state persistence, and failed/canceled grouping
- **Local Security Boundary**: Internal API and proxy bind to `127.0.0.1` by default, with token checks for browser-facing routes
- **Zero Configuration**: Automated certificate installation and proxy setup for easy onboarding

## Screenshots

![Main Interface](assets/images/image1.png)

### Video Sniffer
![Video Sniffer Page](assets/images/image2.png)

### Bilibili Download
![Bilibili Download Page](assets/images/image3.png)
![Download Progress](assets/images/image4.png)

### Douyin Download
![Douyin Download Page](assets/images/image5.png)


## Tech Stack

- **Frontend**: Vue 3 + TypeScript + Naive UI + Tailwind CSS
- **Backend**: Go
- **Desktop Framework**: Wails v2

## Usage

### First Time Setup

> Skip this step if you don't need the WeChat Channels sniffer feature.

1. **Run as Administrator** EasyDownload.exe
2. Go to "Settings" page, click "Install Certificate" to install the CA root certificate
3. Return to the main page, click "Start Proxy" button in the sidebar

### Download WeChat Channels Videos

1. Ensure the proxy service is running (sidebar shows green running status)
2. Open **WeChat PC client** and browse Channels content
3. Detected videos will automatically appear on the "Video Sniffer" page
4. Click the "Download" button on the video card to download

> The proxy and internal API bind to the local loopback interface by default. MITM is limited to the WeChat Channels page/script allowlist; video CDN and unrelated HTTPS traffic pass through directly.

### Download Bilibili Videos

1. Go to the "Bilibili" page
2. Paste the Bilibili video link (supports BV ID, av ID, ordinary video URLs, or bangumi/PGC ep/ss/md URLs)
3. Click "Parse" button to get video information
4. Select quality and click "Download Video" for ordinary videos; for bangumi, download the current episode directly or expand the full season to select multiple episodes

### Download Xiaohongshu Content

1. Go to the "Xiaohongshu" page
2. Paste the Xiaohongshu note link or share text
3. Click "Parse" button to get content information
4. Supports downloading video notes or batch saving image notes

### Download Douyin Videos

1. Go to the "Douyin" page
2. Paste the Douyin video link or share text
3. Click "Parse" button to get video information
4. Supports slideshow preview, click download to save video

## Download and Security Notes

- Download tasks are handled by a shared queue; tasks above the concurrency limit stay pending instead of being dropped.
- New task state is stored as `downloads.v2.json` under the app data directory. Ordinary URL, WeChat, Bilibili, Douyin, and Xiaohongshu tasks can all recover from persisted `PlatformData`. The legacy `downloads.json` is not imported or modified; the Downloads page shows a one-time preservation and rollback notice.
- The image proxy blocks localhost, private, link-local, metadata-style and other unsafe addresses to avoid internal network probing.
- See [Security Boundary and Download Reliability](docs/security-and-download-reliability.md) for implementation details.

## Development

### Requirements

- Go 1.23+
- Node.js 20+
- Wails CLI v2.11.0

### Install Wails CLI

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0
```

### Development Mode

```bash
wails dev
```

### Build

```bash
wails build
```

Build output is located in the `build/bin/` directory.

### Settings / Wails binding contract

User settings are read and written only through `GetSettings` / `UpdateSettings`; `GetAppInfo` returns runtime metadata only. After changing backend bindings, run:

```bash
go run github.com/wailsapp/wails/v2/cmd/wails@v2.11.0 generate module -nocolour
git diff --exit-code -- frontend/wailsjs
```

Do not hand-edit `frontend/wailsjs/`. `frontend/settings-bindings.denylist.txt` and the frontend tests prevent removed field-level settings bindings from being reintroduced.

`UpdateSettings` is transactional: critical runtime effects are rolled back when commit fails; changing the port of a running proxy returns `restartRequirements(scope=proxy)` so the proxy can be stopped and started explicitly; best-effort synchronization failures do not undo durable settings and are returned through `warnings`. Frontend callers must surface both `warnings` and `restartRequirements`.

## Project Structure

```
EasyDownload/
├── app.go                 # Wails application entry
├── main.go                # Program entry
├── internal/
│   ├── api/               # Internal API server
│   ├── download/          # Download manager, platform downloaders
│   ├── proxy/             # MITM proxy, certificate management
│   └── utils/             # Utility functions
├── frontend/
│   ├── src/
│   │   ├── views/         # Page components
│   │   ├── stores/        # Pinia state management
│   │   ├── router/        # Vue Router
│   │   └── types/         # TypeScript types
│   └── wailsjs/           # Wails generated bindings
└── build/
    └── bin/               # Build output
```

## Disclaimer

This project is for learning and technical research purposes only. Please comply with relevant laws and regulations and respect the rights of content creators. Downloaded content is for personal learning use only and should not be used for commercial purposes or illegal distribution.

## License

[MIT](LICENSE)
