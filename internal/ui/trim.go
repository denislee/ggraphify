package ui

import (
	"sync/atomic"
	"time"
)

// trimEvery is the background cadence of mallocTrim, and trimAfterGap the
// least time between an event-driven trim and the previous one: a scan that
// re-splices the board twice in a row should not pay for two trims.
const (
	trimEvery    = 5 * time.Minute
	trimAfterGap = 30 * time.Second
)

// trimmer returns freed C-heap pages to the kernel periodically and after the
// large widget rebuilds that free the most of it. It runs malloc_trim off the
// main thread: the call walks and locks every arena, and the GTK thread is the
// one that must not stall.
type trimmer struct {
	last    atomic.Int64 // unix nanos of the last trim started
	running atomic.Bool
	now     func() time.Time
	trim    func() bool
}

func newTrimmer() *trimmer {
	return &trimmer{now: time.Now, trim: mallocTrim}
}

// start runs the periodic trim until stop is closed.
func (t *trimmer) start(stop <-chan struct{}) {
	go func() {
		tk := time.NewTicker(trimEvery)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				t.run(false)
			}
		}
	}()
}

// soon asks for a trim after a large rebuild, unless one ran within
// trimAfterGap or is still running. It never blocks.
func (t *trimmer) soon() bool {
	if t == nil {
		return false
	}
	return t.run(true)
}

func (t *trimmer) run(async bool) bool {
	now := t.now().UnixNano()
	if async && now-t.last.Load() < int64(trimAfterGap) {
		return false
	}
	if !t.running.CompareAndSwap(false, true) {
		return false
	}
	t.last.Store(now)
	do := func() {
		defer t.running.Store(false)
		t.trim()
	}
	if async {
		go do()
	} else {
		do()
	}
	return true
}
