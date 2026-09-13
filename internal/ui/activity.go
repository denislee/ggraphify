package ui

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// The activity indicator is the answer to "is anything happening right now".
//
// The jobs dialog (J) already holds every job with its log, but it is a dialog
// over the board: it has to be opened, and while it is closed a long
// extraction is invisible apart from one clause in the status line. The
// indicator lives in the header bar instead, spins while work is in flight,
// and drops its whole popover — every running and queued job, what repository
// it belongs to, how long it has been going, and a cancel button each — one
// click away. It hides itself when the queue is empty, so an idle board does
// not carry a widget that says "0".
func (a *App) buildActivity() *gtk.MenuButton {
	spin := gtk.NewSpinner()
	spin.SetSizeRequest(16, 16)
	lbl := gtk.NewLabel("")

	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.Append(spin)
	box.Append(lbl)

	list := gtk.NewBox(gtk.OrientationVertical, 0)
	list.SetMarginTop(6)
	list.SetMarginBottom(6)
	list.SetMarginStart(6)
	list.SetMarginEnd(6)
	list.SetSizeRequest(420, -1)

	sw := gtk.NewScrolledWindow()
	sw.SetChild(list)
	sw.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sw.SetPropagateNaturalHeight(true)
	sw.SetMaxContentHeight(360)

	open := gtk.NewButtonWithLabel("Open the jobs view (J)")
	open.AddCSSClass("flat")
	open.ConnectClicked(func() {
		a.activity.Popdown()
		a.showJobs()
	})
	cancelAll := gtk.NewButtonWithLabel("Cancel all")
	cancelAll.AddCSSClass("flat")
	cancelAll.AddCSSClass("destructive-action")
	cancelAll.ConnectClicked(func() { a.runner.CancelAll() })

	foot := gtk.NewBox(gtk.OrientationHorizontal, 6)
	foot.SetMarginStart(6)
	foot.SetMarginEnd(6)
	foot.SetMarginBottom(6)
	open.SetHExpand(true)
	foot.Append(open)
	foot.Append(cancelAll)

	content := gtk.NewBox(gtk.OrientationVertical, 6)
	content.Append(sw)
	content.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	content.Append(foot)

	pop := gtk.NewPopover()
	pop.SetChild(content)

	btn := gtk.NewMenuButton()
	btn.SetChild(box)
	btn.SetPopover(pop)
	btn.AddCSSClass("flat")
	btn.SetVisible(false)

	a.activity = btn
	a.activitySpin = spin
	a.activityLbl = lbl
	a.activityList = list
	a.activityPop = pop
	return btn
}

// refreshActivity is called from refreshStatus, which is itself called on
// every job event and once a second. The button's own text is cheap and
// always updated; the popover's rows are rebuilt only while it is open, or
// when the set of live jobs changes underneath it.
func (a *App) refreshActivity() {
	if a.activity == nil {
		return
	}
	queued, running := a.runner.Active()
	if queued+running == 0 {
		a.activity.SetVisible(false)
		a.activitySpin.SetSpinning(false)
		a.activityPop.Popdown()
		a.activityKey = ""
		return
	}

	a.activity.SetVisible(true)
	a.activitySpin.SetSpinning(running > 0)
	a.activityLbl.SetText(activityLabel(queued, running))
	a.activity.SetTooltipText("What is running in the background — " +
		plural(running, "job running", "jobs running") + ", " +
		plural(queued, "queued", "queued"))

	live := a.liveJobs()
	key := activityKey(live)
	if !a.activityPop.Visible() && key == a.activityKey {
		return
	}
	a.activityKey = key
	a.fillActivity(live)
}

// liveJobs is the queue as the indicator shows it: running first, then
// queued, in the runner's own order. Finished jobs are the jobs dialog's
// business — this widget answers "what is happening", not "what happened".
func (a *App) liveJobs() []jobs.Snapshot {
	var running, queued []jobs.Snapshot
	for _, s := range a.runner.Snapshot() {
		switch s.Status {
		case jobs.Running:
			running = append(running, s)
		case jobs.Queued:
			queued = append(queued, s)
		}
	}
	return append(running, queued...)
}

func (a *App) fillActivity(live []jobs.Snapshot) {
	for {
		child := a.activityList.FirstChild()
		if child == nil {
			break
		}
		a.activityList.Remove(child)
	}
	for _, s := range live {
		a.activityList.Append(a.activityRow(s))
	}
}

func (a *App) activityRow(s jobs.Snapshot) *gtk.Box {
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.SetMarginTop(4)
	row.SetMarginBottom(4)

	text, class := jobCell(&s)
	dot := gtk.NewLabel("●")
	if class != "" {
		dot.AddCSSClass(class)
	}
	row.Append(dot)

	// The job's label already names the repository ("Update · cc-docsboard"),
	// which is the one thing a glance at this popover has to answer.
	l := label(s.Label)
	l.SetHExpand(true)
	l.SetTooltipText(s.Command())
	row.Append(l)

	if s.Cost == gfy.Metered {
		m := gtk.NewLabel("$")
		m.AddCSSClass("metered")
		m.SetTooltipText("This job dispatches LLM requests against your API key.")
		row.Append(m)
	}

	status := gtk.NewLabel(text)
	status.AddCSSClass("dim-label")
	row.Append(status)

	id := s.ID
	x := gtk.NewButtonFromIconName("window-close-symbolic")
	x.AddCSSClass("flat")
	x.SetTooltipText("Cancel this job")
	x.ConnectClicked(func() { a.runner.Cancel(id) })
	row.Append(x)
	return row
}

// activityLabel is what the header button says. It stays short — a header bar
// is not a status line — and names the queue only when there is one.
func activityLabel(queued, running int) string {
	var b strings.Builder
	b.WriteString(gfy.Itoa(running))
	b.WriteString(" running")
	if queued > 0 {
		b.WriteString(" · ")
		b.WriteString(gfy.Itoa(queued))
		b.WriteString(" queued")
	}
	return b.String()
}

// activityKey identifies the live set, so a closed popover is rebuilt when
// the queue moves and left alone when it has not.
func activityKey(live []jobs.Snapshot) string {
	var b strings.Builder
	for _, s := range live {
		b.WriteString(gfy.Itoa(int(s.ID)))
		b.WriteByte(':')
		b.WriteString(s.Status.String())
		b.WriteByte('|')
	}
	return b.String()
}
