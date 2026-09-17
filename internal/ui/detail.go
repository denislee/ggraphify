package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/open"
	"github.com/dns/ggraphify/internal/store"
)

// detailPane is the right-hand half: everything about one repository.
type detailPane struct {
	a      *App
	widget gtk.Widgetter

	stack    *adw.ViewStack
	switcher *adw.ViewSwitcher
	empty    *adw.StatusPage
	bin      *adw.Bin

	// Overview.
	ovTitle   *gtk.Label
	ovPath    *gtk.Label
	ovFacts   *gtk.Grid
	ovGroup   *adw.PreferencesGroup
	ovBackend *adw.EntryRow
	ovModel   *adw.EntryRow
	ovOut     *adw.EntryRow
	ovExtra   *adw.EntryRow
	ovExclude *adw.SwitchRow
	ovGuard   bool

	// actRows are the action rows that read a graph, kept so the overview can
	// dim them for a repository that has none.
	actRows []graphAction
	// fixRow is the Fix action, kept so its subtitle can name this row's
	// actual defects instead of describing the button in the abstract.
	fixRow *adw.ActionRow

	// Report.
	report *gtk.TextView

	// Visualize.
	viz *vizPage

	// Wiki.
	wikiList *gtk.ListBox
	wikiBody *gtk.TextView

	// Query.
	query *queryPage

	// Jobs (this repo).
	jobsBox  *gtk.Box
	jobLog   *gtk.TextView
	jobHead  *gtk.Label
	jobPause *gtk.Button
	jobGen   uint64
	jobID    uint64
	histBox  *gtk.Box

	row *board.Row
}

func (a *App) newDetailPane() *detailPane {
	d := &detailPane{a: a}

	d.stack = adw.NewViewStack()
	d.stack.SetVExpand(true)

	d.stack.AddTitledWithIcon(d.buildOverview(), "overview", "Overview", "dialog-information-symbolic")
	d.stack.AddTitledWithIcon(d.buildReport(), "report", "Report", "text-x-generic-symbolic")
	d.viz = a.newVizPage()
	d.stack.AddTitledWithIcon(d.viz.widget, "viz", "Visualize", "view-paged-symbolic")
	d.stack.AddTitledWithIcon(d.buildWiki(), "wiki", "Wiki", "accessories-dictionary-symbolic")
	d.query = a.newQueryPage()
	d.stack.AddTitledWithIcon(d.query.widget, "query", "Query", "system-search-symbolic")
	d.stack.AddTitledWithIcon(d.buildJobs(), "jobs", "Jobs", "view-list-symbolic")

	d.switcher = adw.NewViewSwitcher()
	d.switcher.SetStack(d.stack)
	d.switcher.SetPolicy(adw.ViewSwitcherPolicyWide)

	switchBar := gtk.NewBox(gtk.OrientationHorizontal, 0)
	switchBar.SetHAlign(gtk.AlignCenter)
	switchBar.SetMarginTop(6)
	switchBar.SetMarginBottom(6)
	switchBar.Append(d.switcher)

	d.empty = adw.NewStatusPage()
	d.empty.SetTitle("No repository selected")
	d.empty.SetDescription("Pick a row on the left. j/k move, / filters, ? lists every key.")
	d.empty.SetIconName("view-list-symbolic")

	d.bin = adw.NewBin()
	d.bin.SetChild(d.empty)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(switchBar)
	box.Append(d.bin)
	d.bin.SetVExpand(true)
	// A floor, not a demand: the paned above allows shrinking, so a narrow
	// window compresses this pane rather than clipping the board.
	box.SetSizeRequest(360, -1)
	d.widget = box

	d.stack.NotifyProperty("visible-child-name", func() { d.reload() })
	return d
}

// show binds the pane to a row, or to nothing.
func (d *detailPane) show(r *board.Row) {
	d.row = r
	if r == nil {
		d.bin.SetChild(d.empty)
		return
	}
	d.bin.SetChild(d.stack)
	d.reload()
}

