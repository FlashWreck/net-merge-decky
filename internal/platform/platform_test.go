package platform

import (
	"os"
	"testing"
)

func TestSingleInstanceAndRunning(t *testing.T) {
	// First acquisition should succeed
	if !SingleInstance() {
		t.Fatal("expected SingleInstance to succeed")
	}
	defer func() {
		if lockFile != nil {
			lockFile.Close()
			lockFile = nil
			os.Remove(fallbackLockPath())
		}
	}()

	// While holding lock, InstanceRunning must be true
	if !InstanceRunning() {
		t.Fatal("expected InstanceRunning to be true while lock is held")
	}

	// Close lock
	lockFile.Close()
	lockFile = nil

	// After release, InstanceRunning should be false
	if InstanceRunning() {
		t.Fatal("expected InstanceRunning to be false after lock is closed")
	}
}
