# 安全边界与下载可靠性说明

本文档记录 EasyDownload 当前内部 API、MITM 代理、设置事务、检测列表和 v2 下载引擎的安全/可靠性边界。涉及代理、设置、下载队列、断点续传或状态持久化时，应同步更新本文档。

## 1. 本地服务安全边界

### 1.1 Internal API

Internal API 仅监听本机回环地址：

```text
127.0.0.1:{apiPort}
```

浏览器可访问的内部路由默认需要启动时随机生成的 token：

- Header：`X-EasyDownload-Token: <token>`
- 或用于 `<img>` / `<video>` 这类不能自定义 header 的场景：`?token=<token>`

需要鉴权的路由包括：

- `POST /api/detect`
- `GET /api/videos`
- `POST /api/clear`
- `GET /api/proxy-image`
- `GET /api/proxy-media`

`/api/health` 仅用于健康检查，不需要 token。

CORS 不使用 `*`，只允许桌面前端、本地开发地址和已知注入来源。未带合法 token 的跨站请求即使能到达本机端口，也不能读写内部数据。

### 1.2 图片/媒体代理

`/api/proxy-image` 用于前端封面和图片预览。为避免 SSRF，它会：

- 只允许 `http` / `https`
- 请求前解析目标主机 IP
- 阻止 localhost、回环地址、私网地址、link-local、multicast、unspecified、CGNAT 等地址
- 自定义 Dial 时再次校验解析结果，避免 DNS 变更绕过
- 限制图片最大 20MiB
- 要求响应 `Content-Type` 为 `image/*`
- 限制重定向次数，并对重定向目标重复校验

`/api/proxy-media` 仅允许已知抖音/字节系媒体域名，并支持转发 `Range` 请求供预览播放使用。

## 2. MITM 代理边界

代理同样仅监听：

```text
127.0.0.1:{proxyPort}
```

CONNECT 处理采用严格白名单：只有 `MITMDomains()` 中的页面/脚本域名会被 MITM 解密，用于微信视频号脚本注入和页面检测。其他 HTTPS 域名默认 `OkConnect` 直连。

当前 MITM 白名单：

- `channels.weixin.qq.com`
- `res.wx.qq.com`
- `wxapp.tc.qq.com`

视频 CDN 域名不做 MITM，以避免影响播放性能和扩大隐私边界。上游代理配置在运行时修改后会关闭空闲连接，让新请求读取最新配置。HTML/JS 响应修改前会读取并恢复原始 body；遇到无法解码的 `Content-Encoding` 时跳过修改，不丢失原响应。

## 3. 下载队列与状态持久化

`DownloadManager` 只负责平台无关的调度、生命周期和持久化。`maxConcurrent` 控制并发数，超额任务保持 `pending`；同一任务任意时刻最多有一个 active generation。平台适配器从深复制快照读取 `PlatformData`，只能通过绑定 generation 的 execution context 汇报 progress、checkpoint、artifact、error 和最终发布。

### 3.1 v1 / v2 文件和恢复边界

新任务状态写入：

```text
{appDataDir}/downloads.v2.json
{appDataDir}/downloads.v2.json.lkg
```

旧版 `{appDataDir}/downloads.json` 是 v1 回滚资产：新版本不读取、不导入、不移动也不覆盖它。检测到非空 v1 文件时，下载页显示一次 `download.legacy_state_preserved` 提示，明确给出 v1/v2 路径和可回滚状态。回退旧版本仍可读取未修改的 v1；重新升级后继续读取此前的 v2。

v2 使用带 `schemaVersion` 和单调 `revision` 的完整任务快照：

- manager 在同一锁域捕获某个 revision 的不可变快照；删除任务也产生更高 revision 的“不含该任务”快照。
- `TaskStore` 串行提交，只接受更高 revision，迟到写不能让旧状态或已删除任务复活。
- 写入采用同目录临时文件、flush/sync、平台安全的原子 replace；replace 后的目录 sync 失败作为已提交诊断处理，避免磁盘与内存分叉。
- 主文件缺失或损坏时可读取完整 LKG；未知 v2 schema 直接 fail-closed，不拿旧 schema 猜测恢复。
- 原本 `running` 且没有已发布主产物的任务恢复为 `paused`；若主最终产物已经原子发布，恢复阶段认领产物并标记 `completed`，不会再次执行 adapter。
- `PlatformData` 保存创建时执行输入，`PlatformCheckpoint` 单独保存运行时恢复点；所有 adapter 在解码私有数据前都要求当前 `PlatformDataVersion`，未知版本 fail-closed，不能用 v1 结构猜测执行。持久化字段为 `progress` 和 `lastError`，公共 Wails DTO 对应 `progressSummary` 和 `lastErrorDetail`。

