package ui

import (
	"os"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/jobs"
)

// globalPane is the screen for the one graph that is not about a single
// repository: ~/.graphify/global-graph.json, the merged index an agent can
// ask a cross-repo question of.
//
// It is a page of the window rather than a dialog, on the same footing as the
// Usage dashboard and for the same reason: membership is a property of a file
// that spans the whole board, managing it means looking at two lists side by
// side — what is in, what could be — and that is not a thing you open, read
// and dismiss. The Global column on the board answers "is this one in it"; if
// the answer is to be changed for twenty repositories at once, it happens
// here.
//
// Nothing on this page writes to the manifest directly. Every button submits
// `graphify global add` or `graphify global remove`, one at a time, through
// the same runner as everything else — see globalAdd for why the sequence
// matters.
type globalPane struct {
	a      *App
	widget gtk.Widgetter

	head  *gtk.Label
	tiles *gtk.FlowBox
	note  *gtk.Label

	search *gtk.SearchEntry
	query  string

	// The folder filter, the same one the board's header carries and derived
	// from the same board.Groups: on a machine with two hundred checkouts
	// under four roots, "which of ~/git/nova is in the global graph" is the
	// question, and a text box is the wrong instrument for it. It is this
	// page's own rather than the board's — narrowing this screen must not
	// move what the board is showing underneath it.
	folders     *gtk.DropDown
	folderPaths []string
	folderKey   string
	folderGuard bool
	folder      string

	members    *gtk.Box
	candidates *gtk.Box
	memHead    *gtk.Label
	candHead   *gtk.Label
	out        *gtk.TextView

	// The action buttons carry the tick count, because the two filters above
	// mean the ticked set and the listed set are not the same thing: a batch
	// assembled in one folder survives switching to another, and a button
	// that did not say "Add (12)" while three rows were on screen would be
	// acting on nine repositories nobody could see.
	addBtn    *gtk.Button
	resyncBtn *gtk.Button
	removeBtn *gtk.Button
	mergeBtn  *gtk.Button

	// The check state lives here rather than in the widgets, because the page
	// is rebuilt from scratch whenever a scan lands and a rebuilt CheckButton
	// would forget what was ticked halfway through choosing twenty
	// repositories.
	checkedTags  map[string]bool
	checkedPaths map[string]bool

	// sig is what the lists were last built from, so a board tick that
	// changed nothing on this page does not throw away a scroll position.
	sig string
}

// globalListMax bounds how many candidate rows are built at once. The board
// here runs to ~300 checkouts and every row is a handful of widgets; past
// this the answer is to type in the filter box, which is why the cap says so.
const globalListMax = 150

