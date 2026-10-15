package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CheckDriver reports whether the TUN driver can be loaded: wintun.dll must sit next to the exe.
func CheckDriver() error {
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "wintun.dll")); err != nil {
		return fmt.Errorf("wintun.dll not found next to %s", exe)
	}
	return nil
}

// WaitForTray blocks until the shell's taskbar and notification area exist.
func WaitForTray(max time.Duration) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	findWindow, findWindowEx := user32.NewProc("FindWindowW"), user32.NewProc("FindWindowExW")
	taskbar, _ := windows.UTF16PtrFromString("Shell_TrayWnd")
	notify, _ := windows.UTF16PtrFromString("TrayNotifyWnd")
	deadline := time.Now().Add(max)
	for {
		if h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(taskbar)), 0); h != 0 {
			if n, _, _ := findWindowEx.Call(h, 0, uintptr(unsafe.Pointer(notify)), 0); n != 0 {
				time.Sleep(time.Second) // let the notification area finish initialising
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
