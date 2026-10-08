package ringbuf

import (
	"fmt"
	"strings"
	"testing"
)

// TestWriteNeverGrowsBackingArrayPastCap hammers a buffer with lines of many
// shapes — including single writes larger than the cap, unterminated lines and
// CR progress redraws — and checks after every write that neither the retained
// bytes nor the backing array ever exceed cap.
func TestWriteNeverGrowsBackingArrayPastCap(t *testing.T) {
	for _, capBytes := range []int{64, 1000, 4096} {
		capBytes := capBytes
		t.Run(fmt.Sprintf("cap=%d", capBytes), func(t *testing.T) {
			b := New(capBytes)
			inputs := []string{
				"short\n",
				strings.Repeat("a", capBytes+10) + "\n", // one write larger than cap
				"no-newline-at-all",
				"\rprogress 5/100",
				"\rprogress 6/100",
				"line one\nline two\n",
				strings.Repeat("z", capBytes) + "\n",
				"\r",
				"\r\n",
				"with\rcarriage\rreturns\n",
				strings.Repeat("q", capBytes+1), // larger than cap, no newline
			}
			for i := 0; i < 50; i++ {
				for _, in := range inputs {
					if _, err := b.Write([]byte(in)); err != nil {
						t.Fatalf("write %d: unexpected error: %v", i, err)
					}
					if len(b.buf) > b.cap {
						t.Fatalf("write %d: len(buf)=%d exceeds cap=%d", i, len(b.buf), b.cap)
					}
					if cap(b.buf) > b.cap {
						t.Fatalf("write %d: cap(buf)=%d exceeds cap=%d", i, cap(b.buf), b.cap)
					}
				}
			}
		})
	}
}

// TestCompactKeepsTailAtLineBoundary writes 100 fixed-width lines, compacts to
// 40 bytes and checks the retained text is exactly the last five lines, cut at
// a line boundary, in an exactly-sized slice, with the elision note counting
// the 95 dropped lines.
func TestCompactKeepsTailAtLineBoundary(t *testing.T) {
	b := New(0)
	for i := 0; i < 100; i++ {
		b.WriteString(fmt.Sprintf("line-%02d\n", i))
	}

	b.Compact(40)

	if got := b.Len(); got > 40 {
		t.Fatalf("Len() = %d, want <= 40", got)
	}
	var want strings.Builder
	for i := 95; i < 100; i++ {
		want.WriteString(fmt.Sprintf("line-%02d\n", i))
	}
	if got := b.TailBytes(1 << 20); got != want.String() {
		t.Fatalf("tail = %q, want %q", got, want.String())
	}
	if cap(b.buf) != len(b.buf) {
		t.Fatalf("cap(buf)=%d, len(buf)=%d; want equal", cap(b.buf), len(b.buf))
	}
	const note = "… 95 earlier lines elided …\n"
	if got := b.String(); !strings.HasPrefix(got, note) {
		t.Fatalf("String() = %q, want prefix %q", got, note)
	}
}

// TestCompactShrinksBackingArrayWithoutDroppingText checks that a compaction
// with a budget larger than the content only re-sizes the backing array: the
// text is byte-for-byte identical and Gen is untouched because nothing was
// dropped.
func TestCompactShrinksBackingArrayWithoutDroppingText(t *testing.T) {
	b := New(0)
	for i := 0; i < 10; i++ {
		b.WriteString(fmt.Sprintf("line-%02d\n", i))
	}

	text := b.TailBytes(1 << 20)
	g := b.Gen()

	b.Compact(1 << 20)

	if got := b.TailBytes(1 << 20); got != text {
		t.Fatalf("tail changed after compact: got %q, want %q", got, text)
	}
	if cap(b.buf) != len(b.buf) {
		t.Fatalf("cap(buf)=%d, len(buf)=%d; want equal", cap(b.buf), len(b.buf))
	}
	if got := b.Gen(); got != g {
		t.Fatalf("Gen() = %d, want %d (no content change)", got, g)
	}
}

// TestCompactBumpsGenWhenItDrops checks the generation counter moves whenever a
// compact actually evicts lines, so a render tick notices.
func TestCompactBumpsGenWhenItDrops(t *testing.T) {
	b := New(0)
	for i := 0; i < 100; i++ {
		b.WriteString(fmt.Sprintf("line-%02d\n", i))
	}

	g := b.Gen()
	before := b.Len()
	b.Compact(40)

	if b.Len() >= before {
		t.Fatalf("Len() = %d, want < %d (lines should be dropped)", b.Len(), before)
	}
	if got := b.Gen(); got == g {
		t.Fatalf("Gen() = %d, want a bump from %d", got, g)
	}
}

// TestCompactNonPositiveIsNoOp checks a zero or negative budget leaves the
// retained length and text untouched.
func TestCompactNonPositiveIsNoOp(t *testing.T) {
	b := New(0)
	for i := 0; i < 20; i++ {
		b.WriteString(fmt.Sprintf("line-%02d\n", i))
	}

	for _, n := range []int{0, -1} {
		before := b.Len()
		text := b.TailBytes(1 << 20)
		g := b.Gen()

		b.Compact(n)

		if got := b.Len(); got != before {
			t.Fatalf("Compact(%d): Len() = %d, want %d", n, got, before)
		}
		if got := b.TailBytes(1 << 20); got != text {
			t.Fatalf("Compact(%d): tail changed", n)
		}
		if got := b.Gen(); got != g {
			t.Fatalf("Compact(%d): Gen() = %d, want %d", n, got, g)
		}
	}
}

// TestCompactThenWriteStillBounded checks that writing after a compact still
// honours cap for both the retained bytes and the backing array.
func TestCompactThenWriteStillBounded(t *testing.T) {
	b := New(256)
	b.Compact(16)
	for i := 0; i < 1000; i++ {
		b.WriteString(fmt.Sprintf("line %d\n", i))
		if len(b.buf) > 256 {
			t.Fatalf("write %d: len(buf)=%d exceeds cap=256", i, len(b.buf))
		}
		if cap(b.buf) > 256 {
			t.Fatalf("write %d: cap(buf)=%d exceeds cap=256", i, cap(b.buf))
		}
	}
}

// TestWriteStringMatchesWrite checks WriteString and Write are byte-for-byte
// interchangeable, including CR redraws and a CRLF split across two calls.
func TestWriteStringMatchesWrite(t *testing.T) {
	chunks := []string{
		"hello",
		"\r",
		"\n",
		"world\rprogress",
		" 5/100",
		"\r\n",
		"tail\n",
		strings.Repeat("x", 500),
		"\r",
		"final\n",
	}
	viaWrite := New(64)
	viaString := New(64)
	for i, ch := range chunks {
		if _, err := viaWrite.Write([]byte(ch)); err != nil {
			t.Fatalf("Write chunk %d: %v", i, err)
		}
		viaString.WriteString(ch)
	}
	if got, want := viaString.TailBytes(1<<20), viaWrite.TailBytes(1<<20); got != want {
		t.Fatalf("WriteString tail = %q, Write tail = %q", got, want)
	}
	if got, want := viaString.String(), viaWrite.String(); got != want {
		t.Fatalf("WriteString String() = %q, Write String() = %q", got, want)
	}
}
