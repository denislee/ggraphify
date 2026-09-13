package ui

import (
	"fmt"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// displayDefault is gdk.DisplayGetDefault with a name that reads at the call
// site. The CSS provider is installed against it.
func displayDefault() *gdk.Display { return gdk.DisplayGetDefault() }

// sprintf is fmt.Sprintf, wrapped so this package's one formatting import
// lives in one place.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// pangoEllipsizeEnd is PANGO_ELLIPSIZE_END. gotk4 exposes the Pango enums
// through a separate module this build does not pull in, and the value is
// stable ABI, so it is named here rather than depended on.
const pangoEllipsizeEnd = 3

// cell builds the standard cell container: a box with the board's padding and
// one child, left-aligned unless told otherwise.
func cell(child gtk.Widgetter) *gtk.Box {
	b := gtk.NewBox(gtk.OrientationHorizontal, 0)
	b.AddCSSClass("cellpad")
	b.Append(child)
	return b
}

// label builds a single-line, end-ellipsized label — the default for every
// text cell, because a long repository name must shorten rather than force
// the column wider.
func label(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetXAlign(0)
	l.SetEllipsize(pangoEllipsizeEnd)
	l.SetSingleLineMode(true)
	return l
}

// monoLabel is label for something that has to line up: a command line, a log.
func monoLabel(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetXAlign(0)
	l.SetSelectable(true)
	l.SetWrap(true)
	l.SetWrapMode(2) // PANGO_WRAP_WORD_CHAR
	l.AddCSSClass("argv")
	return l
}

// setClass makes cur the only one of the state classes on w. GTK has no "set
// exactly these classes" call, so the old one is removed explicitly; leaving
// it on is how a row ends up green and red at once after a job fails.
func setClass(w gtk.Widgetter, prev *string, cur string) {
	if *prev == cur {
		return
	}
	base := gtk.BaseWidget(w)
	if *prev != "" {
		base.RemoveCSSClass(*prev)
	}
	if cur != "" {
		base.AddCSSClass(cur)
	}
	*prev = cur
}

// timeoutAdd is coreglib.TimeoutAdd with the signature this package actually
// uses, so a caller does not have to name the glib import for one call.
func timeoutAdd(ms uint, f func() bool) { coreglib.TimeoutAdd(ms, f) }

// idle runs f on the GTK main thread at the next iteration. Every worker
// goroutine in this application talks back through it, and nothing else.
func idle(f func()) { coreglib.IdleAdd(f) }
