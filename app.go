package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"EasyDownload/assets"
	"EasyDownload/assets/icons"
	"EasyDownload/internal/api"
	"EasyDownload/internal/config"
	"EasyDownload/internal/detection"
	"EasyDownload/internal/detection/wechatadapter"
	"EasyDownload/internal/download"
	"EasyDownload/internal/download/bilibili"
	"EasyDownload/internal/download/douyin"
	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/download/xiaohongshu"
	"EasyDownload/internal/infra/ffmpeg"
	"EasyDownload/internal/infra/logger"
	"EasyDownload/internal/proxy"
	"EasyDownload/internal/settings"
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
	detectionStore     detection.Store
	settingsModule     *settings.Module
	settingsRuntimeMu  sync.Mutex

	// Close behavior
	quitRequested atomic.Bool
}

type appFFmpegLocator struct{ manager *ffmpeg.FFmpegManager }

func (locator appFFmpegLocator) Locate(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if locator.manager == nil {
		return "", fmt.Errorf("ffmpeg manager is not initialized")
	}
	path := strings.TrimSpace(locator.manager.GetPath())
	if path == "" {
		return "", fmt.Errorf("ffmpeg is not available")
	}
	return path, nil
}

// NewApp creates a new App application struct
func NewApp() *App {
	appDataDir := utils.GetAppDataDir()
	downloadDir := utils.GetDownloadDir()

	// Ensure directories exist
	utils.EnsureDir(appDataDir)
	utils.EnsureDir(downloadDir)

	return &App{}
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
	a.settingsModule = settings.NewModule(a.configManager, appSettingsEffectPlanner{app: a})
	cfg := a.currentConfig()

	if cfg.ProxyDebug {
		logger.GetGlobalLogger().SetLevel(logger.LevelDebug)
	}

	// Initialize certificate manager
	certDir := filepath.Join(utils.GetAppDataDir(), "certs")
	a.certManager = proxy.NewCertManager(certDir)

	// Certificate generation is now manual only
	// Users must install certificate via Welcome Wizard or Settings page

	// Initialize proxy server
	a.proxyServer = proxy.NewProxyServer(a.certManager, cfg.ProxyPort)
	a.proxyServer.SetDebug(cfg.ProxyDebug)
	if cfg.UseUpstreamProxy && cfg.UpstreamProxy != "" {
		a.proxyServer.SetUpstreamProxy(cfg.UpstreamProxy)
	}

	// Initialize system proxy
	a.systemProxy = proxy.NewSystemProxy()

	// Initialize the session-scoped detection store and API adapter.
	a.detectionStore = detection.NewMemoryStore(100)
	a.internalAPI = api.NewInternalAPI(cfg.APIPort, a.detectionStore)
	a.internalAPI.SetVideoCallback(func(change detection.Change) {
		runtime.EventsEmit(a.ctx, "video:detected", change.Public())
	})

	// Initialize Bilibili downloader
	a.bilibiliDownloader = bilibili.NewBilibiliDownloader(bilibili.NewAPIHTTPClient())
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

	// Initialize FFmpeg manager with embedded FFmpeg
	a.ffmpegManager = ffmpeg.NewFFmpegManager()
	ffmpegDir := filepath.Join(utils.GetAppDataDir(), "ffmpeg")
	a.ffmpegManager.SetExtractDir(ffmpegDir)

	// Set FFmpeg manager for Bilibili downloader
	a.bilibiliDownloader.SetFFmpegManager(a.ffmpegManager)

	// Initialize download manager and restore persisted task state.
	a.downloadManager = downloader.NewDownloadManager(cfg.DownloadDir, cfg.MaxConcurrent)
	// The composition root owns one shared transport capability. Platform
	// adapters can only obtain it through TaskExecutionContext.
	a.downloadManager.SetExecutionCapabilities(fetch.New(nil), appFFmpegLocator{manager: a.ffmpegManager}, nil)
	for _, entry := range []struct {
		name    string
		adapter downloadtask.PlatformAdapter
	}{
		{name: "generic", adapter: downloader.NewGenericAdapter()},
		{name: "wechat", adapter: wechat.NewAdapter()},
		{name: "bilibili", adapter: bilibili.NewAdapter(a.bilibiliDownloader)},
		{name: "douyin", adapter: douyin.NewAdapter(a.douyinDownloader)},
		{name: "xiaohongshu", adapter: xiaohongshu.NewAdapter(a.xhsDownloader)},
	} {
		if err := a.downloadManager.RegisterPlatformAdapter(entry.adapter); err != nil {
			logger.Warn("Failed to register %s download adapter: %v", entry.name, err)
		}
	}
	a.downloadManager.SetStatePath(filepath.Join(utils.GetAppDataDir(), "downloads.json"))
	if err := a.downloadManager.LoadState(); err != nil {
		logger.Error("Failed to load download state: %v", err)
	}
	a.downloadManager.SetProgressCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:progress", a.downloadManager.PublicTaskSnapshot(task))
	})
	a.downloadManager.SetCompleteCallback(func(task *downloader.DownloadTask) {
		runtime.EventsEmit(a.ctx, "download:complete", a.downloadManager.PublicTaskSnapshot(task))
		// Show notification for download complete
		if a.notificationsEnabled() && a.trayManager != nil {
			a.trayManager.ShowNotification("下载完成", task.Title)
		}
	})
	a.downloadManager.SetErrorCallback(func(task *downloader.DownloadTask, err error) {
		publicTask := a.downloadManager.PublicTaskSnapshot(task)
		runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
			"task":  publicTask,
			"error": publicTask.Error,
		})
	})
	a.downloadManager.SetStopEventCallback(func(event downloader.StopEvent) {
		runtime.EventsEmit(a.ctx, "download:lifecycle", event)
	})

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
		a.updateRuntimeMetadataBestEffort(config.RuntimeMetadataPatch{FFmpegPath: &ffmpegPath})
		// Ensure frontend is notified if it wasn't the embedded extraction that triggered it
		runtime.EventsEmit(a.ctx, "ffmpeg:ready", true)
	}

	// Set proxy video callback
	a.proxyServer.SetVideoCallback(func(video wechat.VideoInfo) {
		detected := wechatadapter.FromVideoInfo(video, time.Now())
		if _, err := a.internalAPI.Ingest(a.ctx, detected); err != nil {
			logger.Warn("Failed to store detected video: %v", err)
		}
	})

	// Set download callback for one-click download from video page
	a.proxyServer.GetWeChatHandler().SetDownloadCallback(func(video wechat.VideoInfo) {
		logger.Info("Download requested from video page: title=%q id=%q pageKey=%q source=%q",
			video.Title, video.ID, video.PageKey, video.Source,
		)

		platformData, err := wechat.MarshalPlatformData(
			video.URL,
			map[string]string{"Referer": "https://channels.weixin.qq.com/"},
			video.DecodeKey,
			"",
		)
		if err != nil {
			logger.Error("Failed to prepare download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"error": "无法创建微信下载任务",
			})
			return
		}
		task, err := a.createAndStartDownload(downloader.TaskCreationInput{
			ID: fmt.Sprintf("wechat_%d", time.Now().UnixNano()), PlatformID: downloadtask.PlatformWeChat,
			Title: video.Title, Cover: video.CoverURL, DisplaySource: "wechat",
			SuggestedFilename: video.Title, SuggestedExtension: ".mp4",
			PlatformDataVersion: 1, PlatformData: platformData,
		})
		if err != nil {
			logger.Error("Failed to start download task: %v", err)
			runtime.EventsEmit(a.ctx, "download:error", map[string]interface{}{
				"error": "无法启动微信下载任务",
			})
			return
		}
		logger.Info("Started WeChat download task: %s", task.ID)

		// Show notification
		if a.notificationsEnabled() && a.trayManager != nil {
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result := a.downloadManager.Shutdown(shutdownCtx)
		cancel()
		if !result.Completed {
			logger.Warn("Download shutdown timed out; recoverable tasks remain: %v", result.TimedOutTaskIDs)
		}
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

func (a *App) currentConfig() *config.Config {
	if a.configManager == nil {
		return config.DefaultConfig()
	}
	return a.configManager.Get()
}

func (a *App) currentSettings() settings.Snapshot {
	return settings.FromConfig(a.currentConfig())
}

func (a *App) currentDownloadDir() string {
	return a.currentSettings().DownloadDir
}

func (a *App) notificationsEnabled() bool {
	return a.currentSettings().ShowNotification
}

func (a *App) updateRuntimeMetadataBestEffort(patch config.RuntimeMetadataPatch) {
	if a.configManager == nil {
		return
	}
	if err := a.configManager.UpdateRuntimeMetadata(context.Background(), patch); err != nil {
		logger.Warn("Failed to update runtime metadata: %v", err)
	}
}

type appSettingsEffectPlanner struct {
	app          *App
	proxyRuntime appSettingsProxyRuntime
}

type appSettingsProxyRuntime interface {
	IsRunning() bool
	GetPort() int
	SetPort(int) error
	SetUpstreamProxy(string)
	SetDebug(bool)
}

func (p appSettingsEffectPlanner) currentProxyRuntime() appSettingsProxyRuntime {
	if p.proxyRuntime != nil {
		return p.proxyRuntime
	}
	if p.app == nil {
		return nil
	}
	return p.app.proxyServer
}

type appCriticalSettingsEffect struct {
	name      string
	preflight func(context.Context) error
	apply     func(context.Context) error
	rollback  func(context.Context) error
	commit    func(context.Context) error
}

func (e appCriticalSettingsEffect) Name() string { return e.name }
func (e appCriticalSettingsEffect) Preflight(ctx context.Context) error {
	if e.preflight == nil {
		return ctx.Err()
	}
	return e.preflight(ctx)
}
func (e appCriticalSettingsEffect) Apply(ctx context.Context) error {
	if e.apply == nil {
		return ctx.Err()
	}
	return e.apply(ctx)
}
func (e appCriticalSettingsEffect) Rollback(ctx context.Context) error {
	if e.rollback == nil {
		return ctx.Err()
	}
	return e.rollback(ctx)
}
func (e appCriticalSettingsEffect) Commit(ctx context.Context) error {
	if e.commit == nil {
		return nil
	}
	return e.commit(ctx)
}

type appDeferredSettingsEffect struct {
	requirement settings.RestartRequirement
}

func (e appDeferredSettingsEffect) Requirement() settings.RestartRequirement {
	return e.requirement
}

type appBestEffortSettingsEffect struct {
	name  string
	apply func(context.Context) error
}

func (e appBestEffortSettingsEffect) Name() string { return e.name }
func (e appBestEffortSettingsEffect) Apply(ctx context.Context) error {
	if e.apply == nil {
		return ctx.Err()
	}
	return e.apply(ctx)
}

func (p appSettingsEffectPlanner) Plan(current, candidate settings.SettingsSnapshot, changed settings.Changed) (settings.SettingsEffectPlan, error) {
	plan := settings.SettingsEffectPlan{}
	if p.app == nil {
		return plan, fmt.Errorf("app settings effect planner is not initialized")
	}
	fieldEffects := settings.DefaultFieldEffectRegistry()
	if err := settings.ValidateChangedFields(changed, fieldEffects); err != nil {
		return plan, err
	}
	handled := settings.Changed{}
	markHandled := func(fields ...string) {
		for _, field := range fields {
			if changed.Has(field) {
				handled[field] = true
			}
		}
	}

	checkContext := func(ctx context.Context) error { return ctx.Err() }
	proxyRuntime := p.currentProxyRuntime()
	if changed.Has("downloadDir") || changed.Has("maxConcurrent") {
		var runtimeUpdate *downloader.RuntimeConfigUpdate
		runtimePatch := downloader.RuntimeConfigPatch{}
		if changed.Has("downloadDir") {
			downloadDir := candidate.DownloadDir
			runtimePatch.DownloadDir = &downloadDir
		}
		if changed.Has("maxConcurrent") {
			maxConcurrent := candidate.MaxConcurrent
			runtimePatch.MaxConcurrent = &maxConcurrent
		}
		plan.CriticalReversible = append(plan.CriticalReversible, appCriticalSettingsEffect{
			name: "download_runtime_config",
			preflight: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				if p.app.downloadManager == nil {
					return fmt.Errorf("download manager is not initialized")
				}
				if changed.Has("downloadDir") {
					if err := config.ValidateDownloadDir(candidate.DownloadDir); err != nil {
						return err
					}
				}
				return nil
			},
			apply: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				var err error
				runtimeUpdate, err = p.app.downloadManager.BeginRuntimeConfigUpdate(runtimePatch)
				return err
			},
			rollback: func(context.Context) error {
				if runtimeUpdate == nil {
					return nil
				}
				return runtimeUpdate.Rollback()
			},
			commit: func(context.Context) error {
				if runtimeUpdate == nil {
					return nil
				}
				return runtimeUpdate.Commit()
			},
		})
		markHandled("downloadDir", "maxConcurrent")
	}
	if changed.Has("upstreamProxy") || changed.Has("useUpstreamProxy") {
		proxyValue := func(snapshot settings.SettingsSnapshot) string {
			if snapshot.UseUpstreamProxy {
				return snapshot.UpstreamProxy
			}
			return ""
		}
		plan.CriticalReversible = append(plan.CriticalReversible, appCriticalSettingsEffect{
			name: "upstream_proxy",
			preflight: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				if proxyRuntime == nil {
					return fmt.Errorf("proxy server is not initialized")
				}
				return nil
			},
			apply: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				proxyRuntime.SetUpstreamProxy(proxyValue(candidate))
				return nil
			},
			rollback: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				proxyRuntime.SetUpstreamProxy(proxyValue(current))
				return nil
			},
		})
		markHandled("upstreamProxy", "useUpstreamProxy")
	}
	if changed.Has("proxyDebug") {
		applyDebug := func(enabled bool) {
			if enabled {
				logger.GetGlobalLogger().SetLevel(logger.LevelDebug)
			} else {
				logger.GetGlobalLogger().SetLevel(logger.LevelInfo)
			}
			proxyRuntime.SetDebug(enabled)
		}
		plan.CriticalReversible = append(plan.CriticalReversible, appCriticalSettingsEffect{
			name: "proxy_debug",
			preflight: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				if proxyRuntime == nil {
					return fmt.Errorf("proxy server is not initialized")
				}
				return nil
			},
			apply: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				applyDebug(candidate.ProxyDebug)
				return nil
			},
			rollback: func(ctx context.Context) error {
				if err := checkContext(ctx); err != nil {
					return err
				}
				applyDebug(current.ProxyDebug)
				return nil
			},
		})
		markHandled("proxyDebug")
	}

	if changed.Has("proxyPort") {
		if proxyRuntime == nil {
			return plan, fmt.Errorf("proxy server is not initialized")
		}
		if proxyRuntime.IsRunning() {
			if proxyRuntime.GetPort() != candidate.ProxyPort {
				plan.DeferredRestart = append(plan.DeferredRestart, appDeferredSettingsEffect{requirement: settings.RestartRequirement{
					Scope: "proxy", Fields: []string{"proxyPort"}, Reason: "stop and start the proxy to bind the new proxy port",
				}})
			}
		} else {
			plan.CriticalReversible = append(plan.CriticalReversible, appCriticalSettingsEffect{
				name: "proxy_port",
				preflight: func(ctx context.Context) error {
					if err := checkContext(ctx); err != nil {
						return err
					}
					if proxyRuntime.IsRunning() {
						return fmt.Errorf("proxy started while applying proxyPort")
					}
					return nil
				},
				apply: func(ctx context.Context) error {
					if err := checkContext(ctx); err != nil {
						return err
					}
					return proxyRuntime.SetPort(candidate.ProxyPort)
				},
				rollback: func(ctx context.Context) error {
					if err := checkContext(ctx); err != nil {
						return err
					}
					return proxyRuntime.SetPort(current.ProxyPort)
				},
			})
		}
		markHandled("proxyPort")
	}
	if changed.Has("apiPort") {
		plan.DeferredRestart = append(plan.DeferredRestart, appDeferredSettingsEffect{requirement: settings.RestartRequirement{
			Scope: "app", Fields: []string{"apiPort"}, Reason: "restart the application to bind the new internal API port",
		}})
		markHandled("apiPort")
	}
	for field, didChange := range changed {
		if !didChange || !settings.RequiresSpecificPlannerEffect(fieldEffects[field]) {
			continue
		}
		if !handled.Has(field) {
			return plan, fmt.Errorf("settings field %q was classified but not handled by the app effect planner", field)
		}
	}

	plan.BestEffort = append(plan.BestEffort, appBestEffortSettingsEffect{
		name: "publish_settings_changed",
		apply: func(ctx context.Context) error {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if p.app.ctx == nil {
				return fmt.Errorf("Wails context is not initialized")
			}
			runtime.EventsEmit(p.app.ctx, "settings:changed", map[string]interface{}{
				"settings": candidate,
				"changed":  changed,
			})
			return nil
		},
	})
	return plan, nil
}

