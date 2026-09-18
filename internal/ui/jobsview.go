package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// jobsView is the run queue across every repository: what is queued, what is
// running with its live log, what has finished, and the two things you want
// from a finished job — its exit status and its exact command line.
type jobsView struct {
	a   *App
	dlg *adw.Dialog

	list    *gtk.ListBox
	log     *gtk.TextView
	head    *gtk.Label
	pause   *gtk.Button
	rows    []jobs.Snapshot
	sel     uint64
	lastGen uint64
	// stopAll is held because its label carries the count of what it would
	// stop. A button that says "Stop all" over an empty queue and a button
	// that says it over 290 running jobs are different promises, and the
	// second is the one somebody is looking for in a hurry.
	stopAll *gtk.Button
	// start releases held jobs — the queue a restart brought back, waiting
	// for the click it never got. Two buttons rather than one because the two
	// answers are different questions: "run this one" is the row you are
	// looking at, and "run the lot" is the sweep you started yesterday.
	start    *gtk.Button
	startAll *gtk.Button
	// bars is the progress bar of each listed job, by job id. The list is
	// rebuilt on a transition, so the tick needs a handle on the widgets it
	// repaints between rebuilds.
	bars map[uint64]*gtk.ProgressBar
}

func (a *App) showJobs() {
	if a.jobsPage != nil {
		a.jobsPage.dlg.Present(a.win)
		a.jobsPage.reload()
		return
	}
	v := &jobsView{a: a}

	v.list = gtk.NewListBox()
	v.list.SetSelectionMode(gtk.SelectionSingle)
	v.list.AddCSSClass("navigation-sidebar")
	v.list.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil {
			return
		}
		idx := row.Index()
		if idx < 0 || idx >= len(v.rows) {
			return
		}
		v.sel = v.rows[idx].ID
		v.lastGen = 0
		v.showSelected()
	})
	listSW := gtk.NewScrolledWindow()
	listSW.SetChild(v.list)
	listSW.SetSizeRequest(360, -1)

	v.head = gtk.NewLabel("")
	v.head.SetXAlign(0)
	v.head.SetWrap(true)
	v.head.SetSelectable(true)

	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.AddCSSClass("destructive-action")
	cancel.ConnectClicked(func() {
		if v.sel != 0 {
			a.runner.Cancel(v.sel)
		}
	})
	v.pause = gtk.NewButtonWithLabel("Pause")
	v.pause.AddCSSClass("flat")
	v.pause.ConnectClicked(func() {
		if v.sel != 0 {
			a.togglePause(v.sel)
		}
	})
	v.start = gtk.NewButtonWithLabel("Start")
	v.start.AddCSSClass("suggested-action")
	v.start.SetTooltipText("Run this job now. It was restored from a previous session and " +
		"is being held because it costs something — an LLM bill, or hours of this machine.")
	v.start.ConnectClicked(func() {
		s := v.selected()
		if s == nil || !a.runner.Release(s.ID) {
			return
		}
		a.toastf("started %s", s.Label)
		v.reload()
	})
	retry := gtk.NewButtonWithLabel("Retry")
	retry.AddCSSClass("flat")
	retry.ConnectClicked(func() { v.retry() })
	copyCmd := gtk.NewButtonWithLabel("Copy command line")
	copyCmd.AddCSSClass("flat")
	copyCmd.ConnectClicked(func() {
		if s := v.selected(); s != nil {
			a.win.Clipboard().SetText(s.Command())
			a.toast("command line copied")
		}
	})

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(8)
	bar.SetMarginBottom(8)
	v.head.SetHExpand(true)
	bar.Append(v.head)
	bar.Append(v.start)
	bar.Append(copyCmd)
	bar.Append(retry)
	bar.Append(v.pause)
	bar.Append(cancel)

	v.log = gtk.NewTextView()
	v.log.SetEditable(false)
	v.log.SetMonospace(true)
	v.log.SetWrapMode(gtk.WrapWordChar)
	v.log.SetLeftMargin(12)
	v.log.AddCSSClass("joblog")
	logSW := gtk.NewScrolledWindow()
	logSW.SetChild(v.log)
	logSW.SetVExpand(true)

	right := gtk.NewBox(gtk.OrientationVertical, 0)
	right.Append(bar)
	right.Append(logSW)
	right.SetHExpand(true)

	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetStartChild(listSW)
	paned.SetEndChild(right)
	paned.SetPosition(380)
	paned.SetVExpand(true)

	header := adw.NewHeaderBar()
	// Stop all is deliberately NOT behind a confirm, unlike every other
	// destructive control in this application. The gates elsewhere stand
	// between a click and work starting; this one stops work already running,
	// which is the recoverable direction — every job it kills can simply be
	// run again. Friction here would be friction in the one moment somebody
	// needs it immediately, having just started a sweep over the whole board.
	v.stopAll = gtk.NewButtonWithLabel("Stop all")
	v.stopAll.AddCSSClass("destructive-action")
	v.stopAll.SetTooltipText("Cancel every running and queued job, across every repository. " +
		"Nothing is lost that cannot be re-run.")
	v.stopAll.ConnectClicked(func() {
		queued, running := a.runner.Active()
		a.runner.CancelAll()
		a.toastf("stopped %s", plural(queued+running, "job", "jobs"))
		v.reload()
	})
	header.PackEnd(v.stopAll)

	// Unlike Stop all, this one starts work, so it says exactly how much and
	// disappears when the answer is none. It is the counterpart of the toast
	// the board shows at launch: the held queue in one click.
	v.startAll = gtk.NewButtonWithLabel("Start all held")
	v.startAll.AddCSSClass("suggested-action")
	v.startAll.SetTooltipText("Release every held job at once. The lane limits still apply — " +
		"metered jobs run one at a time, as they always do.")
	v.startAll.ConnectClicked(func() {
		n := a.runner.ReleaseAll()
		a.toastf("started %s", plural(n, "held job", "held jobs"))
		v.reload()
	})
	header.PackEnd(v.startAll)

	toolbar := adw.NewToolbarView()
	toolbar.AddTopBar(header)
	toolbar.SetContent(paned)

	v.dlg = adw.NewDialog()
	v.dlg.SetTitle("Jobs")
	v.dlg.SetContentWidth(1100)
	v.dlg.SetContentHeight(700)
	v.dlg.SetChild(toolbar)

	a.jobsPage = v
	v.reload()
	v.dlg.Present(a.win)
}