// focusBestPage picks the page worth opening for this row: there is nothing to
// visualize on a repository with no graph, so it gets the actions instead.
func (d *detailPane) focusBestPage(r *board.Row) {
	switch {
	case r.Graph.State == graphstate.StateNone:
		d.stack.SetVisibleChildName("overview")
	case r.Graph.HasHTML:
		d.stack.SetVisibleChildName("viz")
	case r.Graph.HasReport:
		d.stack.SetVisibleChildName("report")
	default:
		d.stack.SetVisibleChildName("overview")
	}
}

// SetPage is the keyboard's way in.
func (d *detailPane) SetPage(name string) {
	if d.row == nil {
		return
	}
	d.stack.SetVisibleChildName(name)
}

// reload repaints whichever page is visible. Only the visible one: rendering
// an 18 kB report and a wiki index for a row the user is scrolling past is
// exactly the work a board should not do.
func (d *detailPane) reload() {
	if d.row == nil {
		return
	}
	// The row pointer is refreshed from the model, because a rescan replaces
	// every Row value and the pane would otherwise show a stale graph state.
	if r := d.a.row(d.row.Path); r != nil {
		d.row = r
	}
	switch d.stack.VisibleChildName() {
	case "overview":
		d.loadOverview()
	case "report":
		d.loadReport()
	case "viz":
		d.viz.show(d.row)
	case "wiki":
		d.loadWiki()
	case "query":
		d.query.show(d.row)
	case "jobs":
		d.reloadJobs()
	}
}

// tick is the once-a-second repaint: only the job log needs it, and only when
// it has actually moved.
func (d *detailPane) tick() {
	// The query console renders on this tick too, and is the one page whose
	// job output IS the result rather than a log beside it — so it pulls its
	// own buffer and compares its own generation. It was written and never
	// wired, which left the Query tab showing "running…" until the user
	// switched away and back.
	if d.query != nil && d.stack.VisibleChildName() == "query" {
		d.query.tick()
		return
	}
	if d.row == nil || d.stack.VisibleChildName() != "jobs" {
		return
	}
	s := d.a.jobFor(d.row.Path)
	if s == nil {
		return
	}
	if gen := s.Log.Gen(); gen != d.jobGen || s.ID != d.jobID {
		d.reloadJobs()
	}
	if !s.Status.Done() {
		d.jobHead.SetText(jobHeadline(s))
		setPauseButton(d.jobPause, s)
	}
}

// --- Overview ---------------------------------------------------------------