type SettingsDiagnostic struct {
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	RollbackErrors []string `json:"rollbackErrors,omitempty"`
}

func (a *App) reportSettingsTransactionError(err error) {
	var transactionErr *settings.TransactionError
	if !errors.As(err, &transactionErr) {
		return
	}
	diagnostic := SettingsDiagnostic{Code: "settings.inconsistent", Message: transactionErr.Error()}
	for _, rollbackErr := range transactionErr.RollbackErrors {
		diagnostic.RollbackErrors = append(diagnostic.RollbackErrors, rollbackErr.Error())
	}
	logger.Error("Settings transaction rollback was incomplete: %s", diagnostic.Message)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "settings:diagnostic", diagnostic)
	}
}

// ==================== Proxy Methods ====================

// StartProxy starts the MITM proxy server
func (a *App) StartProxy() error {
	a.settingsRuntimeMu.Lock()
	defer a.settingsRuntimeMu.Unlock()

	current := a.currentSettings()
	if err := prepareProxyRuntimeForStart(a.proxyServer, current); err != nil {
		return fmt.Errorf("prepare proxy listener: %w", err)
	}
	if err := a.proxyServer.Start(); err != nil {
		return err
	}

	// Enable system proxy
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", current.ProxyPort)
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

func prepareProxyRuntimeForStart(proxyRuntime appSettingsProxyRuntime, current settings.SettingsSnapshot) error {
	if proxyRuntime == nil {
		return fmt.Errorf("proxy server is not initialized")
	}
	return proxyRuntime.SetPort(current.ProxyPort)
}

// StopProxy stops the MITM proxy server
func (a *App) StopProxy() error {
	a.settingsRuntimeMu.Lock()
	defer a.settingsRuntimeMu.Unlock()

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
		installed := true
		a.updateRuntimeMetadataBestEffort(config.RuntimeMetadataPatch{CertInstalled: &installed})
	}
	return err
}

