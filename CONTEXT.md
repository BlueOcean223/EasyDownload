# EasyDownload Context

## Glossary

### Download Task

A user-visible download intent tracked by the application, not a transport resource. A download task has a lifecycle, progress, artifacts, and errors. A single download task may internally fetch multiple resources and produce temporary artifacts before producing its user-visible result.

### Transport Resource

A concrete network resource fetched as part of a download task, such as a video stream, audio stream, image, or metadata response. Transport resources are internal to platform-specific download execution and are not shown as separate download tasks.

### Task Artifact

A file, directory, or manifest produced by a download task. Temporary artifacts support execution and cleanup; final artifacts are the user-visible result of the task.

### Final Task Artifact

The user-visible result produced by a download task. A task may create many temporary artifacts while running, but completion is judged by the presence and recorded metadata of its final task artifact.

### Output Policy

The planned filesystem output decision recorded when a download task is created. It includes the output directory, planned final filename, planned final path, and conflict strategy. Platform adapters must use the recorded output policy rather than recomputing final paths during execution.

### Output Conflict Strategy

The rule used when a planned output path already exists. The default strategy is automatic renaming, which preserves existing files by selecting a non-conflicting filename instead of overwriting.

### Album Download

A download task whose user-visible result is assembled from multiple media resources, such as a Douyin album or Xiaohongshu image note. Album downloads produce one final archive artifact by default rather than exposing each media resource as a separate task.

### Task Restoration

Loading a saved download task after the application restarts so the task can be managed again.

### Task Snapshot

The current recorded state of a download task, including lifecycle state, progress summary, artifacts, errors, and platform-specific execution data needed for restoration.

### Platform Data

The platform-owned execution data inside a task snapshot. Platform data contains the request details, resource identifiers, temporary artifact paths, post-processing choices, and other fields that only the platform adapter is allowed to interpret.

### Platform Checkpoint

The platform-owned restoration checkpoint recorded while a task is running. A platform checkpoint contains only runtime-discovered data needed to resume or restore execution, and is updated through the task execution context rather than by mutating the task snapshot directly.

### Display Source

A user-facing description of where a download task came from, such as an original page URL or detected source label. Display source is for presentation and is not execution input for the download manager.

### Task Store

The durable collection of task snapshots used to restore user-confirmed download tasks after application restart.

### Task Progress Summary

The compact progress state stored on a download task for list display, event publication, and throttled persistence. It contains overall percent, optional byte totals, and the current stage identifier or label.

### Task Progress Update

A structured progress report emitted while a task runs. It identifies the current execution stage and may include stage percent, item counts, byte counts, and an optional overall percent. Platform adapters translate resource-level progress into task progress updates.

### Progress Stage

A named phase of task execution, such as downloading media, merging streams, decrypting output, packaging an album archive, or writing the final artifact. Progress stages explain a task's current work without exposing transport resources as separate tasks.

### Task Error

A structured error recorded on a download task. A task error has a stable code, category, retryability hint, optional user action, and diagnostic cause. The download manager records task errors but does not parse error strings or platform-specific error messages.

### Fetch Error

A structured transfer-layer error produced by the file fetcher, such as timeout, HTTP status failure, range unsupported, checksum mismatch, filesystem write failure, or cancellation. Platform adapters may wrap fetch errors into task errors without leaking platform semantics into the download manager.

### Task Resumption

Continuing transfer work from already downloaded partial artifacts.

### Task Retry

Starting another attempt for the same download task after failure or cancellation. A retry may or may not reuse partial artifacts.

### Transport Retry

A short retry of one transport resource performed by the file fetcher for transient transfer-layer failures, such as timeouts, temporary server errors, or interrupted connections. Transport retries must honor task cancellation and do not represent a user-level task retry.

### Platform Fallback

A platform-owned attempt to use an alternate resource, stream, quality, API path, or candidate URL when executing a platform-specific download. Platform fallback is decided by the platform adapter, not by the download manager or fetcher.

### Task Stop Reason

The business reason that a running download task execution stopped. Stop reasons are separate from the task lifecycle state and distinguish pause, cancellation, application shutdown, failure, and task removal.

### Task Pause

A user action that stops current task execution while preserving partial artifacts and restoration data so the same download task can continue later.

### Task Cancellation

A user action that abandons a download task execution and allows temporary execution artifacts to be cleaned up. Cancellation is different from pausing and different from removing the task record.

### Task Removal

A user action that removes a download task from the application's task list and persistent task store. Task removal cleans temporary artifacts but does not imply deleting already produced final artifacts unless the user explicitly asks for file deletion.

### Legacy Download Task

A download task saved by an older application version. During the architecture refactor, legacy download tasks are not a compatibility target and may be discarded rather than inferred.

### Platform-Specific Download

A download whose correct behavior depends on rules from a source platform, such as authentication, resource selection, post-processing, or cleanup.

### Application Settings

User-configurable preferences and runtime options for the application. Application settings have one persisted source of truth and may also require runtime effects when changed.

### Settings Update

A requested change to one or more application settings. A settings update is validated against the complete candidate settings state before it is persisted or applied.

### Settings Effect

A runtime action caused by a settings update. Settings effects apply validated settings changes to running modules without exposing separate user-facing operations for each setting.

### Critical Settings Effect

A settings effect that must succeed for the settings update to be accepted and persisted. Critical effects protect runtime correctness, such as proxy reconfiguration, proxy port availability, download directory writability, or startup integration.

### Best-Effort Settings Effect

A settings effect whose failure does not block persistence of an accepted settings update. Best-effort effects are diagnostic, presentational, or recoverable side effects such as publishing UI events or refreshing tray presentation.

### Wails Binding Surface

The Go methods exposed to the Vue frontend through Wails generation. In this refactor, the Wails binding surface is an internal same-version application seam, not a public compatibility contract.

### Feature Slice Migration

A migration that changes backend bindings, generated Wails bindings, frontend stores, and UI calls for one coherent feature area in the same pull request. Each feature slice must remain buildable and type-checkable after migration.

### Detected Video

A media item discovered by a detection source before the user creates a download task. A detected video has a stable identity, mergeable descriptive fields, candidate media resources, and detection timestamps.

### Detection Store

An in-memory, bounded collection of detected videos for the current application session. The detection store deduplicates and merges detected videos, but it does not persist them across restarts.

### Detection Source

The origin that discovered a detected video, such as the WeChat proxy callback. Detection source is part of detected-video identity so different sources do not accidentally collide.

### Platform Content ID

A stable identifier assigned by a source platform to a media item. When available, it is preferred over page URLs or media URLs for detected-video identity.

### Current Session Candidate

A detected video that is available only in the current application session. It becomes durable only if the user creates a download task from it.
