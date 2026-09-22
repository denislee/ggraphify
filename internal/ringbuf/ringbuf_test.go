package ringbuf

import (
	"strings"
	"sync"
	"testing"
)

func TestKeepsTheTailNotTheHead(t *testing.T) {
	b := New(64)
	for i := 0; i < 200; i++ {
		b.WriteString("line-with-some-padding\n")
	}
	// A failing graphify run's message is at the end, so that is what has to
	// survive; the start is what gets dropped.
	s := b.String()
	if !strings.Contains(s, "line-with-some-padding") {
		t.Fatal("nothing survived")
	}
	if b.Len() > 64 {
		t.Fatalf("Len = %d, past the 64-byte cap", b.Len())
	}
	if !strings.Contains(s, "elided") {
		t.Error("a truncated buffer must say it was truncated")
	}
}

// The buffer always starts at a line boundary, so the UI never renders half a
// line at the top of a log pane.
func TestTrimsToALineBoundary(t *testing.T) {
	b := New(32)
	b.WriteString("aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc\ndddddddddd\n")
	s := b.String()
	body := s[strings.Index(s, "\n")+1:] // past the elision note
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		if len(line) != 10 {
			t.Fatalf("partial line retained: %q", line)
		}
	}
}

// A single enormous line with no newline in it must not be able to grow the
// heap either.
func TestOneHugeLineIsBounded(t *testing.T) {
	b := New(64)
	b.WriteString(strings.Repeat("x", 10000))
	if b.Len() > 64 {
		t.Fatalf("Len = %d", b.Len())
	}
}

func TestTail(t *testing.T) {
	b := New(0)
	for i := 0; i < 10; i++ {
		b.WriteString("line\n")
	}
	got := strings.Count(b.Tail(3), "line")
	if got != 3 {
		t.Fatalf("Tail(3) has %d lines, want 3", got)
	}
	if strings.Count(b.Tail(100), "line") != 10 {
		t.Error("Tail past the end must return everything")
	}
	if b.Tail(0) != "" {
		t.Error("Tail(0) must be empty")
	}
}

func TestLastLine(t *testing.T) {
	b := New(0)
	b.WriteString("first\nsecond\n")
	if got := b.LastLine(); got != "second" {
		t.Fatalf("LastLine = %q", got)
	}
}

// Gen is what lets the UI's render tick skip the whole render — and the cgo
// calls inside it — when the buffer has not moved.
func TestGenMovesOnlyOnWrite(t *testing.T) {
	b := New(0)
	g := b.Gen()
	if b.Gen() != g {
		t.Fatal("Gen moved without a write")
	}
	b.WriteString("x")
	if b.Gen() == g {
		t.Fatal("Gen did not move on a write")
	}
}

// One writer draining a pipe, one reader on the GTK main thread.
func TestConcurrentWriteAndRead(t *testing.T) {
	b := New(4096)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			b.WriteString("some output line\n")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			_ = b.Tail(40)
			_ = b.Gen()
			_ = b.LastLine()
		}
	}()
	wg.Wait()
	if b.Len() > 4096 {
		t.Fatalf("Len = %d", b.Len())
	}
}

// TailBytes is the sidecar's budget: bytes, not lines. Tail(8<<10) silently
// returned the entire buffer, which is how a bounded sidecar field stopped
// being bounded.
func TestTailBytesHonoursAByteBudget(t *testing.T) {
	b := New(0)
	for i := 0; i < 5000; i++ {
		b.WriteString("this is a fairly ordinary log line of output\n")
	}
	got := b.TailBytes(8 << 10)
	if len(got) > 8<<10 {
		t.Errorf("TailBytes(8192) returned %d bytes, over budget", len(got))
	}
	if len(got) == 0 {
		t.Fatal("TailBytes returned nothing")
	}
	if got[0] == '\n' || !strings.HasSuffix(got, "\n") {
		t.Error("TailBytes must start at a line boundary and keep the final newline")
	}
	// It has to be the END of the log, not the start.
	if !strings.HasPrefix(got, "this is") {
		t.Errorf("unexpected first line %q", got[:20])
	}
}

func TestTailBytesReturnsAllOfAShortBuffer(t *testing.T) {
	b := New(0)
	b.WriteString("one\ntwo\n")
	if got := b.TailBytes(8 << 10); got != "one\ntwo\n" {
		t.Errorf("TailBytes = %q, want the whole buffer", got)
	}
	if got := b.TailBytes(0); got != "" {
		t.Errorf("TailBytes(0) = %q, want empty", got)
	}
}

