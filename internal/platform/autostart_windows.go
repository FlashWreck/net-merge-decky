package platform

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const taskName = "net-merge"

// trayExe prefers the windowless tray build installed next to this exe.
func trayExe() string {
	exe, _ := os.Executable()
	gui := filepath.Join(filepath.Dir(exe), "net-merge-tray.exe")
	if _, err := os.Stat(gui); err == nil {
		return gui
	}
	return exe
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// A scheduled task (not a Run-key entry) is what lets an admin-only app start at sign-in without
// a UAC prompt. It's defined by XML because the schtasks command line can't turn off the defaults
// that would break a tray app: "stop after 3 days" and "don't run on battery".
func taskXML(user, exe string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Starts net-merge (multi-adapter connection balancer) at sign-in.</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlEscape(user) + `</UserId><Delay>PT10S</Delay></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + xmlEscape(user) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(exe) + `</Command><Arguments>tray</Arguments></Exec></Actions>
</Task>`
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE // BOM
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2+2*i:], c)
	}
	return b
}

// Autostart turns start-at-sign-in "on" or "off", or prints its "status".
func Autostart(action string) error {
	if action == "status" {
		if run("schtasks", "/Query", "/TN", taskName) == nil {
			fmt.Println("autostart is ON (starts at sign-in, no UAC prompt).")
		} else {
			fmt.Println("autostart is OFF.")
		}
		return nil
	}
	if !IsAdmin() {
		return RelaunchElevated([]string{"autostart", action}, false)
	}
	if action == "off" {
		if run("schtasks", "/Query", "/TN", taskName) != nil {
			fmt.Println("autostart is already OFF.")
			return nil
		}
		if err := run("schtasks", "/Delete", "/F", "/TN", taskName); err != nil {
			return err
		}
		fmt.Println("autostart is OFF.")
		return nil
	}
	user := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" {
		user = d + `\` + user
	}
	tmp := filepath.Join(os.TempDir(), "net-merge-task.xml")
	if err := os.WriteFile(tmp, utf16le(taskXML(user, trayExe())), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := run("schtasks", "/Create", "/F", "/TN", taskName, "/XML", tmp); err != nil {
		return err
	}
	fmt.Println("autostart is ON: net-merge starts at sign-in without a UAC prompt.")
	return nil
}

// HasConsole is false for the windowless tray build and for a double-clicked GUI launch.
func HasConsole() bool {
	r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	return r != 0
}

// ShowError is how the windowless build reports a fatal error: there is no console to print to.
func ShowError(msg string) {
	t, _ := windows.UTF16PtrFromString(msg)
	c, _ := windows.UTF16PtrFromString("net-merge")
	windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONERROR)
}