// UninstallCert removes the CA certificate from the system trust store
func (a *App) UninstallCert() error {
	err := a.certManager.UninstallCert()
	if err == nil {
		// Cache the uninstallation status immediately
		installed := false
		a.updateRuntimeMetadataBestEffort(config.RuntimeMetadataPatch{CertInstalled: &installed})
	}
	return err
}

// GetCertPath returns the CA certificate file path
func (a *App) GetCertPath() string {
	return a.certManager.GetCertPath()
}

// ==================== Video Detection Methods ====================

// GetDetectedVideos returns all detected videos
func (a *App) GetDetectedVideos() detection.PublicSnapshot {
	return a.internalAPI.GetDetectionSnapshot()
}

// ClearDetectedVideos clears all detected videos
func (a *App) ClearDetectedVideos() (detection.PublicChange, error) {
	return a.internalAPI.ClearVideos()
}

// RemoveDetectedVideo removes a detected video by ID
func (a *App) RemoveDetectedVideo(id string) (detection.PublicChange, error) {
	return a.internalAPI.RemoveVideo(id)
}

// ==================== Download Methods ====================

func (a *App) createAndStartDownload(input downloader.TaskCreationInput) (*downloader.DownloadTask, error) {
	if a.downloadManager == nil {
		return nil, fmt.Errorf("download manager is not initialized")
	}
	task, err := a.downloadManager.CreateAndStartTask(input)
	if err != nil {
		return nil, err
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "download:start", a.downloadManager.PublicTaskSnapshot(task))
	}
	return task, nil
}

