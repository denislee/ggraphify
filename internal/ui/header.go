package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

// filterChips are the state filters across the top, in the order they appear.
// "all" is the empty filter rather than a state of its own.
var filterChips = []struct {
	id    string
	label string
}{
	{"", "All"},
	{graphstate.StateNone.String(), "No graph"},
	{graphstate.StateStale.String(), "Stale"},
	{graphstate.StateRaw.String(), "Unlabeled"},
	{graphstate.StateFresh.String(), "Fresh"},
	{graphstate.StateBroken.String(), "Broken"},
	{graphstate.StateRunning.String(), "Running"},
}

func (a *App) buildHeader() gtk.Widgetter {
	header := adw.NewHeaderBar()
	header.SetTitleWidget(a.title)

	// Left: the primary action a board is for — rescan — and select mode.
	rescan := gtk.NewButtonFromIconName("view-refresh-symbolic")
	rescan.AddCSSClass("flat")
	rescan.SetTooltipText("Rescan every root (r)")
	rescan.ConnectClicked(func() { a.rescan() })
	header.PackStart(rescan)

	selBtn := gtk.NewToggleButton()
	selBtn.SetIconName("selection-mode-symbolic")
	selBtn.AddCSSClass("flat")
	selBtn.SetTooltipText("Select rows for a batch action (v)")
	selBtn.ConnectToggled(func() { a.setSelectMode(selBtn.Active()) })
	header.PackStart(selBtn)
	a.selBtn = selBtn

	// The board-wide free sweep. It sits with rescan rather than with the
	// per-row actions in the detail pane because it is not about the selected
	// row at all, and it is an icon button like the rest of the header: the
	// dialog it opens is where the repository count and the plan are.
	freeAll := gtk.NewButtonFromIconName("system-run-symbolic")
	freeAll.AddCSSClass("flat")
	freeAll.SetTooltipText("Run the free fix steps on every repository (Ctrl+H) — no API key, no LLM call")
	freeAll.ConnectClicked(func() { a.actFixFreeAll() })
	header.PackStart(freeAll)

	// The graft sweep, beside the graphify one: same shape (a free, board-wide
	// button whose dialog carries the count and the plan), different indexer.
	// It is scoped to the folder filter in force, which is the dropdown one
	// row below it.
	graftAll := gtk.NewButtonFromIconName("folder-symbolic")
	graftAll.AddCSSClass("flat")
	graftAll.SetTooltipText("Sync the graft index for every repository in the selected folder (Ctrl+G) — free, no LLM call")
	graftAll.ConnectClicked(func() { a.actSyncGraft() })
	header.PackStart(graftAll)

	// The local-model sweep, third in the same row of board-wide buttons. It
	// is the one of the three that runs an LLM, so it does NOT get the "free"
	// wording the other two carry — a local run costs no money but it costs
	// hours, and a button that implied otherwise would be the same lie the
	// confirm dialog exists to prevent.
	localAll := gtk.NewButtonFromIconName("computer-symbolic")
	localAll.AddCSSClass("flat")
	localAll.SetTooltipText("Full LLM extraction on every repository that never had one (Ctrl+L) — " +
		"runs on the local model: no API key and no bill, but hours of this machine")
	localAll.ConnectClicked(func() { a.actExtractLocalAll() })
	header.PackStart(localAll)

	// Right: the gear is rightmost because it is the canonical home of every
	// preference the other buttons toggle, so it reads as the end of the row.
	gear := gtk.NewButtonFromIconName("emblem-system-symbolic")
	gear.AddCSSClass("flat")
	gear.SetTooltipText("Settings ( , )")
	gear.ConnectClicked(func() { a.showSettings() })
	header.PackEnd(gear)

	helpBtn := gtk.NewButtonFromIconName("help-about-symbolic")
	helpBtn.AddCSSClass("flat")
	helpBtn.SetTooltipText("Keyboard shortcuts ( ? )")
	helpBtn.ConnectClicked(func() { a.showHelp() })
	header.PackEnd(helpBtn)

	a.scheme = gtk.NewButtonWithLabel(schemeLabel(a.currentScheme()))
	a.scheme.AddCSSClass("flat")
	a.scheme.SetTooltipText("Colour scheme (t)")
	a.scheme.ConnectClicked(func() { a.cycleScheme() })
	header.PackEnd(a.scheme)

	header.PackEnd(a.buildActivity())

	jobsBtn := gtk.NewButtonFromIconName("view-list-symbolic")
	jobsBtn.AddCSSClass("flat")
	jobsBtn.SetTooltipText("All jobs (J)")
	jobsBtn.ConnectClicked(func() { a.showJobs() })
	header.PackEnd(jobsBtn)

	// The whole-window Usage view. It is a toggle rather than a button
	// because it REPLACES the board — rows, detail pane and dock — with the
	// dashboard, and a toggle is the control that says "you are somewhere
	// else now" rather than "a thing opened over the top".
	a.usageBtn = gtk.NewToggleButton()
	a.usageBtn.SetIconName("utilities-system-monitor-symbolic")
	a.usageBtn.AddCSSClass("flat")
	a.usageBtn.SetTooltipText("Agent usage: what actually ran graphify and graft (U)")
	a.usageBtn.ConnectToggled(func() {
		if a.usageGuard {
			return
		}
		a.setMainPage(pageUsage(a.usageBtn.Active()))
	})
	header.PackEnd(a.usageBtn)

	globalBtn := gtk.NewButtonFromIconName("network-workgroup-symbolic")
	globalBtn.AddCSSClass("flat")
	globalBtn.SetTooltipText("Cross-repo global graph (G)")
	globalBtn.ConnectClicked(func() { a.showGlobal() })
	header.PackEnd(globalBtn)

	return header
}

