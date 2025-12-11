//go:build !windows

package tray

// showNotification displays a system notification
// This is a placeholder for non-Windows platforms
func showNotification(title, message string) {
	// On non-Windows platforms, we'll rely on the Wails runtime
	// for notifications in the main app
}
