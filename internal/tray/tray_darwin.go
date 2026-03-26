//go:build darwin

package tray

// TrayManager is a no-op implementation on macOS because the current systray
// dependency conflicts with Wails' own AppDelegate symbols.
type TrayManager struct {
	onShow    func()
	onSetting func()
	onExit    func()
}

// NewTrayManager creates a new no-op tray manager for macOS.
func NewTrayManager() *TrayManager {
	return &TrayManager{}
}

// IsSupported reports that the tray integration is disabled on macOS.
func (tm *TrayManager) IsSupported() bool {
	return false
}

func (tm *TrayManager) SetIcon(icon []byte) {}

func (tm *TrayManager) SetActiveIcon(activeIcon []byte) {}

func (tm *TrayManager) SetOnShow(callback func()) {
	tm.onShow = callback
}

func (tm *TrayManager) SetOnSetting(callback func()) {
	tm.onSetting = callback
}

func (tm *TrayManager) SetOnExit(callback func()) {
	tm.onExit = callback
}

func (tm *TrayManager) Start() {}

func (tm *TrayManager) StartAsync() {}

func (tm *TrayManager) Stop() {}

func (tm *TrayManager) SetProxyStatus(running bool) {}

func (tm *TrayManager) ShowNotification(title, message string) {}
