import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type {
  AppRuntimeInfo,
  DetectionChange,
  DetectionSnapshot,
  DetectedVideo,
  DownloadLifecycleEvent,
  DownloadStopReceipt,
  DownloadStopReason,
  DownloadTask,
  LegacyDownloadNotice,
  RestartRequirement,
  SettingsDiagnostic,
  SettingsPatch,
  SettingsSnapshot,
  SettingsUpdateResult,
  SettingsWarning,
  TaskError
} from '@/types'
import {
  StartProxy,
  StopProxy,
  IsProxyRunning,
  IsCertInstalled,
  InstallCert,
  UninstallCert,
  GetDetectedVideos,
  ClearDetectedVideos,
  StartDetectedDownload,
  GetDownloads,
  TakeLegacyDownloadNotice,
  PauseDownload,
  ResumeDownload,
  RetryDownload,
  CancelDownload,
  RemoveDownload,
  SelectDownloadDir,
  OpenDownloadDir,
  GetAppInfo,
  GetSettings,
  UpdateSettings,
  IsFFmpegAvailable,
  InstallFFmpeg,
  RequestClose
} from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

export const useAppStore = defineStore('app', () => {
  // State
  const proxyRunning = ref(false)
  const certInstalled = ref(false)
  const ffmpegAvailable = ref(false)
  const detectedVideos = ref<DetectedVideo[]>([])
  const detectionRevision = ref(0)
  const downloads = ref<DownloadTask[]>([])
  const downloadStopOperations = ref<Record<string, {
    operationId: string
    reason: DownloadStopReason
    revision: number
    instance: number
    generation: number
    error?: TaskError
  }>>({})
  const downloadLifecycleRevisions = new Map<string, number>()
  type DownloadTaskVersion = { instance: number; generation: number; revision: number }
  type DownloadTerminalMarker = DownloadTaskVersion & { status: DownloadTask['status'] | 'removed' }
  const downloadTaskVersions = new Map<string, DownloadTaskVersion>()
  // Terminal markers are generation-aware. A retry/resume may supersede one,
  // while a delayed event from the prior execution can never reopen it.
  const downloadTerminalStatuses = new Map<string, DownloadTerminalMarker>()
  const legacyDownloadNotice = ref<LegacyDownloadNotice | null>(null)
  const downloadDir = ref('')
  const appInfo = ref<AppRuntimeInfo | null>(null)
  const settings = ref<SettingsSnapshot | null>(null)
  const settingsWarnings = ref<SettingsWarning[]>([])
  const restartRequirements = ref<RestartRequirement[]>([])
  const settingsDiagnostic = ref<SettingsDiagnostic | null>(null)
  const loading = ref(false)
  let initialized = false
  let initPromise: Promise<void> | null = null
  let listenersReady = false
  let detectionListenerReady = false
  let detectionSnapshotApplied = false
  let downloadSnapshotApplied = false
  const bufferedDownloadEvents: Array<() => void> = []
  const eventUnsubscribers: Array<() => void> = []

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
    downloads.value.filter(d => d.status === 'pending' || d.status === 'running' || d.status === 'paused')
  )

  const completedDownloads = computed(() =>
    downloads.value.filter(d => d.status === 'completed')
  )

  const problemDownloads = computed(() =>
    downloads.value.filter(d => d.status === 'failed' || d.status === 'canceled')
  )

  function applySettings(snapshot: SettingsSnapshot) {
    settings.value = snapshot
    downloadDir.value = snapshot.downloadDir
    minimizeToTray.value = snapshot.minimizeToTray
    showNotification.value = snapshot.showNotification
    firstRunComplete.value = snapshot.firstRunComplete
    theme.value = snapshot.theme
    language.value = snapshot.language
    useUpstreamProxy.value = snapshot.useUpstreamProxy
    closeAction.value = snapshot.closeAction
    dontAskOnClose.value = snapshot.dontAskOnClose
    dontRemindCertWizard.value = snapshot.dontRemindCertWizard
    document.documentElement.setAttribute('data-theme', snapshot.theme)
  }

  function normalizeSettingsSnapshot(
    snapshot: Awaited<ReturnType<typeof GetSettings>>
  ): SettingsSnapshot {
    const closeAction = snapshot.closeAction
    if (closeAction !== '' && closeAction !== 'exit' && closeAction !== 'minimize') {
      throw new Error(`Unsupported close action from settings binding: ${closeAction}`)
    }
    const theme = snapshot.theme
    if (theme !== 'dark' && theme !== 'light') {
      throw new Error(`Unsupported theme from settings binding: ${theme}`)
    }
    const language = snapshot.language
    if (language !== 'zh-CN' && language !== 'en-US') {
      throw new Error(`Unsupported language from settings binding: ${language}`)
    }
    return {
      proxyPort: snapshot.proxyPort,
      apiPort: snapshot.apiPort,
      downloadDir: snapshot.downloadDir,
      maxConcurrent: snapshot.maxConcurrent,
      minimizeToTray: snapshot.minimizeToTray,
      showNotification: snapshot.showNotification,
      firstRunComplete: snapshot.firstRunComplete,
      closeAction,
      dontAskOnClose: snapshot.dontAskOnClose,
      theme,
      language,
      upstreamProxy: snapshot.upstreamProxy,
      useUpstreamProxy: snapshot.useUpstreamProxy,
      proxyDebug: snapshot.proxyDebug,
      dontRemindCertWizard: snapshot.dontRemindCertWizard
    }
  }

  function normalizeSettingsUpdateResult(
    result: Awaited<ReturnType<typeof UpdateSettings>>
  ): SettingsUpdateResult {
    return {
      settings: normalizeSettingsSnapshot(result.settings),
      warnings: result.warnings?.map(warning => ({
        code: warning.code,
        effect: warning.effect,
        message: warning.message
      })),
      restartRequired: result.restartRequired,
      restartRequirements: result.restartRequirements?.map(requirement => {
        if (requirement.scope !== 'app' && requirement.scope !== 'proxy') {
          throw new Error(`Unsupported restart scope from settings binding: ${requirement.scope}`)
        }
        return {
          scope: requirement.scope,
          fields: requirement.fields,
          reason: requirement.reason
        }
      })
    }
  }

  async function updateSettings(patch: SettingsPatch): Promise<SettingsUpdateResult> {
    const result = normalizeSettingsUpdateResult(await UpdateSettings(patch))
    applySettings(result.settings)
    settingsWarnings.value = result.warnings ?? []
    // The App returns the complete current committed-vs-runtime drift, so this
    // authoritative list both preserves unrelated requirements and clears one
    // when the committed value again matches the live runtime.
    restartRequirements.value = result.restartRequirements ?? []
    // settings.inconsistent is sticky: a later unrelated successful update
    // does not prove that a failed rollback has recovered.
    return result
  }

  type WailsDetectionSnapshot = Awaited<ReturnType<typeof GetDetectedVideos>>
  type WailsDetectedVideo = WailsDetectionSnapshot['videos'][number]
  type WailsDetectedResource = WailsDetectedVideo['candidates'][number]
  type WailsDetectionChange = Awaited<ReturnType<typeof ClearDetectedVideos>>
  type WailsDownloadTask = Awaited<ReturnType<typeof StartDetectedDownload>>
  type WailsTaskArtifact = NonNullable<WailsDownloadTask['artifacts']>[number]
  type WailsTaskError = NonNullable<WailsDownloadTask['lastErrorDetail']>
  type WailsDownloadStopReceipt = Awaited<ReturnType<typeof PauseDownload>>

  const detectionChangeTypes = ['inserted', 'updated', 'removed', 'cleared'] satisfies DetectionChange['type'][]
  const downloadStatuses = [
    'pending', 'running', 'paused', 'completed', 'failed', 'canceled'
  ] satisfies DownloadTask['status'][]
  const artifactKinds = ['final', 'temporary'] satisfies NonNullable<DownloadTask['artifacts']>[number]['kind'][]
  const taskErrorCategories = [
    'transport', 'platform', 'output', 'canceled', 'unexpected'
  ] satisfies TaskError['category'][]
  const downloadStopReasons = [
    'pause', 'cancel', 'shutdown', 'failure', 'task_removal'
  ] satisfies DownloadStopReason[]

  function normalizeStringMember<T extends string>(value: string, allowed: readonly T[], label: string): T {
    const normalized = allowed.find(candidate => candidate === value)
    if (normalized === undefined) {
      throw new Error(`Unsupported ${label} from Wails binding: ${value}`)
    }
    return normalized
  }

  function normalizeDetectionTimestamp(value: unknown, field: string): string {
    if (typeof value !== 'string') {
      throw new Error(`Invalid ${field} from Wails detection binding`)
    }
    return value
  }

  function normalizeDetectedResource(resource: WailsDetectedResource): DetectedVideo['candidates'][number] {
    return { ...resource } satisfies DetectedVideo['candidates'][number]
  }

  function normalizeDetectedVideo(video: WailsDetectedVideo): DetectedVideo {
    return {
      ...video,
      candidates: video.candidates.map(normalizeDetectedResource),
      detectedAt: normalizeDetectionTimestamp(video.detectedAt, 'detectedAt'),
      lastSeenAt: normalizeDetectionTimestamp(video.lastSeenAt, 'lastSeenAt')
    } satisfies DetectedVideo
  }

  function normalizeDetectionSnapshot(snapshot: WailsDetectionSnapshot): DetectionSnapshot {
    return {
      ...snapshot,
      videos: snapshot.videos.map(normalizeDetectedVideo)
    } satisfies DetectionSnapshot
  }

  function normalizeDetectionChange(change: WailsDetectionChange): DetectionChange {
    return {
      ...change,
      type: normalizeStringMember(change.type, detectionChangeTypes, 'detection change type'),
      snapshot: normalizeDetectionSnapshot(change.snapshot)
    } satisfies DetectionChange
  }

  function normalizeTaskError(error: WailsTaskError): TaskError {
    return {
      ...error,
      category: normalizeStringMember(error.category, taskErrorCategories, 'task error category')
    } satisfies TaskError
  }

  function normalizeTaskArtifact(
    artifact: WailsTaskArtifact
  ): NonNullable<DownloadTask['artifacts']>[number] {
    return {
      ...artifact,
      kind: normalizeStringMember(artifact.kind, artifactKinds, 'artifact kind')
    } satisfies NonNullable<DownloadTask['artifacts']>[number]
  }

  function normalizeDownloadTask(task: WailsDownloadTask): DownloadTask {
    const conflictStrategy = task.outputPolicy.conflictStrategy
    if (conflictStrategy !== 'auto_rename') {
      throw new Error(`Unsupported output conflict strategy from Wails binding: ${conflictStrategy}`)
    }
    return {
      ...task,
      outputPolicy: {
        ...task.outputPolicy,
        conflictStrategy
      },
      artifacts: task.artifacts?.map(normalizeTaskArtifact),
      status: normalizeStringMember(task.status, downloadStatuses, 'download status'),
      lastErrorDetail: task.lastErrorDetail ? normalizeTaskError(task.lastErrorDetail) : undefined
    } satisfies DownloadTask
  }

  function normalizeDownloadStopReceipt(receipt: WailsDownloadStopReceipt): DownloadStopReceipt {
    return {
      ...receipt,
      requestedReason: normalizeStringMember(receipt.requestedReason, downloadStopReasons, 'download stop reason'),
      effectiveReason: normalizeStringMember(receipt.effectiveReason, downloadStopReasons, 'download stop reason'),
      error: receipt.error ? normalizeTaskError(receipt.error) : undefined
    } satisfies DownloadStopReceipt
  }

  function normalizeLegacyDownloadNotice(
    notice: Awaited<ReturnType<typeof TakeLegacyDownloadNotice>> | null
  ): LegacyDownloadNotice | null {
    if (!notice) return null
    return { ...notice } satisfies LegacyDownloadNotice
  }

  function applyDetectionSnapshot(snapshot?: DetectionSnapshot) {
    if (!snapshot || !Number.isFinite(snapshot.revision)) return false
    if (detectionSnapshotApplied && snapshot.revision <= detectionRevision.value) return false
    detectionSnapshotApplied = true
    detectionRevision.value = snapshot.revision
    detectedVideos.value = snapshot.videos ?? []
    return true
  }

  function replaceDownload(task: DownloadTask) {
    const index = downloads.value.findIndex(existing => existing.id === task.id)
    if (index === -1) {
      downloads.value.unshift(task)
    } else {
      downloads.value[index] = task
    }
  }

  function clearDownloadStopOperation(taskId: string) {
    if (!downloadStopOperations.value[taskId]) return
    const operations = { ...downloadStopOperations.value }
    delete operations[taskId]
    downloadStopOperations.value = operations
  }

  function currentDownloadCommandRef(taskId: string) {
    const version = downloadTaskVersions.get(taskId)
    if (version && version.instance > 0) return version
    const task = downloads.value.find(item => item.id === taskId)
    if (!task) throw new Error(`Download task ${taskId} not found`)
    return taskVersion(task)
  }

  function isActiveDownloadStatus(status: DownloadTask['status']) {
    return status === 'pending' || status === 'running'
  }

  function taskVersion(task: DownloadTask): DownloadTaskVersion {
    return {
      instance: Number.isFinite(task.instance) ? task.instance : 0,
      generation: Number.isFinite(task.generation) ? task.generation : 0,
      revision: Number.isFinite(task.revision) ? task.revision : 0
    }
  }

  function compareTaskVersions(left: DownloadTaskVersion, right: DownloadTaskVersion) {
    if (left.instance !== right.instance) return left.instance - right.instance
    if (left.generation !== right.generation) return left.generation - right.generation
    return left.revision - right.revision
  }

  function compareTaskIdentity(left: DownloadTaskVersion, right: DownloadTaskVersion) {
    if (left.instance !== right.instance) return left.instance - right.instance
    return left.generation - right.generation
  }

  function isNewerTaskVersion(next: DownloadTaskVersion, current?: DownloadTaskVersion) {
    return !current || compareTaskVersions(next, current) > 0
  }

  function acceptDownloadTaskVersion(task: DownloadTask) {
    const next = taskVersion(task)
    if (!isNewerTaskVersion(next, downloadTaskVersions.get(task.id))) return false
    downloadTaskVersions.set(task.id, next)
    return true
  }

  function markerAllows(task: DownloadTask) {
    const marker = downloadTerminalStatuses.get(task.id)
    if (!marker) return true
    const next = taskVersion(task)
    // A removed marker fences the whole task object, including all of its
    // execution generations. Only a new manager-assigned instance may reuse
    // the same public ID. Other terminal states may be superseded by retry or
    // resume on a later execution generation of the same instance.
    if (next.instance > marker.instance ||
      (next.instance === marker.instance && marker.status !== 'removed' && next.generation > marker.generation)) {
      downloadTerminalStatuses.delete(task.id)
      return true
    }
    return false
  }

  function markTerminal(task: DownloadTask, status: DownloadTerminalMarker['status']) {
    const version = taskVersion(task)
    downloadTerminalStatuses.set(task.id, { ...version, status })
  }

  function upsertDownload(task: DownloadTask) {
    if (!acceptDownloadTaskVersion(task) || !markerAllows(task)) return false
    replaceDownload(task)
    return true
  }

  function applyTerminalDownload(task: DownloadTask, fallbackStatus: DownloadTask['status']) {
    if (!acceptDownloadTaskVersion(task) || !markerAllows(task)) return false
    const terminalTask = isActiveDownloadStatus(task.status)
      ? { ...task, status: fallbackStatus }
      : task
    replaceDownload(terminalTask)
    markTerminal(terminalTask, terminalTask.status)
    return true
  }

  function applyDownloadStart(task: DownloadTask) {
    const previous = downloadTaskVersions.get(task.id)
    if (!acceptDownloadTaskVersion(task) || !markerAllows(task)) return false
    if (previous && (task.instance > previous.instance ||
      (task.instance === previous.instance && task.generation > previous.generation))) {
      clearDownloadStopOperation(task.id)
    }
    replaceDownload(task)
    // Very small downloads may finish before the backend has emitted its
    // start notification. Preserve that terminal snapshot instead of opening
    // a hole for a queued progress update.
    if (!isActiveDownloadStatus(task.status)) {
      markTerminal(task, task.status)
    }
    return true
  }

  function applyInitialDownloadSnapshot(tasks: DownloadTask[]) {
    downloads.value = tasks
    downloadTerminalStatuses.clear()
    downloadTaskVersions.clear()
    for (const task of tasks) {
      downloadTaskVersions.set(task.id, taskVersion(task))
      if (!isActiveDownloadStatus(task.status)) {
        markTerminal(task, task.status)
      }
    }

    // Events registered before GetDownloads may race its response. Replaying
    // them after the snapshot makes event order authoritative without allowing
    // the (possibly older) response to overwrite a newer terminal state.
    downloadSnapshotApplied = true
    const pending = bufferedDownloadEvents.splice(0)
    for (const applyEvent of pending) applyEvent()
  }

  function applyRestartedDownloadSnapshot(tasks: DownloadTask[], restartedTaskId: string) {
    // GetDownloads has task-level versions but no collection revision. The
    // response may therefore have captured its task set before an unrelated
    // start/complete event that the UI already applied. This command only owns
    // the task it restarted, so merge that one task and never prune or replace
    // unrelated IDs from the full-list response.
    const task = tasks.find(item => item.id === restartedTaskId)
    if (!task || !acceptDownloadTaskVersion(task) || !markerAllows(task)) return
    replaceDownload(task)
    if (!isActiveDownloadStatus(task.status)) {
      markTerminal(task, task.status)
    } else if (task.generation > 0) {
      downloadTerminalStatuses.delete(task.id)
      clearDownloadStopOperation(task.id)
    }
  }

  function applyOrBufferDownloadEvent(applyEvent: () => void) {
    if (!downloadSnapshotApplied) {
      bufferedDownloadEvents.push(applyEvent)
      return
    }
    applyEvent()
  }

  function acceptDownloadLifecycleRevision(taskId: string, revision: number, allowEqual = false) {
    if (!taskId || !Number.isFinite(revision)) return false
    const current = downloadLifecycleRevisions.get(taskId) ?? -1
    if (revision < current || (!allowEqual && revision === current)) return false
    if (revision > current) downloadLifecycleRevisions.set(taskId, revision)
    return true
  }

  function applyStopReceipt(receipt: DownloadStopReceipt) {
    if (!receipt) return false
    const receiptTaskVersion: DownloadTaskVersion = {
      instance: Number.isFinite(receipt.taskInstance) ? receipt.taskInstance : 0,
      generation: Number.isFinite(receipt.taskGeneration) ? receipt.taskGeneration : 0,
      revision: Number.isFinite(receipt.taskRevision) ? receipt.taskRevision : 0
    }
    const currentTaskVersion = downloadTaskVersions.get(receipt.taskId)
    if (currentTaskVersion && compareTaskIdentity(receiptTaskVersion, currentTaskVersion) < 0) return false
    if (!acceptDownloadLifecycleRevision(receipt.taskId, receipt.revision)) return false
    if (!currentTaskVersion || compareTaskVersions(receiptTaskVersion, currentTaskVersion) > 0) {
      downloadTaskVersions.set(receipt.taskId, receiptTaskVersion)
    }
    if (receipt.executionState === 'completed' || !receipt.accepted) {
      const operation = downloadStopOperations.value[receipt.taskId]
      if (!operation || (operation.operationId === receipt.operationId &&
        operation.instance === receiptTaskVersion.instance &&
        operation.generation === receiptTaskVersion.generation)) {
        clearDownloadStopOperation(receipt.taskId)
      }
      return true
    }
    downloadStopOperations.value = {
      ...downloadStopOperations.value,
      [receipt.taskId]: {
        operationId: receipt.operationId,
        reason: receipt.effectiveReason,
        revision: receipt.revision,
        instance: receiptTaskVersion.instance,
        generation: receiptTaskVersion.generation,
        error: receipt.error
      }
    }
    return true
  }

  function applyDownloadLifecycleEvent(event: DownloadLifecycleEvent) {
    // The accepted receipt and its initial stopping event intentionally share
    // one lifecycle revision. Processing that equal event is idempotent and
    // also carries the task-event fence needed to reject queued snapshots.
    if (!event || !acceptDownloadLifecycleRevision(event.taskId, event.revision, true)) return false
    const eventTaskVersion: DownloadTaskVersion = {
      instance: Number.isFinite(event.taskInstance) ? event.taskInstance : 0,
      generation: Number.isFinite(event.taskGeneration) ? event.taskGeneration : 0,
      revision: Number.isFinite(event.taskRevision) ? event.taskRevision : 0
    }
    const currentTaskVersion = downloadTaskVersions.get(event.taskId)
    const identityOrder = currentTaskVersion
      ? compareTaskIdentity(eventTaskVersion, currentTaskVersion)
      : 1
    const existingOperation = downloadStopOperations.value[event.taskId]
    const matchesExistingOperation = existingOperation != null &&
      existingOperation.operationId === event.operationId &&
      existingOperation.instance === eventTaskVersion.instance &&
      existingOperation.generation === eventTaskVersion.generation
    if (event.phase === 'stopping') {
      if (identityOrder < 0) return false
      // Fence task snapshots that were emitted before stop closed the backend
      // mutation gate but happen to arrive after this lifecycle event.
      if (!currentTaskVersion || compareTaskVersions(eventTaskVersion, currentTaskVersion) > 0) {
        downloadTaskVersions.set(event.taskId, eventTaskVersion)
      }
      downloadStopOperations.value = {
        ...downloadStopOperations.value,
        [event.taskId]: {
          operationId: event.operationId,
          reason: event.effectiveReason,
          revision: event.revision,
          instance: eventTaskVersion.instance,
          generation: eventTaskVersion.generation,
          error: event.error
        }
      }
      return true
    }

    if (matchesExistingOperation) clearDownloadStopOperation(event.taskId)

    // Operation convergence and task payload ordering are deliberately
    // separate. A terminal event may be older than an already applied public
    // snapshot yet must still clear its matching stopping operation. It may
    // not, however, mutate a newer execution/task instance.
    if (identityOrder < 0) return true

    if (event.removed) {
      const removalFence = currentTaskVersion && compareTaskVersions(currentTaskVersion, eventTaskVersion) > 0
        ? currentTaskVersion
        : eventTaskVersion
      downloadTaskVersions.set(event.taskId, removalFence)
      downloadTerminalStatuses.set(event.taskId, { ...removalFence, status: 'removed' })
      downloads.value = downloads.value.filter(task => task.id !== event.taskId)
      return true
    }

    if (currentTaskVersion && compareTaskVersions(eventTaskVersion, currentTaskVersion) < 0) {
      return true
    }
    downloadTaskVersions.set(event.taskId, eventTaskVersion)

    const task = downloads.value.find(item => item.id === event.taskId)
    const eventPayloadVersion = event.task ? taskVersion(event.task) : undefined
    const hasMatchingPayload = event.task != null && eventPayloadVersion != null &&
      compareTaskVersions(eventPayloadVersion, eventTaskVersion) === 0
    const terminalTask: DownloadTask | undefined = hasMatchingPayload
      ? event.task!
      : task
        ? {
            ...task,
            instance: eventTaskVersion.instance,
            generation: eventTaskVersion.generation,
            revision: eventTaskVersion.revision,
            status: event.resultStatus ?? task.status,
            lastErrorDetail: event.error ?? task.lastErrorDetail,
            error: event.error?.message ?? task.error,
            executionState: 'finished'
          }
        : undefined
    if (terminalTask) {
      replaceDownload(terminalTask)
      if (event.resultStatus) markTerminal(terminalTask, event.resultStatus)
    }
    return true
  }

  // Actions
  function initApp(): Promise<void> {
    if (initialized) return Promise.resolve()
    if (initPromise) return initPromise
    initPromise = initializeApp().finally(() => {
      initPromise = null
    })
    return initPromise
  }

  async function initializeApp() {
    loading.value = true
    try {
      // Register download listeners before any initial RPC. Download events are
      // buffered until GetDownloads resolves, then replayed over that snapshot.
      setupEventListeners()

      // Fetch the full initial view before committing any of it. If one RPC
      // fails, a later initApp call can retry without mixing a partial snapshot
      // with live download events received in the meantime.
      const [runtimeInfo, settingsSnapshot, proxyStatus, certStatus, ffmpegStatus,
        detectionSnapshot, downloadSnapshot] = await Promise.all([
        GetAppInfo(),
        GetSettings(),
        IsProxyRunning(),
        IsCertInstalled(),
        IsFFmpegAvailable(),
        GetDetectedVideos(),
        GetDownloads()
      ])
      const normalizedSettings = normalizeSettingsSnapshot(settingsSnapshot)
      const normalizedDetection = normalizeDetectionSnapshot(detectionSnapshot)
      const normalizedDownloads = downloadSnapshot.map(normalizeDownloadTask)
      // This call consumes a one-time notice, so run it only after all
      // retryable reads and binding validation have succeeded.
      const legacyNotice = normalizeLegacyDownloadNotice(await TakeLegacyDownloadNotice())

      appInfo.value = runtimeInfo
      applySettings(normalizedSettings)
      proxyRunning.value = proxyStatus
      certInstalled.value = certStatus
      ffmpegAvailable.value = ffmpegStatus
      applyDetectionSnapshot(normalizedDetection)
      applyInitialDownloadSnapshot(normalizedDownloads)
      legacyDownloadNotice.value = legacyNotice
      initialized = true
    } catch (error) {
      console.error('Failed to init app:', error)
    } finally {
      loading.value = false
    }
  }

  function setupDetectionListener() {
    if (detectionListenerReady) return
    detectionListenerReady = true
    // The backend DetectionStore owns identity, merge, ordering, and capacity.
    // Each event carries an authoritative snapshot, so this store has no
    // platform-specific merge policy.
    eventUnsubscribers.push(EventsOn('video:detected', (change: DetectionChange) => {
      applyDetectionSnapshot(change?.snapshot)
    }))
  }

  function setupEventListeners() {
    if (listenersReady) return
    listenersReady = true
    setupDetectionListener()

    const on = (eventName: string, callback: (...data: any[]) => void) => {
      eventUnsubscribers.push(EventsOn(eventName, callback))
    }

    on('settings:changed', (payload: { settings?: SettingsSnapshot }) => {
      if (payload?.settings) {
        applySettings(payload.settings)
      }
    })

    on('settings:diagnostic', (diagnostic: SettingsDiagnostic) => {
      settingsDiagnostic.value = diagnostic
    })

    // Download progress event
    on('download:progress', (task: DownloadTask) => {
      applyOrBufferDownloadEvent(() => {
        upsertDownload(task)
      })
    })

    // Download complete event
    on('download:complete', (task: DownloadTask) => {
      applyOrBufferDownloadEvent(() => {
        applyTerminalDownload(task, 'completed')
      })
    })

    // Download error event
    on('download:error', (data: { task?: DownloadTask; error?: string } | string) => {
      const errorText = typeof data === 'string' ? data : data?.error
      if (typeof data === 'string' || !data?.task) {
        console.error(errorText || 'Download error')
        return
      }
      applyOrBufferDownloadEvent(() => {
        applyTerminalDownload({ ...data.task!, error: errorText || data.task!.error }, 'failed')
      })
    })

    // Download start event
    on('download:start', (task: DownloadTask) => {
      applyOrBufferDownloadEvent(() => {
        applyDownloadStart(task)
      })
    })

    on('download:lifecycle', (event: DownloadLifecycleEvent) => {
      applyOrBufferDownloadEvent(() => {
        applyDownloadLifecycleEvent(event)
      })
    })

    // FFmpeg ready event
    on('ffmpeg:ready', (available: boolean) => {
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
        restartRequirements.value = restartRequirements.value.filter(requirement => requirement.scope !== 'proxy')
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
      if (appInfo.value) appInfo.value.certInstalled = true
    } catch (error) {
      console.error('Failed to install certificate:', error)
      throw error
    }
  }

  async function uninstallCertificate() {
    try {
      await UninstallCert()
      certInstalled.value = false
      if (appInfo.value) appInfo.value.certInstalled = false
    } catch (error) {
      console.error('Failed to uninstall certificate:', error)
      throw error
    }
  }

  async function installFFmpeg() {
    try {
      const path = await InstallFFmpeg()
      ffmpegAvailable.value = true
      if (appInfo.value) {
        appInfo.value.ffmpegAvailable = true
        appInfo.value.ffmpegPath = path
      }
      return path
    } catch (error) {
      console.error('Failed to install FFmpeg:', error)
      throw error
    }
  }

  async function clearVideos() {
    const change = normalizeDetectionChange(await ClearDetectedVideos())
    applyDetectionSnapshot(change?.snapshot)
  }

  async function downloadDetectedVideo(video: DetectedVideo, candidateId?: string) {
    try {
      const selected = video.candidates.some(candidate => candidate.id === candidateId)
        ? candidateId
        : video.candidates.find(candidate => candidate.default)?.id || video.candidates[0]?.id
      if (!selected) throw new Error('没有可下载的候选资源')
      const task = normalizeDownloadTask(await StartDetectedDownload(video.id, selected))
      applyDownloadStart(task)
      return task
    } catch (error) {
      console.error('Failed to start download:', error)
      throw error
    }
  }

  async function pauseDownloadTask(id: string) {
    const ref = currentDownloadCommandRef(id)
    applyStopReceipt(normalizeDownloadStopReceipt(await PauseDownload(id, ref.instance, ref.generation)))
  }

  async function resumeDownloadTask(id: string) {
    const ref = currentDownloadCommandRef(id)
    await ResumeDownload(id, ref.instance, ref.generation)
    applyRestartedDownloadSnapshot((await GetDownloads()).map(normalizeDownloadTask), id)
  }

  async function cancelDownloadTask(id: string) {
    const ref = currentDownloadCommandRef(id)
    applyStopReceipt(normalizeDownloadStopReceipt(await CancelDownload(id, ref.instance, ref.generation)))
  }

  async function retryDownloadTask(id: string) {
    const ref = currentDownloadCommandRef(id)
    await RetryDownload(id, ref.instance, ref.generation)
    applyRestartedDownloadSnapshot((await GetDownloads()).map(normalizeDownloadTask), id)
  }

  async function removeDownloadTask(id: string) {
    const ref = currentDownloadCommandRef(id)
    applyStopReceipt(normalizeDownloadStopReceipt(await RemoveDownload(id, ref.instance, ref.generation)))
  }

  async function selectFolder() {
    const dir = await SelectDownloadDir()
    if (dir) {
      await updateSettings({ downloadDir: dir })
    }
    return dir
  }

  async function openFolder() {
    await OpenDownloadDir()
  }

  async function updateDownloadDir(dir: string) {
    await updateSettings({ downloadDir: dir })
  }

  async function completeFirstRun() {
    await updateSettings({ firstRunComplete: true })
  }

  async function setMinimizeToTray(enabled: boolean) {
    await updateSettings({ minimizeToTray: enabled })
  }

  async function setShowNotification(enabled: boolean) {
    await updateSettings({ showNotification: enabled })
  }

  async function setAppTheme(newTheme: 'dark' | 'light') {
    await updateSettings({ theme: newTheme })
  }

  async function setAppLanguage(newLang: 'zh-CN' | 'en-US') {
    await updateSettings({ language: newLang })
  }

  async function setCloseBehavior(action: '' | 'exit' | 'minimize') {
    await updateSettings({ closeAction: action, dontAskOnClose: action !== '' })
  }

  async function setDontRemindCertWizard(dontRemind: boolean) {
    await updateSettings({ dontRemindCertWizard: dontRemind })
  }

  async function setUseUpstreamProxy(enabled: boolean, upstreamProxy?: string) {
    const patch: SettingsPatch = { useUpstreamProxy: enabled }
    // Enabling and supplying the endpoint form one valid candidate snapshot;
    // do not persist the boolean first and leave an impossible intermediate state.
    if (enabled && upstreamProxy !== undefined) {
      patch.upstreamProxy = upstreamProxy
    }
    await updateSettings(patch)
  }

  async function setUpstreamProxy(proxyURL: string) {
    await updateSettings({ upstreamProxy: proxyURL })
  }

  async function setProxyDebug(enabled: boolean) {
    await updateSettings({ proxyDebug: enabled })
  }

  async function requestAppClose(action: 'exit' | 'minimize') {
    await RequestClose(action)
  }

  function dismissSettingsDiagnostic() {
    settingsDiagnostic.value = null
  }

  function dismissRestartRequirements() {
    restartRequirements.value = []
  }

  function dismissSettingsWarning(warning: SettingsWarning) {
    const index = settingsWarnings.value.indexOf(warning)
    if (index === -1) return
    settingsWarnings.value = settingsWarnings.value.filter((_, warningIndex) => warningIndex !== index)
  }

  return {
    // State
    proxyRunning,
    certInstalled,
    ffmpegAvailable,
    detectedVideos,
    detectionRevision,
    downloads,
    downloadStopOperations,
    legacyDownloadNotice,
    downloadDir,
    appInfo,
    settings,
    settingsWarnings,
    restartRequirements,
    settingsDiagnostic,
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
    problemDownloads,

    // Actions
    initApp,
    toggleProxy,
    installCertificate,
    uninstallCertificate,
    installFFmpeg,
    clearVideos,
    downloadDetectedVideo,
    pauseDownloadTask,
    resumeDownloadTask,
    retryDownloadTask,
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
    setCloseBehavior,
    setDontRemindCertWizard,
    setUseUpstreamProxy,
    setUpstreamProxy,
    setProxyDebug,
    updateSettings,
    dismissSettingsDiagnostic,
    dismissRestartRequirements,
    dismissSettingsWarning,
    requestAppClose
  }
})
