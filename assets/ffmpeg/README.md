# FFmpeg 内置资源

此目录用于存放内置的 FFmpeg 二进制文件。

## 使用方法

1. 下载适合您平台的 FFmpeg 静态构建版本：
   - Windows: https://www.gyan.dev/ffmpeg/builds/ (选择 ffmpeg-release-essentials.zip)
   - macOS: https://evermeet.cx/ffmpeg/
   - Linux: https://johnvansickle.com/ffmpeg/

2. 将 `ffmpeg.exe` (Windows) 或 `ffmpeg` (macOS/Linux) 放入此目录

3. 重新构建应用程序，FFmpeg 将被嵌入到可执行文件中

## 注意事项

- FFmpeg 二进制文件较大（约 50-100MB），会显著增加应用程序大小
- 如果不需要内置 FFmpeg，可以保持此目录为空，应用将尝试使用系统安装的 FFmpeg
- 确保使用的 FFmpeg 版本与目标平台兼容

## 文件结构

```
assets/ffmpeg/
├── README.md       # 本文件
├── ffmpeg.exe      # Windows 版本 (可选)
└── ffmpeg          # macOS/Linux 版本 (可选)
```