func (d *detailPane) buildOverview() gtk.Widgetter {
	page := adw.NewPreferencesPage()

	facts := adw.NewPreferencesGroup()
	facts.SetTitle("Graph")
	d.ovTitle = gtk.NewLabel("")
	d.ovTitle.SetXAlign(0)
	d.ovTitle.AddCSSClass("title-2")
	d.ovPath = gtk.NewLabel("")
	d.ovPath.SetXAlign(0)
	d.ovPath.SetSelectable(true)
	d.ovPath.AddCSSClass("dim-label")
	d.ovPath.SetEllipsize(pangoEllipsizeEnd)

	head := gtk.NewBox(gtk.OrientationVertical, 2)
	head.Append(d.ovTitle)
	head.Append(d.ovPath)
	facts.SetHeaderSuffix(head)

	d.ovFacts = gtk.NewGrid()
	d.ovFacts.SetRowSpacing(4)
	d.ovFacts.SetColumnSpacing(18)
	d.ovFacts.SetMarginTop(8)
	d.ovFacts.SetMarginBottom(8)
	d.ovFacts.SetMarginStart(12)
	d.ovFacts.SetMarginEnd(12)
	factsRow := adw.NewActionRow()
	factsRow.SetChild(d.ovFacts)
	facts.Add(factsRow)
	page.Add(facts)

	page.Add(d.buildActionGroups())

	// Per-repository overrides. These are the reason a board over 300
	// checkouts is usable at all: one monorepo wanting a different backend
	// must not force the whole board onto it.
	ov := adw.NewPreferencesGroup()
	ov.SetTitle("This repository")
	ov.SetDescription("Overrides applied to every job for this checkout. Stored in ggraphify's " +
		"sidecar, never inside the repository.")

	d.ovBackend = adw.NewEntryRow()
	d.ovBackend.SetTitle("Backend (blank = the board default)")
	d.ovModel = adw.NewEntryRow()
	d.ovModel.SetTitle("Model")
	d.ovOut = adw.NewEntryRow()
	d.ovOut.SetTitle("Graph directory (blank = the board's location)")
	outPick := gtk.NewButtonWithLabel("Choose…")
	outPick.AddCSSClass("flat")
	outPick.SetVAlign(gtk.AlignCenter)
	outPick.SetTooltipText("Keep this one repository's graphify knowledge somewhere else. " +
		"Set for the whole board in Settings → Graph storage.")
	outPick.ConnectClicked(func() {
		start := ""
		if d.row != nil {
			start = filepath.Dir(d.row.Graph.Out)
		}
		d.a.chooseFolder("Graph directory for this repository", start, func(path string) {
			d.ovOut.SetText(path)
			d.saveOverride()
		})
	})
	d.ovOut.AddSuffix(outPick)
	d.ovExtra = adw.NewEntryRow()
	d.ovExtra.SetTitle("Extra flags, space separated")
	d.ovExclude = adw.NewSwitchRow()
	d.ovExclude.SetTitle("Keep out of batch actions")
	d.ovExclude.SetSubtitle("A single explicit action still applies; a fan-out skips this row.")

	for _, r := range []*adw.EntryRow{d.ovBackend, d.ovModel, d.ovOut, d.ovExtra} {
		row := r
		row.ConnectApply(func() { d.saveOverride() })
		ov.Add(row)
	}
	d.ovExclude.NotifyProperty("active", func() {
		if !d.ovGuard {
			d.saveOverride()
		}
	})
	ov.Add(d.ovExclude)
	d.ovGroup = ov
	page.Add(ov)

	sw := gtk.NewScrolledWindow()
	sw.SetChild(page)
	sw.SetVExpand(true)
	return sw
}

// buildActionGroups lays out every action, grouped by what it costs. The
// graphAction is one action row whose command reads graph.json. The overview
// dims these for a repository that has none, so the precondition is visible
// before the click rather than only in the refusal after it.
type graphAction struct {
	kind string
	row  *adw.ActionRow
	btn  *gtk.Button
	sub  string
}

// syncActions dims or restores the graph-reading rows for the selected
// repository. It is called on every overview load, which is what keeps it
// honest after a build finishes or an output directory is deleted.
func (d *detailPane) syncActions(r *board.Row) {
	if d.fixRow != nil {
		d.fixRow.SetSubtitle(fixSubtitle(r))
	}
	has := r != nil && d.a.hasGraph(*r)
	for _, a := range d.actRows {
		a.btn.SetSensitive(has)
		if has {
			a.row.SetSubtitle(a.sub)
		} else {
			a.row.SetSubtitle(a.sub + " Needs a graph — run Extract or Update first.")
		}
	}
}

