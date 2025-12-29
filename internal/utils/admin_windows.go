//go:build windows

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modShell32        = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

// tokenElevation matches the Windows TOKEN_ELEVATION structure.
type tokenElevation struct {
	TokenIsElevated uint32
}

// tokenElevationClass is the TOKEN_INFORMATION_CLASS value for TokenElevation (20).
const tokenElevationClass = 20

// IsAdmin checks if the current process is running with elevated privileges.
// This checks TokenElevation, not just group membership, which is the correct
// way to detect if UAC elevation has been granted.
func IsAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	var elevation tokenElevation
	var size uint32
	err = windows.GetTokenInformation(
		token,
		tokenElevationClass,
		(*byte)(unsafe.Pointer(&elevation)),
		uint32(unsafe.Sizeof(elevation)),
		&size,
	)
	if err != nil {
		return false
	}

	return elevation.TokenIsElevated != 0
}

// RestartAsAdmin restarts the current application with administrator privileges.
// Returns nil if the elevated process was started successfully.
// The caller should exit the current process after this returns nil.
func RestartAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	// Get command line arguments (skip the executable name)
	args := os.Args[1:]
	argStr := ""
	if len(args) > 0 {
		escaped := make([]string, 0, len(args))
		for _, a := range args {
			escaped = append(escaped, syscall.EscapeArg(a))
		}
		argStr = strings.Join(escaped, " ")
	}

	// Use the executable directory as working dir to reduce DLL hijacking risk.
	cwd := filepath.Dir(exe)

	// Convert strings to UTF16 pointers
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("启动管理员进程失败: 参数包含非法字符: %w", err)
	}
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return fmt.Errorf("启动管理员进程失败: 参数包含非法字符: %w", err)
	}
	params, err := syscall.UTF16PtrFromString(argStr)
	if err != nil {
		return fmt.Errorf("启动管理员进程失败: 参数包含非法字符: %w", err)
	}
	dir, err := syscall.UTF16PtrFromString(cwd)
	if err != nil {
		return fmt.Errorf("启动管理员进程失败: 参数包含非法字符: %w", err)
	}

	// ShellExecuteW returns a value > 32 on success
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(dir)),
		windows.SW_SHOWNORMAL,
	)

	if ret <= 32 {
		// ShellExecuteW returns an "HINSTANCE"-like value where <= 32 indicates an error.
		// These values are *not* Win32 GetLastError codes.
		if msg, ok := shellExecuteWErrorMessage(ret); ok {
			return fmt.Errorf("启动管理员进程失败: %s (ShellExecuteW=%d)", msg, ret)
		}
		return fmt.Errorf("启动管理员进程失败 (ShellExecuteW=%d)", ret)
	}

	return nil
}

func shellExecuteWErrorMessage(code uintptr) (string, bool) {
	switch code {
	case 2:
		return "文件未找到", true
	case 3:
		return "路径未找到", true
	case 5:
		return "拒绝访问或用户取消管理员授权", true
	case 8:
		return "内存不足", true
	case 26:
		return "共享错误", true
	case 27:
		return "关联不完整", true
	case 28:
		return "DDE 超时", true
	case 29:
		return "DDE 失败", true
	case 30:
		return "DDE 忙", true
	case 31:
		return "无关联的程序可打开该文件", true
	case 32:
		return "DLL 未找到", true
	default:
		return "", false
	}
}

// CanRestartAsAdmin returns true on Windows where UAC elevation is supported.
func CanRestartAsAdmin() bool {
	return true
}
