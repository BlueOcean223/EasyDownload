import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { DetectedVideo, DownloadTask, AppInfo } from '@/types'
import {
  StartProxy,
  StopProxy,
  IsProxyRunning,
  IsCertInstalled,
  InstallCert,
  UninstallCert,
  GetDetectedVideos,
  ClearDetectedVideos,
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
  SetFirstRunComplete,
  SetMinimizeToTray,
  SetShowNotification,
  SetTheme,
  SetLanguage,
  SetCloseAction,
  SetDontAskOnClose,
  SetDontRemindCertWizard,
  RequestClose
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

  // Close behavior state
  const closeAction = ref<'' | 'exit' | 'minimize'>('')
  const dontAskOnClose = ref(false)

  // Welcome wizard behavior
  const dontRemindCertWizard = ref(false)

  // Computed
  const pendingDownloads = computed(() =>
    downloads.value.filter(d => d.status === 'pending' || d.status === 'downloading' || d.status === 'paused')
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
      // Note: firstRunComplete is deprecated, wizard display is now based on certInstalled

      // Load settings from appInfo
      if (appInfo.value) {
        minimizeToTray.value = (appInfo.value as any).minimizeToTray ?? true
        showNotification.value = (appInfo.value as any).showNotification ?? true
        theme.value = (appInfo.value as any).theme ?? 'dark'
        language.value = (appInfo.value as any).language ?? 'zh-CN'
        useUpstreamProxy.value = (appInfo.value as any).useUpstreamProxy ?? false
        closeAction.value = (appInfo.value as any).closeAction ?? ''
        dontAskOnClose.value = (appInfo.value as any).dontAskOnClose ?? false
        dontRemindCertWizard.value = (appInfo.value as any).dontRemindCertWizard ?? false

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
      if (low.includes('this is a modal window')) return true
      if (low.includes('modal window') && low.includes('this is')) return true
      if (low.includes('beginning of dialog window')) return true
      if (low.includes('escape will cancel')) return true
      if (low.includes('cancel and close the window')) return true
      if (low === 'play video' || low.includes('play video')) return true
      if (low.includes('restore all settings to the default values')) return true
      if (low.includes('video player is loading')) return true
      if (low.includes('transparency') && low.includes('opaque')) return true
      if (low === 'transparency' || low === 'opaque' || low.includes('semi-transparent') || low.includes('semi transparent')) return true
      if (/[0-9]{1,2}:[0-9]{2}\s*\/\s*[0-9]{1,2}:[0-9]{2}/.test(s)) return true
      if (s.includes('自动续播') || s.includes('小窗模式') || s.includes('默认值')) return true
      if (s === '未知标题') return true
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
      if (low.includes('restore all settings to the default values')) return true
      if (low.includes('video player is loading')) return true
      if (s === '未知作者') return true
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
      // "First valid value locks" strategy:
      // Once a field has a valid value, it won't be overwritten by subsequent updates
      // (except for numeric fields where larger values are considered better)
      const merged: DetectedVideo = { ...oldV }

      // id: always use the stable id (should be the same anyway)
      if (next.id) merged.id = next.id

      // url: only update if old is bad and new is good
      const oldUrlBad = !oldV.url || isLikelyBadWeChatURL(oldV.url)
      const newUrlGood = next.url && !isLikelyBadWeChatURL(next.url)
      if (oldUrlBad && newUrlGood) {
        merged.url = next.url
      }

      // title: only update if old is bad and new is good (first valid locks)
      const oldTitleBad = isBadWeChatTitle(oldV.title)
      const newTitleGood = !isBadWeChatTitle(next.title)
      if (oldTitleBad && newTitleGood) {
        merged.title = next.title
      }

      // author: only update if old is bad and new is good (first valid locks)
      const oldAuthorBad = isBadWeChatAuthor(oldV.author)
      const newAuthorGood = !isBadWeChatAuthor(next.author)
      if (oldAuthorBad && newAuthorGood) {
        merged.author = next.author
      }

      // cover: only update if old is empty and new is not
      if (!oldV.cover && next.cover) {
        merged.cover = next.cover
      }

      // authorAvatar: only update if old is empty and new is not
      if (!oldV.authorAvatar && next.authorAvatar) {
        merged.authorAvatar = next.authorAvatar
      }

      // duration: only update if old is 0/missing and new is positive
      if ((!oldV.duration || oldV.duration <= 0) && next.duration && next.duration > 0) {
        merged.duration = next.duration
      }

      // fileSize: update if old is 0/missing, or if new is significantly larger (more accurate)
      if ((!oldV.fileSize || oldV.fileSize <= 0) && next.fileSize && next.fileSize > 0) {
        merged.fileSize = next.fileSize
      } else if (oldV.fileSize && oldV.fileSize > 0 && next.fileSize && next.fileSize > oldV.fileSize * 1.2) {
        // New fileSize is >20% larger, likely more accurate (full size vs chunk size)
        merged.fileSize = next.fileSize
      }

      // width/height: only update if old is 0/missing and new is positive
      if ((!oldV.width || oldV.width <= 0) && next.width && next.width > 0) {
        merged.width = next.width
      }
      if ((!oldV.height || oldV.height <= 0) && next.height && next.height > 0) {
        merged.height = next.height
      }

      // fileFormats: only update if old is empty and new is not
      if ((!oldV.fileFormats || oldV.fileFormats.length === 0) && next.fileFormats && next.fileFormats.length > 0) {
        merged.fileFormats = next.fileFormats
      }

      // specs: only update if old is empty and new is not
      if ((!oldV.specs || oldV.specs.length === 0) && next.specs && next.specs.length > 0) {
        merged.specs = next.specs
      }

      // decodeKey: only update if old is empty and new is not
      if (!oldV.decodeKey && next.decodeKey) {
        merged.decodeKey = next.decodeKey
      }

      // isCurrentVideo: always take the latest value (this is a state flag, not content)
      merged.isCurrentVideo = next.isCurrentVideo

      // source/quality/timestamp: take from next (metadata fields)
      if (next.source) merged.source = next.source
      if (next.quality) merged.quality = next.quality
      if (next.timestamp) merged.timestamp = next.timestamp

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

    // FFmpeg ready event
    EventsOn('ffmpeg:ready', (available: boolean) => {
      if (available) {
        ffmpegAvailable.value = true
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

  async function uninstallCertificate() {
    try {
      await UninstallCert()
      certInstalled.value = false
    } catch (error) {
      console.error('Failed to uninstall certificate:', error)
      throw error
    }
  }

  async function clearVideos() {
    await ClearDetectedVideos()
    detectedVideos.value = []
  }

  async function downloadDetectedVideo(video: DetectedVideo, selectedFormat?: string) {
    try {
      // DetectedVideo.id is a stable identifier used for dedupe in the sniffer list.
      // Download tasks must use a unique ID per download attempt; otherwise the backend
      // rejects duplicates ("task with ID ... already exists") and subsequent downloads fail.
      const downloadTaskId = `${video.source}_${video.id}_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`
      
      // Use DownloadVideoWithKey if decodeKey is present (for encrypted WeChat videos)
      const task = await DownloadVideoWithKey(
        downloadTaskId,
        video.url,
        video.title,
        video.cover,
        video.source,
        selectedFormat || video.quality || '',
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

  async function setCloseAction(action: '' | 'exit' | 'minimize') {
    await SetCloseAction(action)
    closeAction.value = action
  }

  async function setDontAskOnClose(dontAsk: boolean) {
    await SetDontAskOnClose(dontAsk)
    dontAskOnClose.value = dontAsk
  }

  async function setDontRemindCertWizard(dontRemind: boolean) {
    await SetDontRemindCertWizard(dontRemind)
    dontRemindCertWizard.value = dontRemind
  }

  async function requestAppClose(action: 'exit' | 'minimize') {
    await RequestClose(action)
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
    closeAction,
    dontAskOnClose,
    dontRemindCertWizard,

    // Computed
    pendingDownloads,
    completedDownloads,

    // Actions
    initApp,
    toggleProxy,
    installCertificate,
    uninstallCertificate,
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
    setAppLanguage,
    setCloseAction,
    setDontAskOnClose,
    setDontRemindCertWizard,
    requestAppClose
  }
})
