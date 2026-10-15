package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"net-merge/internal/engine"
)

// The elevated process can't be signalled from a normal shell, so control goes through files in
// a per-user directory: status.json (written once a second while on) and marker files
// ("on", "off", "quit") that the running instance consumes.

type Status struct {
	PID     int
	Mode    string
	V6      string
	Since   time.Time
	Updated time.Time
	NICs    []engine.SlotStats
}

func DefaultDir() string {
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

// Read returns the status only if it is fresh, i.e. a live process is writing it.
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

// TakeMarker consumes a marker, reporting whether it was there.
func TakeMarker(dir, name string) bool { return os.Remove(filepath.Join(dir, name)) == nil }