// grouping is the point: a user should never have to remember which button
// spends money, because the layout already said so.
func (d *detailPane) buildActionGroups() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Actions")
	g.SetDescription("Every button's tooltip is the exact command line it runs.")

	d.actRows = nil
	add := func(title, sub, kind string, fn func(), destructive bool) {
		row := adw.NewActionRow()
		row.SetTitle(title)
		row.SetSubtitle(sub)
		btn := gtk.NewButtonWithLabel("Run")
		btn.SetVAlign(gtk.AlignCenter)
		if destructive {
			btn.AddCSSClass("destructive-action")
		} else {
			btn.AddCSSClass("flat")
		}
		btn.ConnectClicked(fn)
		row.AddSuffix(btn)
		row.SetActivatableWidget(btn)
		// The tooltip is the argv, resolved for whichever row is selected
		// when the pointer stops — which is why it is computed on hover.
		row.SetHasTooltip(true)
		k := kind
		row.ConnectQueryTooltip(func(x, y int, keyboard bool, tip *gtk.Tooltip) bool {
			if d.row == nil {
				return false
			}
			argv := gfy.Argv(k, d.a.params(*d.row))
			if argv == nil {
				return false
			}
			tip.SetText(gfy.Quote(argv))
			return true
		})
		if gfy.NeedsGraph(kind) {
			d.actRows = append(d.actRows, graphAction{kind: kind, row: row, btn: btn, sub: sub})
		}
		g.Add(row)
	}

	// Fix comes first and is the only row that is not a single graphify
	// command: it is whatever sequence of them this repository needs. It is
	// never dimmed for want of a graph, because "there is no graph" is one of
	// the things it fixes.
	d.fixRow = adw.NewActionRow()
	d.fixRow.SetTitle("Fix")
	d.fixRow.SetSubtitle(fixSubtitle(nil))
	// Two buttons, because the plan has two honest shapes: everything this
	// repository needs, and everything it needs that costs nothing. The free
	// one sits to the left and stays flat — it is the cheaper click, not the
	// primary one.
	freeBtn := gtk.NewButtonWithLabel("Free steps")
	freeBtn.SetVAlign(gtk.AlignCenter)
	freeBtn.AddCSSClass("flat")
	freeBtn.SetTooltipText("Run only the steps that need no API key: no LLM request is sent.")
	freeBtn.ConnectClicked(d.a.actFixFree)
	fixBtn := gtk.NewButtonWithLabel("Fix")
	fixBtn.SetVAlign(gtk.AlignCenter)
	fixBtn.AddCSSClass("suggested-action")
	fixBtn.ConnectClicked(d.a.actFix)
	d.fixRow.AddSuffix(freeBtn)
	d.fixRow.AddSuffix(fixBtn)
	d.fixRow.SetActivatableWidget(fixBtn)
	g.Add(d.fixRow)

	add("Update", "AST re-extraction. Free — no API key, no LLM call.", "update", d.a.actUpdate, false)
	add("Re-cluster", "Recompute communities, keeping placeholder names. Free.", "cluster-only", d.a.actCluster, false)
	add("Export graph.html", "Regenerate the interactive visualization. Free.", "export-html", d.a.actExportHTML, false)
	add("Export wiki", "Markdown articles under graphify-out/wiki/. Free.", "export-wiki", d.a.actExportWiki, false)
	add("Export tree", "D3 collapsible tree (GRAPH_TREE.html). Free.", "tree", d.a.actTree, false)
	add("Check for pending re-extraction", "Reads the needs_update flag. Free.", "check-update", d.a.actCheckUpdate, false)
	add("Extract", "Full AST + semantic extraction. METERED — dispatches LLM requests.", "extract", d.a.actExtract, true)
	add("Label communities", "Name the communities with an LLM. METERED.", "label", d.a.actLabel, true)
	add("Add to global graph", "Merge this graph into ~/.graphify/global-graph.json. Free.", "global-add", d.a.actGlobalAdd, false)
	add("Sync graft index", "Rebuild this checkout's graft/ — graft's wiring graph, the one agents query. Free, tree-sitter only.", "graft-build", d.a.actGraftBuild, false)
	add("Install git hooks", "graphify writes post-commit/post-checkout hooks into this repository.", "hook-install", d.a.actHookInstall, false)

	// The two openers, which are not jobs at all.
	openRow := adw.NewActionRow()
	openRow.SetTitle("Open")
	openRow.SetSubtitle("The checkout, or its graphify-out directory.")
	repoBtn := gtk.NewButtonWithLabel("Repository")
	repoBtn.AddCSSClass("flat")
	repoBtn.SetVAlign(gtk.AlignCenter)
	repoBtn.ConnectClicked(func() { d.openPath(func(r *board.Row) string { return r.Path }) })
	outBtn := gtk.NewButtonWithLabel("graphify-out")
	outBtn.AddCSSClass("flat")
	outBtn.SetVAlign(gtk.AlignCenter)
	outBtn.ConnectClicked(func() { d.openPath(func(r *board.Row) string { return r.Graph.Out }) })
	openRow.AddSuffix(repoBtn)
	openRow.AddSuffix(outBtn)
	g.Add(openRow)

	return g
}