// buildFilterBar is the search entry and the state chips.
func (a *App) buildFilterBar() *gtk.Box {
	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(6)
	bar.SetMarginBottom(6)

	a.search = gtk.NewSearchEntry()
	a.search.SetPlaceholderText("Filter repositories ( / )")
	a.search.SetTooltipText("Fuzzy: the letters have to appear in order, not next to each other — " +
		"npc finds nova-platform-console. Several words all have to match. Enter moves to the list.")
	a.search.SetHExpand(true)
	a.search.ConnectSearchChanged(func() {
		a.cfilt.Changed(gtk.FilterChangeDifferent)
		a.persistFilter()
		a.refreshStatus()
	})
	// Enter commits the filter: the typing is done, so the keyboard belongs
	// on the rows that survived it.
	a.search.ConnectActivate(func() { a.focusResults() })
	bar.Append(a.search)

	// The folder filter sits between the search box and the state chips
	// because it narrows *which* repositories are on the board, where the
	// chips narrow which of those are interesting — the coarser cut first.
	a.groupDrop = gtk.NewDropDownFromStrings([]string{groupAllLabel})
	a.groupDrop.SetTooltipText("Show only the repositories under one folder (F)")
	a.groupDrop.NotifyProperty("selected", func() {
		if a.groupGuard {
			return
		}
		i := int(a.groupDrop.Selected())
		if i < 0 || i >= len(a.groupPaths) {
			return
		}
		a.setGroup(a.groupPaths[i])
	})
	bar.Append(a.groupDrop)

	chips := gtk.NewBox(gtk.OrientationHorizontal, 0)
	chips.AddCSSClass("linked")
	for _, c := range filterChips {
		id := c.id
		btn := gtk.NewToggleButton()
		btn.SetLabel(c.label)
		btn.ConnectToggled(func() {
			if a.chipGuard {
				return
			}
			if btn.Active() {
				a.setFilter(id)
			} else if a.activeFilter() == id {
				// Clicking the active chip off means "all", not "nothing":
				// a board showing zero rows because every chip is off is a
				// state nobody asks for on purpose.
				a.setFilter("")
			}
		})
		a.chips[id] = btn
		chips.Append(btn)
	}
	bar.Append(chips)
	return bar
}

// groupAllLabel is the dropdown's first entry: the absence of a folder filter,
// which is not a folder and so cannot be named like one.
const groupAllLabel = "All folders"

// setGroup narrows the board to one folder, or to all of them when path is "".
func (a *App) setGroup(path string) {
	if a.groupID == path {
		return
	}
	a.groupID = path
	a.syncGroupDrop()
	if a.cfilt != nil {
		a.cfilt.Changed(gtk.FilterChangeDifferent)
	}
	a.persistFilter()
	a.refreshStatus()
}