### 3.2 输出路径和最终发布

`OutputPathAllocator` 在任务创建时为所有活跃任务预留最终路径，默认冲突策略为 `auto_rename`。任务专属临时路径不得共用；外部文件或另一个任务占用同名路径时重新分配，不能覆盖。

适配器只能下载或生成临时文件。`PublishFinal` 先持久化包含 generation、临时路径、最终路径、size 和 SHA-256 的 publish intent，再用 no-replace 方式发布，最后在同一个更高 revision 中原子记录主 final artifact 与 `completed`。如果进程在 move 后、完成快照前崩溃，恢复逻辑校验 size/SHA 后认领既有最终文件；无法证明身份时 fail-closed。

### 3.3 异步停止与清理

Wails 的 pause/cancel/remove 请求立即返回 typed accepted receipt，包含 operation ID、effective reason、execution state、operation revision 和 task instance/generation/revision；终态通过 `download:lifecycle` 事件到达。Pause/Resume/Cancel/Retry/Remove 命令带调用方观察到的 instance/generation，后端在同一锁域 compare-and-act，旧卡命令不能操作 remove 后复用同 ID 的新任务。排队时即预留 execution generation，自动 dispatch 复用该 generation，因此排队卡片在首个进度事件前仍可可靠取消。重复停止请求复用同一 operation，pause/cancel 可升级为 remove。

`download:start/progress/complete/error` 的公共任务快照携带 manager-wide task instance、execution generation 和 task-event revision；membership capture、payload copy 和 revision 分配在相应锁域内线性化。lifecycle receipt/event 也携带 task version fence；terminal event 额外携带同版本完整公开任务快照，确保 publish-vs-stop 竞态仍把最终 artifact、重分配路径、100% progress 和已清理错误送到 UI。前端分别按 lifecycle revision 收敛 operation、按 `(instance, generation, revision)` 收敛 task payload：匹配的旧 payload 不能阻止 operation 结束；retry/resume 只能由更高 generation 打开终态；remove tombstone 只能由同 ID 的更高 instance 穿过；旧对象迟到的 start、receipt 或 removal 都不能复活、停止或删除新对象。

停止 coordinator 先取消并等待真实 worker 退出，再用独立 bounded context 执行 cleanup：

- pause 和 shutdown 保留可恢复 partial/sidecar，不做破坏性 cleanup。
- cancel 和 task removal 在 join 后恰好 cleanup 一次；remove 只有在“不含该任务”的快照持久化成功后才发布 removed 终态。
- worker 或 cleanup 超时不会假装完成，任务保持 `stopping` 并发出结构化诊断，后台 coordinator 继续收口。
- stop 开始后，旧 generation 的迟到 progress/artifact/checkpoint/complete 全部被拒绝。
- 前端对 completed/failed/removed 保留带 task version 的终态 marker；completed/failed 只能由同 instance 的更高 generation 清除，removed tombstone 只能由更高 task instance 清除。

## 4. Fetch 文件传输边界

平台文件字节传输统一走 composition root 注入的同一个 `internal/download/fetch.Fetcher`。Fetch 不承载平台 JSON/API 语义，也不直接发布最终文件；成功结果始终是经过 size/SHA 校验的临时文件，随后由 adapter 调用 manager 的 `PublishFinal`。

断点续传身份由 `ResourceIdentity`、ETag/Last-Modified validator 和原子 sidecar 共同约束。续传请求使用 `If-Range`；sidecar 缺失、损坏、身份变化或 validator 不可信时安全清空 partial 并发出显式 progress reset，绝不拼接无法证明属于同一资源的字节。`416` 只有在本地长度与可信远端总长完全一致时才成功。

