package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

func TestReadRejectsStatusSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.json")
	b, err := json.Marshal(Status{PID: 7, Updated: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, filepath.Join(dir, "status.json")); err != nil {
		t.Fatal(err)
	}

	if _, ok := Read(dir); ok {
		t.Fatal("expected symlink status to be rejected")
	}
	content, err := os.ReadFile(decoy)
	if err != nil || string(content) != string(b) {
		t.Fatal("expected decoy to stay unchanged")
	}
}

func TestReadRejectsFifo(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "status.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() {
		_, ok := Read(dir)
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("expected fifo status to be rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked on fifo")
	}
}

func TestReadRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	b := make([]byte, (1<<20)+1)
	if err := os.WriteFile(filepath.Join(dir, "status.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := Read(dir); ok {
		t.Fatal("expected oversize status to be rejected")
	}
}

func TestWriteReplacesStatusSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.json")
	content := "do-not-touch"
	if err := os.WriteFile(decoy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, filepath.Join(dir, "status.json")); err != nil {
		t.Fatal(err)
	}

	Write(dir, Status{PID: 42, Updated: time.Now()})

	got, err := os.ReadFile(decoy)
	if err != nil || string(got) != content {
		t.Fatal("expected decoy to stay unchanged")
	}
	if _, ok := Read(dir); !ok {
		t.Fatal("expected status to be written")
	}
	fi, err := os.Lstat(filepath.Join(dir, "status.json"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected status to be a regular file")
	}
}

func TestWriteStatusMode(t *testing.T) {
	dir := t.TempDir()
	Write(dir, Status{PID: 1, Updated: time.Now()})

	fi, err := os.Stat(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected status mode: %o", fi.Mode().Perm())
	}
}

func TestEnsureDirRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(link); err == nil {
		t.Fatal("expected symlink state dir to be rejected")
	}
}

func TestEnsureDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("unexpected state mode: %o", fi.Mode().Perm())
	}
}
