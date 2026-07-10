// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { useAppStore } from './app'
import ProxiedImage from '@/components/ProxiedImage.vue'
import DownloadTaskCard from '@/components/DownloadTaskCard.vue'
import type { AppRuntimeInfo, DetectionChange, DetectedVideo, DownloadLifecycleEvent, DownloadTask, SettingsSnapshot, SettingsUpdateResult } from '@/types'

const mocks = vi.hoisted(() => {
  const listeners = new Map<string, Array<(...args: any[]) => void>>()
  return {
    listeners,
    app: {
      StartProxy: vi.fn(),
      StopProxy: vi.fn(),
      IsProxyRunning: vi.fn(),
      IsCertInstalled: vi.fn(),
      InstallCert: vi.fn(),
      UninstallCert: vi.fn(),
      GetDetectedVideos: vi.fn(),
      ClearDetectedVideos: vi.fn(),
      StartDetectedDownload: vi.fn(),
      GetDownloads: vi.fn(),
      TakeLegacyDownloadNotice: vi.fn(),
      PauseDownload: vi.fn(),
      ResumeDownload: vi.fn(),
      RetryDownload: vi.fn(),
      CancelDownload: vi.fn(),
      RemoveDownload: vi.fn(),
      SelectDownloadDir: vi.fn(),
      OpenDownloadDir: vi.fn(),
      GetAppInfo: vi.fn(),
      GetSettings: vi.fn(),
      UpdateSettings: vi.fn(),
      IsFFmpegAvailable: vi.fn(),
      InstallFFmpeg: vi.fn(),
      RequestClose: vi.fn()
    },
    runtime: {
      EventsOn: vi.fn((eventName: string, callback: (...args: any[]) => void) => {
        const existing = listeners.get(eventName) ?? []
        existing.push(callback)
        listeners.set(eventName, existing)
        return vi.fn()
      })
    }
  }
})

vi.mock('../../wailsjs/go/main/App', () => mocks.app)
vi.mock('../../wailsjs/runtime/runtime', () => mocks.runtime)

const baseSettings: SettingsSnapshot = {
  downloadDir: 'D:/Downloads',
  proxyPort: 8899,
  apiPort: 8898,
  maxConcurrent: 3,
  firstRunComplete: false,
  minimizeToTray: true,
  showNotification: true,
  theme: 'dark',
  language: 'zh-CN',
  useUpstreamProxy: false,
  upstreamProxy: '',
  proxyDebug: false,
  closeAction: '',
  dontAskOnClose: false,
  dontRemindCertWizard: false
}

const baseRuntimeInfo: AppRuntimeInfo = {
  version: 'test',
  apiPort: 18899,
  apiToken: 'token',
  ffmpegPath: '',
  certPath: 'D:/cert.pem',
  certInstalled: false,
  ffmpegAvailable: false
}

function emit(eventName: string, payload: unknown) {
  for (const listener of mocks.listeners.get(eventName) ?? []) {
    listener(payload)
  }
}

function video(id: string, title: string): DetectedVideo {
  return {
    id,
    source: 'wechat_proxy',
    platform: 'wechat',
    title,
    candidates: [{ id: `${id}:original`, label: '原始画质', default: true, encrypted: true }],
    detectedAt: '2026-07-10T00:00:00Z',
    lastSeenAt: '2026-07-10T00:00:00Z'
  }
}

let taskRevision = 0

function downloadTask(
  id: string,
  status: DownloadTask['status'] = 'running',
  percent = 0,
  generation = 1,
  revision = ++taskRevision,
  instance = 1
): DownloadTask {
  return {
    id,
    instance,
    generation,
    revision,
    title: `video-${id}`,
    cover: '',
    outputPolicy: {
      directory: 'D:/Downloads',
      plannedFilename: `${id}.mp4`,
      plannedFinalPath: `D:/Downloads/${id}.mp4`,
      conflictStrategy: 'auto_rename'
    },
    progressSummary: { percent },
    speed: 1,
    status,
    error: '',
    createdAt: 1,
    completedAt: status === 'completed' ? 2 : 0
  }
}

