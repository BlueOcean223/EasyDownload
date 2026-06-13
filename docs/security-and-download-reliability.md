# 安全边界与下载可靠性说明

本文档记录 EasyDownload 当前内部 API、MITM 代理和通用下载管理器的安全/可靠性边界。涉及代理、下载队列、断点续传或状态持久化时，应同步更新本文档。

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

视频 CDN 域名不做 MITM，以避免影响播放性能和扩大隐私边界。

上游代理配置在运行时修改后，会关闭空闲连接并让新请求读取最新配置，无需重启代理。

HTML/JS 响应修改前会先读取原始 body 并恢复；如果遇到无法解码的 `Content-Encoding`，会跳过修改并保持原响应 body 不丢失。

## 3. 下载队列与状态持久化

`DownloadManager` 负责所有平台的任务调度：

- `maxConcurrent` 控制最大并发下载数
- 超出并发限制的任务进入 pending 队列，而不是直接失败
- 同一个任务 ID 的并发 `StartTask` 只会启动一个 goroutine
- 任务添加时会同时检查磁盘文件和已有任务文件名，避免未落盘任务拿到相同输出名

App 启动时会加载：

```text
{appDataDir}/downloads.json
```

退出或任务状态变化时会保存下载状态。重启恢复策略：

- 原本 `downloading` / `retrying` 的任务会恢复为 `paused`
- Bilibili 任务在继续/重试时重新解析并绑定 DASH 下载函数
- Douyin / Xiaohongshu 的自定义下载闭包无法可靠持久化，重启后的未完成任务会标记为失败并提示“需要重新解析后再下载”

## 4. HTTP 与 multipart 下载校验

普通 HTTP 下载支持断点续传。处理 `416 Range Not Satisfiable` 时，只有本地文件大小与远端总大小完全一致才视为成功；本地文件大于远端或远端大小未知时会报错，避免把损坏文件当成完成。

multipart 下载必须满足更严格条件：

- Range 支持以 `GET Range` 探测结果为准，不能只信 `HEAD Accept-Ranges: bytes`
- 每个 chunk 请求必须返回 `206 Partial Content`
- 必须校验 `Content-Range` 的起止范围和总大小
- chunk 提前 EOF 且未读满预期长度时返回 `io.ErrUnexpectedEOF`

这样可以避免服务器忽略 Range 返回 `200 OK` 时，把完整文件开头写入多个 offset 造成最终文件损坏。

## 5. 取消、暂停和 FFmpeg 合并

下载完成前会再次检查当前任务状态：如果用户已经取消或暂停，不会把任务覆盖成 `completed`。

Bilibili DASH 合并使用 context-aware FFmpeg 调用；在合并阶段暂停/取消时，FFmpeg 进程会随 context 终止，临时文件保留供后续恢复或清理。

下载完成通知受 `showNotification` 设置控制，用户关闭通知后不会再发送完成或开始下载通知。

## 6. 验收命令

关键路径变更后建议至少执行：

```bash
go test ./...
go vet ./...
go test -race ./internal/download ./internal/api ./internal/proxy
cd frontend && npm run typecheck
cd frontend && npm run test
cd frontend && npm run build
```
