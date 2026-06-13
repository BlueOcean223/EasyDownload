package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"EasyDownload/assets"
	"EasyDownload/assets/icons"
	"EasyDownload/internal/api"
	"EasyDownload/internal/config"
	"EasyDownload/internal/download"
	"EasyDownload/internal/download/bilibili"
	"EasyDownload/internal/download/douyin"
	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/download/xiaohongshu"
	"EasyDownload/internal/infra/ffmpeg"
	"EasyDownload/internal/infra/logger"
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
	bilibiliDownloader *bilibili.BilibiliDownloader
	douyinParser       *douyin.Parser
	douyinClient       *douyin.Client
	douyinDownloader   *douyin.Downloader
	xhsParser          *xiaohongshu.Parser
	xhsClient          *xiaohongshu.Client
	xhsDownloader      *xiaohongshu.Downloader
	trayManager        *tray.TrayManager
	ffmpegManager      *ffmpeg.FFmpegManager
	configManager      *config.ConfigManager

	// Settings
	proxyPort            int
	apiPort              int
	downloadDir          string
	minimizeToTray       bool
	showNotification     bool
	firstRunComplete     bool
	dontRemindCertWizard bool
	theme                string // "dark" or "light"
	language             string // "zh-CN" or "en-US"
	upstreamProxy        string // Upstream proxy URL
	useUpstreamProxy     bool   // Whether to use upstream proxy

	// Diagnostics
	proxyDebug bool

	// Close behavior
	closeAction    string // "exit", "minimize", or "" (ask)
	dontAskOnClose bool   // Whether to skip the close confirmation dialog
	quitRequested  atomic.Bool
}

