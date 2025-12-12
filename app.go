package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"EasyDownload/assets"
	"EasyDownload/assets/icons"
	"EasyDownload/internal/api"
	"EasyDownload/internal/config"
	"EasyDownload/internal/downloader"
	"EasyDownload/internal/ffmpeg"
	"EasyDownload/internal/proxy"
	"EasyDownload/internal/tray"
	"EasyDownload/internal/utils"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx                context.Context
	certManager        *proxy.CertManager
	proxyServer        *proxy.ProxyServer
	systemProxy        *proxy.SystemProxy
	internalAPI        *api.InternalAPI
	downloadManager    *downloader.DownloadManager
	bilibiliDownloader *downloader.BilibiliDownloader
	trayManager        *tray.TrayManager
	ffmpegManager      *ffmpeg.FFmpegManager
	configManager      *config.ConfigManager

	// Settings
	proxyPort        int
	apiPort          int
	downloadDir      string
	minimizeToTray   bool
	showNotification bool
	firstRunComplete bool
	theme            string // "dark" or "light"
	language         string // "zh-CN" or "en-US"
	upstreamProxy    string // Upstream proxy URL
	useUpstreamProxy bool   // Whether to use upstream proxy

	// Diagnostics
	proxyDebug   bool
	wechatNoMITM bool
}

