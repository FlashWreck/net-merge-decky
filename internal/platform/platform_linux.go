package platform

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

const rootLockPath = "/run/net-merge.lock"

func fallbackLockPath() string {
	return filepath.Join(os.TempDir(), "net-merge.lock")
}

var lockFile *os.File

// SingleInstance acquires a non-blocking exclusive file lock
func SingleInstance() bool {
	var f *os.File
	var err error
	if os.Geteuid() == 0 {
		f, err = os.OpenFile(rootLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	}
	if f == nil || err != nil {
		f, err = os.OpenFile(fallbackLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return false
		}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return true
}

// InstanceRunning reports whether the daemon is actively running
func InstanceRunning() bool {
	checkFile := func(path string) (bool, bool) {
		f, err := os.Open(path)
		if err != nil {
			return false, false
		}
		defer f.Close()
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			return false, true
		}
		return true, true
	}

	if running, exists := checkFile(rootLockPath); exists {
		return running
	}
	running, _ := checkFile(fallbackLockPath())
	return running
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
