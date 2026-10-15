package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"net-merge/internal/logx"
	"net-merge/internal/platform"
	"net-merge/internal/tray"
	"net-merge/internal/tun"
)

func cmdTray(args []string) {
	fs := flag.NewFlagSet("tray", flag.ExitOnError)
	var c common
	c.register(fs)
	hidden := fs.Bool("hidden", true, "hide the console window of the elevated process")
	startOff := fs.Bool("off", false, "start with system-wide mode OFF (default: ON)")
	fs.Parse(args)

	if !platform.IsAdmin() {
		child := append([]string{"tray"}, args...)
		child = append(child, "-state-dir", c.stateDir)
		if err := platform.RelaunchTray(child, *hidden); err != nil {
			die(err)
		}
		fmt.Println("Approve the UAC prompt. Look for the net-merge icon in the system tray (^ overflow arrow).")
		return
	}
	if !platform.SingleInstance() {
		fmt.Println("net-merge is already running.")
		return
	}
	if c.logFile == "" {
		c.stats = 0 // nobody is watching a hidden console
		if !platform.HasConsole() {
			c.logFile = filepath.Join(c.stateDir, "net-merge.log") // windowless build: keep a log for diagnosis
		}
	}
	if err := c.setup(); err != nil {
		die(err)
	}
	if err := platform.CheckDriver(); err != nil {
		die(err)
	}

	t := tun.New(nil)
	t.MTU = c.mtu
	mode := func() string {
		if t.IsOn() {
			return "system-wide (tray)"
		}
		return ""
	}
	sched, bal, err := c.boot(mode)
	if err != nil {
		die(err)
	}
	t.B = bal

	// Started at sign-in this process can beat the desktop shell by a fraction of a second. With no
	// taskbar the tray library fails to create the icon and never becomes ready, so wait for it.
	if !platform.WaitForTray(3 * time.Minute) {
		logx.Printf("warning: taskbar not found after 3 minutes, trying anyway\n")
	}
	ready := make(chan struct{})
	go func() { // safety net: if the icon still can't be created, exit so autostart can retry
		select {
		case <-ready:
		case <-time.After(30 * time.Second):
			logx.Printf("error: tray icon did not initialise within 30s; exiting so autostart can restart it\n")
			os.Exit(4)
		}
	}()
	tray.Run(t, sched, bal, c.stateDir, !*startOff, ready)
}
