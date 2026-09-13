// Package applog is the application's own log, as opposed to a job's.
//
// A job's stdout belongs to that job and lives in its ringbuf; this is
// everything ggraphify itself has to say — a scan that failed, a version
// probe, a job submitted or refused, a preference that could not be written.
// Until now that went to stderr, which is nowhere at all when the board was
// started from fuzzel rather than from a terminal.
//
// So it is kept in memory, bounded, and rendered in the bottom panel with one
// button that copies the whole thing plus a machine description — which is the
// artefact you actually want to hand to somebody, or to an agent, when asking
// why the board did what it did.
//
// Everything here is safe for concurrent use: workers log from goroutines and
// the GTK main thread renders on a tick.
package applog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultMax is how many entries are retained. Entries are small and the
// board is not chatty; a couple of thousand covers a long session and costs a
// few hundred kilobytes at worst.
const DefaultMax = 2000

// Level is how loud one entry is.
type Level int

// The levels, in increasing order of "you probably want to read this".
const (
	Debug Level = iota
	Info
	Warn
	Error
)

// String is the fixed-width tag the rendered line carries, so a column of
// entries lines up without a text tag table.
func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Warn:
		return "warn "
	case Error:
		return "error"
	default:
		return "info "
	}
}

// Entry is one line.
type Entry struct {
	Time  time.Time
	Level Level
	Msg   string
}

// String renders an entry the way both the pane and the clipboard show it.
func (e Entry) String() string {
	return e.Time.Format("15:04:05.000") + "  " + e.Level.String() + "  " + e.Msg
}

// Log is a bounded FIFO of entries with an optional mirror.
type Log struct {
	mu      sync.Mutex
	entries []Entry
	max     int
	gen     uint64
	dropped int
	mirror  io.Writer
}

// New returns a log retaining at most max entries. A non-positive max uses
// DefaultMax.
func New(max int) *Log {
	if max <= 0 {
		max = DefaultMax
	}
	return &Log{max: max}
}

// Default is the log the whole application writes to. A package-level sink is
// the right shape here precisely because the alternative — plumbing a logger
// through discover, graphstate, jobs and store — would make every one of
// those packages take a dependency for the sake of a diagnostic.
var Default = func() *Log {
	l := New(DefaultMax)
	l.SetMirror(os.Stderr)
	return l
}()

// SetMirror sends every entry to w as it arrives, in the same rendering the
// pane uses. main.go points it at stderr so a board started from a terminal
// still prints; nil turns mirroring off.
func (l *Log) SetMirror(w io.Writer) {
	l.mu.Lock()
	l.mirror = w
	l.mu.Unlock()
}

// Add records one entry.
func (l *Log) Add(level Level, msg string) {
	e := Entry{Time: time.Now(), Level: level, Msg: strings.TrimRight(msg, "\n")}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	if len(l.entries) > l.max {
		// Drop the oldest quarter rather than one per write: a slice shifted
		// on every append is quadratic, and the count is what the pane's
		// elision note reports anyway.
		cut := l.max / 4
		if cut < 1 {
			cut = 1
		}
		l.dropped += cut
		l.entries = append(l.entries[:0], l.entries[cut:]...)
	}
	l.gen++
	w := l.mirror
	l.mu.Unlock()

	if w != nil {
		fmt.Fprintln(w, e.String())
	}
}

// Logf records a formatted entry.
func (l *Log) Logf(level Level, format string, args ...any) {
	l.Add(level, fmt.Sprintf(format, args...))
}

// The four shorthands, which is what call sites actually use.
func (l *Log) Debugf(format string, args ...any) { l.Logf(Debug, format, args...) }
func (l *Log) Infof(format string, args ...any)  { l.Logf(Info, format, args...) }
func (l *Log) Warnf(format string, args ...any)  { l.Logf(Warn, format, args...) }
func (l *Log) Errorf(format string, args ...any) { l.Logf(Error, format, args...) }

// Write makes a Log an io.Writer, so `log.SetOutput(applog.Default)` folds
// everything the standard logger is handed into the same stream. Each line
// becomes one entry; main.go clears the standard logger's own flags and
// prefix, because this rendering already carries a timestamp.
func (l *Log) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		level, msg := classify(line)
		l.Add(level, msg)
	}
	return len(p), nil
}

// classify reads a line's own idea of how important it is.
//
// It exists for one caller in practice: GTK. A GTK application is handed
// glib's structured log lines — "INFO Vulkan: Loader Message: … priority=6
// code_file=… glib_domain=Gdk syslog_identifier=ggraphify" — and a Vulkan
// loader that narrates its device enumeration at INFO would otherwise push
// forty entries of nothing into a bounded log before the board has drawn its
// first frame. Those become Debug, which the pane hides unless asked; a
// WARNING or a CRITICAL from the same source is the opposite of noise and
// keeps its level.
//
// The structured tail is dropped for the same reason: none of it survives
// contact with a human reading a log pane, and all of it is still in the
// journal for anybody who wants it.
func classify(line string) (Level, string) {
	msg := line
	if isGLib(line) {
		if i := strings.Index(msg, " priority="); i > 0 {
			msg = msg[:i]
		}
	}
	switch {
	case hasWord(line, "DEBUG"), hasWord(line, "INFO"):
		return Debug, msg
	case hasWord(line, "WARN"), hasWord(line, "WARNING"):
		return Warn, msg
	case hasWord(line, "CRITICAL"), hasWord(line, "ERROR"):
		return Error, msg
	}
	return Info, msg
}

// isGLib reports whether a line carries glib's structured fields, which is
// what makes trimming the tail safe rather than a guess about somebody else's
// message.
func isGLib(line string) bool {
	return strings.Contains(line, "glib_domain=") || strings.Contains(line, "syslog_identifier=")
}

// hasWord reports whether the line opens with a bare level word. Matching a
// prefix alone would classify "INFORMATION about the graph" as debug.
func hasWord(line, word string) bool {
	if !strings.HasPrefix(line, word) {
		return false
	}
	rest := line[len(word):]
	return rest == "" || rest[0] == ' ' || rest[0] == ':'
}

// Entries is a copy of what is retained, oldest first.
func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry(nil), l.entries...)
}

// Gen is bumped on every entry. The pane's tick compares it against the last
// one it rendered and skips the render — and the cgo calls inside it — when
// the log has not moved.
func (l *Log) Gen() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen
}

// Len is the number of retained entries.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Dropped is how many entries have been evicted, for the elision note.
func (l *Log) Dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// Text is everything retained, which is what the copy button copies below its
// header: a diagnostic that hid half of itself would be worse than useless.
func (l *Log) Text() string { return l.TextFrom(Debug) }

// TextFrom is the log from one level up, which is what the pane renders. The
// hidden entries are still there — turning the pane's verbose switch on, or
// copying, brings them back.
func (l *Log) TextFrom(min Level) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	if l.dropped > 0 && min == Debug {
		fmt.Fprintf(&b, "… %d earlier entries elided …\n", l.dropped)
	}
	for _, e := range l.entries {
		if e.Level < min {
			continue
		}
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// Clear empties the log, keeping the generation counter moving so a pane
// rendering from it repaints.
func (l *Log) Clear() {
	l.mu.Lock()
	l.entries = nil
	l.dropped = 0
	l.gen++
	l.mu.Unlock()
}

// The package-level shorthands, against Default.
func Debugf(format string, args ...any) { Default.Debugf(format, args...) }
func Infof(format string, args ...any)  { Default.Infof(format, args...) }
func Warnf(format string, args ...any)  { Default.Warnf(format, args...) }
func Errorf(format string, args ...any) { Default.Errorf(format, args...) }
