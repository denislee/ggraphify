package applog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAddClipsLongMessages checks a message longer than MaxEntryBytes is cut at
// the cap, reports exactly how many bytes were removed, and stays close to the
// cap in size.
func TestAddClipsLongMessages(t *testing.T) {
	l := New(100)
	l.Add(Info, strings.Repeat("x", 10000))

	entries := l.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(Entries()) = %d, want 1", len(entries))
	}
	got := entries[0].Msg
	if !strings.HasPrefix(got, strings.Repeat("x", MaxEntryBytes)) {
		t.Fatalf("Msg does not start with %d unclipped bytes", MaxEntryBytes)
	}
	if !strings.Contains(got, "[5904 bytes clipped]") {
		t.Fatalf("Msg = %q; want substring %q", got, "[5904 bytes clipped]")
	}
	if len(got) >= MaxEntryBytes+64 {
		t.Fatalf("len(Msg) = %d, want < %d", len(got), MaxEntryBytes+64)
	}
}

// TestAddKeepsShortMessagesVerbatim checks messages at or under the cap are
// stored unchanged.
func TestAddKeepsShortMessagesVerbatim(t *testing.T) {
	l := New(100)
	short := strings.Repeat("a", 100)
	exact := strings.Repeat("b", MaxEntryBytes)
	l.Add(Info, short)
	l.Add(Info, exact)

	entries := l.Entries()
	if len(entries) != 2 {
		t.Fatalf("len(Entries()) = %d, want 2", len(entries))
	}
	if entries[0].Msg != short {
		t.Fatalf("short message changed: got %d bytes, want %d", len(entries[0].Msg), len(short))
	}
	if entries[1].Msg != exact {
		t.Fatalf("exact-cap message changed: got %d bytes, want %d", len(entries[1].Msg), len(exact))
	}
}

// TestClipRespectsUTF8 checks a clip through multibyte runes lands on a rune
// boundary and never exceeds the byte cap.
func TestClipRespectsUTF8(t *testing.T) {
	l := New(100)
	l.Add(Info, strings.Repeat("é", 3000))

	entries := l.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(Entries()) = %d, want 1", len(entries))
	}
	got := entries[0].Msg
	i := strings.Index(got, " … [")
	if i < 0 {
		t.Fatalf("Msg = %q; want clip marker %q", got, " … [")
	}
	prefix := got[:i]
	if !utf8.ValidString(prefix) {
		t.Fatalf("clipped prefix is not valid UTF-8: %q", prefix)
	}
	if len(prefix) > MaxEntryBytes {
		t.Fatalf("clipped prefix is %d bytes, want <= %d", len(prefix), MaxEntryBytes)
	}
}

// TestWriteClipsEachLine checks Write turns each newline-delimited line into its
// own entry and clips only the ones over the cap.
func TestWriteClipsEachLine(t *testing.T) {
	l := New(100)
	short := "hello world"
	if _, err := l.Write([]byte(short + "\n" + strings.Repeat("y", 9000) + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries := l.Entries()
	if len(entries) != 2 {
		t.Fatalf("len(Entries()) = %d, want 2", len(entries))
	}
	if entries[0].Msg != short {
		t.Fatalf("first entry = %q, want %q", entries[0].Msg, short)
	}
	if !strings.Contains(entries[1].Msg, "bytes clipped") {
		t.Fatalf("second entry = %q; want it clipped", entries[1].Msg)
	}
}
