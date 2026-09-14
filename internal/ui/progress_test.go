package ui

import (
	"math"
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/ringbuf"
)

// The parser reads a fraction out of one line of a subprocess's output. What
// it must not do is invent one: a line with a version, a date or a path in it
// looks like a ratio and is not one.
func TestParseProgress(t *testing.T) {
	cases := []struct {
		line string
		frac float64
		text string
		ok   bool
	}{
		{"extract  42%", 0.42, "42%", true},
		{"embedding 7.5 %", 0.075, "7%", true},
		{"[3/17] communities", 3.0 / 17, "3/17", true},
		{"wrote file 3 of 4", 0.75, "3/4", true},
		{"2026/09/13 12:00:00 [3/17] communities", 3.0 / 17, "3/17", true},
		{"100%", 1, "100%", true},
		// Negative controls.
		{"scanning internal/ui", 0, "", false},
		{"go1.24 linux/amd64", 0, "", false},
		{"served 200/0 requests", 0, "", false},
		{"progress: 420%", 0, "", false},
		{"", 0, "", false},
	}
	for _, c := range cases {
		frac, text, ok := parseProgress(c.line)
		if ok != c.ok {
			t.Errorf("parseProgress(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(frac-c.frac) > 0.0001 {
			t.Errorf("parseProgress(%q) frac = %v, want %v", c.line, frac, c.frac)
		}
		if text != c.text {
			t.Errorf("parseProgress(%q) text = %q, want %q", c.line, text, c.text)
		}
	}
}

// Only a running job has progress, and only the newest line that carries a
// number counts — a stale figure further up the log must not outrank it.
func TestJobProgress(t *testing.T) {
	buf := ringbuf.New(0)
	buf.WriteString("extract 10%\nextract 80%\nlinking nodes\n")
	run := &jobs.Snapshot{Status: jobs.Running, Log: buf}
	frac, text, ok := jobProgress(run)
	if !ok || text != "80%" || math.Abs(frac-0.8) > 0.0001 {
		t.Fatalf("jobProgress = (%v, %q, %v), want (0.8, \"80%%\", true)", frac, text, ok)
	}

	done := &jobs.Snapshot{Status: jobs.Succeeded, Log: buf}
	if _, _, ok := jobProgress(done); ok {
		t.Error("a finished job still reports progress")
	}
	if _, _, ok := jobProgress(&jobs.Snapshot{Status: jobs.Running}); ok {
		t.Error("a job with no log still reports progress")
	}
	if _, _, ok := jobProgress(nil); ok {
		t.Error("a nil job still reports progress")
	}
	quiet := &jobs.Snapshot{Status: jobs.Running, Log: ringbuf.New(0)}
	quiet.Log.WriteString("working\n")
	if n := progressNote(quiet); n != "" {
		t.Errorf("progressNote on a silent job = %q, want empty", n)
	}
}

// The bar is styled, not decorative: an undefined class is an invisible
// rendering bug.
func TestJobProgressClassDefined(t *testing.T) {
	if !strings.Contains(boardCSS, ".jobprogress") {
		t.Error("the jobprogress class is not defined in the stylesheet")
	}
}
