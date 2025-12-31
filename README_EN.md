# EasyDownload

English | [简体中文](README.md)

A simple and easy-to-use desktop video downloader that supports downloading content from multiple platforms including WeChat Channels, Bilibili, Xiaohongshu (RedNote), and Douyin (TikTok China).

## Features

- **WeChat Channels Sniffer**: Automatically detects videos played in WeChat PC client, one-click download
- **Bilibili Download**: Supports BV ID, av ID, and full URLs with multiple quality options
- **Xiaohongshu Download**: Supports downloading videos and image notes from Xiaohongshu
- **Douyin Download**: Supports Douyin video download, including slideshow preview and download
- **Visual Interface**: Netflix-style video card display with clear download progress
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

### Download Bilibili Videos

1. Go to the "Bilibili" page
2. Paste the Bilibili video link (supports BV ID, av ID, or full URL)
3. Click "Parse" button to get video information
4. Select quality and click "Download Video"

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

## Development

### Requirements

- Go 1.21+
- Node.js 18+
- Wails CLI v2

### Install Wails CLI

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
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