func (a *App) newGlobalPane() *globalPane {
	p := &globalPane{
		a:            a,
		checkedTags:  map[string]bool{},
		checkedPaths: map[string]bool{},
	}

	p.head = gtk.NewLabel("")
	p.head.SetXAlign(0)
	p.head.SetWrap(true)
	p.head.AddCSSClass("title-2")

	p.note = gtk.NewLabel("")
	p.note.SetXAlign(0)
	p.note.SetWrap(true)
	p.note.AddCSSClass("dim")

	p.tiles = gtk.NewFlowBox()
	p.tiles.SetSelectionMode(gtk.SelectionNone)
	p.tiles.SetColumnSpacing(18)
	p.tiles.SetRowSpacing(12)
	p.tiles.SetMinChildrenPerLine(2)
	p.tiles.SetMaxChildrenPerLine(5)
	p.tiles.SetHomogeneous(true)

	p.search = gtk.NewSearchEntry()
	p.search.SetPlaceholderText("Filter both lists")
	p.search.SetHExpand(true)
	p.search.ConnectSearchChanged(func() {
		p.query = strings.ToLower(strings.TrimSpace(p.search.Text()))
		p.sig = "" // the filter is part of what the lists are built from
		p.reload()
	})

	p.folders = gtk.NewDropDownFromStrings([]string{groupAllLabel})
	p.folders.SetListFactory(&wideTextFactory(nil).ListItemFactory)
	p.folders.SetTooltipText("Narrow both lists to the repositories under one folder (F)")
	p.folders.NotifyProperty("selected", func() {
		if p.folderGuard {
			return
		}
		i := int(p.folders.Selected())
		if i < 0 || i >= len(p.folderPaths) {
			return
		}
		p.setFolder(p.folderPaths[i])
	})

	refresh := gtk.NewButtonFromIconName("view-refresh-symbolic")
	refresh.AddCSSClass("flat")
	refresh.SetTooltipText("Re-read ~/.graphify/global-manifest.json now")
	refresh.ConnectClicked(func() {
		p.a.globals.Invalidate()
		p.sig = ""
		p.reload()
	})

	list := gtk.NewButtonWithLabel("graphify global list")
	list.AddCSSClass("flat")
	list.SetTooltipText("Ask graphify itself what is in the global graph — the output lands below")
	list.ConnectClicked(func() { p.runList() })

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.Append(p.folders)
	bar.Append(p.search)
	bar.Append(list)
	bar.Append(refresh)
	bar.SetMarginTop(12)
	bar.SetMarginBottom(6)
	bar.SetMarginStart(18)
	bar.SetMarginEnd(18)

	p.members = gtk.NewBox(gtk.OrientationVertical, 2)
	p.candidates = gtk.NewBox(gtk.OrientationVertical, 2)
	p.memHead = gtk.NewLabel("")
	p.candHead = gtk.NewLabel("")

	p.out = gtk.NewTextView()
	p.out.SetEditable(false)
	p.out.SetMonospace(true)
	p.out.SetWrapMode(gtk.WrapWordChar)
	p.out.SetLeftMargin(12)
	p.out.AddCSSClass("joblog")
	p.out.Buffer().SetText("Command output lands here.")
	outSW := gtk.NewScrolledWindow()
	outSW.SetChild(p.out)
	outSW.SetSizeRequest(-1, 200)

	body := gtk.NewBox(gtk.OrientationVertical, 18)
	body.SetMarginTop(6)
	body.SetMarginBottom(18)
	body.SetMarginStart(18)
	body.SetMarginEnd(18)
	body.Append(p.head)
	body.Append(p.note)
	body.Append(p.tiles)
	body.Append(p.section(p.memHead, p.memberActions(), p.members))
	body.Append(p.section(p.candHead, p.candidateActions(), p.candidates))
	body.Append(usageSection("Output", outSW))

	sw := gtk.NewScrolledWindow()
	sw.SetChild(clampWidth(body))
	sw.SetVExpand(true)
	sw.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)

	page := gtk.NewBox(gtk.OrientationVertical, 0)
	page.Append(clampWidth(bar))
	page.Append(sw)
	p.widget = page
	return p
}

// section is a titled block with its own action bar above the list.
func (p *globalPane) section(title *gtk.Label, actions, list gtk.Widgetter) gtk.Widgetter {
	title.SetXAlign(0)
	title.AddCSSClass("heading")

	head := gtk.NewBox(gtk.OrientationHorizontal, 6)
	head.Append(title)
	spacer := gtk.NewLabel("")
	spacer.SetHExpand(true)
	head.Append(spacer)
	head.Append(actions)

	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.Append(head)
	box.Append(list)
	return box
}

// memberActions is the bulk bar over the members list: what you press after
// ticking a dozen rows.
func (p *globalPane) memberActions() gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)

	all := gtk.NewButtonWithLabel("All")
	all.AddCSSClass("flat")
	all.SetTooltipText("Tick every listed member")
	all.ConnectClicked(func() { p.checkAllMembers(true) })

	stale := gtk.NewButtonWithLabel("Stale")
	stale.AddCSSClass("flat")
	stale.SetTooltipText("Tick only the members whose repository has been re-extracted since it was merged")
	stale.ConnectClicked(func() { p.checkStaleMembers() })

	none := gtk.NewButtonWithLabel("None")
	none.AddCSSClass("flat")
	none.ConnectClicked(func() { p.checkAllMembers(false) })

	resync := gtk.NewButtonWithLabel("Re-add")
	p.resyncBtn = resync
	resync.AddCSSClass("flat")
	resync.SetTooltipText("Merge the ticked repositories' current graphs again, replacing what the global graph holds")
	resync.ConnectClicked(func() { p.resyncChecked() })

	remove := gtk.NewButtonWithLabel("Remove")
	p.removeBtn = remove
	remove.AddCSSClass("destructive-action")
	remove.SetTooltipText("Drop the ticked repositories' nodes from the global graph")
	remove.ConnectClicked(func() { p.removeChecked() })

	merge := gtk.NewButtonWithLabel("Merge to file")
	p.mergeBtn = merge
	merge.AddCSSClass("flat")
	merge.SetTooltipText("Merge the ticked repositories' graphs into one merged-graph.json, " +
		"leaving the global graph alone")
	merge.ConnectClicked(func() { p.mergeChecked() })

	for _, b := range []gtk.Widgetter{all, stale, none, merge, resync, remove} {
		box.Append(b)
	}
	return box
}