describe('app store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    mocks.listeners.clear()
    vi.clearAllMocks()
    taskRevision = 0

    mocks.app.GetAppInfo.mockResolvedValue({ ...baseRuntimeInfo })
    mocks.app.GetSettings.mockResolvedValue({ ...baseSettings })
    mocks.app.IsProxyRunning.mockResolvedValue(false)
    mocks.app.IsCertInstalled.mockResolvedValue(false)
    mocks.app.IsFFmpegAvailable.mockResolvedValue(false)
    mocks.app.GetDetectedVideos.mockResolvedValue({ revision: 0, videos: [] })
    mocks.app.GetDownloads.mockResolvedValue([])
    mocks.app.TakeLegacyDownloadNotice.mockResolvedValue(null)
  })

  it('replaces detected videos from authoritative backend snapshots', async () => {
    const store = useAppStore()
    await store.initApp()

    const first = video('v1', 'first')
    emit('video:detected', { type: 'inserted', changedId: first.id, snapshot: { revision: 1, videos: [first] } } satisfies DetectionChange)
    expect(store.detectedVideos.map(v => v.title)).toEqual(['first'])

    const updated = video('v1', 'updated')
    emit('video:detected', { type: 'updated', changedId: updated.id, snapshot: { revision: 2, videos: [updated] } } satisfies DetectionChange)
    expect(store.detectedVideos).toHaveLength(1)
    expect(store.detectedVideos[0].title).toBe('updated')

    emit('video:detected', { type: 'cleared', snapshot: { revision: 3, videos: [] } } satisfies DetectionChange)
    expect(store.detectedVideos).toEqual([])
  })

  it('ignores out-of-order detection snapshots and applies capacity eviction', async () => {
    const store = useAppStore()
    await store.initApp()

    const first = video('v1', 'first')
    const second = video('v2', 'second')
    emit('video:detected', { type: 'inserted', snapshot: { revision: 8, videos: [second, first] } } satisfies DetectionChange)
    emit('video:detected', { type: 'updated', snapshot: { revision: 7, videos: [first] } } satisfies DetectionChange)
    expect(store.detectedVideos.map(item => item.id)).toEqual(['v2', 'v1'])

    emit('video:detected', { type: 'inserted', snapshot: { revision: 9, videos: [second] } } satisfies DetectionChange)
    expect(store.detectedVideos.map(item => item.id)).toEqual(['v2'])
    expect(store.detectionRevision).toBe(9)
  })

  it('starts a detected download with opaque IDs only', async () => {
    const store = useAppStore()
    await store.initApp()
    const detected = video('v1', 'first')
    mocks.app.StartDetectedDownload.mockResolvedValue(downloadTask('task-1', 'pending'))

    await store.downloadDetectedVideo(detected, detected.candidates[0].id)

    expect(mocks.app.StartDetectedDownload).toHaveBeenCalledWith('v1', 'v1:original')
  })

  it('falls back to a current candidate when a cached candidate ID was replaced', async () => {
    const store = useAppStore()
    await store.initApp()
    const detected = video('v1', 'updated')
    detected.candidates = [{ id: 'v1:stable', label: '原始画质', default: true }]
    mocks.app.StartDetectedDownload.mockResolvedValue(downloadTask('task-1', 'pending'))

    await store.downloadDetectedVideo(detected, 'v1:derived-old')

    expect(mocks.app.StartDetectedDownload).toHaveBeenCalledWith('v1', 'v1:stable')
  })

  it('uses the effective runtime API port for proxied images until restart', async () => {
    const store = useAppStore()
    await store.initApp()

    const wrapper = mount(ProxiedImage, {
      props: { src: 'https://example.com/cover.jpg' }
    })

    expect(wrapper.get('img').attributes('src')).toContain('127.0.0.1:18899')
    expect(wrapper.get('img').attributes('src')).not.toContain(`127.0.0.1:${baseSettings.apiPort}`)
  })

  it('subscribes before GetDownloads and replays events that race the initial snapshot', async () => {
    const staleRunning = downloadTask('task-init-race', 'running', 20)
    const completed = downloadTask('task-init-race', 'completed', 100)
    let resolveDownloads!: (tasks: DownloadTask[]) => void
    mocks.app.GetDownloads.mockImplementationOnce(() => new Promise<DownloadTask[]>(resolve => {
      resolveDownloads = resolve
    }))

    const store = useAppStore()
    const initializing = store.initApp()
    await vi.waitFor(() => {
      expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(1)
      expect(mocks.listeners.get('download:complete')).toHaveLength(1)
    })

    emit('download:complete', completed)
    resolveDownloads([staleRunning])
    await initializing

    expect(store.downloads).toHaveLength(1)
    expect(store.downloads[0].status).toBe('completed')
    expect(store.downloads[0].progressSummary.percent).toBe(100)
  })

  it('shares concurrent initialization and ignores repeated full snapshots after success', async () => {
    const staleRunning = downloadTask('task-idempotent-init', 'running', 20, 1, 10)
    const completed = downloadTask('task-idempotent-init', 'completed', 100, 1, 11)
    let resolveDownloads!: (tasks: DownloadTask[]) => void
    mocks.app.GetDownloads.mockImplementationOnce(() => new Promise<DownloadTask[]>(resolve => {
      resolveDownloads = resolve
    }))

    const store = useAppStore()
    const first = store.initApp()
    const second = store.initApp()
    await vi.waitFor(() => expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(1))
    resolveDownloads([staleRunning])
    await Promise.all([first, second])

    emit('download:complete', completed)
    mocks.app.GetDownloads.mockResolvedValue([staleRunning])
    await store.initApp()

    expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(1)
    expect(store.downloads[0].status).toBe('completed')
    expect(store.downloads[0].progressSummary.percent).toBe(100)
  })

  it('retries initialization after failure without losing buffered download events', async () => {
    const staleRunning = downloadTask('task-retry-init', 'running', 20, 1, 10)
    const completed = downloadTask('task-retry-init', 'completed', 100, 1, 11)
    mocks.app.GetDownloads.mockResolvedValue([staleRunning])
    mocks.app.GetSettings
      .mockRejectedValueOnce(new Error('settings temporarily unavailable'))
      .mockResolvedValueOnce({ ...baseSettings })
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    const store = useAppStore()
    await store.initApp()
    expect(store.settings).toBeNull()
    emit('download:complete', completed)

    await store.initApp()

    expect(mocks.app.GetSettings).toHaveBeenCalledTimes(2)
    expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(2)
    expect(store.settings?.downloadDir).toBe(baseSettings.downloadDir)
    expect(store.downloads[0].status).toBe('completed')
    expect(store.downloads[0].progressSummary.percent).toBe(100)
    consoleError.mockRestore()
  })

  it('lets a buffered removal fence an older captured initial-list member', async () => {
    const removedTask = downloadTask('task-init-removed', 'running', 10, 1, 10, 1)
    let resolveDownloads!: (tasks: DownloadTask[]) => void
    mocks.app.GetDownloads.mockImplementationOnce(() => new Promise<DownloadTask[]>(resolve => {
      resolveDownloads = resolve
    }))

    const store = useAppStore()
    const initializing = store.initApp()
    await vi.waitFor(() => {
      expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(1)
      expect(mocks.listeners.get('download:lifecycle')).toHaveLength(1)
    })
    emit('download:lifecycle', {
      operationId: 'remove-during-list', taskId: removedTask.id, phase: 'completed',
      effectiveReason: 'task_removal', removed: true, revision: 20,
      taskInstance: 1, taskGeneration: 1, taskRevision: 9, occurredAt: 2
    } satisfies DownloadLifecycleEvent)
    // Even if an old backend projected its captured pointer with a numerically
    // newer task revision, removal is authoritative for the same identity.
    resolveDownloads([removedTask])
    await initializing

    expect(store.downloads).toEqual([])
  })

  it('keeps complete and error terminal markers against late progress', async () => {
    const completeID = 'task-complete'
    const errorID = 'task-error'
    mocks.app.GetDownloads.mockResolvedValue([
      downloadTask(completeID, 'running', 20),
      downloadTask(errorID, 'running', 30)
    ])
    const store = useAppStore()
    await store.initApp()

    emit('download:complete', downloadTask(completeID, 'completed', 100))
    emit('download:progress', downloadTask(completeID, 'running', 55))
    expect(store.downloads.find(task => task.id === completeID)?.status).toBe('completed')
    expect(store.downloads.find(task => task.id === completeID)?.progressSummary.percent).toBe(100)

    emit('download:error', {
      task: downloadTask(errorID, 'failed', 30),
      error: 'network failed'
    })
    emit('download:progress', downloadTask(errorID, 'running', 80))
    expect(store.downloads.find(task => task.id === errorID)?.status).toBe('failed')
    expect(store.downloads.find(task => task.id === errorID)?.error).toBe('network failed')
    expect(store.downloads.find(task => task.id === errorID)?.progressSummary.percent).toBe(30)
  })

  it('applies the authoritative terminal task payload for publish-vs-stop completion', async () => {
    const id = 'task-publish-stop'
    const initial = {
      ...downloadTask(id, 'running', 90, 1, 1, 1),
      speed: 1024,
      error: 'old error'
    }
    mocks.app.GetDownloads.mockResolvedValue([initial])
    const store = useAppStore()
    await store.initApp()

    const finalPath = 'D:/Downloads/video (1).mp4'
    const completed: DownloadTask = {
      ...downloadTask(id, 'completed', 100, 1, 5, 1),
      outputPolicy: {
        directory: 'D:/Downloads', plannedFilename: 'video (1).mp4',
        plannedFinalPath: finalPath, conflictStrategy: 'auto_rename'
      },
      artifacts: [
        { kind: 'final', path: finalPath, primary: true, size: 42 },
        { kind: 'temporary', path: `${finalPath}.tmp`, cleanupFailed: true }
      ],
      speed: 0,
      error: '',
      lastError: '',
      executionState: 'finished'
    }
    emit('download:lifecycle', {
      operationId: 'publish-stop', taskId: id, phase: 'completed', effectiveReason: 'pause',
      resultStatus: 'completed', revision: 6,
      taskInstance: 1, taskGeneration: 1, taskRevision: 5,
      task: completed, occurredAt: 2
    } satisfies DownloadLifecycleEvent)

    const rendered = store.downloads.find(task => task.id === id)
    expect(rendered?.status).toBe('completed')
    expect(rendered?.progressSummary.percent).toBe(100)
    expect(rendered?.speed).toBe(0)
    expect(rendered?.error).toBe('')
    expect(rendered?.outputPolicy.plannedFinalPath).toBe(finalPath)
    expect(rendered?.artifacts?.[0].path).toBe(finalPath)
    expect(rendered?.artifacts?.[1].cleanupFailed).toBe(true)

    const wrapper = mount(DownloadTaskCard, { props: { task: rendered!, mode: 'completed' } })
    expect(wrapper.text()).toContain('有残留文件')
    expect(wrapper.text()).not.toContain(`${finalPath}.tmp`)
  })

  it('creates a terminal card from the lifecycle payload before a delayed start event', async () => {
    const completed = downloadTask('task-terminal-first', 'completed', 100, 1, 5, 1)
    const store = useAppStore()
    await store.initApp()

    emit('download:lifecycle', {
      operationId: 'terminal-first', taskId: completed.id, phase: 'completed',
      effectiveReason: 'pause', resultStatus: 'completed', revision: 5,
      taskInstance: 1, taskGeneration: 1, taskRevision: 5,
      task: completed, occurredAt: 2
    } satisfies DownloadLifecycleEvent)
    emit('download:start', downloadTask(completed.id, 'running', 0, 1, 4, 1))

    expect(store.downloads).toHaveLength(1)
    expect(store.downloads[0].status).toBe('completed')
    expect(store.downloads[0].revision).toBe(5)
  })

  it('converges lifecycle events and late receipts by revision without speculative terminal state', async () => {
    const task = {
      id: 'task-1', instance: 1, generation: 1, revision: 1, title: 'video', cover: '', outputPolicy: {
        directory: 'D:/Downloads', plannedFilename: 'video.mp4',
        plannedFinalPath: 'D:/Downloads/video.mp4', conflictStrategy: 'auto_rename'
      }, progressSummary: { percent: 25 }, speed: 1, status: 'running', error: '',
      createdAt: 1, completedAt: 0
    } satisfies DownloadTask
    mocks.app.GetDownloads.mockResolvedValue([task])
    mocks.app.PauseDownload.mockResolvedValue({
      accepted: true, operationId: 'op-1', taskId: task.id,
      requestedReason: 'pause', effectiveReason: 'pause', executionState: 'stopping', revision: 5,
      taskInstance: 1, taskGeneration: 1, taskRevision: 2
    })
    const store = useAppStore()
    await store.initApp()

    await store.pauseDownloadTask(task.id)
    expect(store.downloads[0].status).toBe('running')
    expect(store.downloadStopOperations[task.id]?.reason).toBe('pause')

    emit('download:lifecycle', {
      operationId: 'op-1', taskId: task.id, phase: 'stopping', effectiveReason: 'pause',
      revision: 5, taskInstance: 1, taskGeneration: 1, taskRevision: 2, occurredAt: 1
    } satisfies DownloadLifecycleEvent)
    emit('download:progress', { ...task, progressSummary: { percent: 30 } })
    expect(store.downloads[0].progressSummary.percent).toBe(25)

    // An authoritative task snapshot may be stamped after the terminal event
    // reserved its version but arrive first. The older terminal lifecycle must
    // still clear the matching operation without overwriting that payload.
    emit('download:progress', downloadTask(task.id, 'paused', 25, 1, 4, 1))

    emit('download:lifecycle', {
      operationId: 'op-1', taskId: task.id, phase: 'completed', effectiveReason: 'pause',
      resultStatus: 'paused', revision: 6,
      taskInstance: 1, taskGeneration: 1, taskRevision: 3, occurredAt: 2
    } satisfies DownloadLifecycleEvent)
    expect(store.downloads[0].status).toBe('paused')
    expect(store.downloads[0].revision).toBe(4)
    expect(store.downloadStopOperations[task.id]).toBeUndefined()

    // A late stopping event/receipt cannot regress the terminal UI state.
    emit('download:lifecycle', {
      operationId: 'op-1', taskId: task.id, phase: 'stopping', effectiveReason: 'pause',
      revision: 5, taskInstance: 1, taskGeneration: 1, taskRevision: 1, occurredAt: 1
    } satisfies DownloadLifecycleEvent)
    expect(store.downloadStopOperations[task.id]).toBeUndefined()
    expect(store.downloads[0].status).toBe('paused')

    // A progress event queued before the terminal lifecycle event cannot
    // overwrite the authoritative paused result when delivered late.
    emit('download:progress', { ...task, status: 'running', progressSummary: { percent: 30 } })
    expect(store.downloads[0].status).toBe('paused')
  })

  it('keeps a task on cleanup failure and removes it only after authoritative lifecycle event', async () => {
    const task = {
      id: 'task-2', instance: 1, generation: 1, revision: 1, title: 'video', cover: '', outputPolicy: {
        directory: 'D:/Downloads', plannedFilename: 'video.mp4',
        plannedFinalPath: 'D:/Downloads/video.mp4', conflictStrategy: 'auto_rename'
      }, progressSummary: { percent: 0 }, speed: 0, status: 'canceled', error: '',
      createdAt: 1, completedAt: 0
    } satisfies DownloadTask
    mocks.app.GetDownloads.mockResolvedValue([task])
    mocks.app.RemoveDownload.mockResolvedValue({
      accepted: true, operationId: 'op-remove', taskId: task.id,
      requestedReason: 'task_removal', effectiveReason: 'task_removal', executionState: 'stopping', revision: 10,
      taskInstance: 1, taskGeneration: 1, taskRevision: 2
    })
    const store = useAppStore()
    await store.initApp()
    await store.removeDownloadTask(task.id)
    expect(store.downloads).toHaveLength(1)

    emit('download:lifecycle', {
      operationId: 'op-remove', taskId: task.id, phase: 'failed', effectiveReason: 'task_removal',
      error: { code: 'task.cleanup_failed', category: 'output', message: '清理失败', retryable: true },
      revision: 11, taskInstance: 1, taskGeneration: 1, taskRevision: 2, occurredAt: 2
    } satisfies DownloadLifecycleEvent)
    expect(store.downloads).toHaveLength(1)
    expect(store.downloads[0].error).toBe('清理失败')

    emit('download:lifecycle', {
      operationId: 'op-remove-2', taskId: task.id, phase: 'completed', effectiveReason: 'task_removal',
      removed: true, revision: 12,
      taskInstance: 1, taskGeneration: 1, taskRevision: 3, occurredAt: 3
    } satisfies DownloadLifecycleEvent)
    expect(store.downloads).toHaveLength(0)

    // Removal fences the entire old task instance. Neither late progress nor a
    // delayed start event from that instance may recreate the card.
    emit('download:progress', downloadTask(task.id, 'running', 50, 1, 50, 1))
    expect(store.downloads).toHaveLength(0)
    emit('download:start', downloadTask(task.id, 'running', 0, 1, 51, 1))
    expect(store.downloads).toHaveLength(0)

    // Reusing the ID creates a manager-wide newer instance, which may cross
    // the tombstone even though its execution generation starts at one again.
    emit('download:start', downloadTask(task.id, 'running', 0, 1, 60, 2))
    emit('download:progress', downloadTask(task.id, 'running', 60, 1, 61, 2))
    expect(store.downloads).toHaveLength(1)
    expect(store.downloads[0].progressSummary.percent).toBe(60)

    // A delayed terminal removal for the old instance cannot delete the new
    // same-ID task, even if its lifecycle sequence arrives later.
    emit('download:lifecycle', {
      operationId: 'op-remove-old-late', taskId: task.id, phase: 'completed',
      effectiveReason: 'task_removal', removed: true, revision: 13,
      taskInstance: 1, taskGeneration: 1, taskRevision: 100, occurredAt: 4
    } satisfies DownloadLifecycleEvent)
    expect(store.downloads).toHaveLength(1)
    expect(store.downloads[0].instance).toBe(2)

    mocks.app.RemoveDownload.mockResolvedValueOnce({
      accepted: true, operationId: 'op-remove-old-receipt', taskId: task.id,
      requestedReason: 'task_removal', effectiveReason: 'task_removal', executionState: 'stopping', revision: 14,
      taskInstance: 1, taskGeneration: 1, taskRevision: 101
    })
    await store.removeDownloadTask(task.id)
    expect(store.downloadStopOperations[task.id]).toBeUndefined()
  })

  it('uses the accepted synthetic generation when upgrading cancel to remove', async () => {
    const task = downloadTask('task-pending-upgrade', 'pending', 0, 0, 1, 1)
    mocks.app.GetDownloads.mockResolvedValue([task])
    mocks.app.CancelDownload.mockResolvedValue({
      accepted: true, operationId: 'op-upgrade', taskId: task.id,
      requestedReason: 'cancel', effectiveReason: 'cancel', executionState: 'stopping', revision: 10,
      taskInstance: 1, taskGeneration: 1, taskRevision: 2
    })
    mocks.app.RemoveDownload.mockResolvedValue({
      accepted: true, operationId: 'op-upgrade', taskId: task.id,
      requestedReason: 'task_removal', effectiveReason: 'task_removal', executionState: 'stopping', revision: 11,
      taskInstance: 1, taskGeneration: 1, taskRevision: 3
    })
    const store = useAppStore()
    await store.initApp()

    await store.cancelDownloadTask(task.id)
    await store.removeDownloadTask(task.id)

    expect(mocks.app.CancelDownload).toHaveBeenCalledWith(task.id, 1, 0)
    expect(mocks.app.RemoveDownload).toHaveBeenCalledWith(task.id, 1, 1)
    expect(store.downloadStopOperations[task.id]?.reason).toBe('task_removal')
  })

  it('clears a failed terminal marker after an explicit retry succeeds', async () => {
    const id = 'task-retry'
    mocks.app.GetDownloads
      .mockResolvedValueOnce([downloadTask(id, 'failed', 25, 1, 10)])
      .mockResolvedValueOnce([downloadTask(id, 'running', 0, 2, 20)])
    mocks.app.RetryDownload.mockResolvedValue(undefined)
    const store = useAppStore()
    await store.initApp()

    emit('download:progress', downloadTask(id, 'running', 40, 1, 11))
    expect(store.downloads[0].status).toBe('failed')

    await store.retryDownloadTask(id)
    // Events stamped by the prior generation may be delivered after the retry
    // RPC and its authoritative snapshot; neither can regress generation 2.
    emit('download:error', {
      task: downloadTask(id, 'failed', 40, 1, 15),
      error: 'old generation failure'
    })
    emit('download:progress', downloadTask(id, 'running', 55, 1, 16))
    emit('download:lifecycle', {
      operationId: 'old-pause', taskId: id, phase: 'completed', effectiveReason: 'pause',
      resultStatus: 'paused', revision: 50,
      taskInstance: 1, taskGeneration: 1, taskRevision: 50, occurredAt: 3
    } satisfies DownloadLifecycleEvent)
    expect(store.downloads[0].status).toBe('running')
    expect(store.downloads[0].progressSummary.percent).toBe(0)

    emit('download:progress', downloadTask(id, 'running', 65, 2, 21))
    expect(store.downloads[0].status).toBe('running')
    expect(store.downloads[0].progressSummary.percent).toBe(65)
  })

  it('merges only the restarted task when its full-list snapshot races an unrelated completion', async () => {
    const taskA = downloadTask('task-a', 'paused', 20, 1, 10, 1)
    let resolveRestartedSnapshot!: (tasks: DownloadTask[]) => void
    mocks.app.GetDownloads
      .mockResolvedValueOnce([taskA])
      .mockImplementationOnce(() => new Promise<DownloadTask[]>(resolve => {
        resolveRestartedSnapshot = resolve
      }))
    mocks.app.ResumeDownload.mockResolvedValue(undefined)
    const store = useAppStore()
    await store.initApp()

    const resuming = store.resumeDownloadTask(taskA.id)
    await vi.waitFor(() => expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(2))

    const taskBStarted = downloadTask('task-b', 'running', 5, 1, 20, 2)
    const taskBCompleted = downloadTask('task-b', 'completed', 100, 1, 21, 2)
    emit('download:start', taskBStarted)
    emit('download:complete', taskBCompleted)
    resolveRestartedSnapshot([downloadTask(taskA.id, 'running', 20, 2, 15, 1)])
    await resuming

    expect(store.downloads.find(task => task.id === taskA.id)?.status).toBe('running')
    expect(store.downloads.find(task => task.id === taskBStarted.id)?.status).toBe('completed')
  })

  it('rejects lower revisions within the same download generation', async () => {
    const id = 'task-revision'
    mocks.app.GetDownloads.mockResolvedValue([downloadTask(id, 'running', 10, 3, 30)])
    const store = useAppStore()
    await store.initApp()

    emit('download:progress', downloadTask(id, 'running', 70, 3, 32))
    emit('download:progress', downloadTask(id, 'running', 60, 3, 31))
    expect(store.downloads[0].progressSummary.percent).toBe(70)
  })

  it('applies unified settings patches from UpdateSettings', async () => {
    const store = useAppStore()
    await store.initApp()

    const next = { ...baseSettings, theme: 'light' as const, downloadDir: 'E:/Media' }
    const result: SettingsUpdateResult = {
      settings: next,
      warnings: [{ code: 'settings.best_effort_failed', effect: 'publish', message: 'event unavailable' }],
      restartRequired: true,
      restartRequirements: [{ scope: 'app', fields: ['apiPort'], reason: 'restart app' }]
    }
    mocks.app.UpdateSettings.mockResolvedValue(result)

    await store.setAppTheme('light')

    expect(mocks.app.UpdateSettings).toHaveBeenCalledWith({ theme: 'light' })
    expect(store.theme).toBe('light')
    expect(store.downloadDir).toBe('E:/Media')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(store.settingsWarnings).toEqual(result.warnings)
    expect(store.restartRequirements).toEqual(result.restartRequirements)
    expect(store.appInfo).toEqual(baseRuntimeInfo)
  })

  it('keeps terminal download events while FFmpeg installation updates runtime state', async () => {
    const running = downloadTask('ffmpeg-race', 'running', 50, 1, 10)
    const completed = downloadTask('ffmpeg-race', 'completed', 100, 1, 11)
    mocks.app.GetDownloads.mockResolvedValue([running])
    const store = useAppStore()
    await store.initApp()
    let resolveInstall!: (path: string) => void
    mocks.app.InstallFFmpeg.mockImplementationOnce(() => new Promise<string>(resolve => {
      resolveInstall = resolve
    }))

    const installing = store.installFFmpeg()
    await vi.waitFor(() => expect(mocks.app.InstallFFmpeg).toHaveBeenCalledTimes(1))
    emit('download:complete', completed)
    resolveInstall('D:/tools/ffmpeg.exe')
    await installing

    expect(mocks.app.GetDownloads).toHaveBeenCalledTimes(1)
    expect(store.downloads[0].status).toBe('completed')
    expect(store.downloads[0].progressSummary.percent).toBe(100)
    expect(store.ffmpegAvailable).toBe(true)
    expect(store.appInfo?.ffmpegAvailable).toBe(true)
    expect(store.appInfo?.ffmpegPath).toBe('D:/tools/ffmpeg.exe')
  })

  it('enables an upstream proxy with one consistent settings patch', async () => {
    const store = useAppStore()
    await store.initApp()

    const endpoint = 'http://127.0.0.1:7890'
    mocks.app.UpdateSettings.mockResolvedValue({
      settings: { ...baseSettings, useUpstreamProxy: true, upstreamProxy: endpoint },
      restartRequired: false
    } satisfies SettingsUpdateResult)

    await store.setUseUpstreamProxy(true, endpoint)

    expect(mocks.app.UpdateSettings).toHaveBeenCalledWith({
      useUpstreamProxy: true,
      upstreamProxy: endpoint
    })
    expect(store.useUpstreamProxy).toBe(true)
    expect(store.settings?.upstreamProxy).toBe(endpoint)
  })

  it('updates the close behavior fields atomically', async () => {
    const store = useAppStore()
    await store.initApp()

    mocks.app.UpdateSettings.mockResolvedValue({
      settings: { ...baseSettings, closeAction: 'minimize', dontAskOnClose: true },
      restartRequired: false
    } satisfies SettingsUpdateResult)

    await store.setCloseBehavior('minimize')

    expect(mocks.app.UpdateSettings).toHaveBeenCalledWith({
      closeAction: 'minimize',
      dontAskOnClose: true
    })
    expect(store.closeAction).toBe('minimize')
    expect(store.dontAskOnClose).toBe(true)
  })

  it('surfaces settings transaction diagnostics without changing the committed snapshot', async () => {
    const store = useAppStore()
    await store.initApp()

    emit('settings:diagnostic', {
      code: 'settings.inconsistent',
      message: 'rollback failed',
      rollbackErrors: ['download_dir: access denied']
    })

    expect(store.settingsDiagnostic?.code).toBe('settings.inconsistent')
    expect(store.settings?.downloadDir).toBe(baseSettings.downloadDir)
  })

  it('keeps pending restart drift and inconsistent diagnostics across unrelated successful updates', async () => {
    const store = useAppStore()
    await store.initApp()
    emit('settings:diagnostic', { code: 'settings.inconsistent', message: 'rollback failed' })

    mocks.app.UpdateSettings
      .mockResolvedValueOnce({
        settings: { ...baseSettings, apiPort: baseSettings.apiPort + 1 },
        restartRequired: true,
        restartRequirements: [{ scope: 'app', fields: ['apiPort'], reason: 'restart app' }]
      } satisfies SettingsUpdateResult)
      .mockResolvedValueOnce({
        settings: { ...baseSettings, apiPort: baseSettings.apiPort + 1, theme: 'light' },
        restartRequired: true,
        restartRequirements: [{ scope: 'app', fields: ['apiPort'], reason: 'restart app' }]
      } satisfies SettingsUpdateResult)

    await store.updateSettings({ apiPort: baseSettings.apiPort + 1 })
    await store.setAppTheme('light')

    expect(store.restartRequirements).toEqual([
      { scope: 'app', fields: ['apiPort'], reason: 'restart app' }
    ])
    expect(store.settingsDiagnostic?.code).toBe('settings.inconsistent')
  })

  it('dismisses settings notices independently and removes only the selected warning', async () => {
    const store = useAppStore()
    await store.initApp()
    emit('settings:diagnostic', { code: 'settings.inconsistent', message: 'rollback failed' })
    mocks.app.UpdateSettings.mockResolvedValue({
      settings: baseSettings,
      warnings: [
        { code: 'settings.best_effort_failed', effect: 'tray', message: 'tray sync failed' },
        { code: 'settings.best_effort_failed', effect: 'event', message: 'event publish failed' }
      ],
      restartRequired: true,
      restartRequirements: [{ scope: 'app', fields: ['apiPort'], reason: 'restart app' }]
    } satisfies SettingsUpdateResult)
    await store.updateSettings({ theme: 'dark' })

    store.dismissSettingsWarning(store.settingsWarnings[0])
    expect(store.settingsWarnings.map(warning => warning.effect)).toEqual(['event'])
    expect(store.restartRequirements).toHaveLength(1)
    expect(store.settingsDiagnostic).not.toBeNull()

    store.dismissRestartRequirements()
    expect(store.restartRequirements).toEqual([])
    expect(store.settingsWarnings).toHaveLength(1)
    expect(store.settingsDiagnostic).not.toBeNull()

    store.dismissSettingsDiagnostic()
    expect(store.settingsDiagnostic).toBeNull()
    expect(store.settingsWarnings).toHaveLength(1)
  })
})
