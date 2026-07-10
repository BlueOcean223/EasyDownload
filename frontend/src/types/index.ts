export interface DetectedResource {
  id: string
  label: string
  mimeType?: string
  quality?: string
  fileFormat?: string
  width?: number
  height?: number
  durationMs?: number
  sizeBytes?: number
  encrypted?: boolean
  default?: boolean
}

export type DetectionChangeType = 'inserted' | 'updated' | 'removed' | 'cleared'

// Backend-owned detected-media domain model. Identity and merge semantics live
// in internal/detection; the frontend only renders authoritative snapshots.
export interface DetectedVideo {
  id: string
  source: string
  platform: string
  title: string
  author?: string
  pageUrl?: string
  coverUrl?: string
  candidates: DetectedResource[]
  detectedAt: string
  lastSeenAt: string
  authorAvatar?: string
  durationMs?: number
  width?: number
  height?: number
  isCurrent?: boolean
}

export interface DetectionSnapshot {
  revision: number
  videos: DetectedVideo[]
}

export interface DetectionChange {
  type: DetectionChangeType
  changedId?: string
  snapshot: DetectionSnapshot
}

// Download task status
export type DownloadStatus = 'pending' | 'running' | 'paused' | 'completed' | 'failed' | 'canceled'

export interface OutputPolicy {
  directory: string
  plannedFilename: string
  plannedFinalPath: string
  conflictStrategy: 'auto_rename'
}

export interface TaskProgressSummary {
  percent: number
  bytesLoaded?: number
  bytesTotal?: number
  currentStage?: string
  stageLabel?: string
  itemsDone?: number
  itemsTotal?: number
}

export interface TaskArtifact {
  id?: string
  kind: 'final' | 'temporary'
  path: string
  fileName?: string
  mediaType?: string
  size?: number
  primary?: boolean
  createdAt?: number
  cleanupFailed?: boolean
}

export interface TaskError {
  code: string
  category: 'transport' | 'platform' | 'output' | 'canceled' | 'unexpected'
  message: string
  retryable: boolean
  userAction?: string
  cause?: string
  metadata?: Record<string, string>
}

// Secret-free task projection returned by the backend. Platform execution
// inputs (URLs, headers, credentials, decode keys and checkpoints) never cross
// the Wails boundary.
export interface DownloadTask {
  id: string
  instance: number
  generation: number
  revision: number
  platformId?: string
  title: string
  cover: string
  displaySource?: string
  outputPolicy: OutputPolicy
  progressSummary: TaskProgressSummary
  artifacts?: TaskArtifact[]
  speed: number
  status: DownloadStatus
  error: string
  lastError?: string
  lastErrorDetail?: TaskError
  createdAt: number
  completedAt: number
  executionState?: 'running' | 'publishing' | 'stopping' | 'finished' | string
}

export type DownloadStopReason = 'pause' | 'cancel' | 'shutdown' | 'failure' | 'task_removal'

export interface DownloadStopReceipt {
  accepted: boolean
  operationId: string
  taskId: string
  requestedReason: DownloadStopReason
  effectiveReason: DownloadStopReason
  executionState: string
  revision: number
  taskInstance: number
  taskGeneration: number
  taskRevision: number
  error?: TaskError
}

export interface DownloadLifecycleEvent {
  operationId: string
  taskId: string
  phase: 'stopping' | 'completed' | 'failed'
  effectiveReason: DownloadStopReason
  resultStatus?: DownloadStatus
  removed?: boolean
  error?: TaskError
  revision: number
  taskInstance: number
  taskGeneration: number
  taskRevision: number
  task?: DownloadTask
  occurredAt: number
}

export interface LegacyDownloadNotice {
  code: string
  legacyPath: string
  v2Path: string
  imported: boolean
  preserved: boolean
  rollbackAvailable: boolean
  message: string
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

// Runtime metadata. User settings are intentionally not duplicated here.
export interface AppRuntimeInfo {
  version: string
  apiPort: number
  apiToken?: string
  ffmpegPath?: string
  certPath?: string
  certInstalled: boolean
  ffmpegAvailable: boolean
}

export interface SettingsSnapshot {
  proxyPort: number
  apiPort: number
  downloadDir: string
  maxConcurrent: number
  minimizeToTray: boolean
  showNotification: boolean
  firstRunComplete: boolean
  closeAction: '' | 'exit' | 'minimize'
  dontAskOnClose: boolean
  theme: 'dark' | 'light'
  language: 'zh-CN' | 'en-US'
  upstreamProxy: string
  useUpstreamProxy: boolean
  proxyDebug: boolean
  dontRemindCertWizard: boolean
}

export interface SettingsPatch {
  proxyPort?: number
  apiPort?: number
  downloadDir?: string
  maxConcurrent?: number
  minimizeToTray?: boolean
  showNotification?: boolean
  firstRunComplete?: boolean
  closeAction?: '' | 'exit' | 'minimize'
  dontAskOnClose?: boolean
  theme?: 'dark' | 'light'
  language?: 'zh-CN' | 'en-US'
  upstreamProxy?: string
  useUpstreamProxy?: boolean
  proxyDebug?: boolean
  dontRemindCertWizard?: boolean
}

export interface SettingsWarning {
  code: string
  effect?: string
  message: string
}

export interface RestartRequirement {
  scope: 'app' | 'proxy'
  fields: string[]
  reason: string
}

export interface SettingsUpdateResult {
  settings: SettingsSnapshot
  warnings?: SettingsWarning[]
  restartRequired: boolean
  restartRequirements?: RestartRequirement[]
}

export interface SettingsDiagnostic {
  code: 'settings.inconsistent' | string
  message: string
  rollbackErrors?: string[]
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