func (v *jobsView) selected() *jobs.Snapshot {
	for i := range v.rows {
		if v.rows[i].ID == v.sel {
			return &v.rows[i]
		}
	}
	return nil
}

// reload rebuilds the list. It runs on a transition, not on the tick, so a
// full rebuild is the honest thing: the list is bounded at the runner's
// history size and diffing it would cost more than it saves.
func (v *jobsView) reload() {
	snaps := v.a.runner.Snapshot()
	v.rows = snaps
	v.bars = map[uint64]*gtk.ProgressBar{}
	for {
		child := v.list.FirstChild()
		if child == nil {
			break
		}
		v.list.Remove(child)
	}
	selIdx := -1
	for i, s := range snaps {
		if s.ID == v.sel {
			selIdx = i
		}
		v.list.Append(v.jobRow(s))
	}
	if selIdx >= 0 {
		v.list.SelectRow(v.list.RowAtIndex(selIdx))
	} else if len(snaps) > 0 {
		v.sel = snaps[0].ID
		v.list.SelectRow(v.list.RowAtIndex(0))
	}
	v.showSelected()
	v.refreshStopAll()
	v.refreshStart()
}

// refreshStart keeps both start buttons on the actual queue: the per-job one is
// visible only when the selected job is one that can be started, and the
// header one names how many are waiting and hides itself when none are.
func (v *jobsView) refreshStart() {
	if v.start != nil {
		s := v.selected()
		v.start.SetVisible(s != nil && s.Status == jobs.Queued && s.Held)
	}
	if v.startAll == nil {
		return
	}
	n := v.a.runner.HeldCount()
	v.startAll.SetVisible(n > 0)
	v.startAll.SetLabel("Start all held (" + gfy.Itoa(n) + ")")
}

// refreshStopAll keeps the button's label and sensitivity on the actual queue:
// it names the number it would stop, and it is insensitive when that number is
// zero rather than being a live button that does nothing.
func (v *jobsView) refreshStopAll() {
	if v.stopAll == nil {
		return
	}
	queued, running := v.a.runner.Active()
	n := queued + running
	if n == 0 {
		v.stopAll.SetLabel("Stop all")
		v.stopAll.SetSensitive(false)
		return
	}
	v.stopAll.SetLabel("Stop all (" + gfy.Itoa(n) + ")")
	v.stopAll.SetSensitive(true)
}

