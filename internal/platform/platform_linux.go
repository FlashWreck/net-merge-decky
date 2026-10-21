package platform

import (
	"bufio"
	"fmt"
	"io"
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
	return filepath.Join(os.TempDir(), fmt.Sprintf("net-merge-%d.lock", os.Getuid()))
}

func fallbackPidPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("net-merge-%d.pid", os.Getuid()))
}

var lockFile *os.File

func SingleInstance() bool {
	isRoot := os.Geteuid() == 0
	lockPath := fallbackLockPath()
	if isRoot {
		lockPath = rootLockPath
	}

	f, err := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		log.Printf("single instance lock: %v", err)
		return false
	}
	if isRoot {
		_ = os.Chmod(rootLockPath, 0o600)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	fmt.Fprintf(f, "%d\n", os.Getpid())

	pidPath := fallbackPidPath()
	if isRoot {
		pidPath = rootPidPath
	}
	writePID(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"))
	return true
}

// writePID replaces the pid file without following links
func writePID(path string, data []byte) {
	f, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC|unix.O_NOFOLLOW,
		0o644,
	)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_ = f.Close()
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
		f, err := os.OpenFile(
			path,
			os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
		if err != nil {
			return false, false
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return false, false
		}
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
		f, err := os.OpenFile(
			path,
			os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
		if err != nil {
			return false
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64 {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(f, 64))
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