// NewApp creates a new App application struct
func NewApp() *App {
	appDataDir := utils.GetAppDataDir()
	downloadDir := utils.GetDownloadDir()

	// Ensure directories exist
	utils.EnsureDir(appDataDir)
	utils.EnsureDir(downloadDir)

	return &App{
		proxyPort:        8899,
		apiPort:          18899,
		downloadDir:      downloadDir,
		minimizeToTray:   true,
		showNotification: true,
		firstRunComplete: false,
		theme:            "dark",
		language:         "zh-CN",
		upstreamProxy:    "",
		useUpstreamProxy: false,
		proxyDebug:       false,
		wechatNoMITM:     false,
	}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Load persisted config (best-effort)
	cfgPath := filepath.Join(utils.GetAppDataDir(), "config.json")
	a.configManager = config.NewConfigManager(cfgPath)
	if err := a.configManager.Load(); err != nil {
		log.Printf("Failed to load config: %v", err)
	}
	if a.configManager != nil {
		cfg := a.configManager.Get()
		if cfg != nil {
			// Apply config to app fields (only if non-zero/meaningful)
			if cfg.ProxyPort > 0 {
				a.proxyPort = cfg.ProxyPort
			}
			if cfg.APIPort > 0 {
				a.apiPort = cfg.APIPort
			}
			if cfg.DownloadDir != "" {
				a.downloadDir = cfg.DownloadDir
			}
			a.minimizeToTray = cfg.MinimizeToTray
			a.showNotification = cfg.ShowNotification
			a.firstRunComplete = cfg.FirstRunComplete
			if cfg.Theme != "" {
				a.theme = cfg.Theme
			}
			if cfg.Language != "" {
				a.language = cfg.Language
			}
			a.upstreamProxy = cfg.UpstreamProxy
			a.useUpstreamProxy = cfg.UseUpstreamProxy
			a.proxyDebug = cfg.ProxyDebug
			a.wechatNoMITM = cfg.WeChatNoMITM
		}
	}

	// Initialize certificate manager
	certDir := filepath.Join(utils.GetAppDataDir(), "certs")
	a.certManager = proxy.NewCertManager(certDir)

	// Generate CA certificate if not exists
	if !a.certManager.CertExists() {
		log.Println("Generating CA certificate...")
		if err := a.certManager.GenerateCACert(); err != nil {
			log.Printf("Failed to generate CA certificate: %v", err)
		}
	}

	// Initialize proxy server
	a.proxyServer = proxy.NewProxyServer(a.certManager, a.proxyPort)
	a.proxyServer.SetDebug(a.proxyDebug)
	a.proxyServer.SetWeChatNoMITM(a.wechatNoMITM)

	// Initialize system proxy
	a.systemProxy = proxy.NewSystemProxy()

	// Initialize internal API
	a.internalAPI = api.NewInternalAPI(a.apiPort)
	a.internalAPI.SetVideoCallback(func(video api.DetectedVideo) {
		// Emit event to frontend
		runtime.EventsEmit(a.ctx, "video:detected", video)
	})

	// Initialize download manager
	a.downloadManager = downloader.NewDownloadManager(a.downloadDir, 3)
	a.downloadManager.SetProgressCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:progress", task.TaskToJSON())
	})
	a.downloadManager.SetCompleteCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:complete", task.TaskToJSON())
		// Show notification for download complete
		if a.trayManager != nil {
			a.trayManager.ShowNotification("下载完成", task.Title)
		}
	})
	a.downloadManager.SetErrorCallback(func(task *downloader.DownloadTask, err error) {
		runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
			"task":  task.TaskToJSON(),
			"error": err.Error(),
		})
	})

	// Initialize Bilibili downloader
	a.bilibiliDownloader = downloader.NewBilibiliDownloader()

	// Initialize FFmpeg manager with embedded FFmpeg
	a.ffmpegManager = ffmpeg.NewFFmpegManager()
	ffmpegDir := filepath.Join(utils.GetAppDataDir(), "ffmpeg")
	a.ffmpegManager.SetExtractDir(ffmpegDir)

	// Set embedded FS and extract if available
	if assets.HasEmbeddedFFmpeg() {
		ffmpeg.SetEmbeddedFS(assets.FFmpegFS)
		if err := a.ffmpegManager.ExtractEmbedded(); err != nil {
			log.Printf("Failed to extract embedded FFmpeg: %v", err)
		} else {
			log.Printf("FFmpeg ready at: %s", a.ffmpegManager.GetPath())
		}
	}

	// Set FFmpeg manager for Bilibili downloader
	a.bilibiliDownloader.SetFFmpegManager(a.ffmpegManager)

	// Set proxy video callback
	a.proxyServer.SetVideoCallback(func(video proxy.VideoInfo) {
		apiVideo := api.DetectedVideo{
			ID:        video.ID,
			Title:     video.Title,
			Cover:     video.Cover,
			URL:       video.URL,
			Source:    video.Source,
			Quality:   video.Quality,
			Duration:  video.Duration,
			Author:    video.Author,
			AuthorAvatar: video.AuthorAvatar,
			Timestamp: video.Timestamp,
			DecodeKey: video.DecodeKey,
			FileSize:  video.FileSize,
			Width:     video.Width,
			Height:    video.Height,
			IsCurrent: video.IsCurrent,
		}
		runtime.EventsEmit(a.ctx, "video:detected", apiVideo)
	})

	// Set download callback for one-click download from video page
	a.proxyServer.GetWeChatHandler().SetDownloadCallback(func(video proxy.WeChatVideoInfo) {
		log.Printf("Download requested from video page: %s", video.Title)

		// Create unique ID
		id := fmt.Sprintf("wechat_%d", time.Now().UnixNano())

		// Add to download manager with decodeKey
		task, err := a.downloadManager.AddTaskWithDecodeKey(
			id,
			video.URL,
			video.Title,
			video.CoverURL,
			"wechat",
			"",
			video.DecodeKey,
		)
		if err != nil {
			log.Printf("Failed to add download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"error": err.Error(),
			})
			return
		}

		// Start downloading immediately
		if err := a.downloadManager.StartTask(id); err != nil {
			log.Printf("Failed to start download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"task":  task.TaskToJSON(),
				"error": err.Error(),
			})
			return
		}

		// Notify frontend
		runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

		// Show notification
		if a.trayManager != nil {
			a.trayManager.ShowNotification("开始下载", video.Title)
		}
	})

	// Start internal API server
	if err := a.internalAPI.Start(); err != nil {
		log.Printf("Failed to start internal API: %v", err)
	}

	// Initialize and start system tray
	a.initTray()

	log.Println("App started successfully")
}