func (d *detailPane) openPath(pick func(*board.Row) string) {
	if d.row == nil {
		return
	}
	p := pick(d.row)
	if err := open.Path(p); err != nil {
		d.a.toastf("open %s: %v", p, err)
	}
}

func (d *detailPane) loadOverview() {
	r := d.row
	if r == nil {
		return
	}
	d.ovTitle.SetText(r.Name)
	d.ovPath.SetText(r.Path)
	d.syncActions(r)

	// Rebuild the fact grid. It is a dozen labels and happens on selection,
	// not on a tick, so a rebuild is cheaper than diffing it.
	for {
		child := d.ovFacts.FirstChild()
		if child == nil {
			break
		}
		d.ovFacts.Remove(child)
	}
	g := r.Graph
	row := 0
	fact := func(k, v, class string) {
		if v == "" {
			return
		}
		kl := gtk.NewLabel(k)
		kl.SetXAlign(0)
		kl.AddCSSClass("dim-label")
		vl := gtk.NewLabel(v)
		vl.SetXAlign(0)
		vl.SetSelectable(true)
		vl.SetWrap(true)
		if class != "" {
			vl.AddCSSClass(class)
		}
		d.ovFacts.Attach(kl, 0, row, 1, 1)
		d.ovFacts.Attach(vl, 1, row, 1, 1)
		row++
	}
	fact("State", stateWord(d.a.displayState(r)), stateClass(d.a.displayState(r)))
	if g.Err != "" {
		fact("", g.Err, "st-broken")
	}
	fact("Output", g.Out, "")
	if r.IsWorktree {
		fact("Worktree", "linked worktree of "+r.Group, "")
	}
	fact("Branch", r.Ref(), "")
	if g.BuiltCommit != "" {
		c := g.BuiltCommit
		if len(c) > 7 {
			c = c[:7]
		}
		if r.Behind {
			fact("Built at", c+" — HEAD is now "+r.ShortSHA(), "behind")
		} else {
			fact("Built at", c+" (current HEAD)", "")
		}
	}
	if g.State != graphstate.StateNone {
		fact("Nodes / links", graphCell(g), "")
		fact("Communities", commCell(g), "")
		fact("Drift", g.DriftString(), "st-stale")
		fact("Drifted files", driftFiles(g), "")
		fact("Settled", settledNote(g), "dim-label")
		if !g.BuiltAt.IsZero() {
			fact("Built", g.BuiltAt.Format("2006-01-02 15:04")+"  ("+board.Age(g.BuiltAt)+" ago)", "")
		}
		fact("Size", board.Bytes(g.SizeBytes), "")
	}
	// The other index. Two lines at most, and only when there is one: a
	// checkout graft has never run on says nothing here, exactly as the Graft
	// column renders a bare ring for it.
	fact("Graft", graftFact(r.Graft), graftClass(r.Graft.State))
	fact("", graftExposureNote(r.Graft), "st-stale")
	if s := d.a.jobFor(r.Path); s != nil {
		fact("Last job", jobHeadline(s), "")
	}

	o := d.a.opts.Store.Override(r.Path)
	d.ovGuard = true
	d.ovBackend.SetText(o.Backend)
	d.ovModel.SetText(o.Model)
	d.ovOut.SetText(o.Out)
	d.ovExtra.SetText(strings.Join(o.Extra, " "))
	d.ovExclude.SetActive(o.ExcludeBatch)
	d.ovGuard = false
}