// candidateActions is the same bar over the "not in it" list.
func (p *globalPane) candidateActions() gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)

	all := gtk.NewButtonWithLabel("All")
	all.AddCSSClass("flat")
	all.SetTooltipText("Tick every listed repository — the filter decides what is listed")
	all.ConnectClicked(func() { p.checkAllCandidates(true) })

	none := gtk.NewButtonWithLabel("None")
	none.AddCSSClass("flat")
	none.ConnectClicked(func() { p.checkAllCandidates(false) })

	add := gtk.NewButtonWithLabel("Add")
	p.addBtn = add
	add.AddCSSClass("suggested-action")
	add.SetTooltipText("Merge the ticked repositories into the global graph, one at a time")
	add.ConnectClicked(func() { p.addChecked() })

	for _, b := range []gtk.Widgetter{all, none, add} {
		box.Append(b)
	}
	return box
}

// --- the model behind the two lists -----------------------------------------

// globalMember is one line of the members list: what the manifest says, plus
// whatever the board knows about the repository it came from.
type globalMember struct {
	entry globalgraph.Entry
	// row is the boarded checkout this entry's graph belongs to, nil when the
	// entry names a graph no root on this board covers.
	row *board.Row
	// missing is true when the source graph.json is not on disk any more, so
	// re-adding it is not something this screen can offer.
	missing bool
	// stale is the same verdict the board column renders.
	stale bool
}

// globalModel is everything both lists are built from, gathered once and
// UNFILTERED. The two filters — the folder dropdown and the text box — are
// applied where the lists are painted, not here, so a tick made under one
// filter is still a tick after it changes. What is listed and what is ticked
// are deliberately different sets; see the counts on the action buttons.
func (p *globalPane) model() (members []globalMember, candidates, ungraphed []board.Row) {
	m := p.a.globals.Load()
	rows := p.a.allRows()
	p.refreshFolders(rows)

	byGraph := make(map[string]*board.Row, len(rows))
	for i := range rows {
		if g := globalgraph.GraphFor(rows[i].Graph.Out); g != "" {
			byGraph[g] = &rows[i]
		}
	}

	for _, e := range m.Entries {
		mem := globalMember{entry: e}
		if r := byGraph[e.Source]; r != nil {
			mem.row = r
			mem.stale = r.Global.Stale
		}
		if e.Source != "" {
			if _, err := os.Stat(e.Source); err != nil {
				mem.missing = true
			}
		}
		members = append(members, mem)
	}

	for i := range rows {
		r := rows[i]
		if r.Global.In {
			continue
		}
		if r.Graph.Nodes == 0 {
			// Nothing to merge. Counted rather than listed: a list of 140
			// repositories that cannot be added is not a list of choices.
			ungraphed = append(ungraphed, r)
			continue
		}
		candidates = append(candidates, r)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].Path < candidates[j].Path
	})
	return members, candidates, ungraphed
}

func (p *globalPane) matches(fields ...string) bool {
	if p.query == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), p.query) {
			return true
		}
	}
	return false
}

// inFolder is the folder filter, asked of a board row.
func (p *globalPane) inFolder(r *board.Row) bool {
	if p.folder == "" {
		return true
	}
	if r == nil {
		// A member whose repository is not on this board is in no folder: it
		// cannot be placed under one, and claiming it belongs to whichever is
		// selected would be an invention. It is listed under “All folders”
		// and counted as hidden otherwise — see the note reload writes.
		return false
	}
	return board.InGroup(*r, p.folder)
}

// showMember is both filters, for a member row. A member is matched on its
// tag and its source path as well as on its repository, because those are
// what the list actually shows — and for an entry with no repository on this
// board they are all there is.
func (p *globalPane) showMember(m globalMember) bool {
	if !p.inFolder(m.row) {
		return false
	}
	name := ""
	if m.row != nil {
		name = m.row.Name
	}
	return p.matches(m.entry.Tag, m.entry.Source, name)
}