// initTray initializes the system tray
func (a *App) initTray() {
	a.trayManager = tray.NewTrayManager()

	// Set icons
	a.trayManager.SetIcon(icons.DefaultIcon)
	a.trayManager.SetActiveIcon(icons.ActiveIcon)

	// Set callbacks
	a.trayManager.SetOnShow(func() {
		runtime.WindowShow(a.ctx)
	})

	a.trayManager.SetOnHide(func() {
		runtime.WindowHide(a.ctx)
	})

	a.trayManager.SetOnExit(func() {
		a.shutdown(a.ctx)
		runtime.Quit(a.ctx)
	})

	a.trayManager.SetOnToggleProxy(func() {
		if a.proxyServer.IsRunning() {
			a.StopProxy()
		} else {
			a.StartProxy()
		}
	})

	// Start tray in background
	a.trayManager.StartAsync()
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	// Stop tray manager
	if a.trayManager != nil {
		a.trayManager.Stop()
	}

	// Stop services
	if a.proxyServer != nil && a.proxyServer.IsRunning() {
		a.proxyServer.Stop()
	}

	if a.systemProxy != nil {
		a.systemProxy.Disable()
	}

	if a.internalAPI != nil {
		a.internalAPI.Stop()
	}

	log.Println("App shutdown complete")
}

// ==================== Proxy Methods ====================

// StartProxy starts the MITM proxy server
func (a *App) StartProxy() error {
	if err := a.proxyServer.Start(); err != nil {
		return err
	}

	// Enable system proxy
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", a.proxyPort)
	if err := a.systemProxy.Enable(proxyAddr); err != nil {
		a.proxyServer.Stop()
		return fmt.Errorf("failed to enable system proxy: %w", err)
	}

	// Update tray status
	if a.trayManager != nil {
		a.trayManager.SetProxyStatus(true)
	}

	return nil
}

// StopProxy stops the MITM proxy server
func (a *App) StopProxy() error {
	// Disable system proxy first
	if err := a.systemProxy.Disable(); err != nil {
		log.Printf("Failed to disable system proxy: %v", err)
	}

	err := a.proxyServer.Stop()

	// Update tray status
	if a.trayManager != nil {
		a.trayManager.SetProxyStatus(false)
	}

	return err
}

// IsProxyRunning returns whether the proxy is running
func (a *App) IsProxyRunning() bool {
	return a.proxyServer != nil && a.proxyServer.IsRunning()
}

// GetProxyPort returns the proxy port
func (a *App) GetProxyPort() int {
	return a.proxyPort
}

// ==================== Certificate Methods ====================

// IsCertInstalled checks if the CA certificate is installed
func (a *App) IsCertInstalled() bool {
	return a.certManager.IsCertInstalled()
}

// InstallCert installs the CA certificate to the system trust store
func (a *App) InstallCert() error {
	return a.certManager.InstallCert()
}

// UninstallCert removes the CA certificate from the system trust store
func (a *App) UninstallCert() error {
	return a.certManager.UninstallCert()
}

// GetCertPath returns the CA certificate file path
func (a *App) GetCertPath() string {
	return a.certManager.GetCertPath()
}

// ==================== Video Detection Methods ====================

// GetDetectedVideos returns all detected videos
func (a *App) GetDetectedVideos() []api.DetectedVideo {
	return a.internalAPI.GetDetectedVideos()
}

// ClearDetectedVideos clears all detected videos
func (a *App) ClearDetectedVideos() {
	a.internalAPI.ClearVideos()
}

// RemoveDetectedVideo removes a detected video by ID
func (a *App) RemoveDetectedVideo(id string) {
	a.internalAPI.RemoveVideo(id)
}

// ==================== Download Methods ====================

// DownloadVideo adds a video to the download queue
func (a *App) DownloadVideo(id, url, title, cover, source, quality string) (map[string]interface{}, error) {
	return a.DownloadVideoWithKey(id, url, title, cover, source, quality, "")
}