func (d *detailPane) saveOverride() {
	if d.row == nil || d.ovGuard {
		return
	}
	o := d.a.opts.Store.Override(d.row.Path)
	o.Backend = strings.TrimSpace(d.ovBackend.Text())
	o.Model = strings.TrimSpace(d.ovModel.Text())
	o.Out = strings.TrimSpace(d.ovOut.Text())
	o.Extra = strings.Fields(d.ovExtra.Text())
	o.ExcludeBatch = d.ovExclude.Active()
	d.a.setOverride(d.row.Path, o)
}

// --- Report -----------------------------------------------------------------

func (d *detailPane) buildReport() gtk.Widgetter {
	d.report = gtk.NewTextView()
	d.report.SetEditable(false)
	d.report.SetCursorVisible(false)
	d.report.SetMonospace(true)
	d.report.SetWrapMode(gtk.WrapWord)
	d.report.SetLeftMargin(12)
	d.report.SetRightMargin(12)
	d.report.SetTopMargin(8)

	open := gtk.NewButtonWithLabel("Open in $EDITOR")
	open.AddCSSClass("flat")
	open.ConnectClicked(func() {
		if d.row == nil {
			return
		}
		p := filepath.Join(d.row.Graph.Out, "GRAPH_REPORT.md")
		if err := d.a.opener().InEditor(p); err != nil {
			d.a.toastf("editor: %v", err)
		}
	})
	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(6)
	bar.SetMarginBottom(6)
	bar.Append(open)

	sw := gtk.NewScrolledWindow()
	sw.SetChild(d.report)
	sw.SetVExpand(true)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(bar)
	box.Append(sw)
	return box
}

// maxInlineBytes is how large a file this pane will render inline. Above it
// the pane says so and offers the editor: GRAPH_REPORT.md is 18 kB on the
// reference graph but nothing guarantees that for a monorepo.
const maxInlineBytes = 2 << 20

func (d *detailPane) loadReport() {
	if d.row == nil {
		return
	}
	p := filepath.Join(d.row.Graph.Out, "GRAPH_REPORT.md")
	d.report.Buffer().SetText(readCapped(p, "No GRAPH_REPORT.md yet — run Update or Re-cluster to generate one."))
}

// readCapped reads a file for inline display, refusing to pull an unbounded
// one into the heap.
func readCapped(path, missing string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return missing
	}
	if fi.Size() > maxInlineBytes {
		return path + "\n\nThis file is " + board.Bytes(fi.Size()) +
			" — too large to render inline. Open it in your editor instead."
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "could not read " + path + ": " + err.Error()
	}
	return string(b)
}

// --- Wiki -------------------------------------------------------------------

func (d *detailPane) buildWiki() gtk.Widgetter {
	d.wikiList = gtk.NewListBox()
	d.wikiList.SetSelectionMode(gtk.SelectionSingle)
	d.wikiList.AddCSSClass("navigation-sidebar")
	d.wikiList.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil || d.row == nil {
			return
		}
		name := row.Name()
		d.wikiBody.Buffer().SetText(readCapped(filepath.Join(d.row.Graph.Out, "wiki", name), ""))
	})

	listSW := gtk.NewScrolledWindow()
	listSW.SetChild(d.wikiList)
	listSW.SetSizeRequest(200, -1)

	d.wikiBody = gtk.NewTextView()
	d.wikiBody.SetEditable(false)
	d.wikiBody.SetCursorVisible(false)
	d.wikiBody.SetMonospace(true)
	d.wikiBody.SetWrapMode(gtk.WrapWord)
	d.wikiBody.SetLeftMargin(12)
	d.wikiBody.SetRightMargin(12)
	bodySW := gtk.NewScrolledWindow()
	bodySW.SetChild(d.wikiBody)
	bodySW.SetHExpand(true)

	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetStartChild(listSW)
	paned.SetEndChild(bodySW)
	paned.SetPosition(220)
	paned.SetVExpand(true)
	return paned
}

