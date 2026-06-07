# B站链接解析下载原理

本文档说明 EasyDownload 中 B站视频解析、清晰度选择和 DASH 下载的主要链路。B站实现以官方 Web API 为主：先解析 BV/av 标识，再获取视频元数据和分 P 信息，最后通过 `playurl` 接口获取 DASH 视频/音频流并交给下载管理器执行下载。

## 1. 总体流程

一次完整的 B站下载链路大致如下：

1. 前端传入 BV 号、av 号或完整 B站视频链接
2. `ParseURL` 解析出视频标识
3. `GetVideoInfo` / `GetVideoInfoWithParts` 请求视频信息接口
4. 后端解析标题、封面、作者、总时长、分 P 列表等元数据
5. `getStreamInfo` 请求 `x/player/playurl` 获取可用 DASH 流
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

### 2.2 播放地址接口

```text
GET https://api.bilibili.com/x/player/playurl?bvid={bvid}&cid={cid}&fnval=4048&fnver=0&fourk=1
```

当前请求 DASH 格式，并尽量请求高规格流。响应中重点使用：

- `accept_quality`：可选清晰度 ID
- `accept_description`：清晰度名称兜底文案
- `support_formats`：清晰度描述信息
- `dash.video`：视频流列表
- `dash.audio`：音频流列表

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

合并成功后删除临时文件和 multipart 状态文件。

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
- 大文件且服务端支持 Range 时使用 multipart 下载
- 不适合 multipart 时回退到顺序下载
- 暂停或取消时保留临时文件，便于后续恢复

## 6. 注意事项

- 高画质通常依赖有效登录态 `SESSDATA`。
- DASH 下载依赖 FFmpeg；没有 FFmpeg 时无法合并视频和音频。
- 当前编码选择偏向更高压缩效率和规格，不额外按设备兼容性降级到 H.264。
- 备用 CDN 只在下载或获取内容长度失败时使用，不改变用户在前端看到的清晰度列表。
