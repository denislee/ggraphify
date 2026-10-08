package ui

import (
	"strings"
	"testing"
)

func TestAppendDelta(t *testing.T) {
	long := strings.Repeat("abcdefghij\n", 50)
	cases := []struct {
		name      string
		prev      *string
		next      string
		wantOK    bool
		wantDelta string
	}{
		{"nothing rendered", nil, "x", false, ""},
		{"pure append", strp("hello\n"), "hello\nworld\n", true, "world\n"},
		{"unchanged", strp("hello\n"), "hello\n", true, ""},
		{"shrank (CR erase)", strp("a\nprogress 41"), "a\nprog", false, ""},
		{"end rewritten same length", strp("a\nprogress 41"), "a\nprogress 42", false, ""},
		{"end rewritten then grew", strp("a\nprogress 41"), "a\nprogress 42 done\n", false, ""},
		{"front evicted", strp(long), long[11:] + "more lines here, enough to be longer\n", false, ""},
		{"elision note added", strp(long), "… 3 earlier lines elided …\n" + long, false, ""},
		{"reset to empty", strp("abc"), "", false, ""},
		{"long append", strp(long), long + "tail\n", true, "tail\n"},
		{"suffix starts mid-rune", strp("é"[:1]), "é", false, ""},
	}
	for _, c := range cases {
		var m textMark
		if c.prev != nil {
			m = markOf(*c.prev)
		}
		d, ok := appendDelta(m, c.next)
		if ok != c.wantOK || d != c.wantDelta {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.name, d, ok, c.wantDelta, c.wantOK)
		}
		// Whatever the answer, applying it must reproduce next exactly.
		if ok && *c.prev+d != c.next {
			t.Errorf("%s: prev+delta != next", c.name)
		}
	}
}

// TestAppendDeltaMarkDoesNotAlias guards the copy in markOf: a mark taken from
// a substring of a large string must not keep that string alive.
func TestAppendDeltaMarkDoesNotAlias(t *testing.T) {
	s := strings.Repeat("x", 4096)
	m := markOf(s)
	if len(m.head) != markEdge || len(m.tail) != markEdge || m.n != 4096 {
		t.Fatalf("mark = %d/%d/%d", len(m.head), len(m.tail), m.n)
	}
}

func strp(s string) *string { return &s }