// NewApp creates a new App application struct
func NewApp() *App {
	appDataDir := utils.GetAppDataDir()
	downloadDir := utils.GetDownloadDir()

	// Ensure directories exist
	utils.EnsureDir(appDataDir)
	utils.EnsureDir(downloadDir)

	return &App{
		proxyPort:            8899,
		apiPort:              18899,
		downloadDir:          downloadDir,
		minimizeToTray:       true,
		showNotification:     true,
		firstRunComplete:     false,
		dontRemindCertWizard: false,
		theme:                "dark",
		language:             "zh-CN",
		upstreamProxy:        "",
		useUpstreamProxy:     false,
		proxyDebug:           false,
		closeAction:          "",    // Empty means ask user
		dontAskOnClose:       false, // Show dialog by default
	}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Load persisted config (best-effort)
	cfgPath := filepath.Join(utils.GetAppDataDir(), "config.json")
	a.configManager = config.NewConfigManager(cfgPath)
	if err := a.configManager.Load(); err != nil {
		logger.Error("Failed to load config: %v", err)
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
			a.dontRemindCertWizard = cfg.DontRemindCertWizard
			if cfg.Theme != "" {
				a.theme = cfg.Theme
			}
			if cfg.Language != "" {
				a.language = cfg.Language
			}
			a.upstreamProxy = cfg.UpstreamProxy
			a.useUpstreamProxy = cfg.UseUpstreamProxy
			a.proxyDebug = cfg.ProxyDebug
			a.closeAction = cfg.CloseAction
			a.dontAskOnClose = cfg.DontAskOnClose
		}
	}

	if a.proxyDebug {
		logger.GetGlobalLogger().SetLevel(logger.LevelDebug)
	}

	// Initialize certificate manager
	certDir := filepath.Join(utils.GetAppDataDir(), "certs")
	a.certManager = proxy.NewCertManager(certDir)

	// Certificate generation is now manual only
	// Users must install certificate via Welcome Wizard or Settings page

	// Initialize proxy server
	a.proxyServer = proxy.NewProxyServer(a.certManager, a.proxyPort)
	a.proxyServer.SetDebug(a.proxyDebug)

	// Initialize system proxy
	a.systemProxy = proxy.NewSystemProxy()

	// Initialize internal API
	a.internalAPI = api.NewInternalAPI(a.apiPort)
	a.internalAPI.SetVideoCallback(func(video api.DetectedVideo) {
		// Emit event to frontend
		runtime.EventsEmit(a.ctx, "video:detected", video)
	})

	// Initialize download manager and restore persisted task state.
	a.downloadManager = downloader.NewDownloadManager(a.downloadDir, 3)
	a.downloadManager.SetStatePath(filepath.Join(utils.GetAppDataDir(), "downloads.json"))
	if err := a.downloadManager.LoadState(); err != nil {
		logger.Error("Failed to load download state: %v", err)
	}
	a.downloadManager.SetProgressCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:progress", task.TaskToJSON())
	})
	a.downloadManager.SetCompleteCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:complete", task.TaskToJSON())
		// Show notification for download complete
		if a.showNotification && a.trayManager != nil {
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
	a.bilibiliDownloader = bilibili.NewBilibiliDownloader()
	if a.configManager != nil {
		a.bilibiliDownloader.SetConfigManager(a.configManager)
		// Load persisted SESSDATA
		_, _ = a.bilibiliDownloader.LoadSessData()
	}

	// Initialize Douyin downloader
	a.douyinParser = douyin.NewParser()
	a.douyinClient = douyin.NewClient()
	a.douyinDownloader = douyin.NewDownloader()

	// Initialize Xiaohongshu components
	a.xhsParser = xiaohongshu.NewParser()
	a.xhsClient = xiaohongshu.NewClient()
	a.xhsDownloader = xiaohongshu.NewDownloader()

	// Douyin/XHS custom downloader closures cannot be reconstructed safely from
	// persisted state alone. Mark unfinished restored tasks as needing re-parse
	// instead of letting them resume through the generic HTTP downloader.
	a.markUnresumableRestoredCustomTasks()

	// Initialize FFmpeg manager with embedded FFmpeg
	a.ffmpegManager = ffmpeg.NewFFmpegManager()
	ffmpegDir := filepath.Join(utils.GetAppDataDir(), "ffmpeg")
	a.ffmpegManager.SetExtractDir(ffmpegDir)

	// Set FFmpeg manager for Bilibili downloader
	a.bilibiliDownloader.SetFFmpegManager(a.ffmpegManager)

	// Set embedded FS and extract if available
	if assets.HasEmbeddedFFmpeg() {
		ffmpeg.SetEmbeddedFS(assets.FFmpegFS)
		if err := a.ffmpegManager.ExtractEmbedded(); err != nil {
			logger.Error("Failed to extract embedded FFmpeg: %v", err)
		} else {
			logger.Debug("FFmpeg ready at: %s", a.ffmpegManager.GetPath())
			runtime.EventsEmit(a.ctx, "ffmpeg:ready", true)
		}
	}

	// Even if embedded FFmpeg is not available, check if FFmpeg already exists
	// This ensures detection works when FFmpeg was previously extracted
	if ffmpegPath := a.ffmpegManager.GetPath(); ffmpegPath != "" {
		logger.Debug("FFmpeg detected at: %s", ffmpegPath)
		// Cache to config for faster startup next time
		if a.configManager != nil {
			_ = a.configManager.Set("ffmpegPath", ffmpegPath)
		}
		// Ensure frontend is notified if it wasn't the embedded extraction that triggered it
		runtime.EventsEmit(a.ctx, "ffmpeg:ready", true)
	}

	// Set proxy video callback
	a.proxyServer.SetVideoCallback(func(video proxy.VideoInfo) {
		// Convert specs from proxy type to api type
		apiSpecs := make([]api.VideoSpec, len(video.Specs))
		for i, spec := range video.Specs {
			apiSpecs[i] = api.VideoSpec{
				FileFormat: spec.FileFormat,
				Width:      spec.Width,
				Height:     spec.Height,
				DurationMs: spec.DurationMs,
			}
		}

		apiVideo := api.DetectedVideo{
			ID:           video.ID,
			Title:        video.Title,
			Cover:        video.Cover,
			URL:          video.URL,
			Source:       video.Source,
			Quality:      video.Quality,
			Duration:     video.Duration,
			Author:       video.Author,
			AuthorAvatar: video.AuthorAvatar,
			Timestamp:    video.Timestamp,
			DecodeKey:    video.DecodeKey,
			FileSize:     video.FileSize,
			Width:        video.Width,
			Height:       video.Height,
			IsCurrent:    video.IsCurrent,
			FileFormats:  video.FileFormats,
			Specs:        apiSpecs,
		}
		runtime.EventsEmit(a.ctx, "video:detected", apiVideo)
	})

	// Set download callback for one-click download from video page
	a.proxyServer.GetWeChatHandler().SetDownloadCallback(func(video wechat.VideoInfo) {
		logger.Info("Download requested from video page: title=%q id=%q pageKey=%q source=%q",
			video.Title, video.ID, video.PageKey, video.Source,
		)

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
			logger.Error("Failed to add download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"error": err.Error(),
			})
			return
		}

		// Start downloading immediately
		if err := a.downloadManager.StartTask(id); err != nil {
			logger.Error("Failed to start download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"task":  task.TaskToJSON(),
				"error": err.Error(),
			})
			return
		}

		// Notify frontend
		runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

		// Show notification
		if a.showNotification && a.trayManager != nil {
			a.trayManager.ShowNotification("开始下载", video.Title)
		}
	})

	// Start internal API server
	if err := a.internalAPI.Start(); err != nil {
		logger.Error("Failed to start internal API: %v", err)
	}

	// Initialize and start system tray
	a.initTray()

	logger.Debug("App started successfully")
}

// initTray initializes the system tray
func (a *App) initTray() {
	a.trayManager = tray.NewTrayManager()

	// Set icons
	a.trayManager.SetIcon(icons.DefaultIcon)
	a.trayManager.SetActiveIcon(icons.ActiveIcon)

	// Set callbacks
	a.trayManager.SetOnShow(func() {
		a.RestoreFromTray()
	})

	a.trayManager.SetOnSetting(func() {
		// Emit event to frontend to navigate to settings
		runtime.EventsEmit(a.ctx, "navigate:settings")
	})

	a.trayManager.SetOnExit(func() {
		a.RequestQuit()
	})

	// Start tray in background
	a.trayManager.StartAsync()
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	if a.downloadManager != nil {
		if err := a.downloadManager.SaveState(); err != nil {
			logger.Warn("Failed to save download state on shutdown: %v", err)
		}
	}

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

	logger.Debug("App shutdown complete")
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
		logger.Warn("Failed to disable system proxy: %v", err)
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
	// Generate CA certificate if not exists
	if !a.certManager.CertExists() {
		logger.Info("Generating CA certificate...")
		if err := a.certManager.GenerateCACert(); err != nil {
			return fmt.Errorf("failed to generate CA certificate: %w", err)
		}
	}

	err := a.certManager.InstallCert()
	if err == nil {
		// Cache the installation status immediately
		if a.configManager != nil {
			_ = a.configManager.Set("certInstalled", true)
		}
	}
	return err
}

