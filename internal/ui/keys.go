package ui

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// keyBinding is one row of the help overlay and of `ggraphify -h`.
//
// It carries no function pointer: dispatchKey below is the single switch that
// routes a keystroke, and a second copy of that routing here would be a place
// for the two to disagree. This table is the documentation, dispatchKey is the
// behaviour, and the test in keys_test.go asserts that every entry here is a
// key dispatchKey actually handles.
type keyBinding struct {
	Keys  string
	What  string
	Group string
}

var keyBindings = []keyBinding{
	{"j / ↓", "Next repository", "Moving"},
	{"k / ↑", "Previous repository", "Moving"},
	{"g / G", "First / last repository", "Moving"},
	{"Enter", "Open the most useful detail page for this row", "Moving"},
	{"Ctrl+F / Ctrl+B", "Page down / page up", "Moving"},
	{"/", "Filter repositories", "Moving"},
	{"f", "Cycle the state filter", "Moving"},
	{"F", "Cycle the folder filter", "Moving"},
	{"s", "Show only this repository's folder", "Moving"},
	{"Esc", "Clear the search / leave select mode / clear the folder filter", "Moving"},
	{"r", "Rescan every root", "Moving"},

	{"h", "Fix — run whatever this repository needs to become healthy", "Building"},
	{"H", "Fix, free steps only — no API key, no LLM call", "Building"},
	{"Ctrl+H", "Fix, free steps only, on every repository on the board", "Building"},
	{"Ctrl+G", "Sync the graft index for every repository in the selected folder", "Building"},
	{"u", "Update — AST re-extraction, free", "Building"},
	{"c", "Re-cluster, keeping placeholder names, free", "Building"},
	{"E", "Extract — full LLM extraction, METERED", "Building"},
	{"l", "Label communities — METERED", "Building"},
	{"e", "Export graph.html", "Building"},
	{"w", "Export wiki", "Building"},
	{"W", "Start/stop `graphify watch` on this repository", "Building"},
	{"p", "Pause / resume this row's running job", "Building"},
	{"x", "Cancel this row's running job", "Building"},

	{"o", "Overview page", "Pages"},
	{"R", "Report page", "Pages"},
	{"V", "Visualize page", "Pages"},
	{"K", "Wiki page", "Pages"},
	{"q", "Query console", "Pages"},
	{"J", "All jobs, across every repository", "Pages"},
	{"U", "Usage tab: what agents actually ran, every repository", "Pages"},
	{"G", "Cross-repo global graph", "Pages"},

	{"v", "Select mode, for batch actions", "Batch"},
	{"Space", "Add / remove this row from the batch", "Batch"},
	{"A", "Add every visible row to the batch", "Batch"},
	{"X", "Keep this repository out of batch actions", "Batch"},

	{"b", "Show or hide the bottom panel", "Board"},
	{"L", "Bottom panel: the application log", "Board"},
	{"t", "Cycle the colour scheme", "Board"},
	{",", "Settings", "Board"},
	{"?", "This list", "Board"},
	{"Ctrl+Q", "Quit", "Board"},
}

// installKeys wires the board's single-key bindings.
//
// They are a capture-phase controller on the window rather than accelerators,
// because a single letter cannot be an accelerator without eating every text
// entry in the application. The handler therefore checks first whether the
// focus is in something that wants letters, and declines if it is.
func (a *App) installKeys() {
	ctl := gtk.NewEventControllerKey()
	ctl.SetPropagationPhase(gtk.PhaseCapture)
	ctl.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		if a.typing() {
			// One exception: Escape always works, so a filter box can be left
			// without reaching for the mouse.
			if keyval == gdk.KEY_Escape {
				a.escape()
				return true
			}
			return false
		}
		ctrl := state&gdk.ControlMask != 0
		if ctrl {
			switch keyval {
			case gdk.KEY_q, gdk.KEY_Q:
				a.win.Close()
				return true
			case gdk.KEY_f, gdk.KEY_F:
				a.movePage(1)
				return true
			case gdk.KEY_b, gdk.KEY_B:
				a.movePage(-1)
				return true
			case gdk.KEY_r:
				a.rescan()
				return true
			case gdk.KEY_h, gdk.KEY_H:
				a.actFixFreeAll()
				return true
			case gdk.KEY_g, gdk.KEY_G:
				a.actSyncGraft()
				return true
			}
			return false
		}
		if state&(gdk.AltMask|gdk.SuperMask) != 0 {
			return false
		}
		return a.dispatchKey(keyval)
	})
	a.win.AddController(ctl)
}