// refreshGroups rebuilds the dropdown from the rows that are actually on the
// board. It is called on every scan, so it rebuilds only when the set of
// folders or their counts moved — replacing the model on a 30-second tick
// would close the popover under a user who had it open.
func (a *App) refreshGroups(rows []board.Row) {
	if a.groupDrop == nil {
		return
	}
	groups := board.Groups(rows)
	key := groupKeyOf(len(rows), groups)
	if key == a.groupKey {
		return
	}
	a.groupKey = key

	labels := make([]string, 0, len(groups)+1)
	paths := make([]string, 0, len(groups)+1)
	labels = append(labels, groupAllLabel+"  ("+gfy.Itoa(len(rows))+")")
	paths = append(paths, "")
	for _, g := range groups {
		label := g.Label
		if g.Depth > 0 {
			// A subdirectory is shown under its root rather than as a second
			// absolute path: the indent is what says "inside the one above".
			label = "   ↳ " + label
		}
		labels = append(labels, label+"  ("+gfy.Itoa(g.Count)+")")
		paths = append(paths, g.Path)
	}

	a.groupGuard = true
	a.groupDrop.SetModel(gtk.NewStringList(labels))
	a.groupPaths = paths
	a.groupGuard = false

	// A folder that has gone away — a root removed from settings, or a
	// subdirectory whose last checkout was deleted — cannot stay in force, or
	// the board would show nothing and offer no way back.
	if a.groupID != "" && indexOf(paths, a.groupID) < 0 {
		a.groupID = ""
		a.persistFilter()
		if a.cfilt != nil {
			a.cfilt.Changed(gtk.FilterChangeDifferent)
		}
	}
	a.syncGroupDrop()
}

// syncGroupDrop puts the dropdown on the folder actually in force.
func (a *App) syncGroupDrop() {
	if a.groupDrop == nil {
		return
	}
	i := indexOf(a.groupPaths, a.groupID)
	if i < 0 {
		i = 0
	}
	a.groupGuard = true
	a.groupDrop.SetSelected(uint(i))
	a.groupGuard = false
}

// cycleGroup is what F does: step through the folders, ending back at all.
func (a *App) cycleGroup() {
	if len(a.groupPaths) < 2 {
		a.toast("every repository is in one folder")
		return
	}
	i := indexOf(a.groupPaths, a.groupID)
	if i < 0 {
		i = 0
	}
	next := a.groupPaths[(i+1)%len(a.groupPaths)]
	a.setGroup(next)
	if next == "" {
		a.toast(groupAllLabel)
	} else {
		a.toast(board.Tilde(next))
	}
}

// groupThisRow narrows the board to the selected row's own folder, which is
// how "the rest of these" is asked for without reaching for the dropdown.
func (a *App) groupThisRow() {
	r := a.current()
	if r == nil {
		a.toast("no repository selected")
		return
	}
	g := board.GroupOf(*r)
	if a.groupID == g {
		a.setGroup("")
		a.toast(groupAllLabel)
		return
	}
	a.setGroup(g)
	a.toastf("folder %s", board.Tilde(g))
}

