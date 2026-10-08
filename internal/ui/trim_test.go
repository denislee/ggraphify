package ui

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestTrimmerSoonIsThrottled(t *testing.T) {
	var calls atomic.Int32
	clock := time.Unix(1_000_000, 0)
	done := make(chan struct{}, 4)
	tr := &trimmer{
		now:  func() time.Time { return clock },
		trim: func() bool { calls.Add(1); done <- struct{}{}; return true },
	}
	wait := func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("trim did not run")
		}
		for tr.running.Load() {
			time.Sleep(time.Millisecond)
		}
	}
	if !tr.soon() {
		t.Fatal("first soon() should trim")
	}
	wait()
	clock = clock.Add(trimAfterGap / 2)
	if tr.soon() {
		t.Fatal("soon() within the gap should not trim")
	}
	clock = clock.Add(trimAfterGap)
	if !tr.soon() {
		t.Fatal("soon() after the gap should trim")
	}
	wait()
	// The periodic path ignores the gap.
	if !tr.run(false) {
		t.Fatal("periodic run should trim")
	}
	<-done
	if got := calls.Load(); got != 3 {
		t.Fatalf("trim calls = %d, want 3", got)
	}
	var nilT *trimmer
	if nilT.soon() {
		t.Fatal("nil trimmer must be a no-op")
	}
}

func TestMallocTrimRuns(t *testing.T) {
	_ = mallocTrim() // must not crash; result depends on the heap state
}