// showRow is both filters, for a repository row.
func (p *globalPane) showRow(r board.Row) bool {
	return p.inFolder(&r) && p.matches(r.Name, r.Path, board.GroupOf(r))
}

// setFolder narrows both lists to one folder, or to all of them when path is
// "". The ticks are deliberately left alone: assembling a batch across two
// folders is a thing people do, and the button counts say how big it is.
func (p *globalPane) setFolder(path string) {
	if p.folder == path {
		return
	}
	p.folder = path
	p.sig = ""
	p.reload()
}

// cycleFolder is what F does on this page: step through the folders, ending
// back at all of them — the same gesture the board's F is.
func (p *globalPane) cycleFolder() {
	if len(p.folderPaths) < 2 {
		p.a.toast("every repository is in one folder")
		return
	}
	i := indexOf(p.folderPaths, p.folder)
	if i < 0 {
		i = 0
	}
	next := p.folderPaths[(i+1)%len(p.folderPaths)]
	p.setFolder(next)
	if next == "" {
		p.a.toast(groupAllLabel)
		return
	}
	p.a.toastf("folder %s", board.Tilde(next))
}

// refreshFolders rebuilds the dropdown from the rows on the board, and only
// when the folders themselves moved: replacing the model on every scan would
// close the popover under somebody who had it open.
//
// The counts are the board's, not this page's. A folder's membership split is
// what the two lists below show once it is selected; putting a second pair of
// numbers in the dropdown would be answering the question before it is asked,
// and would mean recounting every folder on every tick.
func (p *globalPane) refreshFolders(rows []board.Row) {
	if p.folders == nil {
		return
	}
	groups := board.Groups(rows)
	key := groupKeyOf(len(rows), groups)
	if key == p.folderKey {
		return
	}
	p.folderKey = key

	labels := make([]string, 0, len(groups)+1)
	paths := make([]string, 0, len(groups)+1)
	labels = append(labels, groupAllLabel+"  ("+gfy.Itoa(len(rows))+")")
	paths = append(paths, "")
	for _, g := range groups {
		label := g.Label
		if g.Depth > 0 {
			label = "   ↳ " + label
		}
		labels = append(labels, label+"  ("+gfy.Itoa(g.Count)+")")
		paths = append(paths, g.Path)
	}

	p.folderGuard = true
	p.folders.SetModel(gtk.NewStringList(labels))
	p.folderPaths = paths
	p.folderGuard = false

	// A folder that has gone away cannot stay in force, or this page would
	// show nothing and offer no way back.
	if p.folder != "" && indexOf(paths, p.folder) < 0 {
		p.folder = ""
		p.sig = ""
	}
	p.syncFolderDrop()
}

// syncFolderDrop puts the dropdown on the folder actually in force.
func (p *globalPane) syncFolderDrop() {
	i := indexOf(p.folderPaths, p.folder)
	if i < 0 {
		i = 0
	}
	p.folderGuard = true
	p.folders.SetSelected(uint(i))
	p.folderGuard = false
}

// --- painting ----------------------------------------------------------------

// reloadIfChanged repaints only when the page would actually look different.
// It is what lets the board's 30-second scan reach this page without resetting
// a scroll position or a half-made selection every tick.
func (p *globalPane) reloadIfChanged() {
	if p == nil {
		return
	}
	members, candidates, ungraphed := p.model()
	if p.signature(members, candidates, ungraphed) == p.sig {
		return
	}
	// The model just built is the one reload would build again — with an
	// os.Stat per member — so it is handed over rather than recomputed.
	p.reloadWith(members, candidates, ungraphed)
}