// UninstallCert removes the CA certificate from the system trust store
func (a *App) UninstallCert() error {
	err := a.certManager.UninstallCert()
	if err == nil {
		// Cache the uninstallation status immediately
		if a.configManager != nil {
			_ = a.configManager.Set("certInstalled", false)
		}
	}
	return err
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

func (a *App) markUnresumableRestoredCustomTasks() {
	if a.downloadManager == nil {
		return
	}
	changed := false
	for _, task := range a.downloadManager.GetAllTasks() {
		status := task.GetStatus()
		if status == downloader.StatusCompleted || status == downloader.StatusCancelled {
			continue
		}
		source := strings.ToLower(strings.TrimSpace(task.Source))
		if (source == "douyin" || source == "xiaohongshu") && task.GetCustomDownloader() == nil {
			task.SetStatus(downloader.StatusFailed)
			task.SetError("应用重启后需要重新解析后再下载")
			changed = true
		}
	}
	if changed {
		if err := a.downloadManager.SaveState(); err != nil {
			logger.Warn("Failed to save restored custom task state: %v", err)
		}
	}
}

func (a *App) ensureBilibiliTaskDownloader(task *downloader.DownloadTask) error {
	if task == nil || strings.ToLower(strings.TrimSpace(task.Source)) != "bilibili" || task.GetCustomDownloader() != nil {
		return nil
	}

	quality := 80
	fmt.Sscanf(task.Quality, "%d", &quality)

	partIndex := parseBilibiliTaskPartIndex(task.ID)
	var video *bilibili.BilibiliVideo
	var err error
	if partIndex >= 0 {
		video, err = a.getBilibiliVideoInfoWithPartsForDownload(task.URL)
	} else {
		video, err = a.GetBilibiliVideoInfo(task.URL)
		if err == nil && video.IsBangumi {
			partIndex = video.CurrentPartIndex
		}
	}
	if err != nil {
		return fmt.Errorf("failed to get Bilibili video info: %w", err)
	}
	task.SetCustomDownloader(a.createBilibiliDownloader(video, quality, partIndex))
	return nil
}

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
	task, err := a.downloadManager.GetTask(id)
	if err != nil {
		return err
	}

	// For Bilibili tasks, ensure custom downloader is set (may be lost after app restart).
	if err := a.ensureBilibiliTaskDownloader(task); err != nil {
		return err
	}

	if task.GetCustomDownloader() == nil {
		source := strings.ToLower(strings.TrimSpace(task.Source))
		if source == "douyin" || source == "xiaohongshu" {
			return fmt.Errorf("%s 任务在应用重启后需要重新解析后再下载", task.Source)
		}
	}

	// Use unified resume logic for all sources
	return a.downloadManager.ResumeTask(id)
}

// CancelDownload cancels a download
func (a *App) CancelDownload(id string) error {
	return a.downloadManager.CancelTask(id)
}

