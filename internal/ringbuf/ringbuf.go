// Package ringbuf is a bounded, line-oriented byte buffer.
//
// A graphify run can emit an unbounded amount of stdout — a debug extraction
// over a monorepo will happily produce tens of megabytes — and the UI only
// ever renders the tail of it. Keeping the whole stream would grow the heap
// in proportion to how chatty the subprocess is, which is not a property a
// desktop application should have.
//
// Buf therefore keeps at most Cap bytes, dropping whole lines off the front.
// It is safe for concurrent use: one writer goroutine draining a pipe, one
// reader on the GTK main thread rendering it on a tick.
package ringbuf

import (
	"bytes"
	"strings"
	"sync"
)

// DefaultCap is the per-job budget: enough that a failing run's traceback is
// still intact, small enough that 128 of them cost 32 MB in the worst case
// and, in practice, nothing at all.
const DefaultCap = 256 << 10

// Buf is a bounded FIFO of text.
type Buf struct {
	mu      sync.Mutex
	buf     []byte
	cap     int
	dropped int // lines evicted off the front, for the "…N lines elided" note
	gen     uint64
}

// New returns a buffer holding at most capBytes. A non-positive cap uses
// DefaultCap.
func New(capBytes int) *Buf {
	if capBytes <= 0 {
		capBytes = DefaultCap
	}
	return &Buf{cap: capBytes}
}

// Write appends p, evicting whole lines off the front to stay within cap.
// It never fails and never blocks on anything but its own mutex, so a
// subprocess cannot be back-pressured by a slow UI.
//
// Eviction trims down to a LOW-WATER MARK rather than to exactly cap, and that
// is the whole performance story of this package. Reclaiming exactly the
// overflow means the very next write overflows again, so past capacity every
// single write paid a memmove of up to cap — 256 KiB by default — to make room
// for one line. graphify is verbose: a 50 MB log arriving line by line is tens
// of thousands of full-buffer compactions per job, multiplied by the number of
// lanes. Freeing a quarter of the buffer instead amortizes that one memmove
// over the next cap/4 bytes written, which for line-sized writes is a three
// order of magnitude reduction and costs one extra constant.
//
// Len stays within cap throughout, as documented: the mark is below cap, never
// above it.
func (b *Buf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	b.gen++
	if len(b.buf) <= b.cap {
		return len(p), nil
	}
	// Trim to the first newline at or after the low-water mark, so the buffer
	// always starts at a line boundary and the UI never renders half a line.
	over := len(b.buf) - b.lowWater()
	if over < 0 {
		over = 0
	}
	if i := bytes.IndexByte(b.buf[over:], '\n'); i >= 0 {
		over += i + 1
	} else {
		over = len(b.buf) // the whole thing is one enormous line; drop it
	}
	b.dropped += bytes.Count(b.buf[:over], []byte{'\n'})
	// Copy down rather than reslice: reslicing keeps the original backing
	// array alive forever, which defeats the point of a bounded buffer.
	n := copy(b.buf, b.buf[over:])
	b.buf = b.buf[:n]
	return len(p), nil
}

// trimFraction is how much of the buffer an eviction reclaims, as a divisor:
// 4 means "drop a quarter". Larger wastes less of the retained tail and
// compacts more often; smaller is the reverse. A quarter is the point where the
// amortized cost is already negligible and the retained log is still within a
// screenful of the budget the user was promised.
const trimFraction = 4

// lowWater is the size an eviction trims down to. Caller holds mu.
//
// Never zero and never cap: a buffer that trimmed to cap would evict on every
// write (the case this exists to avoid), and one that trimmed to zero would
// throw the whole log away to make room for one line.
func (b *Buf) lowWater() int {
	w := b.cap - b.cap/trimFraction
	if w < 1 {
		w = 1
	}
	return w
}

// WriteString is Write for a string, without the conversion.
func (b *Buf) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

// String returns the retained text, prefixed with an elision note when lines
// have been dropped.
func (b *Buf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return string(b.buf)
	}
	var sb strings.Builder
	sb.Grow(len(b.buf) + 48)
	sb.WriteString("… ")
	sb.WriteString(itoa(b.dropped))
	sb.WriteString(" earlier lines elided …\n")
	sb.Write(b.buf)
	return sb.String()
}

// TailBytes returns at most n bytes from the end, cut at a line boundary so
// the result never begins mid-line.
//
// It is Tail's counterpart for a caller with a byte budget rather than a line
// count — the sidecar, which is sizing a JSON field and has no opinion about
// how many lines fit in it. Passing a byte budget to Tail instead is a silent
// no-op: 8192 lines is the whole buffer several times over.
func (b *Buf) TailBytes(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.buf) == 0 {
		return ""
	}
	if len(b.buf) <= n {
		return string(b.buf)
	}
	cut := len(b.buf) - n
	// Advance to just past the next newline, so the tail starts at a line
	// boundary. If the final n bytes hold no newline at all the tail is one
	// partial enormous line; returning it verbatim is better than returning
	// nothing, and it is already within budget.
	if i := bytes.IndexByte(b.buf[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	return string(b.buf[cut:])
}

// Tail returns at most n lines from the end. The UI's steady-state render.
func (b *Buf) Tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.buf) == 0 {
		return ""
	}
	cut := len(b.buf)
	if cut > 0 && b.buf[cut-1] == '\n' {
		cut-- // a trailing newline is not a line of its own
	}
	seen := 0
	for i := cut - 1; i >= 0; i-- {
		if b.buf[i] != '\n' {
			continue
		}
		seen++
		if seen == n {
			return string(b.buf[i+1:])
		}
	}
	return string(b.buf)
}

// LastLine returns the final non-empty line, which is what a row badge shows
// for a failed job.
func (b *Buf) LastLine() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Walk back over blank and whitespace-only lines rather than stopping at
	// the last one. A subprocess that signs off with a trailing blank line —
	// and graphify does, on several paths — would otherwise reduce the badge
	// to an empty string and hide the very line that says why it failed.
	s := strings.TrimRight(string(b.buf), "\n")
	for s != "" {
		line := s
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			line, s = s[i+1:], s[:i]
		} else {
			s = ""
		}
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Gen is a counter bumped on every write. The UI's render tick compares it
// against the last one it drew and skips the whole render — and the cgo calls
// inside it — when the buffer has not moved.
func (b *Buf) Gen() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

// Len is the number of retained bytes.
func (b *Buf) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