// signature is the cheap identity of what the lists show — including both
// filters, since the same model under a different folder is a different page.
func (p *globalPane) signature(members []globalMember, candidates, ungraphed []board.Row) string {
	var b strings.Builder
	b.WriteString(p.query)
	b.WriteByte('@')
	b.WriteString(p.folder)
	b.WriteByte('|')
	for _, m := range members {
		b.WriteString(m.entry.Tag)
		b.WriteString(m.entry.Hash)
		if m.stale {
			b.WriteByte('~')
		}
		if m.missing {
			b.WriteByte('!')
		}
		b.WriteByte(',')
	}
	b.WriteByte('|')
	for _, r := range candidates {
		b.WriteString(r.Path)
		b.WriteByte(',')
	}
	b.WriteString(sprintf("|%d", len(ungraphed)))
	// The ticks are on the buttons, so they are part of what this page looks
	// like: without them a tick made between two scans would not repaint the
	// count until something else moved.
	b.WriteString(sprintf("|%d:%d", len(p.ticked(members)), len(p.tickedRows(candidates))))
	return b.String()
}

// reload rebuilds the whole page from the manifest and the board's rows.
func (p *globalPane) reload() {
	if p == nil || p.a == nil {
		return
	}
	members, candidates, ungraphed := p.model()
	p.reloadWith(members, candidates, ungraphed)
}

// reloadWith is reload over a model the caller already built.
func (p *globalPane) reloadWith(members []globalMember, candidates, ungraphed []board.Row) {
	m := p.a.globals.Load()
	p.sig = p.signature(members, candidates, ungraphed)

	p.head.SetText("Global graph")

	path := globalgraph.GraphPath()
	note := board.Tilde(path) + " — one graph across every repository merged into it. " +
		"An agent asks it a cross-repo question; a repository that is not in it cannot be an answer."
	switch {
	case m.Err != nil:
		note = "⚠ " + board.Tilde(m.Path) + " could not be read: " + m.Err.Error() +
			"\nEvery row below is reported as “not a member” until that is fixed, " +
			"and adding one would overwrite whatever the file still holds."
	case !m.Exists:
		note = board.Tilde(path) + " does not exist yet — nothing has been added to the global graph. " +
			"Adding the first repository creates it."
	}
	p.note.SetText(note)

	nodes, edges := m.Totals()
	var stale int
	for _, mem := range members {
		if mem.stale {
			stale++
		}
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}

	clearFlow(p.tiles)
	p.tiles.Append(tile(sprintf("%d", len(members)), "repositories", ""))
	p.tiles.Append(tile(shortCount(int64(nodes)), "nodes", ""))
	p.tiles.Append(tile(shortCount(int64(edges)), "edges", ""))
	p.tiles.Append(tile(sprintf("%d", stale), "re-extracted since", staleTileClass(stale)))
	p.tiles.Append(tile(board.Bytes(size), "on disk", ""))

	// The members list.
	clearBox(p.members)
	shown, offBoard := 0, 0
	for _, mem := range members {
		if !p.showMember(mem) {
			if p.folder != "" && mem.row == nil {
				// Hidden by the folder filter and unplaceable by it. Counted
				// rather than dropped silently: these are the entries most
				// worth removing, and a filter that made them invisible
				// without saying so would hide exactly the wrong list.
				offBoard++
			}
			continue
		}
		shown++
		p.members.Append(p.memberRow(mem))
	}
	if len(members) == 0 {
		p.members.Append(dimLabel("Nothing has been added to the global graph yet."))
	} else if shown == 0 {
		p.members.Append(dimLabel("No member matches the filter."))
	}
	if offBoard > 0 {
		p.members.Append(dimLabel(sprintf(
			"%s in the global graph %s to no checkout on this board, so no folder can hold %s — "+
				"switch to “%s” to see and remove %s.",
			plural(offBoard, "member", "members"),
			map[bool]string{true: "belongs", false: "belong"}[offBoard == 1],
			map[bool]string{true: "it", false: "them"}[offBoard == 1],
			groupAllLabel,
			map[bool]string{true: "it", false: "them"}[offBoard == 1])))
	}
	p.memHead.SetText("In the global graph — " + countOf(shown, len(members)))

	// The candidates list.
	clearBox(p.candidates)
	listed, matched := 0, 0
	for i := range candidates {
		r := candidates[i]
		if !p.showRow(r) {
			continue
		}
		matched++
		if listed >= globalListMax {
			continue
		}
		listed++
		p.candidates.Append(p.candidateRow(r))
	}
	if matched > listed {
		p.candidates.Append(dimLabel(sprintf(
			"…and %d more. Narrow by folder or type in the filter box — “All” ticks only what is listed.",
			matched-listed)))
	}
	if len(candidates) == 0 {
		p.candidates.Append(dimLabel("Every repository on the board with a graph is already in the global graph."))
	} else if matched == 0 {
		p.candidates.Append(dimLabel("No repository matches the filter."))
	}
	var noGraph int
	for _, r := range ungraphed {
		if p.showRow(r) {
			noGraph++
		}
	}
	if noGraph > 0 {
		p.candidates.Append(dimLabel(sprintf(
			"%s no graph to merge — extract or update %s first.",
			plural(noGraph, "repository here has", "repositories here have"),
			map[bool]string{true: "it", false: "them"}[noGraph == 1])))
	}
	p.candHead.SetText("On the board, not in it — " + countOf(matched, len(candidates)))

	p.syncActionCounts(members, candidates)
}

