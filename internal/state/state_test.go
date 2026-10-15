package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateWriteRead(t *testing.T) {
	dir := t.TempDir()
	st := Status{
		PID:          1234,
		On:           true,
		Mode:         "system-wide",
		TotalRateIn:  1000,
		TotalRateOut: 2000,
		Updated:      time.Now(),
	}

	Write(dir, st)

	got, ok := Read(dir)
	if !ok || got == nil {
		t.Fatal("expected Read to return valid status")
	}
	if got.PID != 1234 || !got.On || got.Mode != "system-wide" {
		t.Fatalf("unexpected status values: %+v", got)
	}

	Clear(dir)
	if _, ok := Read(dir); ok {
		t.Fatal("expected Read to fail after Clear")
	}
}

func TestSymlinkProtectionWrite(t *testing.T) {
	dir := t.TempDir()
	decoyTarget := filepath.Join(dir, "target.txt")
	decoyContent := "sensitive-content-do-not-truncate"
	if err := os.WriteFile(decoyTarget, []byte(decoyContent), 0o600); err != nil {
		t.Fatal(err)
	}

	tmpLink := filepath.Join(dir, "status.json.tmp")
	if err := os.Symlink(decoyTarget, tmpLink); err != nil {
		t.Fatal(err)
	}

	st := Status{
		PID:     4321,
		On:      false,
		Updated: time.Now(),
	}
	Write(dir, st)

	content, err := os.ReadFile(decoyTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != decoyContent {
		t.Fatalf("decoy target was modified or truncated! got: %q", string(content))
	}

	got, ok := Read(dir)
	if !ok || got.PID != 4321 {
		t.Fatalf("expected status.json to be created properly, got: %+v", got)
	}
}

func TestSymlinkProtectionSendMarker(t *testing.T) {
	dir := t.TempDir()
	decoyTarget := filepath.Join(dir, "target.txt")
	decoyContent := "secret-root-data"
	if err := os.WriteFile(decoyTarget, []byte(decoyContent), 0o600); err != nil {
		t.Fatal(err)
	}

	markerLink := filepath.Join(dir, "on")
	if err := os.Symlink(decoyTarget, markerLink); err != nil {
		t.Fatal(err)
	}

	if err := SendMarker(dir, "on"); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(decoyTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != decoyContent {
		t.Fatalf("decoy target was modified! got: %q", string(content))
	}

	if !TakeMarker(dir, "on") {
		t.Fatal("expected TakeMarker to return true")
	}
	if TakeMarker(dir, "on") {
		t.Fatal("expected second TakeMarker to return false")
	}
}
