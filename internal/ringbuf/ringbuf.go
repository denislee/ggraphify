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
	"unsafe"
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
	// pendingCR remembers a chunk that ended on a carriage return, so a CRLF
	// split across two writes is still read as one line terminator rather
	// than as a redraw of the line it terminates.
	pendingCR bool
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
	n := len(p)
	b.gen++

	// A carriage return is a redraw, not text.
	//
	// graft's --deep pass reports progress the way a terminal expects it: one
	// "\rreading concepts 41/444: some/file.go" per file, no newline anywhere
	// in the stream. Appended verbatim, a 40-minute run over a large checkout
	// is megabytes of one single line — and a single line is exactly what the
	// eviction below cannot trim, so it used to throw the ENTIRE buffer away
	// on overflow. That is how job 203 (`graft build --deep` on pulumi, exit
	// 1 after 42 minutes) came to be recorded with no log at all: not one
	// byte of the failure survived, not even the "$ …" command header, and
	// the board could only report the command back. A cancelled run of the
	// same shape kept nothing but its own 60-byte cancellation notice.
	//
	// So the return is honoured here the way a terminal honours it: it erases
	// the unterminated line it returns to. A progress stream then occupies
	// one line rather than the whole budget, the header and every real line
	// of output stay put, and Tail/TailBytes/FirstErrorLine downstream see
	// something a human can read instead of one megabyte-long "line".
	if b.pendingCR {
		b.pendingCR = false
		// "\r\n" split across two writes: a terminator, so leave the line.
		if len(p) == 0 || p[0] != '\n' {
			b.eraseLine()
		}
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\r')
		if i < 0 {
			b.put(p)
			break
		}
		b.put(p[:i])
		p = p[i+1:]
		switch {
		case len(p) == 0:
			// Decided on the next write, which knows whether an '\n' follows.
			b.pendingCR = true
		case p[0] == '\n':
			b.put(newline)
			p = p[1:]
			continue
		default:
			b.eraseLine()
			continue
		}
		break
	}
	return n, nil
}

// newline is put's argument for a CRLF folded to LF, as a package-level slice
// so that path does not allocate.
var newline = []byte{'\n'}

// put appends q, evicting whole lines off the front FIRST so that neither the
// retained text nor the backing array ever exceeds cap. Caller holds mu.
//
// Appending first and trimming afterwards — what Write used to do — kept the
// bytes within cap but not the array: append grows by doubling, so a buffer
// that ran past cap once held an array of up to twice cap for the rest of its
// life, and copy-down never gives it back. Two hundred retired jobs is two
// hundred such arrays.
//
// The trim itself is unchanged: the concatenation of buf and q is cut to the
// first newline at or after the low-water mark, or at the mark itself when
// what is retained is one enormous line.
func (b *Buf) put(q []byte) {
	if len(q) == 0 {
		return
	}
	need := len(b.buf) + len(q)
	if need > b.cap {
		// over indexes the virtual concatenation buf+q.
		over := need - b.lowWater()
		if over < len(b.buf) {
			if i := bytes.IndexByte(b.buf[over:], '\n'); i >= 0 {
				over += i + 1
			} else if i := bytes.IndexByte(q, '\n'); i >= 0 {
				over = len(b.buf) + i + 1
			}
		} else if i := bytes.IndexByte(q[over-len(b.buf):], '\n'); i >= 0 {
			over += i + 1
		}
		if over <= len(b.buf) {
			b.dropped += bytes.Count(b.buf[:over], newline)
			// Copy down rather than reslice: reslicing keeps the original
			// backing array alive forever, which defeats the point.
			m := copy(b.buf, b.buf[over:])
			b.buf = b.buf[:m]
		} else {
			k := over - len(b.buf)
			b.dropped += bytes.Count(b.buf, newline) + bytes.Count(q[:k], newline)
			b.buf = b.buf[:0]
			q = q[k:]
		}
		need = len(b.buf) + len(q)
	}
	// Grow by hand so the array is capped at cap rather than at whatever
	// append's doubling lands on.
	if need > cap(b.buf) {
		c := 2 * cap(b.buf)
		if c < need {
			c = need
		}
		if c > b.cap {
			c = b.cap
		}
		nb := make([]byte, len(b.buf), c)
		copy(nb, b.buf)
		b.buf = nb
	}
	b.buf = append(b.buf, q...)
}

// Compact keeps at most the newest n bytes, cut at a line boundary the way
// TailBytes cuts, in a slice of exactly that size — releasing whatever larger
// array the buffer grew while its job was running. The runner calls it when a
// job retires: a finished job's log is read, not written, and two hundred of
// them at full size is the board's largest idle allocation. Lines it drops
// count towards the elision note like any other eviction. A non-positive n is
// a no-op. Writing after Compact is allowed; cap is unchanged.
func (b *Buf) Compact(n int) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cut := 0
	if len(b.buf) > n {
		cut = len(b.buf) - n
		if b.buf[cut-1] != '\n' {
			if i := bytes.IndexByte(b.buf[cut:], '\n'); i >= 0 {
				cut += i + 1
			}
		}
	}
	if cut == 0 && cap(b.buf) == len(b.buf) {
		return
	}
	var nb []byte
	if rest := len(b.buf) - cut; rest > 0 {
		nb = make([]byte, rest)
		copy(nb, b.buf[cut:])
	}
	if cut > 0 {
		b.dropped += bytes.Count(b.buf[:cut], newline)
		b.gen++
	}
	b.buf = nb
}

// eraseLine drops the unterminated line at the end of the buffer — what a
// carriage return does to the line it returns to, once something is written
// over it. Caller holds mu.
func (b *Buf) eraseLine() {
	if i := bytes.LastIndexByte(b.buf, '\n'); i >= 0 {
		b.buf = b.buf[:i+1]
		return
	}
	b.buf = b.buf[:0]
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
//
// The bytes are viewed in place rather than copied by []byte(s): Write only
// reads p and copies what it keeps, so the view is never written through and
// never outlives the call.
func (b *Buf) WriteString(s string) (int, error) {
	return b.Write(unsafe.Slice(unsafe.StringData(s), len(s)))
}

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
