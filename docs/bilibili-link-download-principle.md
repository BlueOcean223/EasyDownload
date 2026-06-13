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

合并使用 context-aware 调用：如果用户在合并阶段暂停或取消，FFmpeg 进程会随 context 终止，临时文件保留供恢复或清理。合并成功后删除临时文件和 multipart 状态文件。

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
- 大文件且服务端通过 `GET Range` 证实支持 Range 时使用 multipart 下载
- multipart chunk 必须返回 `206 Partial Content`，并校验 `Content-Range` 起止范围
- chunk 提前 EOF 会失败，避免生成损坏文件
- 不适合 multipart 时回退到顺序下载
- 暂停或取消时保留临时文件，便于后续恢复

## 6. 番剧交互和权限处理

- `/bangumi/play/ep{id}` 默认把该 ep 作为当前集，同时保留整季剧集列表供「展开全部」多选。
- `/bangumi/play/ss{id}` 和 `/bangumi/media/md{id}` 没有明确 ep 时，默认选择第一集可播放正片作为当前集。
- 当前集会先拉取清晰度用于直接下载；展开整季时不会批量请求所有集的播放流，避免长番一次性触发大量接口请求。
- 每集会保留 `badge`（会员/限免/预告等）并在前端选集列表展示。

## 7. 注意事项

- 高画质通常依赖有效登录态 `SESSDATA`，番剧会员集还可能需要大会员权限。
- DASH 下载依赖 FFmpeg；没有 FFmpeg 时无法合并视频和音频。
- App 重启后，B站任务会在继续/重试时重新解析流信息并绑定下载函数，因为 CDN URL 具有时效性。
- 当前编码选择偏向更高压缩效率和规格，不额外按设备兼容性降级到 H.264。
- 备用 CDN 只在下载或获取内容长度失败时使用，不改变用户在前端看到的清晰度列表。
- 如果 PGC 流返回 `bilidrm_uri` 且没有可用解密 key，当前会提示 DRM 保护内容暂不支持下载。
