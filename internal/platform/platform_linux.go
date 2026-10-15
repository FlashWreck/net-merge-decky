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

const (
	rootLockPath = "/run/net-merge.lock"
	rootPidPath  = "/run/net-merge.pid"
)

func fallbackLockPath() string {
	return filepath.Join(os.TempDir(), "net-merge.lock")
}

func fallbackPidPath() string {
	return filepath.Join(os.TempDir(), "net-merge.pid")
}

var lockFile *os.File

func SingleInstance() bool {
	var f *os.File
	var err error
	isRoot := os.Geteuid() == 0
	if isRoot {
		f, err = os.OpenFile(rootLockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			_ = os.Chmod(rootLockPath, 0o600)
		}
	}
	if f == nil || err != nil {
		f, err = os.OpenFile(fallbackLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
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

	pidBytes := []byte(strconv.Itoa(os.Getpid()) + "\n")
	pidPath := fallbackPidPath()
	if isRoot {
		pidPath = rootPidPath
	}
	_ = os.WriteFile(pidPath, pidBytes, 0o644)
	return true
}

func IsPIDRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := unix.Kill(pid, 0); err != nil {
		return false
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	name := strings.TrimSpace(string(comm))
	return name == "net-merge" || strings.HasPrefix(name, "net-merge")
}

// InstanceRunning reports whether the daemon is actively running
func InstanceRunning() bool {
	checkLock := func(path string) (bool, bool) {
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

	if running, accessible := checkLock(rootLockPath); accessible {
		return running
	}

	checkPID := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return false
		}
		return IsPIDRunning(pid)
	}

	if checkPID(rootPidPath) {
		return true
	}

	if running, accessible := checkLock(fallbackLockPath()); accessible {
		return running
	}
	return checkPID(fallbackPidPath())
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
