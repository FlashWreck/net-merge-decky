package state

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"net-merge/internal/engine"
)

// status and control markers are managed through files in state directory
type Status struct {
	PID          int                `json:"pid"`
	On           bool               `json:"on"`
	Mode         string             `json:"mode"`
	V6           string             `json:"v6"`
	V6Mode       string             `json:"v6_mode"`
	Since        time.Time          `json:"since"`
	Updated      time.Time          `json:"updated"`
	TotalRateIn  float64            `json:"total_rate_in"`
	TotalRateOut float64            `json:"total_rate_out"`
	NICs         []engine.SlotStats `json:"nics"`
	Error        string             `json:"error,omitempty"`
}

// DefaultDir returns the active decky runtime state path or secure runtime fallback
func DefaultDir() string {
	if os.Geteuid() == 0 {
		return "/run/net-merge-decky/state"
	}
	if dir := os.Getenv("DECKY_PLUGIN_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "state")
	}
	deckyRun := "/run/decky/runtime/net-merge-decky/state"
	if _, err := os.Stat(deckyRun); err == nil {
		return deckyRun
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("net-merge-%d", os.Getuid()), "state")
}

// EnsureDir creates and validates the state directory
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a real directory", dir)
	}
	if os.Geteuid() == 0 && st.Uid != 0 {
		return fmt.Errorf("%s must be owned by root", dir)
	}
	if st.Mode&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// openStateDir opens and validates the state directory
func openStateDir(dir string) (int, error) {
	fd, err := unix.Open(
		dir,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return -1, err
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if os.Geteuid() == 0 && (st.Uid != 0 || st.Mode&0o077 != 0) {
		unix.Close(fd)
		return -1, fmt.Errorf("%s must be root owned and private", dir)
	}
	return fd, nil
}

// Write replaces status atomically inside the state directory
func Write(dir string, st Status) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}

	dirFD, err := openStateDir(dir)
	if err != nil {
		return
	}
	defer unix.Close(dirFD)

	tmp := "status.json.tmp"
	_ = unix.Unlinkat(dirFD, tmp, 0)

	fd, err := unix.Openat(
		dirFD,
		tmp,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return
	}

	f := os.NewFile(uintptr(fd), tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = unix.Unlinkat(dirFD, tmp, 0)
		return
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(dirFD, tmp, 0)
		return
	}

	_ = unix.Renameat(dirFD, tmp, dirFD, "status.json")
}

func Clear(dir string) {
	dirFD, err := openStateDir(dir)
	if err != nil {
		return
	}
	defer unix.Close(dirFD)
	_ = unix.Unlinkat(dirFD, "status.json", 0)
}

// Read rejects links and non regular files
func Read(dir string) (*Status, bool) {
	dirFD, err := openStateDir(dir)
	if err != nil {
		return nil, false
	}
	defer unix.Close(dirFD)

	fd, err := unix.Openat(
		dirFD,
		"status.json",
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, false
	}

	f := os.NewFile(uintptr(fd), "status.json")
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return nil, false
	}

	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, false
	}

	var st Status
	if json.Unmarshal(b, &st) != nil || time.Since(st.Updated) > 3*time.Second {
		return nil, false
	}
	return &st, true
}

// SendMarker validates the state directory first
func SendMarker(dir, name string) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}

	dirFD, err := openStateDir(dir)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)

	_ = unix.Unlinkat(dirFD, name, 0)

	fd, err := unix.Openat(
		dirFD,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// TakeMarker consumes marker file
func TakeMarker(dir, name string) bool {
	dirFD, err := openStateDir(dir)
	if err != nil {
		return false
	}
	defer unix.Close(dirFD)
	return unix.Unlinkat(dirFD, name, 0) == nil
}
