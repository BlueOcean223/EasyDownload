# B站链接解析下载原理

本文档说明 EasyDownload 中 B站视频解析、清晰度选择和 DASH 下载的主要链路。B站实现以官方 Web API 为主：普通视频先解析 BV/av 标识，再获取视频元数据和分 P 信息，最后通过 `x/player/playurl` 接口获取 DASH 视频/音频流；番剧/影视链接先解析 ep/ss/md 标识，再通过 PGC 接口获取季/集信息和播放流，最终都交给下载管理器执行下载。

## 1. 总体流程

一次完整的 B站下载链路大致如下：

1. 前端传入 BV 号、av 号、完整普通视频链接，或番剧/影视 ep/ss/md 链接
2. `ParseURL` 解析普通视频标识；失败后用 `ParseBangumiURL` 解析番剧标识
3. 普通视频用 `GetVideoInfo` / `GetVideoInfoWithParts` 请求视频信息接口；番剧用 `GetBangumiInfoByID` 请求 PGC season 信息
4. 后端解析标题、封面、作者/内容类型、总时长、分 P 列表或剧集列表等元数据
5. 普通视频用 `getStreamInfo` 请求 `x/player/playurl`；番剧用 `getBangumiStreamInfo` 请求 `pgc/player/web/playurl`
6. 每个清晰度下选择一个视频流，并选择一个音频流
7. 下载时分别落盘视频 `.m4s` 和音频 `.m4s`
8. 使用 FFmpeg 合并为最终 `.mp4`
9. 成功后清理临时文件和分片状态文件

## 2. 关键接口

### 2.1 视频信息接口

```text
GET https://api.bilibili.com/x/web-interface/view?bvid={bvid}
```

该接口用于获取：

- BV / av 信息
- 标题、封面、简介、作者
- 总时长
- 当前视频的 `cid`
- 分 P 列表 `pages`

如果 `pages` 为空，后端会创建一个默认分 P，保证下载流程统一按分 P 处理。

### 2.2 番剧/影视信息接口

```text
GET https://api.bilibili.com/pgc/view/web/season?ep_id={ep_id}
GET https://api.bilibili.com/pgc/view/web/season?season_id={season_id}
```

该接口用于获取 PGC 内容信息：

- `season_id` / `media_id`
- 标题、封面、简介、内容类型
- 当前 season 下的完整 `episodes` 列表
- 每集独立的 `aid`、`bvid`、`cid`、`ep_id`、标题、时长和 badge

`/bangumi/media/md{id}` 会先尝试直接用 `media_id` 请求 season 接口；若不可用，会通过 `pgc/review/user?media_id={media_id}` 解析出 `season_id` 后重试。

### 2.3 播放地址接口

```text
GET https://api.bilibili.com/x/player/playurl?bvid={bvid}&cid={cid}&fnval=4048&fnver=0&fourk=1
```

普通视频当前请求 DASH 格式，并尽量请求高规格流。响应中重点使用：

- `accept_quality`：可选清晰度 ID
- `accept_description`：清晰度名称兜底文案
- `support_formats`：清晰度描述信息
- `dash.video`：视频流列表
- `dash.audio`：音频流列表

番剧/影视必须使用 PGC 播放地址接口：

```text
GET https://api.bilibili.com/pgc/player/web/playurl?bvid={bvid}&cid={cid}&qn={quality}&fnval=4048&fnver=0&fourk=1&drm_tech_type=2&otype=json&platform=web
```

会员集在无权限时可能返回 `code=0` 但 `dash.video` 为空；此时后端会把它视为无可播放流，而不是仅依赖 `code` 判断成功。PGC CDN URL 同样有时效性，因此下载/恢复时会重新拉取流地址。

## 3. 流选择策略

### 3.1 视频流选择

B站同一清晰度下可能返回多个不同编码的视频流。当前每个清晰度只暴露一个最终可下载流，选择顺序为：

```text
AV1(codecid=13) > HEVC(codecid=12) > H.264(codecid=7)
```

如果同一编码族有多个候选，则选择 `bandwidth` 更高的流。

### 3.2 音频流选择

音频流选择当前使用最高 `bandwidth` 的 DASH 音频流。

该音频流会复用于所有清晰度选项，因为 B站 DASH 响应中音频通常独立于视频清晰度。

### 3.3 清晰度名称

清晰度名称按以下顺序解析：

1. `support_formats[].new_description`
2. `support_formats[].display_desc`
3. `support_formats[].format`
4. `accept_description`
5. 内置清晰度映射表
6. `未知({quality})`

这样可以避免某些接口字段缺失时前端显示空名称。

## 4. URL 字段兼容和备用 CDN

B站 DASH 流中的 URL 字段可能出现 camelCase 或 snake_case 两种形式：

- `baseUrl` / `base_url`
- `backupUrl` / `backup_url`
- `frameRate` / `frame_rate`
- `mimeType` / `mime_type`

解析时会优先使用 camelCase 主 URL，缺失时使用 snake_case；备用 URL 会合并、去空、去重。

如果主 URL 缺失但备用 URL 存在，会将第一个备用 URL 提升为主 URL，剩余备用 URL 继续作为 fallback。

## 5. 下载阶段

### 5.1 DASH 下载

DASH 视频会被拆成两个临时文件：