// staleTileClass paints the stale count as a warning only when it is not zero:
// a red nought is a false alarm.
func staleTileClass(n int) string {
	if n == 0 {
		return ""
	}
	return "st-stale"
}

// memberRow is one line of the members list.
func (p *globalPane) memberRow(m globalMember) gtk.Widgetter {
	tag := m.entry.Tag

	check := gtk.NewCheckButton()
	check.SetActive(p.checkedTags[tag])
	check.SetVAlign(gtk.AlignCenter)
	check.ConnectToggled(func() { p.checkedTags[tag] = check.Active() })

	title := gtk.NewLabel(tag)
	title.SetXAlign(0)
	title.AddCSSClass("heading")

	sub := board.Tilde(m.entry.Source)
	switch {
	case m.entry.Source == "":
		sub = "no source graph recorded"
	case m.missing:
		sub += " — the source graph is gone"
	case m.row == nil:
		sub += " — not on this board"
	}
	subtitle := dimLabel(sub)
	subtitle.SetEllipsize(pangoEllipsizeEnd)

	text := gtk.NewBox(gtk.OrientationVertical, 0)
	text.SetHExpand(true)
	text.Append(title)
	text.Append(subtitle)

	facts := sprintf("%s n / %s e", shortCount(int64(m.entry.Nodes)), shortCount(int64(m.entry.Edges)))
	if !m.entry.AddedAt.IsZero() {
		facts += " · merged " + board.Age(m.entry.AddedAt)
	}
	factLbl := dimLabel(facts)

	row := gtk.NewBox(gtk.OrientationHorizontal, 10)
	row.Append(check)
	row.Append(text)
	row.Append(factLbl)

	if m.stale {
		chip := gtk.NewLabel("re-extracted since")
		chip.AddCSSClass("st-stale")
		chip.SetTooltipText("This repository's graph was rebuilt after it was merged, so the " +
			"global graph still holds the previous extraction's nodes. Re-add it to catch up.")
		row.Append(chip)
	}

	if m.row != nil && !m.missing {
		re := gtk.NewButtonFromIconName("view-refresh-symbolic")
		re.AddCSSClass("flat")
		re.SetVAlign(gtk.AlignCenter)
		re.SetTooltipText("Merge this repository's current graph again")
		// Capture the path, not a Row copy: the row is looked up again on click.
		path := m.row.Path
		re.ConnectClicked(func() {
			if r := p.a.row(path); r != nil {
				p.a.runGlobalChain("global-add", []board.Row{*r})
			}
		})
		row.Append(re)
	}

	rm := gtk.NewButtonFromIconName("user-trash-symbolic")
	rm.AddCSSClass("flat")
	rm.SetVAlign(gtk.AlignCenter)
	rm.SetTooltipText("Drop this repository's nodes from the global graph")
	rm.ConnectClicked(func() { p.removeTags([]string{tag}) })
	row.Append(rm)

	return row
}

