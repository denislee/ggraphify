package ui

import (
	"unicode/utf8"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// textMark fingerprints the text a view last rendered, without holding it:
// its length plus copies of its first and last few bytes. A log view keeps one
// of these instead of the previous string, which for a job log is up to the
// ring buffer's 256 KiB pinned per open view.
type textMark struct {
	n          int
	head, tail string
	set        bool
}

// markEdge is how many bytes of each end a mark keeps.
const markEdge = 64

func markOf(s string) textMark {
	h := s
	if len(h) > markEdge {
		h = s[:markEdge]
	}
	t := s
	if len(t) > markEdge {
		t = s[len(s)-markEdge:]
	}
	return textMark{n: len(s), head: string([]byte(h)), tail: string([]byte(t)), set: true}
}

// appendDelta reports whether s is the previously rendered text plus a suffix,
// and returns that suffix. ok is false whenever an in-place append would not
// reproduce s exactly — nothing rendered yet, the text shrank (a carriage
// return erased a line, a buffer reset), the front moved (ring-buffer
// eviction, the "lines elided" note), the old end was rewritten, or the
// suffix would start inside a UTF-8 sequence — and the caller then falls back
// to a full SetText. A false negative only costs the old full redraw.
func appendDelta(m textMark, s string) (delta string, ok bool) {
	if !m.set || len(s) < m.n {
		return "", false
	}
	if len(m.head) > len(s) || s[:len(m.head)] != m.head {
		return "", false
	}
	if s[m.n-len(m.tail):m.n] != m.tail {
		return "", false
	}
	if m.n < len(s) && !utf8.RuneStart(s[m.n]) {
		return "", false
	}
	return s[m.n:], true
}

// textSync keeps a TextView's buffer equal to a growing string, inserting
// only the new bytes when the text grew by appending and redrawing in full
// otherwise. The rendered content is always exactly s.
type textSync struct{ m textMark }

func (t *textSync) reset() { t.m = textMark{} }

func (t *textSync) update(buf *gtk.TextBuffer, s string) {
	if delta, ok := appendDelta(t.m, s); ok {
		if delta != "" {
			buf.Insert(buf.EndIter(), delta)
		}
	} else {
		buf.SetText(s)
	}
	t.m = markOf(s)
}
