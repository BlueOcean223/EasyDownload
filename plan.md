---
name: EasyDownload 开发计划
overview: 一款使用 Wails v2 (Go+Vue3) 构建的桌面应用，用于下载 B站 和 微信视频号 的视频。它通过将 GUI 与 MITM 代理相结合，智能提取视频元数据（通过 JS 注入或流量分析），提供用户友好的"已检测视频"列表（带预览），而非原始终端或杂乱的数据包列表，从而解决现有工具的痛点。
todos:
  - id: init-project
    content: 使用 Vue3/TypeScript 模板初始化 Wails 项目
    status: completed
  - id: backend-structure
    content: 搭建后端目录结构 (internal/proxy, internal/downloader)
    status: in_progress
    dependencies:
      - init-project
  - id: cert-manager
    content: 实现证书生成与安装逻辑 (CA)
    status: pending
    dependencies:
      - backend-structure
  - id: proxy-basic
    content: 实现基础 MITM 代理服务器 (goproxy)
    status: pending
    dependencies:
      - backend-structure
  - id: frontend-ui
    content: 开发前端 UI 骨架 (Naive UI + Tailwind)
    status: pending
    dependencies:
      - init-project
  - id: wechat-intercept
    content: 实现微信流量拦截器与 JS 注入逻辑
    status: pending
    dependencies:
      - proxy-basic
      - cert-manager
  - id: internal-api
    content: 实现内部 API 以接收注入数据
    status: pending
    dependencies:
      - backend-structure
  - id: bilibili-module
    content: 实现 B站下载器封装
    status: pending
    dependencies:
      - backend-structure
  - id: download-manager
    content: 实现下载管理器（队列、进度）
    status: pending
    dependencies:
      - backend-structure
---

# EasyDownload 开发计划

参考项目
1. [res-downloader](./reference/res-downloader)
2. [wx_channels_download](./reference/wx_channels_download)

## 1. 架构概述

本应用是一款使用 **Wails v2** 构建的 **Windows 桌面应用**。

-   **前端**: Vue 3 + TypeScript + TailwindCSS（用于 UI）。
-   **后端**: Go（用于系统代理、证书管理、文件 I/O、网络请求）。

### 核心模块

1.  **MITM 代理服务（核心创新）**:

    -   内置 HTTP/HTTPS 代理（使用 `goproxy`）。
    -   拦截微信流量以检测视频源。
    -   **策略**: "智能注入"。我们拦截微信视频号的 webview 请求，向页面注入一小段 JavaScript 代码。该脚本监听视频播放事件，直接从浏览器的 DOM/内存中提取元数据（标题、封面、URL）（此时数据已解密），并将其发送回我们的桌面应用。
    -   这解决了 `res-downloader` 的"加密流量"问题和 `wx_channels_download` 的"糟糕用户体验"问题。

2.  **B站下载器**:

    -   集成 `lux`（原 annie）或类似的成熟 Go 库/CLI 作为内嵌引擎，处理复杂的 B站解析（4K、音视频合并）。

3.  **下载管理器**:

    -   统一的流媒体下载队列系统。
    -   支持 `ffmpeg`（内置）进行音视频轨道合并。

## 2. 技术栈

-   **GUI 框架**: [Wails v2](https://wails.io)
-   **前端**: Vue 3, TypeScript, Pinia（状态管理）, Naive UI（或 Element Plus）组件库。
-   **后端 (Go)**:
    -   `github.com/elazarl/goproxy`（或 `ouqiang/goproxy`）用于 MITM。
    -   `github.com/iawia002/lux`（B站参考实现）。
    -   `net/http` 用于内部 API 服务器（接收注入数据）。

## 3. 详细实现步骤

### 阶段 1: 项目初始化与基础设施

-   [ ] **搭建 Wails 项目**: 初始化 `wails init -n EasyDownload -t vue-ts`。
-   [ ] **UI 框架**: 安装 Naive UI（简洁、支持暗色模式）+ TailwindCSS。
-   [ ] **后端结构**: 创建 `internal/proxy`, `internal/downloader`, `internal/utils`。

### 阶段 2: MITM 代理与证书管理器（微信模块）

这是微信视频号功能最关键的部分。

-   [ ] **证书颁发机构 (CA) 管理器**:
    -   实现 `internal/proxy/cert.go`，在首次运行时生成根 CA 证书（`cert.pem`, `key.pem`）。
    -   实现"安装证书"功能：使用 `certutil`（Windows）将 CA 添加到"受信任的根证书颁发机构"存储中。（对 HTTPS 拦截至关重要）。
-   [ ] **代理服务器**:
    -   实现 `internal/proxy/server.go`。
    -   监听本地端口（如 `:8899`）。
    -   实现"系统代理开关"：修改 Windows 注册表或使用库将系统全局代理设置为 `127.0.0.1:8899`。
-   [ ] **拦截逻辑**:
    -   **流量过滤**: 识别发往 `channels.weixin.qq.com` 或 `finder.video.qq.com` 的请求。
    -   **注入**: 对匹配的 HTML 响应，在 body 末尾追加 `<script>...我们的代码...</script>`。
    -   **注入代码 (`inject.js`)**:
        -   监控 DOM 中的视频元素。
        -   提取元数据（标题、封面图、媒体 URL）。
        -   发送 `POST http://127.0.0.1:APP_PORT/api/detect` 请求，携带 JSON 数据。

### 阶段 3: B站模块

-   [ ] **URL 解析器**: 输入 URL -> 验证是否为 `bilibili.com`。
-   [ ] **引擎集成**:
    -   下载 `lux.exe` 或 `ffmpeg.exe`（或检查是否已存在）。
    -   封装这些工具的执行或导入库代码以获取流信息。
-   [ ] **画质选择**: 解析可用的流（1080p, 720p）并展示给前端。

### 阶段 4: 前端与用户体验

-   [ ] **仪表盘**:
    -   状态指示器："代理服务：运行中/已停止"。
    -   "安装证书"按钮（未安装显示红色，已安装显示绿色）。
-   [ ] **嗅探器标签页（微信）**:
    -   列表视图：卡片显示 [缩略图] [标题] [来源: 微信]。
    -   操作：卡片上的"下载"按钮。
-   [ ] **下载管理器标签页**:
    -   进度条、速度、暂停/继续控制。
    -   "打开文件夹"按钮。

### 阶段 5: 打包

-   [ ] **二进制文件**: 打包 `ffmpeg.exe` 和 `certutil` 脚本（如需要），或确保首次运行时下载。
-   [ ] **构建**: `wails build`。

## 4. 创新总结

-   **可视化嗅探**: 不同于 `res-downloader`（大量数据包）或 `wx_channels`（盲目的 CLI），我们展示类似 **Netflix 风格的视频列表**。
-   **零配置（目标）**: 我们自动化证书安装和代理设置（经用户许可），消除非技术用户的最大障碍。