- `{output}_video.m4s`
- `{output}_audio.m4s`

下载完成后通过 FFmpeg 执行无重编码合并：

```text
ffmpeg -i video.m4s -i audio.m4s -c copy -y output.mp4
```

合并使用 context-aware 调用：如果用户在合并阶段暂停或取消，FFmpeg 进程会随 task context 终止。暂停由 manager 保留临时文件供恢复；取消或删除则必须先等待 worker 退出，再由 adapter cleanup 删除任务专属临时文件和 sidecar。合并成功后清理中间 `.m4s` 文件。

### 5.2 备用 URL 重试

视频和音频下载都会使用同一套 fallback 逻辑：

1. 先尝试主 URL
2. 主 URL 失败且任务未取消时，按顺序尝试备用 URL
3. 所有 URL 都失败后返回最后一次错误
4. 如果用户暂停或取消，立即返回，不继续尝试备用 URL

下载 URL 在进入重试逻辑前会先去空、trim 和去重，避免重复请求同一个 CDN 地址。

### 5.3 内容长度和进度

下载前会用 `HEAD` 获取视频和音频的 `Content-Length`，用于：

- 汇报总大小
- 计算 DASH 视频/音频的加权进度

如果主 URL 获取大小失败，会继续尝试备用 URL。若所有 URL 都无法获取有效长度，大小会记为 `0`，下载仍会继续，只是进度精确度会下降。

### 5.4 断点续传和分片下载

实际文件下载复用通用下载能力：

- 已有临时文件时尝试断点续传
- 视频、音频 `.m4s` 文件都通过 `internal/download/fetch` 下载
- 主 URL 和 B站声明为同一 DASH 字节实体的备用 CDN 作为 `EquivalentMirrorURLs` 交给 Fetch；Fetch 继续校验 validator/size，不能把不同清晰度当成镜像
- Fetch 负责 Range 续传、Range 被忽略时完整重下、短重试和字节级进度
- 暂停保留临时文件；取消/删除在 join 后清理，shutdown 保留以便重启恢复

### 5.5 API context、媒体 Cookie 与最终发布

分 P 和 playurl 请求使用注入的 `HTTPDoer` 并传播 task context，pause/cancel 能中止尚未返回的 API 请求；默认 API request timeout 为 30 秒。playurl 的 HTTP 状态和 B站业务码会映射为稳定的 `bilibili.auth_required`、`bilibili.risk_control` 或 `bilibili.resource_expired`，前端不解析错误字符串。SESSDATA 只用于 B站 API/auth 和权限判断，不发送给媒体 CDN Fetch 请求；QR poll 成功后在后端保存 Cookie，但公开 status DTO 明确省略 `sessData`。

Adapter 始终把媒体写到任务专属临时路径，FFmpeg 合并结果仍是临时产物；只有 `PublishFinal` 能按 manager 预留的路径执行 no-replace 发布。主 final artifact 与 `completed` 在同一 revision 持久化；重启看到已经发布的可信主产物时直接认领，不重复请求 API 或执行 adapter。

## 6. 番剧交互和权限处理

- `/bangumi/play/ep{id}` 默认把该 ep 作为当前集，同时保留整季剧集列表供「展开全部」多选。
- `/bangumi/play/ss{id}` 和 `/bangumi/media/md{id}` 没有明确 ep 时，默认选择第一集可播放正片作为当前集。
- 当前集会先拉取清晰度用于直接下载；展开整季时不会批量请求所有集的播放流，避免长番一次性触发大量接口请求。
- 每集会保留 `badge`（会员/限免/预告等）并在前端选集列表展示。

## 7. 注意事项

- 高画质通常依赖有效登录态 `SESSDATA`，番剧会员集还可能需要大会员权限。
- DASH 下载依赖 FFmpeg；没有 FFmpeg 时无法合并视频和音频。
- 创建下载任务时会把 B站稳定的 BV/CID、分 P/剧集索引和用户选择的清晰度序列化为 `platformData`；为兼容现有 schema，其中可能仍带有展示阶段取得的 `Streams`，但执行端不信任这些短期 CDN 直链。普通视频使用的历史 `partIndex=-1` 在执行时映射到首个分段。每次实际执行（包括重启恢复、暂停后继续和排队后启动）都会用 BV/CID 重新请求 playurl，再按用户清晰度选择新流，避免复用已经过期的签名 URL。App 重启后由 B站平台适配器恢复执行，不再依赖不可持久化的下载函数。Adapter 在解码前要求当前 `PlatformDataVersion`，未知版本直接拒绝恢复。
- 取消或删除任务时，临时 `.m4s` 文件和状态文件按最终输出路径派生清理，不再从展示标题里推断清理前缀。
- 新任务保存在 `downloads.v2.json`；旧 `downloads.json` 不导入且保持原样。pause/cancel/remove 先返回 accepted receipt，终态由 revisioned lifecycle event 通知前端。
- 当前编码选择偏向更高压缩效率和规格，不额外按设备兼容性降级到 H.264。
- 备用 CDN 只在下载或获取内容长度失败时使用，不改变用户在前端看到的清晰度列表。
- 如果 PGC 流返回 `bilidrm_uri` 且没有可用解密 key，当前会提示 DRM 保护内容暂不支持下载。