// RetryDownload retries a failed download task.
func (a *App) RetryDownload(id string) error {
	task, err := a.downloadManager.GetTask(id)
	if err != nil {
		return err
	}
	if err := a.ensureBilibiliTaskDownloader(task); err != nil {
		return err
	}
	if task.GetCustomDownloader() == nil {
		source := strings.ToLower(strings.TrimSpace(task.Source))
		if source == "douyin" || source == "xiaohongshu" {
			return fmt.Errorf("%s 任务在应用重启后需要重新解析后再下载", task.Source)
		}
	}
	return a.downloadManager.RetryTask(id)
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

// GetBilibiliVideoInfo fetches video info from Bilibili.
// It supports ordinary BV/av videos and PGC/bangumi ep/ss/md URLs.
func (a *App) GetBilibiliVideoInfo(rawURL string) (*bilibili.BilibiliVideo, error) {
	if bvid, err := a.bilibiliDownloader.ParseURL(rawURL); err == nil {
		return a.bilibiliDownloader.GetVideoInfo(bvid)
	}

	kind, id, err := a.bilibiliDownloader.ParseBangumiURL(rawURL)
	if err != nil {
		return nil, err
	}
	return a.bilibiliDownloader.GetBangumiInfoByID(kind, id)
}

// GetBilibiliVideoInfoWithAllParts fetches video info with stream info for all ordinary-video parts.
// For bangumi URLs it returns the full season episode list but only fetches streams for the current
// episode to avoid issuing hundreds of playurl requests for long seasons.
func (a *App) GetBilibiliVideoInfoWithAllParts(rawURL string) (*bilibili.BilibiliVideo, error) {
	if bvid, err := a.bilibiliDownloader.ParseURL(rawURL); err == nil {
		video, err := a.bilibiliDownloader.GetVideoInfoWithParts(bvid)
		if err != nil {
			return nil, err
		}

		// Get stream info for all parts
		if err := a.bilibiliDownloader.GetAllPartsStreams(video); err != nil {
			logger.Debug("Failed to get all parts streams: %v", err)
			// Continue anyway, streams will be empty for failed parts
		}

		// Also get streams for the first part to maintain backward compatibility
		if len(video.Parts) > 0 {
			streams, err := a.bilibiliDownloader.GetPartStreams(video, 0)
			if err == nil {
				video.Streams = streams
			}
		}

		return video, nil
	}

	kind, id, err := a.bilibiliDownloader.ParseBangumiURL(rawURL)
	if err != nil {
		return nil, err
	}
	return a.bilibiliDownloader.GetBangumiInfoByID(kind, id)
}

func (a *App) getBilibiliVideoInfoWithPartsForDownload(rawURL string) (*bilibili.BilibiliVideo, error) {
	if bvid, err := a.bilibiliDownloader.ParseURL(rawURL); err == nil {
		return a.bilibiliDownloader.GetVideoInfoWithParts(bvid)
	}

	kind, id, err := a.bilibiliDownloader.ParseBangumiURL(rawURL)
	if err != nil {
		return nil, err
	}
	return a.bilibiliDownloader.GetBangumiInfoByID(kind, id)
}

// DownloadBilibiliVideo downloads a Bilibili video or the current bangumi episode.
func (a *App) DownloadBilibiliVideo(rawURL string, quality int) (string, error) {
	video, err := a.GetBilibiliVideoInfo(rawURL)
	if err != nil {
		return "", err
	}

	partIndex := -1
	title := video.Title
	cover := video.Cover
	idKey := video.BV
	if video.IsBangumi {
		partIndex = video.CurrentPartIndex
		if partIndex < 0 || partIndex >= len(video.Parts) {
			partIndex = 0
		}
		part := video.Parts[partIndex]
		title = formatBilibiliTaskTitle(video, part)
		cover = formatBilibiliTaskCover(video, part)
		idKey = formatBilibiliTaskIDKey(video, part)
	}

	// Create unique ID. Bangumi uses an explicit part marker so resume can recreate the same episode.
	id := fmt.Sprintf("bilibili_%s_%d", idKey, time.Now().UnixNano())
	if partIndex >= 0 {
		id = fmt.Sprintf("bilibili_%s_p%d_%d", idKey, video.Parts[partIndex].Page, time.Now().UnixNano())
	}

	// Create custom downloader function for Bilibili DASH format
	bilibiliDownloader := a.createBilibiliDownloader(video, quality, partIndex)

	// Add task with custom downloader
	task, err := a.downloadManager.AddTaskWithDownloader(id, rawURL, title, cover, "bilibili", fmt.Sprintf("%d", quality), bilibiliDownloader)
	if err != nil {
		return "", err
	}

	// Start download via DownloadManager (handles progress, completion, retry automatically)
	if err := a.downloadManager.StartTask(id); err != nil {
		return "", err
	}

	// Emit start event for frontend
	runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

	return id, nil
}

// DownloadBilibiliPart downloads a specific part/episode of a Bilibili video.
func (a *App) DownloadBilibiliPart(rawURL string, partIndex int, quality int) (string, error) {
	video, err := a.getBilibiliVideoInfoWithPartsForDownload(rawURL)
	if err != nil {
		return "", err
	}

	if partIndex < 0 || partIndex >= len(video.Parts) {
		return "", fmt.Errorf("invalid part index: %d", partIndex)
	}

	part := video.Parts[partIndex]

	// Create unique ID with part info
	id := fmt.Sprintf("bilibili_%s_p%d_%d", formatBilibiliTaskIDKey(video, part), part.Page, time.Now().UnixNano())

	// Create title with part info
	title := formatBilibiliTaskTitle(video, part)
	cover := formatBilibiliTaskCover(video, part)

	// Create custom downloader function for this specific part
	bilibiliDownloader := a.createBilibiliDownloader(video, quality, partIndex)

	// Add task with custom downloader
	task, err := a.downloadManager.AddTaskWithDownloader(id, rawURL, title, cover, "bilibili", fmt.Sprintf("%d", quality), bilibiliDownloader)
	if err != nil {
		return "", err
	}

	// Start download via DownloadManager
	if err := a.downloadManager.StartTask(id); err != nil {
		return "", err
	}

	// Emit start event for frontend
	runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())

	return id, nil
}

func formatBilibiliTaskTitle(video *bilibili.BilibiliVideo, part bilibili.BilibiliPart) string {
	if video == nil {
		return part.PartName
	}
	if video.IsBangumi {
		if video.Title == "" {
			return part.PartName
		}
		if part.PartName == "" {
			return video.Title
		}
		return fmt.Sprintf("%s - %s", video.Title, part.PartName)
	}
	if len(video.Parts) > 1 {
		return fmt.Sprintf("%s - P%d %s", video.Title, part.Page, part.PartName)
	}
	return video.Title
}

func formatBilibiliTaskCover(video *bilibili.BilibiliVideo, part bilibili.BilibiliPart) string {
	if part.Cover != "" {
		return part.Cover
	}
	if video != nil {
		return video.Cover
	}
	return ""
}

