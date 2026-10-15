package platform

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// isadmin reports whether process holds cap net admin or root
func IsAdmin() bool {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return os.Geteuid() == 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && caps&(1<<unix.CAP_NET_ADMIN) != 0
		}
	}
	return os.Geteuid() == 0
}

// abstract unix socket single instance lock
const lockAddr = "@net-merge"

var lock net.Listener

// singleinstance acquires single instance lock
func SingleInstance() bool {
	ln, err := net.Listen("unix", lockAddr)
	if err != nil {
		return false
	}
	lock = ln
	go func() { // accept and close check connections
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return true
}

// instancerunning checks if daemon holds lock
func InstanceRunning() bool {
	c, err := net.DialTimeout("unix", lockAddr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// redirectstd redirects standard descriptors to file
func RedirectStd(f *os.File) {
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f) // redirect std logger
	unix.Dup3(int(f.Fd()), 1, 0)
	unix.Dup3(int(f.Fd()), 2, 0)
}

// checkdriver verifies tun device exists
func CheckDriver() error {
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("/dev/net/tun not found: %w", err)
	}
	return nil
}