`EquivalentMirrorURLs` 只接受平台明确声明为同一字节实体的 CDN 镜像，并继续执行同一身份校验。清晰度、媒体候选、API 路径、登录态等语义 fallback 留在平台 adapter。Fetch 仅对单资源的网络中断、短读和明确允许的状态码做短重试；`429` 默认不重试，只有 adapter 在 `RetryableStatusCodes` 显式允许时才重试。

每次 HTTP attempt 都有 no-progress watchdog，默认两分钟；等待响应头或响应体长期没有新字节会返回结构化 timeout。安全重试前会重建或校验 Range 和 sidecar，并在需要完整重下时发 reset；持续收到字节会刷新 watchdog。

multipart 必须显式启用，且满足：

- Range 支持以 `GET Range` 探测结果为准，不能只信 `HEAD Accept-Ranges: bytes`。
- 每个 chunk 必须返回 `206`，并校验 `Content-Range` 起止范围和总大小。
- chunk 短读返回 `io.ErrUnexpectedEOF`。
- 所有 part 必须共享同一 validator 和总大小，全部通过后才能组装临时文件。

## 5. Bilibili context、凭据和 FFmpeg

Bilibili 的分 P/playurl API 解析和 FFmpeg 合并都传播任务 context，pause/cancel 可以中止正在等待的 API 或外部进程；默认 API client 的请求上限为 30 秒。playurl 的 HTTP/B站业务码会稳定分类为 `bilibili.auth_required`、`bilibili.risk_control` 或 `bilibili.resource_expired`。pause 保留临时 `.m4s` 与 sidecar；cancel/remove 在 worker 退出后按最终输出路径派生的任务专属路径清理。

SESSDATA 只用于 Bilibili API/auth 请求和权限判断，不发送给媒体 CDN Fetch 请求；二维码轮询成功时把 SESSDATA 存入后端凭据状态，但公开的 QR status DTO 不返回该 Cookie。completion 必须由 `PublishFinal` 的原子 artifact/completed 快照产生，adapter 返回成功但没有主 final artifact 不会被 manager 误判完成。

下载完成通知受 `showNotification` 设置控制。

## 6. 检测列表边界

检测候选由后端 `DetectionStore` 维护。领域 ID 使用 `source + platform + stable platform/page identity`，只有缺少稳定身份时才保守使用规范化 URL/hash；微信 adapter 单独处理 signed URL 的已知稳定参数。candidate 以 opaque ID 合并 URL/format/quality，Store 负责字段补全、默认候选、当前项、排序和最多 100 项的容量淘汰。

每次 upsert、remove 或 clear 都递增 revision 并发布 `inserted/updated/removed/cleared` change，change 内携带完整权威快照。前端先订阅事件再请求初始数据，只接受更高 revision 并整体替换列表，不复制平台合并规则。

媒体 URL、请求 headers、decode key 和原始 platform content ID 只存在于后端。HTTP/Wails DTO 只暴露渲染信息、加密提示和 opaque candidate ID；`StartDetectedDownload(detectionID, candidateID)` 在后端解析私有候选并创建 v2 task。

完整微信检测、encrypted temporary、derived decrypt temporary 和发布流程见 [微信视频号检测与下载原理](wechat-channels-download-principle.md)。

## 7. Settings 事务边界

设置只通过 `GetSettings` / `UpdateSettings` 读写。`settings.Module` 串行执行 patch、normalize、纯内存 validate、effect plan 和原子配置提交；配置先落盘成功再替换内存，commit 前取消会逆序 rollback，commit 后取消返回成功 warning。下载目录的创建与可写性探测不属于纯校验，只在 `downloadDir` 实际变化时由 critical reversible effect 的 preflight 执行；主题、语言、并发数等无关更新不会访问当前下载目录。

副作用分为：

- critical reversible：必须成功，配置提交失败时用独立 bounded context 回滚。
- deferred：设置已提交，但通过带 scope/fields 的 `restartRequirements` 提示 app 或 proxy 重启。
- best-effort：失败不推翻已提交设置，通过 `warnings` 和 `settings:diagnostic` 暴露。

运行中的 proxyPort 变更返回 `scope=proxy`，停止态可 live apply；apiPort 使用 `scope=app`。`app.go` 不保存用户设置镜像，Settings 模块不依赖 Wails。

## 8. 验收命令

```bash
go test ./...
go vet ./...
go test -race ./internal/download/...
cd frontend && npm run test
cd frontend && npm run typecheck
cd frontend && npm run build
```