// dispatchKey routes one plain keystroke. Returning true stops propagation.
func (a *App) dispatchKey(keyval uint) bool {
	// The Usage page replaces the board, so the keys that act on a row would
	// be acting on something nobody can see. Only the handful that are about
	// the window itself stay live there; everything else is inert until U or
	// Escape brings the board back.
	if a.onUsagePage() {
		switch keyval {
		case gdk.KEY_U, gdk.KEY_Escape:
			a.setMainPage(pageBoard)
		case gdk.KEY_t:
			a.cycleScheme()
		case gdk.KEY_comma:
			a.showSettings()
		case gdk.KEY_question:
			a.showHelp()
		case gdk.KEY_J:
			a.showJobs()
		default:
			return false
		}
		return true
	}
	switch keyval {
	case gdk.KEY_j:
		a.moveSelection(1)
	case gdk.KEY_k:
		a.moveSelection(-1)
	case gdk.KEY_g:
		a.moveTo(0)
	case gdk.KEY_G:
		a.showGlobal()
	case gdk.KEY_slash:
		a.searchFocus()
	case gdk.KEY_f:
		a.cycleFilter()
	case gdk.KEY_F:
		a.cycleGroup()
	case gdk.KEY_s:
		a.groupThisRow()
	case gdk.KEY_Escape:
		a.escape()
	case gdk.KEY_r:
		a.rescan()
	case gdk.KEY_h:
		a.actFix()
	case gdk.KEY_H:
		a.actFixFree()
	case gdk.KEY_u:
		a.actUpdate()
	case gdk.KEY_c:
		a.actCluster()
	case gdk.KEY_E:
		a.actExtract()
	case gdk.KEY_l:
		a.actLabel()
	case gdk.KEY_e:
		a.actExportHTML()
	case gdk.KEY_w:
		a.actExportWiki()
	case gdk.KEY_W:
		a.actWatch()
	case gdk.KEY_p:
		a.pauseCurrent()
	case gdk.KEY_x:
		a.cancelCurrent()
	case gdk.KEY_o:
		a.detail.SetPage("overview")
	case gdk.KEY_R:
		a.detail.SetPage("report")
	case gdk.KEY_V:
		a.detail.SetPage("viz")
	case gdk.KEY_K:
		a.detail.SetPage("wiki")
	case gdk.KEY_q:
		a.detail.SetPage("query")
	case gdk.KEY_J:
		a.showJobs()
	case gdk.KEY_U:
		a.showUsage()
	case gdk.KEY_v:
		a.setSelectMode(!a.selectMode)
	case gdk.KEY_space:
		a.spaceKey()
	case gdk.KEY_A:
		a.selectVisible()
	case gdk.KEY_X:
		a.toggleExclude()
	case gdk.KEY_b:
		a.toggleDock()
	case gdk.KEY_L:
		a.showDockPage(dockPageLog)
	case gdk.KEY_t:
		a.cycleScheme()
	case gdk.KEY_comma:
		a.showSettings()
	case gdk.KEY_question:
		a.showHelp()
	default:
		return false
	}
	return true
}

