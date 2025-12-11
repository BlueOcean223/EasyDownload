# EasyDownload

一款简洁易用的桌面视频下载器，支持微信视频号和B站视频下载。

## 功能特点

- **视频号嗅探下载**：自动检测微信PC端播放的视频号视频，一键下载
- **B站视频下载**：支持 BV号、av号、完整链接，多画质选择
- **可视化界面**：Netflix 风格的视频卡片展示，清晰的下载进度
- **零配置目标**：自动化证书安装和代理设置，降低使用门槛

## 技术栈

- **前端**: Vue 3 + TypeScript + Naive UI + Tailwind CSS
- **后端**: Go
- **桌面框架**: Wails v2

## 使用说明

### 首次使用

1. **以管理员身份运行** EasyDownload.exe
2. 进入「设置」页面，点击「安装证书」按钮安装 CA 根证书
3. 返回主页面，点击侧边栏的「启动代理」按钮

### 下载视频号视频

1. 确保代理服务已启动（侧边栏显示绿色运行状态）
2. 打开**微信 PC 端**，浏览视频号内容
3. 检测到的视频会自动显示在「视频嗅探」页面
4. 点击视频卡片上的「下载」按钮即可下载

### 下载B站视频

1. 进入「B站下载」页面
2. 粘贴B站视频链接（支持 BV号、av号、完整链接）
3. 点击「解析」按钮获取视频信息
4. 选择画质后点击「下载视频」

> 注意：下载高清B站视频需要 FFmpeg。请安装 FFmpeg 并添加到系统 PATH。

## 开发说明

### 环境要求

- Go 1.21+
- Node.js 18+
- Wails CLI v2

### 安装 Wails CLI

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

### 开发模式

```bash
wails dev
```

### 构建

```bash
wails build
```

构建产物位于 `build/bin/` 目录。

## 项目结构

```
EasyDownload/
├── app.go                 # Wails 应用主入口
├── main.go                # 程序入口
├── internal/
│   ├── api/               # 内部 API 服务器
│   ├── downloader/        # 下载管理器、B站下载
│   ├── proxy/             # MITM 代理、证书管理
│   └── utils/             # 工具函数
├── frontend/
│   ├── src/
│   │   ├── views/         # 页面组件
│   │   ├── stores/        # Pinia 状态管理
│   │   ├── router/        # Vue Router
│   │   └── types/         # TypeScript 类型
│   └── wailsjs/           # Wails 生成的绑定
└── build/
    └── bin/               # 构建产物
```

## 工作原理

### 视频号下载

1. 应用启动内置 MITM 代理服务器，监听本地端口
2. 设置系统代理，使微信 PC 端流量经过代理
3. 拦截视频号相关域名的 HTTPS 流量
4. 向网页注入 JavaScript 脚本，监听视频播放事件
5. 提取视频元数据（标题、封面、URL）并发送回应用
6. 应用接收数据后在界面展示，用户可一键下载

### B站下载

1. 解析视频链接获取 BV 号
2. 调用 B站 API 获取视频信息和流地址
3. 下载视频流和音频流
4. 使用 FFmpeg 合并为完整视频

## 免责声明

本项目仅供学习和技术研究使用。请遵守相关法律法规，尊重内容创作者的权益。下载的内容仅供个人学习使用，请勿用于商业目的或非法传播。

## License

MIT