func formatBilibiliTaskIDKey(video *bilibili.BilibiliVideo, part bilibili.BilibiliPart) string {
	if video != nil && video.IsBangumi {
		if part.EpID > 0 {
			return fmt.Sprintf("ep%d", part.EpID)
		}
		if part.BV != "" {
			return part.BV
		}
	}
	if video != nil && video.BV != "" {
		return video.BV
	}
	if part.BV != "" {
		return part.BV
	}
	return "unknown"
}

func parseBilibiliTaskPartIndex(id string) int {
	marker := strings.LastIndex(id, "_p")
	if marker < 0 {
		return -1
	}
	rest := id[marker+2:]
	if sep := strings.Index(rest, "_"); sep >= 0 {
		rest = rest[:sep]
	}
	page, err := strconv.Atoi(rest)
	if err != nil || page <= 0 {
		return -1
	}
	return page - 1
}

// createBilibiliDownloader creates a custom download function for Bilibili videos
// partIndex: -1 for first part (single video), >= 0 for specific part
func (a *App) createBilibiliDownloader(video *bilibili.BilibiliVideo, quality int, partIndex int) downloader.DownloadFunc {
	return func(ctx context.Context, task *downloader.DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error {
		var outputPath string
		var downloadErr error

		// Track progress - Bilibili reports percentage, we convert to bytes
		var totalSize int64 = 0

		progressCallback := func(progress float64) {
			if totalSize > 0 {
				downloaded := int64(progress / 100 * float64(totalSize))
				onProgress(downloaded, totalSize)
			}
		}

		sizeCallback := func(size int64) {
			totalSize = size
			onProgress(0, size)
		}

		// 使用 task.FilePath 作为输出路径，确保临时文件名与最终文件名一致
		if partIndex < 0 {
			// Download first/single part
			outputPath, downloadErr = a.bilibiliDownloader.DownloadWithContext(ctx, video, quality, task.FilePath, progressCallback, sizeCallback)
		} else {
			// Download specific part
			outputPath, downloadErr = a.bilibiliDownloader.DownloadPartWithContext(ctx, video, partIndex, quality, task.FilePath, progressCallback, sizeCallback)
		}

		if downloadErr != nil {
			return downloadErr
		}

		onComplete(outputPath)
		return nil
	}
}

// SetBilibiliSessData sets the Bilibili session cookie
func (a *App) SetBilibiliSessData(sessData string) error {
	return a.bilibiliDownloader.SaveSessData(sessData)
}

// HasBilibiliSessData checks if Bilibili SESSDATA exists (without exposing the value)
func (a *App) HasBilibiliSessData() bool {
	sessData, _ := a.bilibiliDownloader.LoadSessData()
	return sessData != ""
}

// GetBilibiliQRCode generates a QR code for Bilibili login
func (a *App) GetBilibiliQRCode() (*bilibili.BilibiliQRCode, error) {
	logger.Info("API call: GetBilibiliQRCode")
	qr, err := a.bilibiliDownloader.GetQRCode()
	if err != nil {
		logger.Warn("GetBilibiliQRCode failed: %v", err)
	}
	return qr, err
}

// PollBilibiliQRCode checks the QR code scan status
func (a *App) PollBilibiliQRCode(qrcodeKey string) (*bilibili.BilibiliLoginStatus, error) {
	logger.Debug("API call: PollBilibiliQRCode")
	status, err := a.bilibiliDownloader.PollQRCodeStatus(qrcodeKey)
	if err != nil {
		logger.Debug("PollBilibiliQRCode error: %v", err)
		return nil, err
	}
	if status.Code == 0 {
		logger.Info("Bilibili login successful via QR code")
	}
	return status, nil
}

// GetBilibiliUserInfo gets the current logged in user info
func (a *App) GetBilibiliUserInfo() (*bilibili.BilibiliUserInfo, error) {
	logger.Debug("API call: GetBilibiliUserInfo")
	return a.bilibiliDownloader.GetUserInfo()
}

// BilibiliLogout clears the saved SESSDATA
func (a *App) BilibiliLogout() error {
	logger.Info("API call: BilibiliLogout")
	err := a.bilibiliDownloader.Logout()
	if err != nil {
		logger.Warn("BilibiliLogout failed: %v", err)
	} else {
		logger.Info("Bilibili logout successful")
	}
	return err
}

// ==================== Douyin Methods ====================

// GetDouyinVideoInfo fetches video or album info from Douyin share text
func (a *App) GetDouyinVideoInfo(shareText string) (*douyin.DouyinItem, error) {
	awemeID, err := a.douyinParser.Parse(shareText)
	if err != nil {
		return nil, err
	}
	return a.douyinClient.GetItemInfo(awemeID)
}

// DownloadDouyinVideo downloads a Douyin video or album
func (a *App) DownloadDouyinVideo(shareText string, qualityKey string) (string, error) {
	item, err := a.GetDouyinVideoInfo(shareText)
	if err != nil {
		return "", err
	}

	id := fmt.Sprintf("douyin_%s_%d", item.ID, time.Now().UnixNano())

	douyinDownloadFunc := a.createDouyinDownloader(item, qualityKey)
	task, err := a.downloadManager.AddTaskWithDownloader(id, shareText, item.Title, item.Cover, "douyin", qualityKey, douyinDownloadFunc)
	if err != nil {
		return "", err
	}

	// Update file extension and album fields for album type
	if item.Type == "album" || len(item.Images) > 0 {
		task.IsAlbum = true
		task.AlbumTotal = len(item.Images)
		task.AlbumCompleted = 0
		if task.FileName != "" {
			ext := filepath.Ext(task.FileName)
			base := task.FileName
			if ext != "" {
				base = task.FileName[:len(task.FileName)-len(ext)]
			}
			task.FileName = base + ".zip"
			task.FilePath = filepath.Join(a.downloadDir, task.FileName)
		}
	}

	if err := a.downloadManager.StartTask(id); err != nil {
		return "", err
	}

	runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())
	return id, nil
}