// DownloadVideoWithKey adds a video to the download queue with optional decryption key
func (a *App) DownloadVideoWithKey(id, url, title, cover, source, quality, decodeKey string) (map[string]interface{}, error) {
	task, err := a.downloadManager.AddTaskWithDecodeKey(id, url, title, cover, source, quality, decodeKey)
	if err != nil {
		return nil, err
	}

	// Start downloading
	if err := a.downloadManager.StartTask(id); err != nil {
		return nil, err
	}

	return task.TaskToJSON(), nil
}

// PauseDownload pauses a download
func (a *App) PauseDownload(id string) error {
	return a.downloadManager.PauseTask(id)
}

// ResumeDownload resumes a paused download
func (a *App) ResumeDownload(id string) error {
	return a.downloadManager.StartTask(id)
}

// CancelDownload cancels a download
func (a *App) CancelDownload(id string) error {
	return a.downloadManager.CancelTask(id)
}

// RemoveDownload removes a download from the list
func (a *App) RemoveDownload(id string) error {
	return a.downloadManager.RemoveTask(id)
}

// GetDownloads returns all download tasks
func (a *App) GetDownloads() []map[string]interface{} {
	tasks := a.downloadManager.GetAllTasks()
	result := make([]map[string]interface{}, len(tasks))
	for i, task := range tasks {
		result[i] = task.TaskToJSON()
	}
	return result
}

// ==================== Bilibili Methods ====================

// GetBilibiliVideoInfo fetches video info from Bilibili
func (a *App) GetBilibiliVideoInfo(url string) (*downloader.BilibiliVideo, error) {
	bvid, err := a.bilibiliDownloader.ParseURL(url)
	if err != nil {
		return nil, err
	}

	return a.bilibiliDownloader.GetVideoInfo(bvid)
}

// DownloadBilibiliVideo downloads a Bilibili video
func (a *App) DownloadBilibiliVideo(url string, quality int) (string, error) {
	video, err := a.GetBilibiliVideoInfo(url)
	if err != nil {
		return "", err
	}

	// Create unique ID
	id := fmt.Sprintf("bilibili_%s_%d", video.BV, time.Now().Unix())

	// Add to download manager for tracking
	task, err := a.downloadManager.AddTask(id, url, video.Title, video.Cover, "bilibili", fmt.Sprintf("%d", quality))
	if err != nil {
		return "", err
	}

	// Download in background
	go func() {
		runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

		outputPath, err := a.bilibiliDownloader.Download(video, quality, a.downloadDir, func(progress float64) {
			task.Progress = progress
			runtime.EventsEmit(a.ctx, "download:progress", task.TaskToJSON())
		})

		if err != nil {
			task.Status = downloader.StatusFailed
			task.Error = err.Error()
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"task":  task.TaskToJSON(),
				"error": err.Error(),
			})
			return
		}

		task.Status = downloader.StatusCompleted
		task.FilePath = outputPath
		task.Progress = 100
		task.CompletedAt = time.Now().Unix()
		runtime.EventsEmit(a.ctx, "download:complete", task.TaskToJSON())
	}()

	return id, nil
}

// DownloadBilibiliPart downloads a specific part of a Bilibili video
func (a *App) DownloadBilibiliPart(url string, partIndex int, quality int) (string, error) {
	video, err := a.bilibiliDownloader.GetVideoInfoWithParts(a.bilibiliDownloader.ParseURLMust(url))
	if err != nil {
		return "", err
	}

	if partIndex < 0 || partIndex >= len(video.Parts) {
		return "", fmt.Errorf("invalid part index: %d", partIndex)
	}

	part := video.Parts[partIndex]

	// Create unique ID with part info
	id := fmt.Sprintf("bilibili_%s_p%d_%d", video.BV, part.Page, time.Now().Unix())

	// Create title with part info
	title := video.Title
	if len(video.Parts) > 1 {
		title = fmt.Sprintf("%s - P%d %s", video.Title, part.Page, part.PartName)
	}

	// Add to download manager for tracking
	task, err := a.downloadManager.AddTask(id, url, title, video.Cover, "bilibili", fmt.Sprintf("%d", quality))
	if err != nil {
		return "", err
	}

	// Download in background
	go func() {
		runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

		outputPath, err := a.bilibiliDownloader.DownloadPart(video, partIndex, quality, a.downloadDir, func(progress float64) {
			task.Progress = progress
			runtime.EventsEmit(a.ctx, "download:progress", task.TaskToJSON())
		})

		if err != nil {
			task.Status = downloader.StatusFailed
			task.Error = err.Error()
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"task":  task.TaskToJSON(),
				"error": err.Error(),
			})
			return
		}

		task.Status = downloader.StatusCompleted
		task.FilePath = outputPath
		task.Progress = 100
		task.CompletedAt = time.Now().Unix()
		runtime.EventsEmit(a.ctx, "download:complete", task.TaskToJSON())
	}()

	return id, nil
}

