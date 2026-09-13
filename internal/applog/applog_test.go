package applog

import (
	"log"
	"strings"
	"testing"
)

func TestAddAndText(t *testing.T) {
	l := New(10)
	l.Infof("hello %s", "world")
	l.Errorf("boom")
	txt := l.Text()
	if !strings.Contains(txt, "hello world") || !strings.Contains(txt, "boom") {
		t.Fatalf("entries missing from rendering:\n%s", txt)
	}
	if !strings.Contains(txt, "error") {
		t.Fatalf("level tag missing:\n%s", txt)
	}
	if l.Len() != 2 {
		t.Fatalf("Len = %d, want 2", l.Len())
	}
}

// The generation counter is what lets the pane skip a render, so a write that
// did not bump it would freeze the log on screen.
func TestGenMoves(t *testing.T) {
	l := New(10)
	before := l.Gen()
	l.Infof("x")
	if l.Gen() == before {
		t.Fatal("Gen did not move on a write")
	}
	// Clear has to move it too: the pane must repaint an emptied log.
	cleared := l.Gen()
	l.Clear()
	if l.Gen() == cleared {
		t.Fatal("Gen did not move on Clear")
	}
	if l.Len() != 0 {
		t.Fatalf("Clear left %d entries", l.Len())
	}
}

// The cap is the whole reason this is not a slice that grows forever.
func TestEviction(t *testing.T) {
	l := New(8)
	for i := 0; i < 100; i++ {
		l.Infof("entry %d", i)
	}
	if l.Len() > 8 {
		t.Fatalf("Len = %d, want at most 8", l.Len())
	}
	if l.Dropped() == 0 {
		t.Fatal("Dropped stayed at zero after eviction")
	}
	txt := l.Text()
	if !strings.Contains(txt, "entry 99") {
		t.Fatalf("newest entry evicted:\n%s", txt)
	}
	if !strings.Contains(txt, "elided") {
		t.Fatalf("elision note missing:\n%s", txt)
	}
}

// log.SetOutput(l) is how everything the standard logger is handed reaches the
// pane, and a multi-line write must not become one unreadable entry.
func TestWriteSplitsLines(t *testing.T) {
	l := New(10)
	if _, err := l.Write([]byte("one\ntwo\n\nthree\n")); err != nil {
		t.Fatal(err)
	}
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (blank lines dropped)", l.Len())
	}
}

func TestStandardLoggerIntegration(t *testing.T) {
	l := New(10)
	logger := log.New(l, "", 0)
	logger.Printf("store: %v", "disk full")
	if !strings.Contains(l.Text(), "store: disk full") {
		t.Fatalf("standard logger output did not arrive:\n%s", l.Text())
	}
}

// A mirror is how a board started from a terminal still prints.
func TestMirror(t *testing.T) {
	l := New(10)
	var sb strings.Builder
	l.SetMirror(&sb)
	l.Warnf("careful")
	if !strings.Contains(sb.String(), "careful") {
		t.Fatalf("mirror got %q", sb.String())
	}
}

// GTK narrates its Vulkan device enumeration at INFO. Forty of those before
// the first frame would evict everything worth keeping from a bounded log, so
// they land at Debug and the pane hides them by default.
func TestGLibNoiseIsDebug(t *testing.T) {
	l := New(50)
	l.Write([]byte("INFO Vulkan: Loader Message: linux_read_sorted_physical_devices: " +
		"priority=6 code_file=../gtk/gdk/gdkvulkancontext.c glib_domain=Gdk syslog_identifier=ggraphify\n"))
	l.Write([]byte("WARNING gtk_widget_measure: assertion failed priority=4 glib_domain=Gtk\n"))
	l.Infof("scan finished")

	quiet := l.TextFrom(Info)
	if strings.Contains(quiet, "Vulkan") {
		t.Errorf("loader noise survived the default filter:\n%s", quiet)
	}
	if !strings.Contains(quiet, "assertion failed") {
		t.Errorf("a GTK warning was filtered out with the noise:\n%s", quiet)
	}
	if !strings.Contains(quiet, "scan finished") {
		t.Errorf("an ordinary entry went missing:\n%s", quiet)
	}
	// The copy button takes everything, so nothing is actually lost.
	if !strings.Contains(l.Text(), "Vulkan") {
		t.Error("Text() dropped an entry it should still carry")
	}
	// The structured tail is trimmed off what a human reads.
	if strings.Contains(l.Text(), "syslog_identifier=") {
		t.Errorf("glib's structured tail was not trimmed:\n%s", l.Text())
	}
}

// A message that merely starts with those letters is not a level.
func TestClassifyDoesNotOverreach(t *testing.T) {
	if lvl, _ := classify("INFORMATION about the graph"); lvl != Info {
		t.Fatalf("level = %v, want Info", lvl)
	}
	if lvl, msg := classify("ERROR: boom"); lvl != Error || msg != "ERROR: boom" {
		t.Fatalf("level = %v, msg = %q", lvl, msg)
	}
	// Nothing is trimmed from a line that is not glib's.
	if _, msg := classify("job failed with priority=high"); msg != "job failed with priority=high" {
		t.Fatalf("trimmed a non-glib line: %q", msg)
	}
}

// GTK spells it both ways depending on which of its own loggers is talking,
// and a warning shown as "info" is a warning nobody reads.
func TestClassifyWarnSpellings(t *testing.T) {
	for _, line := range []string{
		"WARN Cannot get portal org.freedesktop.portal.Inhibit version",
		"WARNING gtk_widget_measure: assertion failed",
	} {
		if lvl, _ := classify(line); lvl != Warn {
			t.Errorf("classify(%q) = %v, want Warn", line, lvl)
		}
	}
}
