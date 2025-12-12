import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { DetectedVideo, DownloadTask, AppInfo } from '@/types'
import {
  StartProxy,
  StopProxy,
  IsProxyRunning,
  IsCertInstalled,
  InstallCert,
  GetDetectedVideos,
  ClearDetectedVideos,
  DownloadVideo,
  DownloadVideoWithKey,
  GetDownloads,
  PauseDownload,
  ResumeDownload,
  CancelDownload,
  RemoveDownload,
  GetDownloadDir,
  SetDownloadDir,
  SelectDownloadDir,
  OpenDownloadDir,
  GetAppInfo,
  IsFFmpegAvailable,
  IsFirstRunComplete,
  SetFirstRunComplete,
  SetMinimizeToTray,
  SetShowNotification,
  GetTheme,
  SetTheme,
  GetLanguage,
  SetLanguage
} from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

export const useAppStore = defineStore('app', () => {
  // State
  const proxyRunning = ref(false)
  const certInstalled = ref(false)
  const ffmpegAvailable = ref(false)
  const detectedVideos = ref<DetectedVideo[]>([])
  const downloads = ref<DownloadTask[]>([])
  const downloadDir = ref('')
  const appInfo = ref<AppInfo | null>(null)
  const loading = ref(false)

  // New state for settings
  const firstRunComplete = ref(false)
  const minimizeToTray = ref(true)
  const showNotification = ref(true)

  // Theme and language state
  const theme = ref<'dark' | 'light'>('dark')
  const language = ref<'zh-CN' | 'en-US'>('zh-CN')

  // Proxy chain state
  const useUpstreamProxy = ref(false)

  // Computed
  const pendingDownloads = computed(() =>
    downloads.value.filter(d => d.status === 'pending' || d.status === 'downloading')
  )

  const completedDownloads = computed(() =>
    downloads.value.filter(d => d.status === 'completed')
  )

  // Actions
  async function initApp() {
    loading.value = true
    try {
      // Get app info
      appInfo.value = await GetAppInfo() as AppInfo
      downloadDir.value = await GetDownloadDir()

      // Check status
      proxyRunning.value = await IsProxyRunning()
      certInstalled.value = await IsCertInstalled()
      ffmpegAvailable.value = await IsFFmpegAvailable()
      firstRunComplete.value = await IsFirstRunComplete()

      // Load settings from appInfo
      if (appInfo.value) {
        minimizeToTray.value = (appInfo.value as any).minimizeToTray ?? true
        showNotification.value = (appInfo.value as any).showNotification ?? true
        theme.value = (appInfo.value as any).theme ?? 'dark'
        language.value = (appInfo.value as any).language ?? 'zh-CN'
        useUpstreamProxy.value = (appInfo.value as any).useUpstreamProxy ?? false

        // Apply theme to DOM
        document.documentElement.setAttribute('data-theme', theme.value)
      }

      // Load existing data
      detectedVideos.value = await GetDetectedVideos() as DetectedVideo[]
      downloads.value = await GetDownloads() as DownloadTask[]

      // Setup event listeners
      setupEventListeners()
    } catch (error) {
      console.error('Failed to init app:', error)
    } finally {
      loading.value = false
    }
  }

  function setupEventListeners() {
    function isBadWeChatTitle(t?: string) {
      if (!t) return true
      const s = String(t).trim()
      if (!s) return true
      const low = s.toLowerCase()
      if (low.includes('beginning of dialog window')) return true
      if (low.includes('escape will cancel')) return true
      if (low.includes('cancel and close the window')) return true
      if (low === 'play video' || low.includes('play video')) return true
      return false
    }

    function isBadWeChatAuthor(a?: string) {
      if (!a) return true
      const s = String(a).trim()
      if (!s) return true
      const low = s.toLowerCase()
      if (low === 'play video' || low.includes('play video')) return true
      if (low.includes('beginning of dialog window')) return true
      if (low.includes('escape will cancel')) return true
      return false
    }

    function isLikelyBadWeChatURL(u?: string) {
      if (!u) return true
      const s = String(u).trim()
      if (!s) return true
      const low = s.toLowerCase()
      // obvious live/stream formats
      if (low.includes('.m3u8') || low.includes('.flv') || low.includes('.mpd')) return true
      // chunk-ish worker URLs can slip through; never prefer them
      if (low.includes('startidx=') || low.includes('size=')) return true
      // if it's a finder download host, require VOD signature
      if (low.includes('finder.video.qq.com') || low.includes('findermp.video.qq.com')) {
        if (!low.includes('stodownload')) return true
        if (!low.includes('encfilekey=')) return true
      }
      return false
    }

    function mergePreferExisting(oldV: DetectedVideo, next: DetectedVideo): DetectedVideo {
      // Never allow "degradation" from incomplete/incorrect payloads.
      const merged: DetectedVideo = { ...oldV, ...next }

      // url: prefer a likely-good VOD url; keep old if new looks bad
      if (next.url && isLikelyBadWeChatURL(next.url) && oldV.url) {
        merged.url = oldV.url
      }

      // title/author: ignore obvious UI placeholders
      if (isBadWeChatTitle(next.title) && !isBadWeChatTitle(oldV.title)) merged.title = oldV.title
      if (isBadWeChatAuthor(next.author) && !isBadWeChatAuthor(oldV.author)) merged.author = oldV.author

      // cover: keep old if new is empty
      if (!next.cover && oldV.cover) merged.cover = oldV.cover

      // duration/fileSize/wh: keep old if new is missing/zero
      if ((!next.duration || next.duration <= 0) && oldV.duration && oldV.duration > 0) merged.duration = oldV.duration
      if ((!next.fileSize || next.fileSize <= 0) && oldV.fileSize && oldV.fileSize > 0) merged.fileSize = oldV.fileSize
      if ((!next.width || next.width <= 0) && oldV.width && oldV.width > 0) merged.width = oldV.width
      if ((!next.height || next.height <= 0) && oldV.height && oldV.height > 0) merged.height = oldV.height

      // isCurrentVideo: preserve true if either says true
      if (oldV.isCurrentVideo && !next.isCurrentVideo) merged.isCurrentVideo = true

      return merged
    }

    // Video detected event
    EventsOn('video:detected', (video: DetectedVideo) => {
      // For WeChat: keep history, but dedupe/update by stable id (pageKey) to avoid spam
      if (video.source === 'wechat') {
        // Ensure only ONE card is marked as "当前"
        if (video.isCurrentVideo) {
          for (const v of detectedVideos.value) {
            if (v.source === 'wechat') v.isCurrentVideo = false
          }
        }

        // Newest first:
        // - New id: unshift to front
        // - Same id: update in place (do not reorder) to avoid UI jumps when metadata refreshes
        const idx = detectedVideos.value.findIndex(v => v.source === 'wechat' && v.id === video.id)
        if (idx !== -1) {
          detectedVideos.value[idx] = mergePreferExisting(detectedVideos.value[idx], video)
        } else {
          detectedVideos.value.unshift(video)
        }

        // cap wechat history (keep other sources untouched)
        const maxWechat = 80
        const wechat = detectedVideos.value.filter(v => v.source === 'wechat')
        const others = detectedVideos.value.filter(v => v.source !== 'wechat')
        detectedVideos.value = [...wechat.slice(0, maxWechat), ...others]
        return
      }

      // Check for duplicates
      const exists = detectedVideos.value.some(v => v.url === video.url)
      if (!exists) {
        detectedVideos.value.unshift(video)
      }
    })

    // Download progress event
    EventsOn('download:progress', (task: DownloadTask) => {
      const index = downloads.value.findIndex(d => d.id === task.id)
      if (index !== -1) {
        downloads.value[index] = task
      }
    })

    // Download complete event
    EventsOn('download:complete', (task: DownloadTask) => {
      const index = downloads.value.findIndex(d => d.id === task.id)
      if (index !== -1) {
        downloads.value[index] = task
      }
    })

    // Download error event
    EventsOn('download:error', (data: { task: DownloadTask; error: string }) => {
      const index = downloads.value.findIndex(d => d.id === data.task.id)
      if (index !== -1) {
        downloads.value[index] = { ...data.task, error: data.error }
      }
    })

    // Download start event
    EventsOn('download:start', (task: DownloadTask) => {
      const exists = downloads.value.some(d => d.id === task.id)
      if (!exists) {
        downloads.value.unshift(task)
      }
    })
  }

  async function toggleProxy() {
    try {
      if (proxyRunning.value) {
        await StopProxy()
        proxyRunning.value = false
      } else {
        await StartProxy()
        proxyRunning.value = true
      }
    } catch (error) {
      console.error('Failed to toggle proxy:', error)
      throw error
    }
  }

  async function installCertificate() {
    try {
      await InstallCert()
      certInstalled.value = true
    } catch (error) {
      console.error('Failed to install certificate:', error)
      throw error
    }
  }

  async function clearVideos() {
    await ClearDetectedVideos()
    detectedVideos.value = []
  }

  async function downloadDetectedVideo(video: DetectedVideo) {
    try {
      // Use DownloadVideoWithKey if decodeKey is present (for encrypted WeChat videos)
      const task = await DownloadVideoWithKey(
        video.id,
        video.url,
        video.title,
        video.cover,
        video.source,
        video.quality || '',
        video.decodeKey || ''
      ) as DownloadTask

      downloads.value.unshift(task)
      return task
    } catch (error) {
      console.error('Failed to start download:', error)
      throw error
    }
  }

  async function pauseDownloadTask(id: string) {
    await PauseDownload(id)
    const task = downloads.value.find(d => d.id === id)
    if (task) {
      task.status = 'paused'
    }
  }

  async function resumeDownloadTask(id: string) {
    await ResumeDownload(id)
    const task = downloads.value.find(d => d.id === id)
    if (task) {
      task.status = 'downloading'
    }
  }

  async function cancelDownloadTask(id: string) {
    await CancelDownload(id)
    const task = downloads.value.find(d => d.id === id)
    if (task) {
      task.status = 'cancelled'
    }
  }

  async function removeDownloadTask(id: string) {
    await RemoveDownload(id)
    downloads.value = downloads.value.filter(d => d.id !== id)
  }

  async function selectFolder() {
    const dir = await SelectDownloadDir()
    if (dir) {
      downloadDir.value = dir
    }
    return dir
  }

  async function openFolder() {
    await OpenDownloadDir()
  }

  async function updateDownloadDir(dir: string) {
    await SetDownloadDir(dir)
    downloadDir.value = dir
  }

  async function completeFirstRun() {
    await SetFirstRunComplete(true)
    firstRunComplete.value = true
  }

  async function setMinimizeToTray(enabled: boolean) {
    await SetMinimizeToTray(enabled)
    minimizeToTray.value = enabled
  }

  async function setShowNotification(enabled: boolean) {
    await SetShowNotification(enabled)
    showNotification.value = enabled
  }

  async function setAppTheme(newTheme: 'dark' | 'light') {
    await SetTheme(newTheme)
    theme.value = newTheme
    document.documentElement.setAttribute('data-theme', newTheme)
  }

  async function setAppLanguage(newLang: 'zh-CN' | 'en-US') {
    await SetLanguage(newLang)
    language.value = newLang
  }

  return {
    // State
    proxyRunning,
    certInstalled,
    ffmpegAvailable,
    detectedVideos,
    downloads,
    downloadDir,
    appInfo,
    loading,
    firstRunComplete,
    minimizeToTray,
    showNotification,
    theme,
    language,
    useUpstreamProxy,

    // Computed
    pendingDownloads,
    completedDownloads,

    // Actions
    initApp,
    toggleProxy,
    installCertificate,
    clearVideos,
    downloadDetectedVideo,
    pauseDownloadTask,
    resumeDownloadTask,
    cancelDownloadTask,
    removeDownloadTask,
    selectFolder,
    openFolder,
    updateDownloadDir,
    completeFirstRun,
    setMinimizeToTray,
    setShowNotification,
    setAppTheme,
    setAppLanguage
  }
})

