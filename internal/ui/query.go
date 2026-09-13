package ui

import (
	"encoding/json"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// queryMode is one of the read-only traversals the console exposes. They are
// deterministic graph operations, not a conversation: the console runs
// graphify's own traversals and renders what comes back.
type queryMode struct {
	id     string
	title  string
	kind   string
	needsB bool // takes a second node (path A B)
	hint   string
}

var queryModes = []queryMode{
	{"query", "Query", "query", false, "a question in plain language"},
	{"explain", "Explain", "explain", false, "a node label, e.g. App"},
	{"path", "Path", "path", true, "a node label"},
	{"affected", "Affected by", "affected", false, "a node label"},
	{"god-nodes", "God nodes", "god-nodes", false, "(no input needed)"},
	{"diagnose", "Diagnose", "diagnose", false, "(no input needed)"},
	{"benchmark", "Benchmark", "benchmark", false, "(no input needed)"},
}

// queryPage is the console over graphify's read-only commands.
type queryPage struct {
	a      *App
	widget gtk.Widgetter

	mode   *gtk.DropDown
	inputA *gtk.Entry
	inputB *gtk.Entry
	budget *gtk.SpinButton
	depth  *gtk.SpinButton
	dfs    *gtk.CheckButton
	out    *gtk.TextView
	save   *gtk.Button

	row     *board.Row
	jobID   uint64
	lastGen uint64
	// lastQ and lastA are what `save-result` would record: the question asked
	// and the answer graphify gave, kept so the feedback loop is one click.
	lastQ, lastA string
}

func (a *App) newQueryPage() *queryPage {
	q := &queryPage{a: a}

	names := make([]string, len(queryModes))
	for i, m := range queryModes {
		names[i] = m.title
	}
	q.mode = gtk.NewDropDownFromStrings(names)
	q.mode.NotifyProperty("selected", func() { q.syncInputs() })

	q.inputA = gtk.NewEntry()
	q.inputA.SetHExpand(true)
	q.inputA.ConnectActivate(func() { q.run() })
	q.inputB = gtk.NewEntry()
	q.inputB.SetHExpand(true)
	q.inputB.SetPlaceholderText("to node")
	q.inputB.ConnectActivate(func() { q.run() })

	q.budget = gtk.NewSpinButtonWithRange(0, 200000, 500)
	q.budget.SetValue(2000)
	q.budget.SetTooltipText("--budget: cap the answer at this many tokens (0 = graphify's default)")
	q.depth = gtk.NewSpinButtonWithRange(0, 10, 1)
	q.depth.SetValue(2)
	q.depth.SetTooltipText("--depth: reverse traversal depth for `affected`")
	q.dfs = gtk.NewCheckButtonWithLabel("DFS")
	q.dfs.SetTooltipText("--dfs: depth-first instead of breadth-first")

	runBtn := gtk.NewButtonWithLabel("Run")
	runBtn.AddCSSClass("suggested-action")
	runBtn.ConnectClicked(func() { q.run() })

	q.save = gtk.NewButtonWithLabel("Save result")
	q.save.AddCSSClass("flat")
	q.save.SetTooltipText("graphify save-result — feeds the answer back into graphify-out/memory/")
	q.save.SetSensitive(false)
	q.save.ConnectClicked(func() { q.saveResult() })

	copyBtn := gtk.NewButtonWithLabel("Copy")
	copyBtn.AddCSSClass("flat")
	copyBtn.ConnectClicked(func() {
		a.win.Clipboard().SetText(q.text())
		a.toast("result copied")
	})

	row1 := gtk.NewBox(gtk.OrientationHorizontal, 6)
	row1.Append(q.mode)
	row1.Append(q.inputA)
	row1.Append(q.inputB)
	row1.Append(runBtn)

	row2 := gtk.NewBox(gtk.OrientationHorizontal, 6)
	row2.Append(gtk.NewLabel("budget"))
	row2.Append(q.budget)
	row2.Append(gtk.NewLabel("depth"))
	row2.Append(q.depth)
	row2.Append(q.dfs)
	spacer := gtk.NewLabel("")
	spacer.SetHExpand(true)
	row2.Append(spacer)
	row2.Append(copyBtn)
	row2.Append(q.save)

	bar := gtk.NewBox(gtk.OrientationVertical, 6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(8)
	bar.SetMarginBottom(8)
	bar.Append(row1)
	bar.Append(row2)

	q.out = gtk.NewTextView()
	q.out.SetEditable(false)
	q.out.SetMonospace(true)
	q.out.SetWrapMode(gtk.WrapWordChar)
	q.out.SetLeftMargin(12)
	q.out.SetRightMargin(12)
	q.out.AddCSSClass("joblog")

	sw := gtk.NewScrolledWindow()
	sw.SetChild(q.out)
	sw.SetVExpand(true)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(bar)
	box.Append(sw)
	q.widget = box
	q.syncInputs()
	return q
}

func (q *queryPage) current() queryMode { return queryModes[int(q.mode.Selected())] }

// syncInputs shows only the controls the selected mode actually uses. A
// budget spinner beside `god-nodes`, which does not take one, is a lie about
// the command line.
func (q *queryPage) syncInputs() {
	m := q.current()
	needsInput := m.id != "god-nodes" && m.id != "diagnose" && m.id != "benchmark"
	q.inputA.SetVisible(needsInput)
	q.inputA.SetPlaceholderText(m.hint)
	q.inputB.SetVisible(m.needsB)
	q.budget.SetVisible(m.id == "query")
	q.dfs.SetVisible(m.id == "query")
	q.depth.SetVisible(m.id == "affected")
}

func (q *queryPage) show(r *board.Row) {
	q.row = r
	if r == nil {
		return
	}
	if r.Graph.Nodes == 0 {
		q.out.Buffer().SetText("This repository has no graph yet.\n\n" +
			"Run Update on the Overview page — it is free and needs no API key.")
	}
}

func (q *queryPage) text() string {
	buf := q.out.Buffer()
	return buf.Text(buf.StartIter(), buf.EndIter(), false)
}

// run submits the read-only job and streams its output into the pane.
//
// Read-only commands are not serialized against a repository's mutating job:
// asking the graph a question while an update rebuilds it is harmless, and
// blocking on it would make the console feel broken.
func (q *queryPage) run() {
	if q.row == nil {
		q.a.toast("select a repository first")
		return
	}
	if !q.a.hasGraph(*q.row) {
		q.a.offerExtract(q.current().kind, []board.Row{*q.row})
		return
	}
	m := q.current()
	p := q.a.params(*q.row)
	p.Question = strings.TrimSpace(q.inputA.Text())
	p.NodeA = p.Question
	p.NodeB = strings.TrimSpace(q.inputB.Text())
	p.DFS = q.dfs.Active()
	p.Budget = int(q.budget.Value())
	p.Depth = int(q.depth.Value())
	p.Top = 20

	if m.id != "god-nodes" && m.id != "diagnose" && m.id != "benchmark" && p.Question == "" {
		q.a.toast("nothing to ask")
		return
	}
	if m.needsB && p.NodeB == "" {
		q.a.toast("`path` needs a second node")
		return
	}

	job, err := q.a.runner.SubmitCmd(m.kind, q.row.Path, m.title+" · "+q.row.Name, p, q.a.opts.Store.Overlay(q.row.Path))
	if err != nil {
		q.a.toastf("%v", err)
		return
	}
	q.jobID = job.ID
	q.lastQ = p.Question
	q.lastA = ""
	q.save.SetSensitive(false)
	q.out.Buffer().SetText("$ " + gfy.Quote(job.Argv) + "\n\nrunning…\n")
}

// tick pulls the running query's output into the pane. The query console is
// the one place where a job's stdout *is* the result, so it renders the whole
// buffer rather than a tail.
func (q *queryPage) tick() {
	if q.jobID == 0 {
		return
	}
	var s *jobs.Snapshot
	for _, snap := range q.a.runner.Snapshot() {
		if snap.ID == q.jobID {
			snap := snap
			s = &snap
			break
		}
	}
	if s == nil {
		return
	}
	gen := s.Log.Gen()
	if gen == q.lastGen {
		return
	}
	q.lastGen = gen
	out := s.Log.String()
	q.out.Buffer().SetText(prettyIfJSON(out))
	if s.Status.Done() {
		q.lastA = out
		q.save.SetSensitive(s.Status == jobs.Succeeded && q.lastQ != "")
		q.jobID = 0
	}
}

// prettyIfJSON re-indents a --json response so god-nodes and diagnose are
// readable, and leaves everything else exactly as graphify printed it.
//
// The leading platform-skew warning has to come off first: it goes to stderr
// ahead of the payload on every invocation, and a naive Unmarshal would fail
// on it and silently fall back to raw text.
func prettyIfJSON(s string) string {
	trimmed := gfy.TrimNoise(s)
	if trimmed == "" {
		return s
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	// Keep the command line and any warnings above the payload: they are the
	// context that makes a surprising answer explicable.
	prefix := s[:len(s)-len(trimmed)]
	return prefix + string(b) + "\n"
}

// saveResult feeds the answer back into graphify's memory loop.
func (q *queryPage) saveResult() {
	if q.row == nil || q.lastQ == "" || q.lastA == "" {
		return
	}
	p := q.a.params(*q.row)
	p.Question = q.lastQ
	p.NodeA = q.lastA
	if _, err := q.a.runner.SubmitCmd("save-result", q.row.Path, "Save result · "+q.row.Name, p, nil); err != nil {
		q.a.toastf("%v", err)
		return
	}
	q.a.toast("saved to graphify-out/memory/")
	q.save.SetSensitive(false)
}