func (d *detailPane) loadWiki() {
	if d.row == nil {
		return
	}
	for {
		child := d.wikiList.FirstChild()
		if child == nil {
			break
		}
		d.wikiList.Remove(child)
	}
	dir := filepath.Join(d.row.Graph.Out, "wiki")
	ents, err := os.ReadDir(dir)
	if err != nil {
		d.wikiBody.Buffer().SetText("No wiki yet.\n\nRun the \"Export wiki\" action on this repository " +
			"— it is free, and CLAUDE.md already points agents at graphify-out/wiki/index.md " +
			"for broad navigation.")
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	// index.md first — it is the navigation entry point — then the rest
	// alphabetically.
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "index.md") != (names[j] == "index.md") {
			return names[i] == "index.md"
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		row := gtk.NewListBoxRow()
		row.SetName(n)
		l := label(strings.TrimSuffix(n, ".md"))
		l.SetMarginStart(8)
		l.SetMarginEnd(8)
		l.SetMarginTop(6)
		l.SetMarginBottom(6)
		row.SetChild(l)
		d.wikiList.Append(row)
	}
	if len(names) > 0 {
		d.wikiList.SelectRow(d.wikiList.RowAtIndex(0))
	}
}

// --- Jobs (this repo) -------------------------------------------------------

func (d *detailPane) buildJobs() gtk.Widgetter {
	d.jobHead = gtk.NewLabel("")
	d.jobHead.SetXAlign(0)
	d.jobHead.SetSelectable(true)
	d.jobHead.SetWrap(true)

	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.AddCSSClass("destructive-action")
	cancel.ConnectClicked(func() { d.a.cancelCurrent() })
	d.jobPause = gtk.NewButtonWithLabel("Pause")
	d.jobPause.AddCSSClass("flat")
	d.jobPause.ConnectClicked(func() { d.a.pauseCurrent() })
	copyCmd := gtk.NewButtonWithLabel("Copy command line")
	copyCmd.AddCSSClass("flat")
	copyCmd.ConnectClicked(func() {
		if d.row == nil {
			return
		}
		if s := d.a.jobFor(d.row.Path); s != nil {
			d.a.win.Clipboard().SetText(s.Command())
			d.a.toast("command line copied")
		}
	})

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(6)
	bar.SetMarginBottom(6)
	d.jobHead.SetHExpand(true)
	bar.Append(d.jobHead)
	bar.Append(copyCmd)
	bar.Append(d.jobPause)
	bar.Append(cancel)

	d.jobLog = gtk.NewTextView()
	d.jobLog.SetEditable(false)
	d.jobLog.SetCursorVisible(false)
	d.jobLog.SetMonospace(true)
	d.jobLog.SetWrapMode(gtk.WrapWordChar)
	d.jobLog.SetLeftMargin(12)
	d.jobLog.SetRightMargin(12)
	d.jobLog.AddCSSClass("joblog")

	sw := gtk.NewScrolledWindow()
	sw.SetChild(d.jobLog)
	sw.SetVExpand(true)

	d.jobsBox = gtk.NewBox(gtk.OrientationVertical, 0)
	d.jobsBox.Append(bar)
	d.jobsBox.Append(d.buildJobHistory())
	d.jobsBox.Append(sw)
	return d.jobsBox
}

