//go:build windows

package tray

import (
	"os/exec"
	"strings"
)

// showNotification displays a Windows notification using PowerShell
// This is a simple cross-compatible approach that works on Windows 10+
func showNotification(title, message string) {
	// Escape special characters for PowerShell
	title = escapeForPowerShell(title)
	message = escapeForPowerShell(message)

	// Use PowerShell to show a toast notification
	script := `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null

$template = @"
<toast>
    <visual>
        <binding template="ToastText02">
            <text id="1">` + title + `</text>
            <text id="2">` + message + `</text>
        </binding>
    </visual>
</toast>
"@

$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("EasyDownload").Show($toast)
`

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	// Run in background, don't wait for completion
	go cmd.Run()
}

// escapeForPowerShell escapes special characters for PowerShell strings
func escapeForPowerShell(s string) string {
	// Replace characters that could break the XML or PowerShell
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