func (v *jobsView) jobRow(s jobs.Snapshot) *gtk.ListBoxRow {
	box := gtk.NewBox(gtk.OrientationHorizontal, 8)
	box.SetMarginStart(8)
	box.SetMarginEnd(8)
	box.SetMarginTop(6)
	box.SetMarginBottom(6)

	text, class := jobCell(&s)
	dot := gtk.NewLabel("●")
	if class != "" {
		dot.AddCSSClass(class)
	}
	box.Append(dot)

	l := label(s.Label)
	l.SetHExpand(true)
	box.Append(l)

	if s.Cost == gfy.Metered {
		m := gtk.NewLabel("$")
		m.AddCSSClass("metered")
		m.SetTooltipText("This job dispatched LLM requests against your API key.")
		box.Append(m)
	}
	prog := newJobProgressBar()
	box.Append(progressSlot(prog))
	setJobProgress(prog, &s)
	if v.bars != nil {
		v.bars[s.ID] = prog
	}

	status := gtk.NewLabel(text)
	status.AddCSSClass("dim-label")
	box.Append(status)

	row := gtk.NewListBoxRow()
	row.SetChild(box)
	return row
}

func (v *jobsView) showSelected() {
	s := v.selected()
	setPauseButton(v.pause, s)
	if s == nil {
		v.head.SetText("")
		v.log.Buffer().SetText("")
		return
	}
	v.head.SetText(jobHeadline(s) + "\n" + s.Command())
	v.log.Buffer().SetText(s.Log.String())
	v.lastGen = s.Log.Gen()
	v.refreshStart()
}

// tick refreshes the selected job's log when it has moved, and nothing else.
func (v *jobsView) tick() {
	// Before the early return below: the count on Stop all has to keep up with
	// a draining queue even when nothing is selected, which is exactly the
	// state a finished sweep leaves the dialog in.
	v.refreshStopAll()
	v.refreshStart()

	s := v.selected()
	if s == nil {
		return
	}
	if gen := s.Log.Gen(); gen != v.lastGen {
		v.lastGen = gen
		v.log.Buffer().SetText(s.Log.String())
	}
	if !s.Status.Done() {
		v.head.SetText(jobHeadline(s) + "\n" + s.Command())
		setPauseButton(v.pause, s)
	}
	// Every listed job's bar, not just the selected one: the list is the
	// place you watch a queue from, and a bar that only moved when its row
	// was selected would be worse than none.
	for i := range v.rows {
		setJobProgress(v.bars[v.rows[i].ID], &v.rows[i])
	}
}

// retry re-runs a finished job with the identical argv, which is what makes a
// transient failure — a rate limit, a flaky network — a one-click recovery.
func (v *jobsView) retry() {
	s := v.selected()
	if s == nil || !s.Status.Done() {
		return
	}
	v.a.runner.Submit(&jobs.Job{
		Kind: s.Kind, Repo: s.Repo, Label: s.Label, Cost: s.Cost,
		Argv: s.Argv,
		// The identical argv still carries the --max-concurrency the original
		// run was sized with, so the overlay that makes graphify honour it has
		// to be rebuilt here too. A retry that dropped it would quietly run
		// serially and look like the first attempt having been slow.
		Env: gfy.ClaudeAccountEnv(
			gfy.ClaudeCLIModelEnv(
				gfy.ClaudeCLIEnv(
					gfy.LocalEnv(gfy.JobEnv(v.a.opts.Store.Overlay(s.Repo)), gfy.ArgvBackend(s.Argv)),
					gfy.ArgvBackend(s.Argv)),
				gfy.ArgvBackend(s.Argv), gfy.ArgvModel(s.Argv)),
			v.a.opts.Store.Settings().ClaudeAccount),
		Dir: s.Repo,
	})
	v.a.toastf("re-queued %s", s.Label)
}

// --- the global graph view ---------------------------------------------------