func (d *detailPane) buildJobHistory() gtk.Widgetter {
	// The persisted history for this repository, expandable. It answers "what
	// did this do last Tuesday, and did it fail?" — which is the question a
	// toast that disappeared cannot.
	d.histBox = gtk.NewBox(gtk.OrientationVertical, 2)
	d.histBox.SetMarginStart(12)
	d.histBox.SetMarginEnd(12)
	exp := gtk.NewExpander("History")
	exp.SetChild(d.histBox)
	exp.SetMarginStart(12)
	exp.SetMarginBottom(6)
	return exp
}

func (d *detailPane) reloadJobs() {
	if d.row == nil {
		return
	}
	s := d.a.jobFor(d.row.Path)
	setPauseButton(d.jobPause, s)
	if s == nil {
		d.jobHead.SetText("No job has run against this repository in this session.")
		d.jobLog.Buffer().SetText("")
		d.jobGen, d.jobID = 0, 0
	} else {
		d.jobHead.SetText(jobHeadline(s))
		// Tail rather than the whole ring: the buffer is a quarter of a
		// megabyte and GtkTextView does not enjoy being handed all of it
		// every second.
		d.jobLog.Buffer().SetText(s.Log.Tail(400))
		d.jobGen, d.jobID = s.Log.Gen(), s.ID
		d.scrollLogToEnd()
	}
	d.loadHistory()
}

func (d *detailPane) scrollLogToEnd() {
	buf := d.jobLog.Buffer()
	mark := buf.CreateMark("end", buf.EndIter(), false)
	d.jobLog.ScrollMarkOnscreen(mark)
}

func (d *detailPane) loadHistory() {
	for {
		child := d.histBox.FirstChild()
		if child == nil {
			break
		}
		d.histBox.Remove(child)
	}
	n := 0
	for _, h := range d.a.opts.Store.History() {
		if h.Repo != d.row.Path {
			continue
		}
		l := monoLabel(historyLine(h))
		if h.Status == "failed" {
			l.AddCSSClass("st-broken")
		}
		d.histBox.Append(l)
		n++
		if n >= 12 {
			break
		}
	}
	if n == 0 {
		d.histBox.Append(label("nothing yet"))
	}
}

func historyLine(h store.HistoryEntry) string {
	return h.Started.Format("2006-01-02 15:04") + "  " + h.Kind +
		"  " + h.Status + "  " + shortDurSeconds(h.Duration) + "  " + gfy.Quote(h.Argv)
}

func shortDurSeconds(sec float64) string {
	if sec < 60 {
		return gfy.Itoa(int(sec)) + "s"
	}
	return gfy.Itoa(int(sec)/60) + "m" + gfy.Itoa(int(sec)%60) + "s"
}

func jobHeadline(s *jobs.Snapshot) string {
	var b strings.Builder
	b.WriteString(s.Label)
	b.WriteString(" — ")
	if s.Status == jobs.Running && s.Paused {
		b.WriteString("paused")
	} else if s.Status == jobs.Queued && s.Held {
		b.WriteString("held — waiting to be started")
	} else {
		b.WriteString(s.Status.String())
	}
	if s.Status == jobs.Running || s.Status.Done() {
		b.WriteString(" (")
		b.WriteString(shortDur(s.Elapsed()))
		b.WriteString(")")
	}
	if s.Cost == gfy.Metered {
		b.WriteString("  [metered]")
	}
	if s.Status.Done() && s.Exit != 0 {
		b.WriteString("  exit ")
		b.WriteString(gfy.Itoa(s.Exit))
	}
	return b.String()
}

// openConfig is open.Config, aliased so the detail pane reads without an
// import that would otherwise appear only here.
type openConfig = open.Config

// opener resolves the external-tool config from settings.
func (a *App) opener() openConfig {
	set := a.opts.Store.Settings()
	c := a.opts.Opener
	if c.Terminal == "" {
		c.Terminal = set.Terminal
	}
	if c.Editor == "" {
		c.Editor = set.Editor
	}
	return c
}