// SetBilibiliSessData sets the Bilibili session cookie
func (a *App) SetBilibiliSessData(sessData string) error {
	return a.bilibiliDownloader.SaveSessData(sessData)
}

// GetBilibiliSessData gets the Bilibili session cookie
func (a *App) GetBilibiliSessData() string {
	sessData, _ := a.bilibiliDownloader.LoadSessData()
	return sessData
}

// IsFFmpegAvailable checks if ffmpeg is available
func (a *App) IsFFmpegAvailable() bool {
	return a.bilibiliDownloader.IsFFmpegAvailable()
}

// ==================== Settings Methods ====================

// GetDownloadDir returns the download directory
func (a *App) GetDownloadDir() string {
	return a.downloadDir
}

// SetDownloadDir sets the download directory
func (a *App) SetDownloadDir(dir string) error {
	if err := a.downloadManager.SetDownloadDir(dir); err != nil {
		return err
	}
	a.downloadDir = dir
	return nil
}

// SelectDownloadDir opens a folder dialog to select download directory
func (a *App) SelectDownloadDir() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择下载目录",
		DefaultDirectory: a.downloadDir,
	})
	if err != nil {
		return "", err
	}
	if dir != "" {
		a.SetDownloadDir(dir)
	}
	return dir, nil
}

// OpenDownloadDir opens the download directory in file explorer
func (a *App) OpenDownloadDir() error {
	return utils.OpenFolder(a.downloadDir)
}

// OpenFile opens a file with the default application
func (a *App) OpenFile(path string) error {
	return utils.OpenFile(path)
}

// ==================== System Methods ====================

// GetAppInfo returns app information
func (a *App) GetAppInfo() map[string]interface{} {
	return map[string]interface{}{
		"version":          "1.0.0",
		"proxyPort":        a.proxyPort,
		"apiPort":          a.apiPort,
		"downloadDir":      a.downloadDir,
		"certPath":         a.certManager.GetCertPath(),
		"minimizeToTray":   a.minimizeToTray,
		"showNotification": a.showNotification,
		"firstRunComplete": a.firstRunComplete,
		"theme":            a.theme,
		"language":         a.language,
		"upstreamProxy":    a.upstreamProxy,
		"useUpstreamProxy": a.useUpstreamProxy,
		"proxyDebug":       a.proxyDebug,
		"wechatNoMITM":     a.wechatNoMITM,
	}
}

// ==================== Tray Methods ====================

// MinimizeToTray minimizes the window to system tray
func (a *App) MinimizeToTray() {
	runtime.WindowHide(a.ctx)
	if a.trayManager != nil {
		a.trayManager.SetWindowVisible(false)
	}
}

// RestoreFromTray restores the window from system tray
func (a *App) RestoreFromTray() {
	runtime.WindowShow(a.ctx)
	if a.trayManager != nil {
		a.trayManager.SetWindowVisible(true)
	}
}

// IsMinimizeToTrayEnabled returns whether minimize to tray is enabled
func (a *App) IsMinimizeToTrayEnabled() bool {
	return a.minimizeToTray
}

// SetMinimizeToTray sets whether to minimize to tray on close
func (a *App) SetMinimizeToTray(enabled bool) {
	a.minimizeToTray = enabled
}

// IsShowNotificationEnabled returns whether notifications are enabled
func (a *App) IsShowNotificationEnabled() bool {
	return a.showNotification
}

