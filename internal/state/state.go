package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

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
}

// defaultdir returns active decky runtime state path or user cache fallback
func DefaultDir() string {
	if dir := os.Getenv("DECKY_PLUGIN_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "state")
	}
	deckyRun := "/run/decky/runtime/net-merge-decky/state"
	if _, err := os.Stat(deckyRun); err == nil {
		return deckyRun
	}
	tmpRun := "/tmp/net-merge-decky/state"
	if _, err := os.Stat(tmpRun); err == nil {
		return tmpRun
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "net-merge")
}

func Write(dir string, st Status) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "status.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, "status.json"))
	}
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
	return os.WriteFile(filepath.Join(dir, name), nil, 0o644)
}

// takemarker consumes marker file
func TakeMarker(dir, name string) bool { return os.Remove(filepath.Join(dir, name)) == nil }
