// Video specification for a specific quality (WeChat)
export interface VideoSpec {
  fileFormat: string
  width: number
  height: number
  durationMs: number
}

// Video detected by proxy/sniffer
export interface DetectedVideo {
  id: string
  title: string
  cover: string
  url: string
  source: 'wechat' | 'bilibili'
  quality: string
  duration: number
  author: string
  authorAvatar?: string  // Author avatar URL
  timestamp: number
  decodeKey?: string  // Decryption key for WeChat videos
  fileSize?: number   // File size in bytes
  width?: number      // Video width in pixels
  height?: number     // Video height in pixels
  isCurrentVideo?: boolean
  fileFormats?: string[]  // Available quality formats (e.g., 'mp4_720p', 'mp4_1080p')
  specs?: VideoSpec[]     // Detailed spec info for each quality
}

// Download task status
export type DownloadStatus = 'pending' | 'downloading' | 'paused' | 'completed' | 'failed' | 'cancelled'

// Download task
export interface DownloadTask {
  id: string
  url: string
  title: string
  cover: string
  source: string
  quality: string
  filePath: string
  fileName: string
  fileSize: number
  downloaded: number
  progress: number
  speed: number
  status: DownloadStatus
  error: string
  createdAt: number
  completedAt: number
}

// Bilibili video stream
export interface BilibiliStream {
  quality: number
  qualityName: string
  format: string
  size: number
  videoUrl: string
  audioUrl: string
}

// Bilibili video part (分P)
export interface BilibiliPart {
  cid: number
  page: number
  partName: string
  duration: number
}

// Bilibili video info
export interface BilibiliVideo {
  bv: string
  av: string
  title: string
  cover: string
  author: string
  duration: number
  desc: string
  parts: BilibiliPart[]
  streams: BilibiliStream[]
}

// App info
export interface AppInfo {
  version: string
  proxyPort: number
  apiPort: number
  downloadDir: string
  certPath: string
  minimizeToTray: boolean
  showNotification: boolean
  firstRunComplete: boolean
  proxyDebug?: boolean
  wechatNoMITM?: boolean
}

// App status
export interface AppStatus {
  proxyRunning: boolean
  certInstalled: boolean
  ffmpegAvailable: boolean
}

