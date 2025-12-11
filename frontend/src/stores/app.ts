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
    // Video detected event
    EventsOn('video:detected', (video: DetectedVideo) => {
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
      const task = await DownloadVideo(
        video.id,
        video.url,
        video.title,
        video.cover,
        video.source,
        video.quality
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