func (a *App) createDouyinDownloader(item *douyin.DouyinItem, qualityKey string) downloader.DownloadFunc {
	base := a.douyinDownloader.BuildDownloadFunc(item, qualityKey, a.downloadDir)
	return func(ctx context.Context, task *downloader.DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error {
		return base(ctx, task, onProgress, func(outputPath string) {
			if task != nil && outputPath != "" {
				task.FilePath = outputPath
				task.FileName = filepath.Base(outputPath)
			}
			if onComplete != nil {
				onComplete(outputPath)
			}
		})
	}
}

// DownloadDouyinAlbumPartial downloads a subset of album images (0-based indices) into a ZIP.
func (a *App) DownloadDouyinAlbumPartial(shareText string, indices []int) (string, error) {
	item, err := a.GetDouyinVideoInfo(shareText)
	if err != nil {
		return "", err
	}
	if item == nil {
		return "", fmt.Errorf("nil douyin item")
	}
	if len(indices) == 0 {
		return "", fmt.Errorf("empty indices")
	}
	if len(item.Images) == 0 {
		return "", douyin.ErrNoImages
	}

	seen := make(map[int]struct{}, len(indices))
	unique := make([]int, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(item.Images) {
			return "", fmt.Errorf("index out of range: %d", idx)
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		unique = append(unique, idx)
	}
	if len(unique) == 0 {
		return "", fmt.Errorf("empty indices")
	}

	id := fmt.Sprintf("douyin_%s_partial_%d", item.ID, time.Now().UnixNano())

	douyinDownloadFunc := a.createDouyinAlbumPartialDownloader(item, unique)
	task, err := a.downloadManager.AddTaskWithDownloader(id, shareText, item.Title, item.Cover, "douyin", "partial", douyinDownloadFunc)
	if err != nil {
		return "", err
	}

	// Set album fields and force ZIP output
	task.IsAlbum = true
	task.AlbumTotal = len(unique)
	task.AlbumCompleted = 0
	if task.FileName != "" {
		ext := filepath.Ext(task.FileName)
		base := task.FileName
		if ext != "" {
			base = task.FileName[:len(task.FileName)-len(ext)]
		}
		task.FileName = base + ".zip"
		task.FilePath = filepath.Join(a.downloadDir, task.FileName)
	}

	if err := a.downloadManager.StartTask(id); err != nil {
		return "", err
	}

	runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())
	return id, nil
}

func (a *App) createDouyinAlbumPartialDownloader(item *douyin.DouyinItem, indices []int) downloader.DownloadFunc {
	indicesCopy := append([]int(nil), indices...)
	return func(ctx context.Context, task *downloader.DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error {
		if item == nil {
			return fmt.Errorf("nil douyin item")
		}

		destPath := filepath.Join(a.downloadDir, fmt.Sprintf("%s.zip", item.ID))
		if task != nil && task.FilePath != "" {
			destPath = task.FilePath
			ext := filepath.Ext(destPath)
			if ext == "" {
				destPath = destPath + ".zip"
			} else if ext != ".zip" {
				destPath = destPath[:len(destPath)-len(ext)] + ".zip"
			}
		}

		total := int64(len(indicesCopy))
		progressCallback := func(p float64) {
			completedCount := int(p/100*float64(total) + 0.5)
			if completedCount > int(total) {
				completedCount = int(total)
			}
			if task != nil {
				task.AlbumCompleted = completedCount
				task.Progress = p // Update progress percentage
			}
		}

		if err := a.douyinDownloader.DownloadAlbumPartialWithBytes(ctx, item, indicesCopy, destPath, progressCallback, onProgress); err != nil {
			return err
		}

		if task != nil {
			task.AlbumCompleted = int(total)
			task.Progress = 100
			task.FilePath = destPath
			task.FileName = filepath.Base(destPath)
		}
		if onComplete != nil {
			onComplete(destPath)
		}
		return nil
	}
}

// ==================== Xiaohongshu Methods ====================

// GetXHSNoteInfo parses input into noteID and fetches full note info.
func (a *App) GetXHSNoteInfo(input string) (*xiaohongshu.XHSItem, error) {
	logger.Info("API call: GetXHSNoteInfo")
	if a.xhsParser == nil || a.xhsClient == nil {
		return nil, fmt.Errorf("xiaohongshu components not initialized")
	}

	// Use ParseWithURL to get both noteID and xsec_token
	result, err := a.xhsParser.ParseWithURL(input)
	if err != nil {
		logger.Warn("GetXHSNoteInfo parse failed: %v", err)
		return nil, err
	}

	// Use GetNoteInfoWithToken to include xsec_token if available
	item, err := a.xhsClient.GetNoteInfoWithToken(result.NoteID, result.XsecToken)
	if err != nil {
		logger.Warn("GetXHSNoteInfo fetch failed: %v", err)
		return nil, err
	}
	return item, nil
}

// DownloadXHSNote creates a download task and starts downloading the note content.
// selectedImages: 0-based indices; empty means all images.
// quality: video stream quality key (used for video notes).
func (a *App) DownloadXHSNote(item *xiaohongshu.XHSItem, selectedImages []int, quality string, saveDir string) error {
	logger.Info("API call: DownloadXHSNote")
	if a.downloadManager == nil || a.xhsDownloader == nil {
		return fmt.Errorf("download manager not initialized")
	}
	if item == nil {
		return fmt.Errorf("nil xiaohongshu item")
	}
	if item.ID == "" {
		return fmt.Errorf("empty xiaohongshu note id")
	}

	if item.IsAlbum() {
		if err := item.ValidateSelectedImages(selectedImages); err != nil {
			logger.Warn("DownloadXHSNote invalid selection: %v", err)
			return err
		}
	}

	outputDir := a.downloadDir
	if saveDir != "" {
		utils.EnsureDir(saveDir)
		outputDir = saveDir
	}

	id := fmt.Sprintf("xiaohongshu_%s_%d", item.ID, time.Now().UnixNano())
	downloadFunc := a.xhsDownloader.BuildDownloadFunc(item, selectedImages, quality, outputDir)
	task, err := a.downloadManager.AddTaskWithDownloader(id, item.ID, item.Title, item.Cover, "xiaohongshu", quality, downloadFunc)
	if err != nil {
		logger.Warn("DownloadXHSNote add task failed: %v", err)
		return err
	}

	// Configure album fields and force ZIP output for image notes.
	if item.IsAlbum() {
		task.IsAlbum = true
		task.AlbumTotal = item.SelectedCount(selectedImages)
		task.AlbumCompleted = 0
		if task.FileName != "" {
			ext := filepath.Ext(task.FileName)
			base := task.FileName
			if ext != "" {
				base = task.FileName[:len(task.FileName)-len(ext)]
			}
			task.FileName = base + ".zip"
		}
	}

	if outputDir != "" && task.FileName != "" {
		task.FilePath = filepath.Join(outputDir, task.FileName)
	}

	if err := a.downloadManager.StartTask(id); err != nil {
		logger.Warn("DownloadXHSNote start task failed: %v", err)
		return err
	}

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "download:start", task.TaskToJSON())
	}
	return nil
}