// candidateRow is one line of the "not in it" list.
func (p *globalPane) candidateRow(r board.Row) gtk.Widgetter {
	path := r.Path

	check := gtk.NewCheckButton()
	check.SetActive(p.checkedPaths[path])
	check.SetVAlign(gtk.AlignCenter)
	check.ConnectToggled(func() { p.checkedPaths[path] = check.Active() })

	title := gtk.NewLabel(r.Name)
	title.SetXAlign(0)
	title.AddCSSClass("heading")

	subtitle := dimLabel(board.Tilde(r.Path))
	subtitle.SetEllipsize(pangoEllipsizeEnd)

	text := gtk.NewBox(gtk.OrientationVertical, 0)
	text.SetHExpand(true)
	text.Append(title)
	text.Append(subtitle)

	facts := dimLabel(sprintf("%s n / %s e · %s",
		shortCount(int64(r.Graph.Nodes)), shortCount(int64(r.Graph.Links)),
		board.Age(r.Graph.BuiltAt)))

	add := gtk.NewButtonFromIconName("list-add-symbolic")
	add.AddCSSClass("flat")
	add.SetVAlign(gtk.AlignCenter)
	add.SetTooltipText("Merge this repository into the global graph")
	add.ConnectClicked(func() {
		if r := p.a.row(path); r != nil {
			p.a.globalAdd([]board.Row{*r})
		}
	})

	box := gtk.NewBox(gtk.OrientationHorizontal, 10)
	box.Append(check)
	box.Append(text)
	box.Append(facts)
	box.Append(add)
	return box
}

// --- what the buttons do ------------------------------------------------------

// checkAllMembers ticks, or clears, every member the filters currently list.
// Only what is listed: "All" reaching a folder that is not on screen is how a
// narrowed list becomes a batch nobody meant to assemble.
func (p *globalPane) checkAllMembers(on bool) {
	members, _, _ := p.model()
	for _, m := range members {
		if !p.showMember(m) {
			continue
		}
		p.checkedTags[m.entry.Tag] = on
	}
	p.sig = ""
	p.reload()
}

// checkStaleMembers ticks the members worth re-adding, within the filters:
// "every stale repository in ~/git/nova" is the whole point of having the
// folder dropdown on this page.
func (p *globalPane) checkStaleMembers() {
	members, _, _ := p.model()
	for _, m := range members {
		if !p.showMember(m) {
			continue
		}
		p.checkedTags[m.entry.Tag] = m.stale
	}
	p.sig = ""
	p.reload()
}

func (p *globalPane) checkAllCandidates(on bool) {
	_, candidates, _ := p.model()
	listed := 0
	for _, r := range candidates {
		if !p.showRow(r) {
			continue
		}
		if listed >= globalListMax {
			break
		}
		listed++
		p.checkedPaths[r.Path] = on
	}
	p.sig = ""
	p.reload()
}

// syncActionCounts writes the ticked count onto each button and dims the ones
// with nothing to act on. The count is the whole ticked set, not the listed
// part of it — see model.
func (p *globalPane) syncActionCounts(members []globalMember, candidates []board.Row) {
	ticked := p.ticked(members)
	cand := len(p.tickedRows(candidates))
	mem := len(ticked)
	var resyncable, mergeable int
	for _, m := range ticked {
		if m.row != nil && !m.missing {
			resyncable++
		}
		if m.entry.Source != "" && !m.missing {
			mergeable++
		}
	}
	setCount := func(b *gtk.Button, verb string, n int) {
		if b == nil {
			return
		}
		if n == 0 {
			b.SetLabel(verb)
			b.SetSensitive(false)
			return
		}
		b.SetLabel(sprintf("%s (%d)", verb, n))
		b.SetSensitive(true)
	}
	setCount(p.addBtn, "Add", cand)
	setCount(p.resyncBtn, "Re-add", resyncable)
	setCount(p.removeBtn, "Remove", mem)
	// Merging takes graphs from both lists, and needs two of them.
	merge := cand + mergeable
	setCount(p.mergeBtn, "Merge to file", merge)
	if p.mergeBtn != nil && merge < 2 {
		p.mergeBtn.SetSensitive(false)
	}
}

// countOf is "12" when nothing is filtered and "5 of 12" when something is.
func countOf(shown, total int) string {
	if shown == total {
		return sprintf("%d", total)
	}
	return sprintf("%d of %d", shown, total)
}

// ticked filters a member list to what is checked, and tickedRows does the
// same for repository rows. They take the list rather than fetching it so one
// repaint gathers the model once: reload, its signature and the button counts
// are all asking about the same slice.
func (p *globalPane) ticked(members []globalMember) []globalMember {
	var out []globalMember
	for _, m := range members {
		if p.checkedTags[m.entry.Tag] {
			out = append(out, m)
		}
	}
	return out
}

func (p *globalPane) tickedRows(rows []board.Row) []board.Row {
	var out []board.Row
	for _, r := range rows {
		if p.checkedPaths[r.Path] {
			out = append(out, r)
		}
	}
	return out
}

