package tray

import (
	"sync"

	"github.com/getlantern/systray"
)

// TrayManager manages the system tray icon and menu
type TrayManager struct {
	icon       []byte
	activeIcon []byte
	tooltip    string
	running    bool
	mu         sync.RWMutex

	// Menu items
	mShow        *systray.MenuItem
	mHide        *systray.MenuItem
	mToggleProxy *systray.MenuItem
	mQuit        *systray.MenuItem

	// Callbacks
	onShow        func()
	onHide        func()
	onExit        func()
	onToggleProxy func()

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

// SetOnHide sets the callback for hiding the window
func (tm *TrayManager) SetOnHide(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onHide = callback
}

// SetOnExit sets the callback for exiting the application
func (tm *TrayManager) SetOnExit(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onExit = callback
}

// SetOnToggleProxy sets the callback for toggling the proxy
func (tm *TrayManager) SetOnToggleProxy(callback func()) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onToggleProxy = callback
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

	systray.Run(tm.onReady, tm.onQuit)
}

// StartAsync starts the system tray in a separate goroutine
// This is useful when you need to start the tray from a non-main goroutine
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
	tm.mu.RLock()
	icon := tm.icon
	tooltip := tm.tooltip
	tm.mu.RUnlock()

	// Set initial icon and tooltip
	if len(icon) > 0 {
		systray.SetIcon(icon)
	}
	systray.SetTitle("EasyDownload")
	systray.SetTooltip(tooltip)

	// Create menu items
	tm.mShow = systray.AddMenuItem("显示窗口", "显示主窗口")
	tm.mHide = systray.AddMenuItem("隐藏窗口", "隐藏主窗口")
	tm.mHide.Hide() // Initially hidden since window is visible

	systray.AddSeparator()

	tm.mToggleProxy = systray.AddMenuItem("启动代理", "启动/停止代理服务")

	systray.AddSeparator()

	tm.mQuit = systray.AddMenuItem("退出", "退出应用程序")

	// Handle menu clicks
	go tm.handleMenuClicks()
}

// onQuit is called when the systray is quitting
func (tm *TrayManager) onQuit() {
	tm.mu.Lock()
	tm.running = false
	tm.mu.Unlock()
}

// handleMenuClicks handles menu item clicks
func (tm *TrayManager) handleMenuClicks() {
	for {
		select {
		case <-tm.mShow.ClickedCh:
			tm.showWindow()
		case <-tm.mHide.ClickedCh:
			tm.hideWindow()
		case <-tm.mToggleProxy.ClickedCh:
			tm.toggleProxy()
		case <-tm.mQuit.ClickedCh:
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

	// Update menu items
	if tm.mShow != nil {
		tm.mShow.Hide()
	}
	if tm.mHide != nil {
		tm.mHide.Show()
	}

	if callback != nil {
		callback()
	}
}

// hideWindow hides the main window
func (tm *TrayManager) hideWindow() {
	tm.mu.Lock()
	callback := tm.onHide
	tm.windowVisible = false
	tm.mu.Unlock()

	// Update menu items
	if tm.mShow != nil {
		tm.mShow.Show()
	}
	if tm.mHide != nil {
		tm.mHide.Hide()
	}

	if callback != nil {
		callback()
	}
}

// toggleProxy toggles the proxy state
func (tm *TrayManager) toggleProxy() {
	tm.mu.RLock()
	callback := tm.onToggleProxy
	tm.mu.RUnlock()

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

// SetProxyStatus updates the tray icon and menu based on proxy status
func (tm *TrayManager) SetProxyStatus(running bool) {
	tm.mu.Lock()
	tm.proxyRunning = running
	icon := tm.icon
	activeIcon := tm.activeIcon
	tm.mu.Unlock()

	// Update icon based on proxy status
	if running {
		if len(activeIcon) > 0 {
			systray.SetIcon(activeIcon)
		}
		systray.SetTooltip("EasyDownload - 代理运行中")
		if tm.mToggleProxy != nil {
			tm.mToggleProxy.SetTitle("停止代理")
		}
	} else {
		if len(icon) > 0 {
			systray.SetIcon(icon)
		}
		systray.SetTooltip("EasyDownload")
		if tm.mToggleProxy != nil {
			tm.mToggleProxy.SetTitle("启动代理")
		}
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

	// Update menu items
	if visible {
		if tm.mShow != nil {
			tm.mShow.Hide()
		}
		if tm.mHide != nil {
			tm.mHide.Show()
		}
	} else {
		if tm.mShow != nil {
			tm.mShow.Show()
		}
		if tm.mHide != nil {
			tm.mHide.Hide()
		}
	}
}

// ShowNotification displays a system notification
func (tm *TrayManager) ShowNotification(title, message string) {
	// Note: systray doesn't have built-in notification support
	// We'll use a platform-specific implementation
	showNotification(title, message)
}