// IsFFmpegAvailable checks if ffmpeg is available and caches the path
func (a *App) IsFFmpegAvailable() bool {
	available := a.bilibiliDownloader.IsFFmpegAvailable()
	// Cache the FFmpeg path to config for faster startup next time
	if available && a.configManager != nil && a.ffmpegManager != nil {
		path := a.ffmpegManager.GetPath()
		if path != "" {
			_ = a.configManager.Set("ffmpegPath", path)
		}
	}
	return available
}

// InstallFFmpeg installs or discovers a usable FFmpeg binary and returns its path.
func (a *App) InstallFFmpeg() (string, error) {
	if a.ffmpegManager == nil {
		return "", fmt.Errorf("ffmpeg manager not initialized")
	}

	path, err := a.ffmpegManager.EnsureAvailable()
	if err != nil {
		return "", err
	}

	if a.configManager != nil {
		_ = a.configManager.Set("ffmpegPath", path)
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ffmpeg:ready", true)
	}
	return path, nil
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
	if a.configManager != nil {
		_ = a.configManager.Set("downloadDir", dir)
	}
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
	ffmpegPath := ""
	if a.ffmpegManager != nil {
		ffmpegPath = a.ffmpegManager.GetPath()
	}
	apiToken := ""
	if a.internalAPI != nil {
		apiToken = a.internalAPI.GetToken()
	}
	certPath := ""
	if a.certManager != nil {
		certPath = a.certManager.GetCertPath()
	}

	return map[string]interface{}{
		"version":              "1.0.0",
		"proxyPort":            a.proxyPort,
		"apiPort":              a.apiPort,
		"apiToken":             apiToken,
		"downloadDir":          a.downloadDir,
		"ffmpegPath":           ffmpegPath,
		"certPath":             certPath,
		"minimizeToTray":       a.minimizeToTray,
		"showNotification":     a.showNotification,
		"firstRunComplete":     a.firstRunComplete,
		"dontRemindCertWizard": a.dontRemindCertWizard,
		"theme":                a.theme,
		"language":             a.language,
		"upstreamProxy":        a.upstreamProxy,
		"useUpstreamProxy":     a.useUpstreamProxy,
		"proxyDebug":           a.proxyDebug,
		"closeAction":          a.closeAction,
		"dontAskOnClose":       a.dontAskOnClose,
	}
}

// ==================== Tray Methods ====================

// MinimizeToTray minimizes the window to system tray
func (a *App) MinimizeToTray() {
	if a.ctx == nil {
		return
	}
	a.applyMinimizeToTray(a.ctx)
}

func (a *App) applyMinimizeToTray(ctx context.Context) {
	if a.trayManager == nil || !a.trayManager.IsSupported() {
		runtime.WindowMinimise(ctx)
		return
	}
	runtime.WindowHide(ctx)
}

