package state

import (
	"encoding/json"
	"fmt"
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

// DefaultDir returns active decky runtime state path or secure runtime fallback
func DefaultDir() string {
	if dir := os.Getenv("DECKY_PLUGIN_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "state")
	}
	deckyRun := "/run/decky/runtime/net-merge-decky/state"
	if _, err := os.Stat(deckyRun); err == nil {
		return deckyRun
	}
	if os.Geteuid() == 0 {
		return "/run/net-merge-decky/state"
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("net-merge-%d", os.Getuid()), "state")
}

func Write(dir string, st Status) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "status.json.tmp")
	_ = os.Remove(tmp) // Remove pre-existing symlink if present

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return
	}
	f.Close()
	_ = os.Rename(tmp, filepath.Join(dir, "status.json"))
}

func Clear(dir string) { os.Remove(filepath.Join(dir, "status.json")) }

// read returns status if written recently
func Read(dir string) (*Status, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		return nil, false
	}
	var st Status
	if json.Unmarshal(b, &st) != nil || time.Since(st.Updated) > 3*time.Second {
		return nil, false
	}
	return &st, true
}

func SendMarker(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	_ = os.Remove(path) // Unlink any pre-existing symlink without following
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// takemarker consumes marker file
func TakeMarker(dir, name string) bool { return os.Remove(filepath.Join(dir, name)) == nil }