// typing reports whether the keyboard focus is somewhere that wants letters.
// A single-key binding that fired while the user was typing a query would be
// a bug of the worst kind: silent, destructive and only in the one case where
// the user is concentrating.
func (a *App) typing() bool {
	if a.win == nil {
		return false
	}
	focus := a.win.Focus()
	if focus == nil {
		return false
	}
	switch focus.(type) {
	case *gtk.Text, *gtk.Entry, *gtk.SearchEntry, *gtk.TextView,
		*gtk.SpinButton, *gtk.PasswordEntry, *gtk.EditableLabel:
		return true
	}
	// gotk4 casts a focus widget to the most derived type it knows, which for
	// the GtkText inside an AdwEntryRow is not any of the above. Anything
	// implementing GtkEditable wants letters, so ask the interface rather
	// than trying to enumerate every concrete type that embeds one.
	if _, ok := focus.(gtk.EditableTextWidgetter); ok {
		return true
	}
	return false
}

// spaceKey adds the row to the batch, turning select mode on if it is off —
// pressing Space is itself a statement that a batch is wanted.
func (a *App) spaceKey() {
	r := a.current()
	if r == nil {
		return
	}
	if !a.selectMode {
		a.setSelectMode(true)
	}
	a.toggleSelected(r)
}

// escape is the universal back-out: clear the filter, leave select mode, or
// return the keyboard to the list.
func (a *App) escape() {
	switch {
	case a.searchText() != "":
		a.clearSearch()
	case a.selectMode:
		a.setSelectMode(false)
	case a.groupID != "":
		a.setGroup("")
		a.toast(groupAllLabel)
	default:
		a.view.GrabFocus()
	}
}

// movePage moves the selection by whole screenfuls, the way Ctrl+F and Ctrl+B
// do in a pager. Ctrl+F used to focus the filter box; `/` still does that, and
// a board that scrolls with the pager keys is worth more than a second way to
// reach a box that already has one.
func (a *App) movePage(pages int) {
	a.moveSelection(pages * a.pageStep())
}

// pageStep is how many rows one screenful is. It is measured from the
// scroller rather than assumed, so it stays right across row heights, font
// sizes and window sizes; the fallback covers the moment before the first
// allocation, when the adjustment has nothing to say yet.
func (a *App) pageStep() int {
	const fallback = 10
	if a.rowsSW == nil {
		return fallback
	}
	adj := a.rowsSW.VAdjustment()
	n := int(a.sorted.NItems())
	if adj == nil || n == 0 {
		return fallback
	}
	rowH := adj.Upper() / float64(n)
	if rowH <= 0 {
		return fallback
	}
	if step := int(adj.PageSize() / rowH); step >= 1 {
		return step
	}
	return 1
}

// moveSelection walks the visible rows by delta.
func (a *App) moveSelection(delta int) {
	n := int(a.sorted.NItems())
	if n == 0 {
		return
	}
	cur := int(a.sel.Selected())
	if cur < 0 || cur >= n {
		cur = 0
		if delta < 0 {
			cur = n - 1
		}
		a.moveTo(cur)
		return
	}
	a.moveTo(clamp(cur+delta, 0, n-1))
}

func (a *App) moveTo(i int) {
	n := int(a.sorted.NItems())
	if n == 0 {
		return
	}
	i = clamp(i, 0, n-1)
	a.sel.SetSelected(uint(i))
	a.view.ScrollTo(uint(i), nil, gtk.ListScrollFocus, nil)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// UsageKeys renders the bindings for `ggraphify -h`, from the same table the
// help dialog reads.
func UsageKeys() string {
	var b []byte
	group := ""
	for _, k := range keyBindings {
		if k.Group != group {
			group = k.Group
			b = append(b, "\n  "...)
			b = append(b, group...)
			b = append(b, '\n')
		}
		b = append(b, "    "...)
		b = append(b, pad(k.Keys, 10)...)
		b = append(b, k.What...)
		b = append(b, '\n')
	}
	return string(b)
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}