// checkedMembers is the ticked members, in list order. Unlike the display,
// it is NOT narrowed by the filters: a batch assembled across two folders is
// a batch, and the button counts say how big it is.
func (p *globalPane) checkedMembers() []globalMember {
	members, _, _ := p.model()
	return p.ticked(members)
}

// checkedCandidates is the ticked rows from the lower list.
func (p *globalPane) checkedCandidates() []board.Row {
	_, candidates, _ := p.model()
	return p.tickedRows(candidates)
}

func (p *globalPane) addChecked() {
	rows := p.checkedCandidates()
	if len(rows) == 0 {
		p.a.toast("tick the repositories to add first")
		return
	}
	p.a.globalAdd(rows)
}

// resyncChecked re-merges the ticked members' current graphs. This is the
// answer to the stale chip, and the reason the count of those has a tile of
// its own: an extraction that is not re-added is work the global graph never
// sees.
func (p *globalPane) resyncChecked() {
	var rows []board.Row
	var skipped int
	for _, m := range p.checkedMembers() {
		if m.row == nil || m.missing {
			skipped++
			continue
		}
		rows = append(rows, *m.row)
	}
	if len(rows) == 0 {
		if skipped > 0 {
			p.a.toastf("%s no longer on this board — re-adding needs the source graph",
				plural(skipped, "member is", "members are"))
			return
		}
		p.a.toast("tick the members to re-add first")
		return
	}
	if skipped > 0 {
		p.a.toastf("re-adding %d; %d skipped — no source graph on this board", len(rows), skipped)
	}
	p.a.runGlobalChain("global-add", rows)
}

func (p *globalPane) removeChecked() {
	var tags []string
	for _, m := range p.checkedMembers() {
		tags = append(tags, m.entry.Tag)
	}
	if len(tags) == 0 {
		p.a.toast("tick the members to remove first")
		return
	}
	p.removeTags(tags)
}

// removeTags is the by-tag removal path, which is the only one that works for
// a member whose repository is not on this board — a checkout that was moved,
// renamed or deleted after it was merged. Those entries are exactly the ones
// worth removing, so they cannot be left to a row-based action.
func (p *globalPane) removeTags(tags []string) {
	if len(tags) == 0 {
		return
	}
	p.a.confirmGlobal("Remove from the global graph",
		"graphify will drop these repositories' nodes from ~/.graphify/global-graph.json:\n\n"+
			strings.Join(namesUpTo(tags, 8), ", ")+"\n\n"+
			"Nothing inside any checkout is touched — the repositories' own graphs stay "+
			"where they are, and adding them back is free.",
		"Remove", adw.ResponseDestructive,
		func() {
			steps := make([]jobs.ChainStep, 0, len(tags))
			for _, tag := range tags {
				steps = append(steps, jobs.ChainStep{
					Kind:   "global-remove",
					Label:  gfy.Title("global-remove") + " · " + tag,
					Params: gfy.Params{Tag: tag},
				})
			}
			for _, tag := range tags {
				delete(p.checkedTags, tag)
			}
			p.a.submitGlobalChain(steps, gfy.Title("global-remove"))
		})
}

// mergeChecked merges the ticked members' graphs into a file of their own.
// It is not a membership change at all — merge-graphs writes one output and
// leaves the global graph alone — but it is the other cross-repo thing this
// screen is where you would look for.
func (p *globalPane) mergeChecked() {
	var graphs []string
	for _, m := range p.checkedMembers() {
		if m.entry.Source != "" && !m.missing {
			graphs = append(graphs, m.entry.Source)
		}
	}
	for _, r := range p.checkedCandidates() {
		if g := globalgraph.GraphFor(r.Graph.Out); g != "" {
			graphs = append(graphs, g)
		}
	}
	p.a.mergeGraphs(graphs, p.out)
}

// runList runs `graphify global list` and streams it into the output view.
// The page is built from the manifest directly, so this is not how it gets its
// facts — it is how you check that graphify agrees with them.
func (p *globalPane) runList() {
	job, err := p.a.runner.SubmitCmd("global-list", "", "Global list", gfy.Params{}, nil)
	if err != nil {
		p.a.toastf("%v", err)
		return
	}
	p.a.watchInto(job.ID, p.out)
}