func groupKeyOf(rows int, groups []board.Group) string {
	var b strings.Builder
	b.WriteString(gfy.Itoa(rows))
	for _, g := range groups {
		b.WriteByte('|')
		b.WriteString(g.Path)
		b.WriteByte(':')
		b.WriteString(gfy.Itoa(g.Count))
	}
	return b.String()
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// activeFilter is the state currently filtered on, or "".
func (a *App) activeFilter() string { return a.filterID }

// setFilter applies one and puts every chip back in step.
func (a *App) setFilter(id string) {
	a.filterID = id
	a.chipGuard = true
	for cid, btn := range a.chips {
		btn.SetActive(cid == id)
	}
	a.chipGuard = false
	if a.cfilt != nil {
		a.cfilt.Changed(gtk.FilterChangeDifferent)
	}
	a.persistFilter()
	a.refreshStatus()
}

// cycleFilter is what the `f` key does: step through the chips.
func (a *App) cycleFilter() {
	cur := a.activeFilter()
	for i, c := range filterChips {
		if c.id == cur {
			a.setFilter(filterChips[(i+1)%len(filterChips)].id)
			return
		}
	}
	a.setFilter("")
}

func (a *App) persistFilter() {
	set := a.opts.Store.Settings()
	set.Filter = a.filterID
	set.Group = a.groupID
	set.Search = a.searchText()
	if col := a.view.Sorter(); col != nil {
		// The sort is persisted from the same place, because both are
		// restored at the same moment on the next launch.
		set.SortCol, set.SortDesc = a.currentSort()
	}
	a.opts.Store.SetSettings(set)
}

// currentSort reads back which column the view is sorted by.
func (a *App) currentSort() (string, bool) {
	cs := a.view.Sorter()
	if cs == nil {
		return "", false
	}
	col, ok := cs.Cast().(*gtk.ColumnViewSorter)
	if !ok {
		return "", false
	}
	prim := col.PrimarySortColumn()
	if prim == nil {
		return "", false
	}
	return prim.ID(), col.PrimarySortOrder() == gtk.SortDescending
}

// restoreFilter puts the search box and the chips back where the last run left
// them.
func (a *App) restoreFilter() {
	set := a.opts.Store.Settings()
	if set.Search != "" {
		a.search.SetText(set.Search)
	}
	// The folder is restored before the dropdown has a model — the first scan
	// builds that — and refreshGroups puts the widget on it, or drops it when
	// the folder is no longer on this machine.
	a.groupID = set.Group
	a.setFilter(set.Filter)
}

// rescan forces a full re-walk, ignoring the discovery cache.
func (a *App) rescan() {
	a.toast("rescanning…")
	a.refresh(true)
}

// setSelectMode turns batch selection on or off.
func (a *App) setSelectMode(on bool) {
	if a.selectMode == on {
		return
	}
	a.selectMode = on
	if !on {
		a.selected = map[string]bool{}
	}
	if a.selBtn != nil && a.selBtn.Active() != on {
		a.selBtn.SetActive(on)
	}
	a.view.QueueDraw()
	a.refreshStatus()
	if on {
		a.toast("select mode — Space adds a row, then take an action; Esc leaves")
	}
}

// toggleSelected adds or removes a row from the batch.
func (a *App) toggleSelected(r *board.Row) {
	if a.selected[r.Path] {
		delete(a.selected, r.Path)
	} else {
		a.selected[r.Path] = true
	}
	a.refreshStatus()
}

// batch is the set of rows an action applies to: the batch selection when
// there is one, otherwise the single selected row.
//
// Rows marked "exclude from batch actions" are dropped from a *batch* and kept
// for a single explicit action — the override means "do not sweep me up", not
// "never touch me".
func (a *App) batch() []board.Row {
	if a.selectMode && len(a.selected) > 0 {
		var out []board.Row
		for _, r := range a.allRows() {
			if a.selected[r.Path] && !r.Excluded {
				out = append(out, r)
			}
		}
		return out
	}
	if r := a.current(); r != nil {
		return []board.Row{*r}
	}
	return nil
}

// selectVisible adds every row the filter currently admits to the batch. This
// is how "update everything stale" is expressed: filter to stale, press A.
func (a *App) selectVisible() {
	a.setSelectMode(true)
	n := 0
	for _, r := range a.visibleRows() {
		if r.Excluded {
			continue
		}
		a.selected[r.Path] = true
		n++
	}
	a.refreshStatus()
	a.toastf("selected %s", plural(n, "row", "rows"))
}

// searchFocus puts the keyboard in the filter box.
func (a *App) searchFocus() {
	a.search.GrabFocus()
	a.search.SelectRegion(0, -1)
}

// focusResults moves the keyboard from the filter box to the list, landing on
// a row so the arrow keys and the row actions work straight away.
func (a *App) focusResults() {
	a.view.GrabFocus()
	if a.sorted.NItems() == 0 {
		return
	}
	if cur := int(a.sel.Selected()); cur < 0 || cur >= int(a.sorted.NItems()) {
		a.moveTo(0)
		return
	}
	a.view.ScrollTo(a.sel.Selected(), nil, gtk.ListScrollFocus, nil)
}

// clearSearch empties the box and returns the keyboard to the list.
func (a *App) clearSearch() {
	if strings.TrimSpace(a.searchText()) != "" {
		a.search.SetText("")
	}
	a.view.GrabFocus()
}
