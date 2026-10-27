package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The single-instance lock must survive garbage collection. It used to be held
// by an *os.File nothing referred to, whose finalizer closed the fd on the next
// GC and released the flock — after which a second board started.
func TestInstanceLockSurvivesGC(t *testing.T) {
	dir := t.TempDir()
	if !lockInstance(dir) {
		t.Fatal("first lockInstance failed on a fresh directory")
	}
	t.Cleanup(func() {
		if instanceLock != nil {
			instanceLock.Close()
			instanceLock = nil
		}
	})

	// Two cycles plus a pause: the first GC queues the finalizer, which then
	// runs on its own goroutine.
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}

	// flock belongs to the open file description, so a second open in this
	// same process contends exactly as a second launch would.
	f, err := os.OpenFile(filepath.Join(dir, "ggraphify.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("a second flock succeeded after GC: the instance lock was released")
	}
	if lockInstance(dir) {
		t.Fatal("a second lockInstance succeeded while the first holds the lock")
	}
}