// SetShowNotification sets whether to show notifications
func (a *App) SetShowNotification(enabled bool) {
	a.showNotification = enabled
}

// IsFirstRunComplete returns whether first run setup is complete
func (a *App) IsFirstRunComplete() bool {
	return a.firstRunComplete
}

// SetFirstRunComplete marks first run setup as complete
func (a *App) SetFirstRunComplete(complete bool) {
	a.firstRunComplete = complete
}

// ShowNotification shows a system notification
func (a *App) ShowNotification(title, message string) {
	if a.trayManager != nil {
		a.trayManager.ShowNotification(title, message)
	}
}

// ==================== Log Methods ====================

// OpenLogDir opens the log directory in file explorer
func (a *App) OpenLogDir() error {
	logDir := filepath.Join(utils.GetAppDataDir(), "logs")
	return utils.OpenFolder(logDir)
}

// GetLogDir returns the log directory path
func (a *App) GetLogDir() string {
	return filepath.Join(utils.GetAppDataDir(), "logs")
}

// ==================== Appearance Methods ====================

// GetTheme returns the current theme
func (a *App) GetTheme() string {
	return a.theme
}

// SetTheme sets the theme (dark or light)
func (a *App) SetTheme(theme string) error {
	if theme != "dark" && theme != "light" {
		return fmt.Errorf("invalid theme: must be 'dark' or 'light'")
	}
	a.theme = theme
	return nil
}

// GetLanguage returns the current language
func (a *App) GetLanguage() string {
	return a.language
}

// SetLanguage sets the language (zh-CN or en-US)
func (a *App) SetLanguage(lang string) error {
	if lang != "zh-CN" && lang != "en-US" {
		return fmt.Errorf("invalid language: must be 'zh-CN' or 'en-US'")
	}
	a.language = lang
	return nil
}

// ==================== Proxy Chain Methods ====================

// GetUpstreamProxy returns the upstream proxy URL
func (a *App) GetUpstreamProxy() string {
	return a.upstreamProxy
}

// SetUpstreamProxy sets the upstream proxy URL
func (a *App) SetUpstreamProxy(proxyURL string) {
	a.upstreamProxy = proxyURL
	if a.useUpstreamProxy && a.proxyServer != nil {
		a.proxyServer.SetUpstreamProxy(proxyURL)
	}
	if a.configManager != nil {
		_ = a.configManager.Set("upstreamProxy", proxyURL)
	}
}

// IsUseUpstreamProxy returns whether upstream proxy is enabled
func (a *App) IsUseUpstreamProxy() bool {
	return a.useUpstreamProxy
}

// SetUseUpstreamProxy enables or disables the upstream proxy
func (a *App) SetUseUpstreamProxy(enabled bool) {
	a.useUpstreamProxy = enabled
	if a.proxyServer != nil {
		if enabled && a.upstreamProxy != "" {
			a.proxyServer.SetUpstreamProxy(a.upstreamProxy)
		} else {
			a.proxyServer.SetUpstreamProxy("")
		}
	}
	if a.configManager != nil {
		_ = a.configManager.Set("useUpstreamProxy", enabled)
	}
}

// ==================== Proxy Diagnostics Methods ====================

func (a *App) GetProxyDebug() bool {
	return a.proxyDebug
}

func (a *App) SetProxyDebug(enabled bool) {
	a.proxyDebug = enabled
	if a.proxyServer != nil {
		a.proxyServer.SetDebug(enabled)
	}
	if a.configManager != nil {
		_ = a.configManager.Set("proxyDebug", enabled)
	}
}

func (a *App) GetWeChatNoMITM() bool {
	return a.wechatNoMITM
}

func (a *App) SetWeChatNoMITM(enabled bool) {
	a.wechatNoMITM = enabled
	if a.proxyServer != nil {
		a.proxyServer.SetWeChatNoMITM(enabled)
	}
	if a.configManager != nil {
		_ = a.configManager.Set("wechatNoMITM", enabled)
	}
}
