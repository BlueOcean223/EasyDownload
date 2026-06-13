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
export type DownloadStatus = 'pending' | 'downloading' | 'retrying' | 'paused' | 'completed' | 'failed' | 'cancelled'

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
  retryCount?: number
  maxRetry?: number
  lastError?: string
  createdAt: number
  completedAt: number
  // Album fields (for Douyin albums)
  isAlbum?: boolean
  albumTotal?: number
  albumCompleted?: number
}

// Bilibili video stream
export interface BilibiliStream {
  quality: number
  qualityName: string
  format: string
  size: number
  videoUrl: string
  audioUrl: string
  drmKey?: string
  drmTechType?: number
  kid?: string
  biliDrmUri?: string
}

// Bilibili QR code for login
export interface BilibiliQRCode {
  url: string
  qrcodeKey: string
}

// Bilibili login status
export interface BilibiliLoginStatus {
  code: number    // 0=success, 86038=expired, 86090=scanned waiting, 86101=not scanned
  message: string
  sessData: string
}

// Bilibili user info
export interface BilibiliUserInfo {
  isLogin: boolean
  uid: number
  username: string
  face: string      // Avatar URL
  isVip: boolean    // Is active 大会员 (type > 0 AND status == 1)
  vipType: number   // 0=无, 1=月度, 2=年度
  vipStatus: number // 0=无效/过期, 1=有效
}

// Bilibili video part (分P)
export interface BilibiliPart {
  cid: number
  page: number
  partName: string
  duration: number
  streams?: BilibiliStream[]  // Stream info for this part (optional, loaded on demand)
  bv?: string
  aid?: number
  epId?: number
  badge?: string
  badgeType?: number
  sectionType?: number
  cover?: string
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
  seasonId?: number
  mediaId?: number
  epId?: number
  badge?: string
  seasonType?: number
  isBangumi?: boolean
  totalEps?: number
  currentPartIndex?: number
}

// App info
export interface AppInfo {
  version: string
  proxyPort: number
  apiPort: number
  apiToken?: string
  downloadDir: string
  ffmpegPath?: string
  certPath: string
  minimizeToTray: boolean
  showNotification: boolean
  firstRunComplete: boolean
  dontRemindCertWizard?: boolean
  theme?: 'dark' | 'light'
  language?: 'zh-CN' | 'en-US'
  upstreamProxy?: string
  useUpstreamProxy?: boolean
  proxyDebug?: boolean
  closeAction?: '' | 'exit' | 'minimize'
  dontAskOnClose?: boolean
  wechatNoMITM?: boolean
}

// App status
export interface AppStatus {
  proxyRunning: boolean
  certInstalled: boolean
  ffmpegAvailable: boolean
}

// Douyin stream (field names match Go exported struct)
export interface DouyinStream {
  QualityKey: string
  QualityName: string
  Width: number
  Height: number
  Bitrate: number
  URL: string
  Size: number // File size in bytes (estimated via HEAD request)
}

// DisplayImage is a common interface for image display components.
// Used by ImageSelector, ImagePreviewModal, and LazyImageGrid.
export interface DisplayImage {
  URL: string
  Width: number
  Height: number
}

// Douyin image (field names match Go exported struct)
// For mixed content (aweme_type 68), items can be images or videos.
// If VideoURL is non-empty, the item is a video.
export interface DouyinImage extends DisplayImage {
  VideoURL?: string // Non-empty for video items in mixed content albums
}

// Douyin item (video or album) (field names match Go exported struct)
export interface DouyinItem {
  Type: string // 'video' | 'album'
  ID: string
  Title: string
  Cover: string
  Author: string
  AuthorID: string
  Duration: number
  Streams: DouyinStream[]
  Images: DouyinImage[]
}

// XHS stream
export interface XHSStream {
  QualityKey: string
  QualityName: string
  Width: number
  Height: number
  URL: string
  BackupURLs?: string[]
  Size: number
  Format?: string
  FPS?: number
  VideoCodec?: string
  VideoBitrate?: number
  AudioCodec?: string
  AudioBitrate?: number
  StreamDesc?: string
  StreamType?: number
  Weight?: number
  Duration?: number
  DefaultStream?: number
  HDRType?: number
  Rotate?: number
}

export interface XHSTag {
  ID: string
  Name: string
  Type: string
}

export interface XHSInteractInfo {
  LikedCount: string
  CollectedCount: string
  CommentCount: string
  ShareCount: string
}

// XHS image (extends DisplayImage for shared component compatibility)
export interface XHSImage extends DisplayImage {
  BackupURLs?: string[]
  TraceId?: string
  FileID?: string
  LivePhoto?: boolean
  LivePhotoURL?: string
}

// XHS item (note)
export interface XHSItem {
  Type: string // 'video' | 'image'
  ID: string
  Title: string
  Desc: string
  Cover: string
  Author: string
  AuthorID: string
  AuthorAvatar: string
  Timestamp: number
  IPLocation?: string
  Tags?: XHSTag[]
  InteractInfo?: XHSInteractInfo
  Streams: XHSStream[]
  Images: XHSImage[]
}