// RestoreFromTray restores the window from system tray
func (a *App) RestoreFromTray() {
	if a.ctx == nil {
		return
	}
	a.applyRestoreFromTray(a.ctx)
}

func (a *App) applyRestoreFromTray(ctx context.Context) {
	runtime.WindowShow(ctx)
}

// SetMinimizeToTray sets whether to minimize to tray on close
func (a *App) SetMinimizeToTray(enabled bool) {
	a.minimizeToTray = enabled
	if a.configManager != nil {
		_ = a.configManager.Set("minimizeToTray", enabled)
	}
}

// SetShowNotification sets whether to show notifications
func (a *App) SetShowNotification(enabled bool) {
	a.showNotification = enabled
	if a.configManager != nil {
		_ = a.configManager.Set("showNotification", enabled)
	}
}

// SetFirstRunComplete marks first run setup as complete
func (a *App) SetFirstRunComplete(complete bool) {
	a.firstRunComplete = complete
	if a.configManager != nil {
		_ = a.configManager.Set("firstRunComplete", complete)
	}
}

// IsDontRemindCertWizard returns whether to suppress the certificate welcome wizard prompt
func (a *App) IsDontRemindCertWizard() bool {
	return a.dontRemindCertWizard
}

// SetDontRemindCertWizard sets whether to suppress the certificate welcome wizard prompt
func (a *App) SetDontRemindCertWizard(dontRemind bool) {
	a.dontRemindCertWizard = dontRemind
	if a.configManager != nil {
		_ = a.configManager.Set("dontRemindCertWizard", dontRemind)
	}
}

// ==================== Log Methods ====================

// OpenLogDir opens the log directory in file explorer
func (a *App) OpenLogDir() error {
	logDir := logger.GetGlobalLogger().GetLogDir()
	return utils.OpenFolder(logDir)
}

// GetLogDir returns the log directory path
func (a *App) GetLogDir() string {
	return logger.GetGlobalLogger().GetLogDir()
}

// GetLogSize returns the total size of all log files in bytes
func (a *App) GetLogSize() int64 {
	size, err := logger.GetGlobalLogger().GetTotalLogSize()
	if err != nil {
		logger.Warn("Failed to get log size: %v", err)
		return 0
	}
	return size
}

// ClearLogs removes all log files and recreates the current log file
func (a *App) ClearLogs() error {
	err := logger.GetGlobalLogger().ClearAllLogs()
	if err != nil {
		logger.Error("Failed to clear logs: %v", err)
		return err
	}
	logger.Info("Logs cleared by user")
	return nil
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
	if a.configManager != nil {
		_ = a.configManager.Set("theme", theme)
	}
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
	if a.configManager != nil {
		_ = a.configManager.Set("language", lang)
	}
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

// ==================== Frontend Logging ====================

// LogFrontend logs a message from the frontend to the persistent log file
func (a *App) LogFrontend(message string) {
	logger.Info("[Frontend] %s", message)
}

// ==================== Close Behavior Methods ====================

// GetCloseAction returns the current close action setting
func (a *App) GetCloseAction() string {
	return a.closeAction
}

// SetCloseAction sets the close action ("exit", "minimize", or "" for ask)
func (a *App) SetCloseAction(action string) error {
	if action != "" && action != "exit" && action != "minimize" {
		return fmt.Errorf("invalid close action: must be '', 'exit', or 'minimize'")
	}
	a.closeAction = action
	if a.configManager != nil {
		_ = a.configManager.Set("closeAction", action)
	}
	return nil
}

// IsDontAskOnClose returns whether to skip the close confirmation dialog
func (a *App) IsDontAskOnClose() bool {
	return a.dontAskOnClose
}

// SetDontAskOnClose sets whether to skip the close confirmation dialog
func (a *App) SetDontAskOnClose(dontAsk bool) {
	a.dontAskOnClose = dontAsk
	if a.configManager != nil {
		_ = a.configManager.Set("dontAskOnClose", dontAsk)
	}
}

// RequestClose is called from frontend when user confirms close action
// action: "exit" to quit, "minimize" to minimize to tray
func (a *App) RequestClose(action string) {
	switch action {
	case "exit":
		a.RequestQuit()
	case "minimize":
		a.MinimizeToTray()
	}
}

// RequestQuit requests the app to quit immediately without showing the close dialog.
func (a *App) RequestQuit() {
	a.quitRequested.Store(true)
	if a.ctx == nil {
		return
	}
	runtime.Quit(a.ctx)
}

func (a *App) IsQuitRequested() bool {
	return a.quitRequested.Load()
}

// ==================== Admin Methods ====================

// IsAdmin checks if the current process has administrator privileges
func (a *App) IsAdmin() bool {
	return utils.IsAdmin()
}

// RestartAsAdmin restarts the application with administrator privileges.
// Returns nil on success. The backend will call RequestQuit() automatically,
// so the frontend does not need to close the app explicitly.
func (a *App) RestartAsAdmin() error {
	err := utils.RestartAsAdmin()
	if err != nil {
		return err
	}
	// Request quit after successfully launching elevated process
	a.RequestQuit()
	return nil
}

// CanRestartAsAdmin returns true if the platform supports restarting with admin privileges.
func (a *App) CanRestartAsAdmin() bool {
	return utils.CanRestartAsAdmin()
}
