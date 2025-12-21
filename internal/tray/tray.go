package tray

import (
	"EasyDownload/internal/logger"
	"sync"

	"github.com/getlantern/systray"
)

// TrayManager manages the system tray icon and menu
type TrayManager struct {
	icon       []byte
	activeIcon []byte
	tooltip    string
	running    bool
	ready      bool
	mu         sync.RWMutex

	// Menu items
	mShow    *systray.MenuItem
	mSetting *systray.MenuItem
	mQuit    *systray.MenuItem

	// Callbacks
	onShow    func()
	onSetting func()
	onExit    func()

	// State
	proxyRunning  bool
	windowVisible bool
}

// NewTrayManager creates a new TrayManager instance
func NewTrayManager() *TrayManager {
	return &TrayManager{
		tooltip:       "EasyDownload",
		windowVisible: true,
	}
}

// SetIcon sets the default tray icon
func (tm *TrayManager) SetIcon(icon []byte) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.icon = icon
}

// SetActiveIcon sets the icon shown when proxy is running
func (tm *TrayManager) SetActiveIcon(activeIcon []byte) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.activeIcon = activeIcon
}

// SetOnShow sets the callback for showing the window
func (tm *TrayManager) SetOnShow(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onShow = callback
}

// SetOnHide is deprecated, kept for compatibility
func (tm *TrayManager) SetOnHide(callback func()) {
	// No longer used
}

// SetOnSetting sets the callback for opening settings
func (tm *TrayManager) SetOnSetting(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onSetting = callback
}

// SetOnExit sets the callback for exiting the application
func (tm *TrayManager) SetOnExit(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onExit = callback
}

// SetOnToggleProxy is deprecated, kept for compatibility
func (tm *TrayManager) SetOnToggleProxy(callback func()) {
	// No longer used
}

// Start starts the system tray
// Note: This should be called from the main goroutine on some platforms
func (tm *TrayManager) Start() {
	tm.mu.Lock()
	if tm.running {
		tm.mu.Unlock()
		return
	}
	tm.running = true
	tm.mu.Unlock()

	logger.Debug("Starting system tray...")
	systray.Run(tm.onReady, tm.onQuit)
}

// StartAsync starts the system tray in a separate goroutine
func (tm *TrayManager) StartAsync() {
	go tm.Start()
}

// Stop stops the system tray
func (tm *TrayManager) Stop() {
	tm.mu.Lock()
	if !tm.running {
		tm.mu.Unlock()
		return
	}
	tm.mu.Unlock()

	systray.Quit()
}

// onReady is called when the systray is ready
func (tm *TrayManager) onReady() {
	logger.Debug("System tray ready, initializing menu...")

	tm.mu.Lock()
	icon := tm.icon
	tooltip := tm.tooltip
	tm.ready = true
	tm.mu.Unlock()

	// Set initial icon and tooltip
	if len(icon) > 0 {
		systray.SetIcon(icon)
		logger.Debug("Tray icon set, size: %d bytes", len(icon))
	} else {
		logger.Warn("No tray icon provided")
	}
	systray.SetTitle("")
	systray.SetTooltip(tooltip)

	// Create simple menu: Show Window, Settings, Exit
	tm.mShow = systray.AddMenuItem("📺 显示窗口", "显示主窗口")
	tm.mSetting = systray.AddMenuItem("⚙️ 设置", "打开设置页面")
	systray.AddSeparator()
	tm.mQuit = systray.AddMenuItem("❌ 退出", "退出应用程序")

	logger.Debug("Tray menu created successfully")

	// Handle menu clicks
	go tm.handleMenuClicks()
}

// onQuit is called when the systray is quitting
func (tm *TrayManager) onQuit() {
	tm.mu.Lock()
	tm.running = false
	tm.ready = false
	tm.mu.Unlock()
	logger.Debug("System tray stopped")
}

// handleMenuClicks handles menu item clicks
func (tm *TrayManager) handleMenuClicks() {
	for {
		select {
		case <-tm.mShow.ClickedCh:
			logger.Debug("Tray menu: Show window clicked")
			tm.showWindow()
		case <-tm.mSetting.ClickedCh:
			logger.Debug("Tray menu: Settings clicked")
			tm.openSetting()
		case <-tm.mQuit.ClickedCh:
			logger.Debug("Tray menu: Quit clicked")
			tm.quit()
			return
		}
	}
}

// showWindow shows the main window
func (tm *TrayManager) showWindow() {
	tm.mu.Lock()
	callback := tm.onShow
	tm.windowVisible = true
	tm.mu.Unlock()

	if callback != nil {
		callback()
	}
}

// openSetting opens the settings page
func (tm *TrayManager) openSetting() {
	tm.mu.Lock()
	callback := tm.onSetting
	tm.mu.Unlock()

	// First show the window
	tm.showWindow()

	// Then trigger settings callback
	if callback != nil {
		callback()
	}
}

// quit exits the application
func (tm *TrayManager) quit() {
	tm.mu.RLock()
	callback := tm.onExit
	tm.mu.RUnlock()

	if callback != nil {
		callback()
	}

	systray.Quit()
}

// SetProxyStatus updates the tray icon based on proxy status
func (tm *TrayManager) SetProxyStatus(running bool) {
	tm.mu.Lock()
	tm.proxyRunning = running
	icon := tm.icon
	activeIcon := tm.activeIcon
	ready := tm.ready
	tm.mu.Unlock()

	if !ready {
		return
	}

	// Update icon based on proxy status
	if running {
		if len(activeIcon) > 0 {
			systray.SetIcon(activeIcon)
		}
		systray.SetTooltip("EasyDownload - 代理运行中")
	} else {
		if len(icon) > 0 {
			systray.SetIcon(icon)
		}
		systray.SetTooltip("EasyDownload")
	}
}

// IsProxyRunning returns whether the proxy is running
func (tm *TrayManager) IsProxyRunning() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.proxyRunning
}

// IsWindowVisible returns whether the window is visible
func (tm *TrayManager) IsWindowVisible() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.windowVisible
}

// SetWindowVisible sets the window visibility state
func (tm *TrayManager) SetWindowVisible(visible bool) {
	tm.mu.Lock()
	tm.windowVisible = visible
	tm.mu.Unlock()
}

// ShowNotification displays a system notification
func (tm *TrayManager) ShowNotification(title, message string) {
	showNotification(title, message)
}
