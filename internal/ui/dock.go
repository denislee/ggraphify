package ui

import (
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// The bottom dock is the always-on half of two things the board previously
// only had behind a dialog.
//
//   - Jobs. The header's activity indicator says how many, and the jobs
//     dialog (J) says everything — but both are things you have to go and
//     look at. A strip pinned to the bottom of the window is the answer to
//     "what is this machine doing right now" without a click, which is the
//     whole point of a board.
//   - The application's own log. Not a job's stdout, which belongs to that
//     job: ggraphify's own account of what it did and what went wrong. A
//     board launched from fuzzel has no terminal, so before this pane that
//     account went nowhere at all. It carries one button that copies the
//     whole log with a description of the machine under it, because the
//     thing you actually want to do with a log is hand it to somebody — or
//     to an agent — and ask why.
//
// Both pages are optional and both are on by default; the dock hides itself
// when the last one is switched off, and the settings dialog is where that
// happens.
const (
	dockPageJobs = "jobs"
	dockPageLog  = "log"

	// dockMinHeight is how small the splitter may be dragged. Below this the
	// pane shows a row and a half and is worse than useless.
	dockMinHeight = 90
	// dockDefaultHeight is the first-run height: about six job rows.
	dockDefaultHeight = 220
	// dockMaxJobRows bounds the strip. It is a glance, not the jobs dialog;
	// everything older than this is one click away in J.
	dockMaxJobRows = 60
)

type dockPane struct {
	a      *App
	widget *gtk.Box

	stack    *gtk.Stack
	switcher *gtk.StackSwitcher
	pageJobs *gtk.StackPage
	pageLog  *gtk.StackPage
	actions  *gtk.Stack

	// The jobs strip. rows is parallel to what the list holds, so the
	// once-a-second tick can repaint an elapsed time without rebuilding a
	// single widget; key is the set the list was last built from, so the
	// rebuild happens exactly when the queue actually moves.
	jobsList    *gtk.ListBox
	jobsEmpty   *gtk.Label
	jobsSummary *gtk.Label
	rows        []*dockJobRow
	key         string
	showDone    bool

	// The log pane.
	logView *gtk.TextView
	logSW   *gtk.ScrolledWindow
	logGen  uint64
	follow  bool
	// verbose shows the debug level too, which in practice means GTK's own
	// narration. Off by default and remembered for the session only: it is a
	// thing you turn on for five minutes while chasing something.
	verbose bool
}

// dockJobRow is the mutable part of one strip row.
// Only the status text and the tooltip are repainted between rebuilds — the
// mark and its colour belong to a status, and a status change rebuilds the row.
type dockJobRow struct {
	id     uint64
	class  string
	status *gtk.Label
	row    *gtk.ListBoxRow
	// prog is the running job's progress bar. It is filled when the job's
	// output carries a fraction and pulsed when it does not, on the same
	// tick that repaints the elapsed time.
	prog *gtk.ProgressBar
}

// buildDock constructs the panel. It is added to the window's vertical paned
// whether or not it is enabled; visibility is what the setting moves, because
// building it lazily would mean the log pane misses everything logged before
// it was first shown.
func (a *App) buildDock() *dockPane {
	d := &dockPane{a: a, showDone: true, follow: true}

	d.stack = gtk.NewStack()
	d.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	d.stack.SetVExpand(true)

	d.pageJobs = d.stack.AddTitled(d.buildJobsPage(), dockPageJobs, "Jobs")
	d.pageLog = d.stack.AddTitled(d.buildLogPage(), dockPageLog, "Log")

	d.switcher = gtk.NewStackSwitcher()
	d.switcher.SetStack(d.stack)

	// One action bar per page, swapped by the same name the stack uses, so
	// "Cancel all" is never on screen next to the log.
	d.actions = gtk.NewStack()
	d.actions.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	d.actions.AddTitled(d.jobsActions(), dockPageJobs, "Jobs")
	d.actions.AddTitled(d.logActions(), dockPageLog, "Log")

	hide := gtk.NewButtonFromIconName("go-down-symbolic")
	hide.AddCSSClass("flat")
	hide.SetTooltipText("Hide the bottom panel (b). Bring it back from settings, or with b.")
	hide.ConnectClicked(func() { a.setDockVisible(false) })

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.AddCSSClass("dockbar")
	bar.SetMarginStart(8)
	bar.SetMarginEnd(8)
	bar.SetMarginTop(4)
	bar.SetMarginBottom(4)
	spacer := gtk.NewBox(gtk.OrientationHorizontal, 0)
	spacer.SetHExpand(true)
	bar.Append(d.switcher)
	bar.Append(spacer)
	bar.Append(d.actions)
	bar.Append(hide)

	d.widget = gtk.NewBox(gtk.OrientationVertical, 0)
	d.widget.AddCSSClass("dock")
	d.widget.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	d.widget.Append(bar)
	d.widget.Append(d.stack)
	d.widget.SetSizeRequest(-1, dockMinHeight)

	// The action bar follows the page, and a page that has been read stops
	// asking for attention.
	d.stack.NotifyProperty("visible-child-name", func() {
		name := d.stack.VisibleChildName()
		d.actions.SetVisibleChildName(name)
		if name == dockPageLog {
			d.pageLog.SetNeedsAttention(false)
			d.renderLog(true)
		}
		if name == dockPageJobs {
			d.pageJobs.SetNeedsAttention(false)
		}
		s := a.opts.Store.Settings()
		if s.BottomPage != name {
			s.BottomPage = name
			a.opts.Store.SetSettings(s)
		}
	})

	return d
}

// --- the jobs strip ----------------------------------------------------------

func (d *dockPane) buildJobsPage() gtk.Widgetter {
	d.jobsList = gtk.NewListBox()
	d.jobsList.SetSelectionMode(gtk.SelectionNone)
	d.jobsList.AddCSSClass("docklist")

	d.jobsEmpty = gtk.NewLabel("Nothing is running. Press u to update the selected " +
		"repository, or h to run whatever it needs.")
	d.jobsEmpty.AddCSSClass("dim-label")
	d.jobsEmpty.SetWrap(true)
	d.jobsEmpty.SetVExpand(true)
	d.jobsEmpty.SetHExpand(true)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(d.jobsEmpty)
	box.Append(d.jobsList)

	sw := gtk.NewScrolledWindow()
	sw.SetChild(box)
	sw.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sw.SetVExpand(true)
	return sw
}

func (d *dockPane) jobsActions() gtk.Widgetter {
	// The one-line answer, left of the buttons: how many are running, how many
	// are waiting, how the finished ones ended, and how much of each lane is
	// in use. It is what you read when you do not want to read the rows.
	d.jobsSummary = gtk.NewLabel("")
	d.jobsSummary.AddCSSClass("docknote")
	d.jobsSummary.SetSingleLineMode(true)
	d.jobsSummary.SetTooltipText("Running and queued jobs, how the finished ones ended, " +
		"and how many of the free and metered lanes are busy.")

	done := gtk.NewToggleButton()
	done.SetLabel("Finished")
	done.SetActive(d.showDone)
	done.AddCSSClass("flat")
	done.SetTooltipText("Also list jobs that have already ended.")
	done.ConnectToggled(func() {
		d.showDone = done.Active()
		d.key = ""
		d.reloadJobs()
	})

	open := gtk.NewButtonWithLabel("Open jobs view")
	open.AddCSSClass("flat")
	open.SetTooltipText("The full jobs dialog, with each job's log and command line (J)")
	open.ConnectClicked(func() { d.a.showJobs() })

	cancelAll := gtk.NewButtonWithLabel("Cancel all")
	cancelAll.AddCSSClass("flat")
	cancelAll.AddCSSClass("destructive-action")
	cancelAll.ConnectClicked(func() { d.a.runner.CancelAll() })

	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.Append(d.jobsSummary)
	box.Append(done)
	box.Append(open)
	box.Append(cancelAll)
	return box
}

// dockEntry is one line of the strip: either a group header or a job under it.
// The headers are half the point — "nothing is queued" is an answer, and a
// flat list of rows only ever says it by omission.
type dockEntry struct {
	header string // non-empty for a group header
	count  int    // how many jobs the header covers
	note   string // the header's trailing detail: lane usage, how they ended
	snap   jobs.Snapshot
	pos    int    // 1-based position in the queue, for a queued job
	wait   string // why a queued job has not started yet
}

// isHeader distinguishes the two kinds of entry.
func (e dockEntry) isHeader() bool { return e.header != "" }

// dockEntries is what the strip lists: running first, then queued, then the
// most recently finished — the order in which they matter — each group under a
// header that says how many there are.
//
// Within a group the order is oldest first, because a queue is read in the
// order it will run and the running jobs started in that same order.
func (d *dockPane) dockEntries() []dockEntry {
	var running, queued, done []jobs.Snapshot
	for _, s := range d.a.runner.Snapshot() {
		switch s.Status {
		case jobs.Running:
			running = append(running, s)
		case jobs.Queued:
			queued = append(queued, s)
		default:
			done = append(done, s)
		}
	}
	reverseSnaps(running)
	reverseSnaps(queued)

	lanes := d.laneUse(running)
	out := []dockEntry{{header: "Running", count: len(running), note: lanes.String()}}
	for _, s := range running {
		out = append(out, dockEntry{snap: s})
	}

	out = append(out, dockEntry{header: "Queued", count: len(queued)})
	for i, s := range queued {
		out = append(out, dockEntry{snap: s, pos: i + 1, wait: d.waitReason(s, lanes)})
	}

	fin := dockEntry{header: "Finished", count: len(done), note: outcomeNote(done)}
	if !d.showDone {
		if len(done) > 0 {
			fin.note = "hidden — press Finished to list them"
		}
		return append(out, fin)
	}
	out = append(out, fin)
	if len(done) > dockMaxJobRows {
		done = done[:dockMaxJobRows]
	}
	for _, s := range done {
		out = append(out, dockEntry{snap: s})
	}
	return out
}

// laneUse is how much of each lane is busy — the reason a queued job is still
// queued, and the one number the strip could not previously show at all.
type laneUse struct {
	freeBusy, freeLanes       int
	meteredBusy, meteredLanes int
}

func (l laneUse) String() string {
	return fmt.Sprintf("free %d/%d · metered %d/%d",
		l.freeBusy, l.freeLanes, l.meteredBusy, l.meteredLanes)
}

// full reports whether the lane a job of this cost would take is saturated.
func (l laneUse) full(c gfy.Cost) bool {
	if c == gfy.Metered {
		return l.meteredBusy >= l.meteredLanes
	}
	return l.freeBusy >= l.freeLanes
}

func (d *dockPane) laneUse(running []jobs.Snapshot) laneUse {
	l := laneUse{}
	l.freeLanes, l.meteredLanes = d.a.runner.Lanes()
	for _, s := range running {
		if s.Cost == gfy.Metered {
			l.meteredBusy++
		} else {
			l.freeBusy++
		}
	}
	return l
}

// waitReason says why a queued job is not running. There are exactly three
// answers the runner's scheduler can give — the repository is held by a
// mutating job, the lane is full, or nothing is in the way and it starts on the
// next dispatch — and a strip that shows "queued" for all three is the reason
// somebody goes looking for a hung job that is not hung.
func (d *dockPane) waitReason(s jobs.Snapshot, l laneUse) string {
	if s.Repo != "" {
		if id, held := d.a.runner.Busy(s.Repo); held && id != s.ID {
			return "waiting for job " + gfy.Itoa(int(id)) + " on the same repository"
		}
	}
	if l.full(s.Cost) {
		return "waiting for a " + laneName(s.Cost) + " lane"
	}
	return "starting"
}

func laneName(c gfy.Cost) string {
	if c == gfy.Metered {
		return "metered"
	}
	return "free"
}

// outcomeNote breaks the finished group down by how it ended, which is the
// thing you actually want from a list of things that are no longer running.
func outcomeNote(done []jobs.Snapshot) string {
	var ok, failed, cancelled int
	for _, s := range done {
		switch s.Status {
		case jobs.Succeeded:
			ok++
		case jobs.Failed:
			failed++
		default:
			cancelled++
		}
	}
	var parts []string
	if ok > 0 {
		parts = append(parts, gfy.Itoa(ok)+" ok")
	}
	if failed > 0 {
		parts = append(parts, gfy.Itoa(failed)+" failed")
	}
	if cancelled > 0 {
		parts = append(parts, gfy.Itoa(cancelled)+" cancelled")
	}
	return strings.Join(parts, " · ")
}

func reverseSnaps(s []jobs.Snapshot) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// reloadJobs rebuilds the strip when the set of jobs has moved, and does
// nothing at all when it has not. It is called on every job event and on the
// one-second tick, so "does nothing" is the case that has to be cheap.
func (d *dockPane) reloadJobs() {
	if d.widget == nil || !d.widget.Visible() {
		return
	}
	entries := d.dockEntries()
	d.setJobsSummary(entries)
	key := dockKey(entries)
	if key == d.key {
		return
	}
	d.key = key

	for {
		child := d.jobsList.FirstChild()
		if child == nil {
			break
		}
		d.jobsList.Remove(child)
	}
	d.rows = d.rows[:0]
	total := 0
	for _, e := range entries {
		if e.isHeader() {
			d.jobsList.Append(d.headerRow(e))
			continue
		}
		total++
		d.jobsList.Append(d.jobRow(e))
	}
	// The placeholder stands in for the whole strip only when there is nothing
	// at all to say; once a single job exists the headers carry the emptiness.
	empty := total == 0 && !d.hasHiddenJobs(entries)
	d.jobsEmpty.SetVisible(empty)
	d.jobsList.SetVisible(!empty)
}

// hasHiddenJobs reports whether a group is non-empty but unlisted, which is
// the case where the headers have something to say and the placeholder does not.
func (d *dockPane) hasHiddenJobs(entries []dockEntry) bool {
	for _, e := range entries {
		if e.isHeader() && e.count > 0 {
			return true
		}
	}
	return false
}

// setJobsSummary writes the one-line account into the action bar, and puts the
// number of jobs in flight on the tab title so it is legible from the log page.
//
// It reads the group headers rather than recounting the snapshots, so the line
// and the rows under it can never disagree about how many are running.
func (d *dockPane) setJobsSummary(entries []dockEntry) {
	var running, queued int
	var lanes, finished string
	for _, e := range entries {
		if !e.isHeader() {
			continue
		}
		switch e.header {
		case "Running":
			running, lanes = e.count, e.note
		case "Queued":
			queued = e.count
		case "Finished":
			finished = e.note
		}
	}

	parts := []string{gfy.Itoa(running) + " running", gfy.Itoa(queued) + " queued"}
	if d.showDone && finished != "" {
		parts = append(parts, finished)
	}
	if lanes != "" {
		parts = append(parts, lanes)
	}
	if d.jobsSummary != nil {
		d.jobsSummary.SetText(strings.Join(parts, "  ·  "))
	}
	if d.pageJobs != nil {
		title := "Jobs"
		if n := running + queued; n > 0 {
			title = "Jobs · " + gfy.Itoa(n)
		}
		d.pageJobs.SetTitle(title)
	}
}

// headerRow is a group heading: the name, how many jobs it holds, and the
// group's own detail — lane usage for the running ones, the ok/failed/cancelled
// breakdown for the finished ones.
func (d *dockPane) headerRow(e dockEntry) *gtk.ListBoxRow {
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.SetMarginStart(8)
	box.SetMarginEnd(8)
	box.SetMarginTop(6)
	box.SetMarginBottom(2)

	name := gtk.NewLabel(strings.ToUpper(e.header) + "  " + gfy.Itoa(e.count))
	name.SetXAlign(0)
	name.AddCSSClass("dockhead")
	if e.count == 0 {
		name.AddCSSClass("st-none")
	} else if e.header == "Running" {
		name.AddCSSClass("st-running")
	}
	box.Append(name)

	if e.count == 0 {
		empty := gtk.NewLabel(emptyGroupNote(e.header))
		empty.AddCSSClass("docknote")
		box.Append(empty)
	}

	spacer := gtk.NewBox(gtk.OrientationHorizontal, 0)
	spacer.SetHExpand(true)
	box.Append(spacer)

	if e.note != "" {
		note := gtk.NewLabel(e.note)
		note.AddCSSClass("docknote")
		box.Append(note)
	}

	row := gtk.NewListBoxRow()
	row.SetChild(box)
	row.SetSelectable(false)
	row.SetActivatable(false)
	row.AddCSSClass("dockheadrow")
	return row
}

// emptyGroupNote is what an empty group says about itself, so a zero reads as
// a state rather than as a strip that failed to draw.
func emptyGroupNote(header string) string {
	switch header {
	case "Running":
		return "— nothing is running"
	case "Queued":
		return "— nothing is waiting"
	default:
		return "— nothing has finished yet"
	}
}

func (d *dockPane) jobRow(e dockEntry) *gtk.ListBoxRow {
	s := e.snap
	box := gtk.NewBox(gtk.OrientationHorizontal, 8)
	box.SetMarginStart(8)
	box.SetMarginEnd(8)
	box.SetMarginTop(3)
	box.SetMarginBottom(3)

	text, class := dockStatus(e)

	// A running job gets a spinner, because motion is the one mark that cannot
	// be confused with a state that has stopped moving; everything else gets
	// the board's own shape, which is readable without colour vision.
	if s.Status == jobs.Running && !s.Paused {
		spin := gtk.NewSpinner()
		spin.SetSpinning(true)
		spin.SetSizeRequest(14, 14)
		box.Append(spin)
	} else if s.Paused {
		// A spinner that keeps spinning over a stopped process group would be
		// the one mark on this row that is not true.
		dot := gtk.NewLabel("‖")
		dot.AddCSSClass("st-none")
		box.Append(dot)
	} else {
		dot := gtk.NewLabel(stateGlyphForJob(s.Status))
		if class != "" {
			dot.AddCSSClass(class)
		}
		box.Append(dot)
	}

	l := label(s.Label)
	l.SetHExpand(true)
	box.Append(l)

	if s.Cost == gfy.Metered {
		m := gtk.NewLabel("$")
		m.AddCSSClass("metered")
		m.SetTooltipText("This job dispatches LLM requests against your API key.")
		box.Append(m)
	}

	prog := newJobProgressBar()
	box.Append(progressSlot(prog))
	setJobProgress(prog, &s)

	status := gtk.NewLabel(text)
	if class != "" {
		status.AddCSSClass(class)
	}
	box.Append(status)

	if !s.Status.Done() {
		id := s.ID
		if s.Status == jobs.Running {
			box.Append(d.a.pauseIconButton(s))
		} else {
			box.Append(buttonSpacer(1))
		}
		x := gtk.NewButtonFromIconName("window-close-symbolic")
		x.AddCSSClass("flat")
		x.SetTooltipText("Cancel this job")
		x.ConnectClicked(func() { d.a.runner.Cancel(id) })
		box.Append(x)
	} else {
		// A fixed-width placeholder so the finished rows' text does not
		// shift sideways relative to the running ones above them.
		box.Append(buttonSpacer(2))
	}

	row := gtk.NewListBoxRow()
	row.SetChild(box)
	row.SetTooltipText(dockRowTooltip(e))
	row.SetActivatable(s.Repo != "")
	if s.Repo != "" {
		repo := s.Repo
		row.ConnectActivate(func() { d.a.selectRepo(repo) })
	}

	d.rows = append(d.rows, &dockJobRow{id: s.ID, class: class, status: status, row: row, prog: prog})
	return row
}

// dockStatus is the strip's status text. It is wordier than the board column's
// jobCell on purpose: a column is scanned beside a repository that supplies the
// context, while the strip has to answer "is this running, is it waiting and on
// what, or is it over and how did it end" entirely on its own.
func dockStatus(e dockEntry) (text, class string) {
	s := e.snap
	switch s.Status {
	case jobs.Queued:
		t := "queued"
		if e.pos > 0 {
			t += " #" + gfy.Itoa(e.pos)
		}
		if e.wait != "" {
			t += " · " + e.wait
		}
		return t, "st-none"
	case jobs.Running:
		if s.Paused {
			// Still "st-none", not "st-running": a paused job is not making
			// progress, and colouring it as if it were is the whole reason
			// somebody would misread this strip.
			return joinDot("paused", progressNote(&s), "ran "+shortDur(s.Elapsed())), "st-none"
		}
		return joinDot("running", progressNote(&s), shortDur(s.Elapsed())), "st-running"
	case jobs.Succeeded:
		return joinDot("finished ok", "took "+shortDur(s.Elapsed()), endedAgo(s)), "st-fresh"
	case jobs.Canceled:
		return joinDot("cancelled", endedAgo(s)), "st-none"
	default:
		return joinDot(fmt.Sprintf("FAILED · exit %d", s.Exit), endedAgo(s)), "st-broken"
	}
}

// endedAgo is how long ago a finished job ended, or "" if it has no end time.
func endedAgo(s jobs.Snapshot) string {
	if s.Ended.IsZero() {
		return ""
	}
	age := board.Age(s.Ended)
	if age == "" || age == "now" {
		return "just now"
	}
	return age + " ago"
}

func joinDot(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}

// dockRowTooltip is the long form: everything the strip had to leave out, so a
// row answers the follow-up question on hover rather than sending you to J.
func dockRowTooltip(e dockEntry) string {
	s := e.snap
	text, _ := dockStatus(e)
	var b strings.Builder
	b.WriteString(s.Label + "\n")
	b.WriteString("job " + gfy.Itoa(int(s.ID)) + " · " + s.Kind + " · " + laneName(s.Cost) + " lane\n")
	if s.Repo != "" {
		b.WriteString(s.Repo + "\n")
	}
	b.WriteString(text + "\n")
	if !s.Queued.IsZero() {
		b.WriteString("queued " + s.Queued.Format("15:04:05"))
	}
	if !s.Started.IsZero() {
		b.WriteString(" · started " + s.Started.Format("15:04:05"))
	}
	if !s.Ended.IsZero() {
		b.WriteString(" · ended " + s.Ended.Format("15:04:05"))
	}
	b.WriteString("\n")
	if s.Err != "" {
		b.WriteString(s.Err + "\n")
	}
	b.WriteString("\n" + s.Command())
	return b.String()
}

// tickJobs repaints only what changes second to second: a running job's
// elapsed time, a queued job's reason for waiting, and the summary line.
func (d *dockPane) tickJobs() {
	if len(d.rows) == 0 {
		return
	}
	entries := d.dockEntries()
	byID := map[uint64]dockEntry{}
	for _, e := range entries {
		if !e.isHeader() {
			byID[e.snap.ID] = e
		}
	}
	for _, r := range d.rows {
		e, ok := byID[r.id]
		if !ok {
			continue
		}
		// Only the text moves here. A status change moves dockKey and so
		// rebuilds the row outright, which is what keeps the colour classes
		// and the spinner correct without this path having to track them.
		text, class := dockStatus(e)
		r.status.SetText(text)
		setClass(r.status, &r.class, class)
		setJobProgress(r.prog, &e.snap)
		if r.row != nil {
			r.row.SetTooltipText(dockRowTooltip(e))
		}
	}
}

// dockKey identifies the listed set, so the strip is rebuilt when a job
// arrives, ends or is cancelled — or when a group's heading changes — and left
// alone on every other tick.
func dockKey(entries []dockEntry) string {
	var b strings.Builder
	for _, e := range entries {
		if e.isHeader() {
			b.WriteString(e.header)
			b.WriteByte('=')
			b.WriteString(gfy.Itoa(e.count))
			b.WriteByte('/')
			b.WriteString(e.note)
			b.WriteByte('|')
			continue
		}
		b.WriteString(gfy.Itoa(int(e.snap.ID)))
		b.WriteByte(':')
		b.WriteString(e.snap.Status.String())
		// Pausing does not change the status, but it changes the row — the
		// spinner becomes a mark and the button flips — so it has to move the
		// key, or the rebuild would be skipped as a no-op.
		if e.snap.Paused {
			b.WriteString(":paused")
		}
		b.WriteByte('|')
	}
	return b.String()
}

// stateGlyphForJob is the strip's leading mark. It reuses the board's shapes
// so a running job looks the same here as it does in the row it belongs to.
func stateGlyphForJob(st jobs.Status) string {
	switch st {
	case jobs.Queued:
		return "○"
	case jobs.Running:
		return "◌"
	case jobs.Succeeded:
		return "●"
	case jobs.Canceled:
		return "·"
	default:
		return "△"
	}
}

// --- the application log -----------------------------------------------------

func (d *dockPane) buildLogPage() gtk.Widgetter {
	d.logView = gtk.NewTextView()
	d.logView.SetEditable(false)
	d.logView.SetMonospace(true)
	d.logView.SetWrapMode(gtk.WrapWordChar)
	d.logView.SetLeftMargin(10)
	d.logView.SetRightMargin(10)
	d.logView.AddCSSClass("joblog")

	d.logSW = gtk.NewScrolledWindow()
	d.logSW.SetChild(d.logView)
	d.logSW.SetVExpand(true)
	return d.logSW
}

func (d *dockPane) logActions() gtk.Widgetter {
	follow := gtk.NewToggleButton()
	follow.SetLabel("Follow")
	follow.SetActive(d.follow)
	follow.AddCSSClass("flat")
	follow.SetTooltipText("Keep the view pinned to the newest line.")
	follow.ConnectToggled(func() {
		d.follow = follow.Active()
		if d.follow {
			d.scrollLogToEnd()
		}
	})

	// The reason this pane exists. What lands on the clipboard is not just
	// the log: it is the log under a description of this machine, this
	// graphify, this configuration — which is what makes a paste into an
	// issue, or into an agent, answerable rather than a guessing game.
	copyAll := gtk.NewButtonWithLabel("Copy all")
	copyAll.AddCSSClass("suggested-action")
	copyAll.SetTooltipText("Copy the whole log, with a description of this machine's " +
		"graphify setup above it — ready to paste into an issue or hand to an AI agent.")
	copyAll.ConnectClicked(func() { d.a.copyDiagnostics() })

	verbose := gtk.NewToggleButton()
	verbose.SetLabel("Verbose")
	verbose.SetActive(d.verbose)
	verbose.AddCSSClass("flat")
	verbose.SetTooltipText("Also show debug entries — mostly GTK's own narration. " +
		"They are kept either way, and Copy all always includes them.")
	verbose.ConnectToggled(func() {
		d.verbose = verbose.Active()
		d.renderLog(true)
	})

	clear := gtk.NewButtonWithLabel("Clear")
	clear.AddCSSClass("flat")
	clear.ConnectClicked(func() {
		applog.Default.Clear()
		applog.Infof("log cleared")
		d.renderLog(true)
	})

	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.Append(verbose)
	box.Append(follow)
	box.Append(copyAll)
	box.Append(clear)
	return box
}

// renderLog repaints the log pane when it has moved. force re-renders even
// when the generation is unchanged, for the moment the page becomes visible.
func (d *dockPane) renderLog(force bool) {
	if d.logView == nil {
		return
	}
	gen := applog.Default.Gen()
	if gen == d.logGen && !force {
		return
	}
	d.logGen = gen
	d.logView.Buffer().SetText(applog.Default.TextFrom(d.logLevel()))
	if d.follow {
		d.scrollLogToEnd()
	}
}

// logLevel is the lowest level the pane renders.
func (d *dockPane) logLevel() applog.Level {
	if d.verbose {
		return applog.Debug
	}
	return applog.Info
}

// scrollLogToEnd pins the view to the newest line. The scroll is deferred to
// an idle callback because the adjustment does not know the new extent until
// the buffer change has been laid out.
func (d *dockPane) scrollLogToEnd() {
	idle(func() {
		if d.logSW == nil {
			return
		}
		adj := d.logSW.VAdjustment()
		if adj == nil {
			return
		}
		adj.SetValue(adj.Upper() - adj.PageSize())
	})
}

// --- the panel as a whole ----------------------------------------------------

// tick is the once-a-second repaint, called from App.tick.
func (d *dockPane) tick() {
	if d.widget == nil || !d.widget.Visible() {
		return
	}
	switch d.stack.VisibleChildName() {
	case dockPageJobs:
		d.reloadJobs()
		d.tickJobs()
	case dockPageLog:
		d.renderLog(false)
	}
}

// notifyLog is called when something is logged while the log page is not the
// one on screen: the tab asks for attention rather than the board interrupting
// with a toast that was never asked for.
func (d *dockPane) notifyLog() {
	if d.pageLog == nil {
		return
	}
	if !d.widget.Visible() || d.stack.VisibleChildName() != dockPageLog {
		d.pageLog.SetNeedsAttention(true)
	}
}
