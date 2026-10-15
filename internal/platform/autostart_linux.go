package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// autostartFile is an XDG autostart entry, honoured by GNOME, KDE, XFCE, Cinnamon, LXQt and others
// at sign-in. On NixOS the module's programs.net-merge.autostart (a systemd user service) is an
// alternative that doesn't depend on the desktop.
func autostartFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "net-merge.desktop"), nil
}

// trayExe prefers net-merge on PATH: on NixOS that is the capability wrapper in /run/wrappers/bin,
// while os.Executable is the store path, which doesn't carry the capabilities.
func trayExe() string {
	if p, err := exec.LookPath("net-merge"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	exe, _ := os.Executable()
	return exe
}

// desktopQuote quotes an Exec argument as the Desktop Entry spec requires.
func desktopQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$`") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(s) + `"`
}

// Autostart turns start-at-sign-in "on" or "off", or prints its "status".
func Autostart(action string) error {
	path, err := autostartFile()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	switch action {
	case "status":
		if statErr == nil {
			fmt.Println("autostart is ON (" + path + ").")
		} else {
			fmt.Println("autostart is OFF.")
		}
		return nil
	case "off":
		if statErr != nil {
			fmt.Println("autostart is already OFF.")
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Println("autostart is OFF.")
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=net-merge\n" +
		"Comment=Use all your network adapters at once\n" +
		"Exec=" + desktopQuote(trayExe()) + " tray\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return err
	}
	fmt.Println("autostart is ON: net-merge starts at sign-in (" + path + ").")
	if !IsAdmin() {
		fmt.Println("note: the tray needs CAP_NET_ADMIN to start merging; see `net-merge tray` or the README.")
	}
	return nil
}