// StartDetectedDownload resolves an opaque candidate ID entirely in the
// backend. Wails clients never provide media URLs, request headers, or keys.
func (a *App) StartDetectedDownload(detectionID, candidateID string) (downloader.PublicDownloadTask, error) {
	video, candidate, err := a.detectionStore.ResolveCandidate(context.Background(), detectionID, candidateID)
	if err != nil {
		return downloader.PublicDownloadTask{}, &downloadtask.TaskError{
			Code: "detection.candidate_expired", Category: downloadtask.TaskErrorCategoryPlatform,
			Message: "检测到的资源已失效", Retryable: false, UserAction: "请重新检测后再下载",
		}
	}
	platformID := downloadtask.PlatformGeneric
	platformData, err := downloader.MarshalGenericPlatformData(candidate.URL, candidate.Headers)
	if strings.EqualFold(video.Platform, string(downloadtask.PlatformWeChat)) {
		platformID = downloadtask.PlatformWeChat
		platformData, err = wechat.MarshalPlatformData(
			candidate.URL, candidate.Headers, candidate.DecodeKey, candidate.FileFormat,
		)
	}
	if err != nil {
		return downloader.PublicDownloadTask{}, err
	}
	task, err := a.createAndStartDownload(downloader.TaskCreationInput{
		ID: fmt.Sprintf("detected_%d", time.Now().UnixNano()), PlatformID: platformID,
		Title: video.Title, Cover: video.CoverURL, DisplaySource: video.Platform,
		SuggestedFilename: video.Title, SuggestedExtension: ".mp4",
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		return downloader.PublicDownloadTask{}, err
	}
	return a.downloadManager.PublicTaskSnapshot(task), nil
}

// PauseDownload accepts an asynchronous stop operation. The terminal paused
// state arrives through download:lifecycle.
func (a *App) PauseDownload(id string, instance, generation uint64) (downloader.StopReceipt, error) {
	return a.downloadManager.RequestPauseTaskExpected(id, instance, generation)
}

// ResumeDownload resumes a paused download
func (a *App) ResumeDownload(id string, instance, generation uint64) error {
	return a.downloadManager.ResumeTaskExpected(id, instance, generation)
}

// CancelDownload cancels a download
func (a *App) CancelDownload(id string, instance, generation uint64) (downloader.StopReceipt, error) {
	return a.downloadManager.RequestCancelTaskExpected(id, instance, generation)
}

// RetryDownload retries a failed download task.
func (a *App) RetryDownload(id string, instance, generation uint64) error {
	return a.downloadManager.RetryTaskExpected(id, instance, generation)
}

// RemoveDownload removes a download from the list
func (a *App) RemoveDownload(id string, instance, generation uint64) (downloader.StopReceipt, error) {
	return a.downloadManager.RequestRemoveTaskExpected(id, instance, generation)
}

// GetDownloads returns all download tasks
func (a *App) GetDownloads() []downloader.PublicDownloadTask {
	return a.downloadManager.GetPublicTaskSnapshots()
}

// TakeLegacyDownloadNotice returns the one-shot v1 preservation notice shown
// on the download page after the v2 activation.
func (a *App) TakeLegacyDownloadNotice() *downloader.LegacyTaskStateNotice {
	return a.downloadManager.TakeLegacyStateNotice()
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

	platformData, err := bilibili.MarshalTaskData(video, quality, partIndex)
	if err != nil {
		return "", err
	}
	_, err = a.createAndStartDownload(downloader.TaskCreationInput{
		ID: id, PlatformID: downloadtask.PlatformBilibili,
		Title: title, Cover: cover, DisplaySource: "bilibili",
		SuggestedFilename: title, SuggestedExtension: ".mp4",
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		return "", err
	}

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

	platformData, err := bilibili.MarshalTaskData(video, quality, partIndex)
	if err != nil {
		return "", err
	}
	_, err = a.createAndStartDownload(downloader.TaskCreationInput{
		ID: id, PlatformID: downloadtask.PlatformBilibili,
		Title: title, Cover: cover, DisplaySource: "bilibili",
		SuggestedFilename: title, SuggestedExtension: ".mp4",
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		return "", err
	}

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

	platformData, err := douyin.MarshalTaskData(item, qualityKey, nil, false)
	if err != nil {
		return "", err
	}
	extension := ".mp4"
	if item.Type == "album" || len(item.Images) > 0 {
		extension = ".zip"
	}
	_, err = a.createAndStartDownload(downloader.TaskCreationInput{
		ID: id, PlatformID: downloadtask.PlatformDouyin,
		Title: item.Title, Cover: item.Cover, DisplaySource: "douyin",
		SuggestedFilename: item.Title, SuggestedExtension: extension,
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		return "", err
	}
	return id, nil
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

	platformData, err := douyin.MarshalTaskData(item, "partial", unique, true)
	if err != nil {
		return "", err
	}
	_, err = a.createAndStartDownload(downloader.TaskCreationInput{
		ID: id, PlatformID: downloadtask.PlatformDouyin,
		Title: item.Title, Cover: item.Cover, DisplaySource: "douyin",
		SuggestedFilename: item.Title, SuggestedExtension: ".zip",
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		return "", err
	}
	return id, nil
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

	id := fmt.Sprintf("xiaohongshu_%s_%d", item.ID, time.Now().UnixNano())
	platformData, err := xiaohongshu.MarshalTaskData(item, selectedImages, quality)
	if err != nil {
		logger.Warn("DownloadXHSNote marshal task data failed: %v", err)
		return err
	}
	extension := ".mp4"
	if item.IsAlbum() {
		extension = ".zip"
	}
	_, err = a.createAndStartDownload(downloader.TaskCreationInput{
		ID: id, PlatformID: downloadtask.PlatformXiaohongshu,
		Title: item.Title, Cover: item.Cover, DisplaySource: "xiaohongshu",
		OutputDirectory: saveDir, SuggestedFilename: item.Title, SuggestedExtension: extension,
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		logger.Warn("DownloadXHSNote start task failed: %v", err)
		return err
	}
	return nil
}

// IsFFmpegAvailable checks if ffmpeg is available and caches the path
func (a *App) IsFFmpegAvailable() bool {
	available := a.bilibiliDownloader.IsFFmpegAvailable()
	// Cache the FFmpeg path to config for faster startup next time
	if available && a.ffmpegManager != nil {
		path := a.ffmpegManager.GetPath()
		if path != "" {
			a.updateRuntimeMetadataBestEffort(config.RuntimeMetadataPatch{FFmpegPath: &path})
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

	a.updateRuntimeMetadataBestEffort(config.RuntimeMetadataPatch{FFmpegPath: &path})
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ffmpeg:ready", true)
	}
	return path, nil
}

// ==================== Settings Methods ====================

func (a *App) GetSettings() (settings.SettingsSnapshot, error) {
	if a.settingsModule == nil {
		return a.currentSettings(), nil
	}
	return a.settingsModule.GetSettings(context.Background())
}

func (a *App) UpdateSettings(patch settings.SettingsPatch) (settings.SettingsUpdateResult, error) {
	if a.settingsModule == nil {
		return settings.SettingsUpdateResult{}, fmt.Errorf("settings module is not initialized")
	}
	a.settingsRuntimeMu.Lock()
	defer a.settingsRuntimeMu.Unlock()
	result, err := a.settingsModule.UpdateSettings(context.Background(), patch)
	if err != nil {
		a.reportSettingsTransactionError(err)
		return result, err
	}
	// Return the complete current runtime drift, not only requirements created
	// by this patch. This lets an unrelated update preserve a pending restart and
	// lets reverting a port to the actually bound value clear it deterministically.
	result.RestartRequirements = a.runtimeRestartRequirements(result.Settings)
	result.RestartRequired = len(result.RestartRequirements) > 0
	for _, warning := range result.Warnings {
		logger.Warn("Settings update warning [%s/%s]: %s", warning.Code, warning.Effect, warning.Message)
	}
	return result, nil
}

func (a *App) runtimeRestartRequirements(snapshot settings.SettingsSnapshot) []settings.RestartRequirement {
	requirements := make([]settings.RestartRequirement, 0, 2)
	if a.internalAPI != nil && a.internalAPI.GetPort() != snapshot.APIPort {
		requirements = append(requirements, settings.RestartRequirement{
			Scope: "app", Fields: []string{"apiPort"}, Reason: "restart the application to bind the new internal API port",
		})
	}
	if a.proxyServer != nil && a.proxyServer.IsRunning() && a.proxyServer.GetPort() != snapshot.ProxyPort {
		requirements = append(requirements, settings.RestartRequirement{
			Scope: "proxy", Fields: []string{"proxyPort"}, Reason: "stop and start the proxy to bind the new proxy port",
		})
	}
	return requirements
}

// SelectDownloadDir opens a folder dialog and returns the selected directory.
// Persisting the setting is handled by UpdateSettings.
func (a *App) SelectDownloadDir() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择下载目录",
		DefaultDirectory: a.currentDownloadDir(),
	})
	if err != nil {
		return "", err
	}
	return dir, nil
}

// OpenDownloadDir opens the download directory in file explorer
func (a *App) OpenDownloadDir() error {
	return utils.OpenFolder(a.currentDownloadDir())
}

// OpenFile opens a file with the default application
func (a *App) OpenFile(path string) error {
	return utils.OpenFile(path)
}

// ==================== System Methods ====================

type AppRuntimeInfo struct {
	Version         string `json:"version"`
	APIPort         int    `json:"apiPort"`
	APIToken        string `json:"apiToken,omitempty"`
	FFmpegPath      string `json:"ffmpegPath,omitempty"`
	CertPath        string `json:"certPath,omitempty"`
	CertInstalled   bool   `json:"certInstalled"`
	FFmpegAvailable bool   `json:"ffmpegAvailable"`
}

// GetAppInfo returns runtime metadata only. User settings are exposed solely
// through GetSettings and UpdateSettings.
func (a *App) GetAppInfo() AppRuntimeInfo {
	cfg := a.currentConfig()
	info := AppRuntimeInfo{Version: cfg.Version, APIPort: cfg.APIPort}
	if a.ffmpegManager != nil {
		info.FFmpegPath = a.ffmpegManager.GetPath()
	}
	if a.internalAPI != nil {
		info.APIPort = a.internalAPI.GetPort()
		info.APIToken = a.internalAPI.GetToken()
	}
	if a.certManager != nil {
		info.CertPath = a.certManager.GetCertPath()
		info.CertInstalled = a.certManager.IsCertInstalled()
	}
	if a.bilibiliDownloader != nil {
		info.FFmpegAvailable = a.bilibiliDownloader.IsFFmpegAvailable()
	}
	return info
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

// ==================== Frontend Logging ====================

// LogFrontend logs a message from the frontend to the persistent log file
func (a *App) LogFrontend(message string) {
	logger.Info("[Frontend] %s", message)
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