// showGlobal is the cross-repo surface: which repositories are in
// ~/.graphify/global-graph.json, adding the current selection to it, and
// merging a chosen subset into one file. This is the answer the CLI makes
// tedious — the whole reason a board over every checkout is worth having.
func (a *App) showGlobal() {
	page := adw.NewPreferencesPage()

	out := gtk.NewTextView()
	out.SetEditable(false)
	out.SetMonospace(true)
	out.SetWrapMode(gtk.WrapWordChar)
	out.SetLeftMargin(12)
	out.AddCSSClass("joblog")
	out.Buffer().SetText("Press “List” to read ~/.graphify/global-graph.json.")

	g := adw.NewPreferencesGroup()
	g.SetTitle("Global graph")
	g.SetDescription("~/.graphify/global-graph.json — one graph across every repository you have added to it.")

	listRow := adw.NewActionRow()
	listRow.SetTitle("List repositories in the global graph")
	listRow.SetSubtitle("graphify global list")
	listBtn := gtk.NewButtonWithLabel("List")
	listBtn.SetVAlign(gtk.AlignCenter)
	listBtn.AddCSSClass("flat")
	listBtn.ConnectClicked(func() {
		job, err := a.runner.SubmitCmd("global-list", "", "Global list", gfy.Params{}, nil)
		if err != nil {
			a.toastf("%v", err)
			return
		}
		a.watchInto(job.ID, out)
	})
	listRow.AddSuffix(listBtn)
	g.Add(listRow)

	addRow := adw.NewActionRow()
	addRow.SetTitle("Add the selection to the global graph")
	addRow.SetSubtitle("graphify global add <graph.json> --as <tag>")
	addBtn := gtk.NewButtonWithLabel("Add")
	addBtn.SetVAlign(gtk.AlignCenter)
	addBtn.AddCSSClass("flat")
	addBtn.ConnectClicked(func() { a.actGlobalAdd() })
	addRow.AddSuffix(addBtn)
	g.Add(addRow)

	mergeRow := adw.NewActionRow()
	mergeRow.SetTitle("Merge the selected repositories into one graph")
	mergeRow.SetSubtitle("graphify merge-graphs g1 g2 … --out merged-graph.json")
	mergeBtn := gtk.NewButtonWithLabel("Merge")
	mergeBtn.SetVAlign(gtk.AlignCenter)
	mergeBtn.AddCSSClass("flat")
	mergeBtn.ConnectClicked(func() { a.mergeSelected(out) })
	mergeRow.AddSuffix(mergeBtn)
	g.Add(mergeRow)

	page.Add(g)

	outGroup := adw.NewPreferencesGroup()
	outGroup.SetTitle("Output")
	sw := gtk.NewScrolledWindow()
	sw.SetChild(out)
	sw.SetSizeRequest(-1, 320)
	outRow := adw.NewActionRow()
	outRow.SetChild(sw)
	outGroup.Add(outRow)
	page.Add(outGroup)

	dlg := adw.NewPreferencesDialog()
	dlg.SetTitle("Global graph")
	dlg.Add(page)
	dlg.Present(a.win)
}

// mergeSelected merges every selected repository's graph.json into one file.
func (a *App) mergeSelected(out *gtk.TextView) {
	rows := a.batch()
	if len(rows) < 2 {
		a.toast("merge needs at least two repositories — use select mode (v) and Space")
		return
	}
	var graphs []string
	for _, r := range rows {
		if r.Graph.Nodes == 0 {
			continue
		}
		graphs = append(graphs, r.Graph.Out+"/graph.json")
	}
	if len(graphs) < 2 {
		a.toast("at least two of the selected repositories need a graph first")
		return
	}
	p := gfy.Params{Graphs: graphs, OutFile: "merged-graph.json"}
	job, err := a.runner.SubmitCmd("merge-graphs", "", "Merge "+plural(len(graphs), "graph", "graphs"), p, nil)
	if err != nil {
		a.toastf("%v", err)
		return
	}
	a.watchInto(job.ID, out)
}

// watchInto streams a job's output into a text view until it finishes. It is
// the one place the board polls rather than listening, because these are
// one-shot dialogs whose lifetime is shorter than a subscription's.
func (a *App) watchInto(id uint64, view *gtk.TextView) {
	view.Buffer().SetText("running…\n")
	var lastGen uint64
	tick := func() bool {
		s, ok := a.runner.Get(id)
		if !ok {
			return false
		}
		if gen := s.Log.Gen(); gen != lastGen {
			lastGen = gen
			view.Buffer().SetText(strings.TrimRight(prettyIfJSON(s.Log.String()), "\n"))
		}
		return !s.Status.Done()
	}
	timeoutAdd(400, tick)
}
