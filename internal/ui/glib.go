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

// wideTextFactory renders a GtkStringList entry as a full-width, non-truncated
// label: a dropdown whose items are sentences rather than words needs the
// popup sized to the text instead of the text clipped to the row.
//
// The label wraps rather than growing without bound, because an entry this
// board has never seen could be arbitrarily long and a popover wider than the
// monitor is its own kind of unreadable.
//
// online may be nil, which is the common case: only the model list has entries
// a gateway can refuse, and every other dropdown wants the width and nothing
// else.
func wideTextFactory(online func(pos uint) bool) *gtk.SignalListItemFactory {
	f := gtk.NewSignalListItemFactory()
	// GTK recycles list items, so the label is kept per item rather than
	// looked up from the item on bind — the same reason the board's column
	// factories keep a map.
	labels := map[uintptr]*gtk.Label{}
	f.ConnectSetup(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := gtk.NewLabel("")
		l.SetXAlign(0)
		l.SetWrap(true)
		l.SetWrapMode(2) // PANGO_WRAP_WORD_CHAR
		l.SetMaxWidthChars(72)
		li.SetChild(l)
		labels[li.Native()] = l
	})
	f.ConnectBind(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := labels[li.Native()]
		if l == nil {
			return
		}
		so, ok := li.Item().Cast().(*gtk.StringObject)
		if !ok {
			return
		}
		// Items are recycled, so every bind has to say BOTH states: a label
		// left faded from the row it used to render would report a model as
		// refused that nothing has ever refused.
		faded := false
		if item, isList := obj.Cast().(*gtk.ListItem); isList && online != nil {
			faded = !online(item.Position())
		}
		if faded {
			l.AddCSSClass("dim-label")
			l.SetMarkup("<span strikethrough=\"true\">" + escapeMarkup(so.String()) + "</span>")
			return
		}
		l.RemoveCSSClass("dim-label")
		l.SetText(so.String())
	})
	f.ConnectTeardown(func(obj *coreglib.Object) {
		if li := asCell(obj); li != nil {
			delete(labels, li.Native())
		}
	})
	return f
}

// valueFactory renders the *selected* entry of an AdwComboRow — the text that
// sits at the right of the row itself, not in the popup. Adw's own display is
// a single ellipsized line, so a backend named "ollama (a model on this
// machine — free, slow, no API key)" arrives as "ollama (a model on this …",
// cut exactly where the parenthesis that explains the choice begins.
//
// The label wraps instead, so the row grows a line rather than the dialog
// growing a column: max-width-chars is what stops a long entry from widening
// the preferences window, and the right alignment is what keeps the row
// looking like every other Adw value row.
//
// Pair it with SetListFactory(wideTextFactory(nil)) — Adw uses `factory` for
// both the row and the popup unless `list-factory` is set as well.
func valueFactory() *gtk.SignalListItemFactory {
	f := gtk.NewSignalListItemFactory()
	// Same recycling story as wideTextFactory: the label belongs to the list
	// item, not to the row it currently shows.
	labels := map[uintptr]*gtk.Label{}
	f.ConnectSetup(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := gtk.NewLabel("")
		l.SetXAlign(1)
		l.SetJustify(gtk.JustifyRight)
		l.SetWrap(true)
		l.SetWrapMode(2) // PANGO_WRAP_WORD_CHAR
		l.SetMaxWidthChars(34)
		l.SetVAlign(gtk.AlignCenter)
		l.AddCSSClass("dim-label")
		li.SetChild(l)
		labels[li.Native()] = l
	})
	f.ConnectBind(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := labels[li.Native()]
		if l == nil {
			return
		}
		if so, ok := li.Item().Cast().(*gtk.StringObject); ok {
			l.SetText(so.String())
		}
	})
	f.ConnectTeardown(func(obj *coreglib.Object) {
		if li := asCell(obj); li != nil {
			delete(labels, li.Native())
		}
	})
	return f
}