// LastLine is documented as the final NON-EMPTY line; a trailing blank line
// used to reduce a failed job's badge to nothing.
func TestLastLineSkipsTrailingBlankLines(t *testing.T) {
	b := New(0)
	b.WriteString("[graphify watch] No code files found - nothing to rebuild.\n   \n\n")
	want := "[graphify watch] No code files found - nothing to rebuild."
	if got := b.LastLine(); got != want {
		t.Errorf("LastLine() = %q, want %q", got, want)
	}
}

func TestLastLineOfAnAllBlankBufferIsEmpty(t *testing.T) {
	b := New(0)
	b.WriteString("  \n\t\n\n")
	if got := b.LastLine(); got != "" {
		t.Errorf("LastLine() = %q, want empty", got)
	}
}

// BenchmarkWriteLinesPastCapacity is the shape that actually happens: a verbose
// subprocess writing line by line into a buffer that is already full. Trimming
// to exactly cap made every one of these writes a full-buffer memmove; the
// low-water mark amortizes it over the next quarter of the buffer.
func BenchmarkWriteLinesPastCapacity(b *testing.B) {
	buf := New(DefaultCap)
	line := []byte(strings.Repeat("x", 63) + "\n")
	// Fill it first, so the benchmark measures the steady state and not the
	// free writes before the first eviction.
	for buf.Len() < DefaultCap {
		buf.Write(line)
	}
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(line)
	}
}

// A carriage-return progress stream is a redraw of one line, not an
// ever-growing line. graft's --deep pass emits nothing else for tens of
// minutes; appended verbatim it filled the whole budget and then evicted
// every real line of output along with itself.
func TestCarriageReturnRedrawsOneLine(t *testing.T) {
	b := New(0)
	b.WriteString("$ graft build --deep /home/dns/git/pulumi\n")
	for i := 1; i <= 444; i++ {
		b.WriteString("\rreading concepts " + itoa(i) + "/444: some/package/file.go        ")
	}
	got := b.String()
	if n := strings.Count(got, "reading concepts"); n != 1 {
		t.Errorf("progress kept %d times, want 1 redrawn line:\n%q", n, got)
	}
	if !strings.HasPrefix(got, "$ graft build --deep") {
		t.Errorf("the command header did not survive the progress stream: %q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "444/444") {
		t.Error("the retained progress line is not the latest one")
	}
	if b.Len() > 1<<10 {
		t.Errorf("Len = %d; a redrawn line must not grow the buffer", b.Len())
	}
}

// CRLF terminates a line; it does not redraw it. Including when the pair is
// split across two writes, which is what a pipe does at a 64 KiB boundary.
func TestCarriageReturnBeforeNewlineIsATerminator(t *testing.T) {
	b := New(0)
	b.WriteString("first\r\nsecond\r")
	b.WriteString("\nthird\n")
	if got, want := b.String(), "first\nsecond\nthird\n"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

// The case job 203 died on: a failing run whose output carries no newline at
// all must still leave its tail behind. Evicting the whole buffer recorded
// the failure with an empty log.
func TestOverflowOfOneHugeLineKeepsTheTail(t *testing.T) {
	b := New(1024)
	b.WriteString(strings.Repeat("x", 4096) + "the deep pass did not complete")
	got := b.String()
	if got == "" {
		t.Fatal("the whole buffer was discarded; a failed job would record no log")
	}
	if b.Len() > 1024 {
		t.Errorf("Len = %d, over cap", b.Len())
	}
	if !strings.HasSuffix(got, "the deep pass did not complete") {
		t.Errorf("kept the head rather than the tail: %q", got[max(0, len(got)-60):])
	}
}

// And the sidecar, which is where the empty log was actually observed.
func TestTailBytesSurvivesANewlineFreeStream(t *testing.T) {
	b := New(0)
	b.WriteString("$ graft build --deep /repo\n")
	for i := 0; i < 20000; i++ {
		b.WriteString("\rsummarizing " + itoa(i) + "/20000: " + strings.Repeat("y", 60))
	}
	if got := b.TailBytes(8 << 10); got == "" {
		t.Fatal("TailBytes returned nothing to persist")
	}
	if got := b.LastLine(); !strings.HasPrefix(got, "summarizing 19999/") {
		t.Errorf("LastLine = %q, want the newest progress line", ellipsis(got))
	}
}

func ellipsis(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
