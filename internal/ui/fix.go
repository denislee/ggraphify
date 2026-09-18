package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/heal"
	"github.com/dns/ggraphify/internal/jobs"
)

// The Fix action: take the selected repositories from whatever state they are
// in to a healthy graph — one that exists, matches the tree, has named
// communities and a GRAPH_REPORT.md — by running the ordered sequence of
// graphify commands that gets there, and nothing else.
//
// It is deliberately not a macro that runs everything. The plan is computed
// from each row's actual state (internal/heal), it is shown in full before it
// starts, and every step is one of the commands that already has a button —
// so "Fix" can never do something the board could not already be asked to do
// one click at a time.

// rowPlan pairs a row with the plan for it, because a batch fix is N different
// sequences, not one sequence run N times.
type rowPlan struct {
	row  board.Row
	plan heal.Plan
}

// fixRequest is one press of one of the fix buttons: which plans, how many
// rows were skipped for having nothing to do, whether the LLM was allowed, and
// whether this came from the button that takes the whole board — which is the
// one press that can queue a chain per repository.
type fixRequest struct {
	plans        []rowPlan
	healthy      int
	allowMetered bool
	everyRepo    bool
}

// actFix is the button. It plans first, then asks.
func (a *App) actFix() { a.fixWith(true) }

// actFixFree is the same button with the LLM taken away: it plans, asks and
// runs only the steps that cost nothing. It is a button of its own rather than
// a second click inside the Fix dialog because "take these repositories as far
// as free work goes" is a thing people want on a whole batch without reading a
// metered plan first — and on a healthy-but-for-money row, the Fix dialog it
// would have to go through is the one that spends.
func (a *App) actFixFree() { a.fixWith(false) }

// actFixFreeAll is the free fix over every repository on the board, rather
// than over the selection: the sweep you run after a day of committing, which
// costs nothing and so needs no per-repository decision. Rows kept out of
// batch actions (X) are left alone, because that flag means exactly this.
//
// Unlike the other two it plans off the main thread. Planning one repository
// reads graph.json, sizes the output directory and walks the tree for drift;
// doing that for a hundred checkouts inside the click handler would freeze the
// window for as long as it took.
func (a *App) actFixFreeAll() {
	rows := a.fixableRows()
	if len(rows) == 0 {
		a.toast("no repositories to fix")
		return
	}

	// Everything the worker needs is resolved here, on the main thread, so the
	// goroutine touches nothing but the filesystem.
	type plannable struct {
		row  board.Row
		opts graphstate.Options
	}
	work := make([]plannable, 0, len(rows))
	for _, r := range rows {
		work = append(work, plannable{row: r, opts: graphstate.Options{
			Repo:     r.Path,
			Out:      a.params(r).Out,
			Head:     r.HeadSHA,
			Baseline: a.opts.Store.DriftBaseline(r.Path),
		}})
	}
	a.toastf("planning free steps for %s…", plural(len(rows), "repo", "repos"))

	go func() {
		var plans []rowPlan
		healthy := 0
		for _, w := range work {
			g, err := graphstate.Read(w.opts)
			if err != nil {
				g = w.row.Graph
			}
			p := heal.For(g, false)
			if p.Empty() {
				healthy++
				continue
			}
			plans = append(plans, rowPlan{row: w.row, plan: p})
		}
		idle(func() {
			if len(plans) == 0 {
				a.toastf("no free step left on any of %s",
					plural(healthy, "repo", "repos"))
				return
			}
			a.confirmFix(fixRequest{plans: plans, healthy: healthy, everyRepo: true})
		})
	}()
}

// fixableRows is every repository on the board that batch actions may touch —
// the whole model, not the filtered view: a board-wide sweep that quietly
// skipped whatever the search box happened to hide would be a trap.
func (a *App) fixableRows() []board.Row {
	all := a.allRows()
	out := make([]board.Row, 0, len(all))
	for _, r := range all {
		if !r.Excluded {
			out = append(out, r)
		}
	}
	return out
}

// fixWith plans the selected rows and opens the confirmation. allowMetered
// chooses which plan is computed: the free plan is not the metered one with
// its paid steps removed, it is a different sequence (see the "free" response
// in confirmFix).
func (a *App) fixWith(allowMetered bool) {
	rows := a.batch()
	if len(rows) == 0 {
		a.toast("nothing selected")
		return
	}

	// The plan is computed from the graph state as it is on disk right now,
	// not from the last scan: a build that finished during the tick would
	// otherwise be planned for all over again.
	var plans []rowPlan
	var healthy []string
	for _, r := range rows {
		g := a.freshGraph(r)
		p := heal.For(g, allowMetered)
		if p.Empty() {
			healthy = append(healthy, r.Name)
			continue
		}
		plans = append(plans, rowPlan{row: r, plan: p})
	}

	if len(plans) == 0 {
		// In free mode "nothing to do" does not mean healthy: the repository
		// may still need an extraction or a labelling run, which is precisely
		// what this button declined to do.
		switch {
		case !allowMetered && len(healthy) == 1:
			a.toastf("%s: no free step left — what remains needs the LLM", healthy[0])
		case !allowMetered:
			a.toastf("no free step left on %s — what remains needs the LLM",
				plural(len(healthy), "repo", "repos"))
		case len(healthy) == 1:
			a.toastf("%s is already healthy — nothing to fix", healthy[0])
		default:
			a.toastf("all %s are already healthy", plural(len(healthy), "repo", "repos"))
		}
		return
	}
	a.confirmFix(fixRequest{plans: plans, healthy: len(healthy), allowMetered: allowMetered})
}

// freshGraph re-reads one row's graph state from disk. The board's own copy is
// up to 30 seconds old, and the whole point of this button is to act on what
// is true now.
func (a *App) freshGraph(r board.Row) graphstate.Graph {
	// The baseline matters here more than anywhere: without it the plan is
	// computed against drift a previous update already settled, and Fix
	// prescribes an update that cannot change the number it is reacting to.
	g, err := graphstate.Read(graphstate.Options{
		Repo:     r.Path,
		Out:      a.params(r).Out,
		Head:     r.HeadSHA,
		Baseline: a.opts.Store.DriftBaseline(r.Path),
	})
	if err != nil {
		return r.Graph
	}
	return g
}

// confirmFix is the gate. It shows every step of every plan, marks the metered
// ones, and offers the free-only plan as a first-class alternative rather than
// as a thing to go and configure.
//
// req.allowMetered is false when the caller is one of the free-steps buttons:
// the plans it was handed already contain nothing metered, and the dialog says
// so rather than offering a downgrade that would change nothing.
func (a *App) confirmFix(req fixRequest) {
	plans, healthy, allowMetered := req.plans, req.healthy, req.allowMetered
	set := a.opts.Store.Settings()

	var body strings.Builder
	if len(plans) == 1 {
		body.WriteString(plans[0].row.Name + "\n" + plans[0].row.Path + "\n")
	} else {
		body.WriteString(plural(len(plans), "repository", "repositories") + " to fix")
		if healthy > 0 {
			body.WriteString(" (" + gfy.Itoa(healthy) + " already healthy, skipped)")
		}
		body.WriteString("\n")
	}

	metered := false
	steps := 0
	for _, rp := range plans {
		if rp.plan.Metered() {
			metered = true
		}
		steps += len(rp.plan.Steps)
	}

	if metered {
		body.WriteString("\n" + a.meteredNotice(a.params(plans[0].row)))
	}
	if !allowMetered {
		body.WriteString("\nFree steps only — no API key is used and no LLM request is sent. " +
			"Anything that needs one is left undone.")
	}
	body.WriteString("\n" + plural(steps, "command", "commands") + " in total. " +
		"Each waits for the one before it, and the first failure stops the rest.")

	title := "Fix"
	confirm := "Fix"
	if !allowMetered {
		title = "Free steps"
		confirm = "Run free steps"
	}
	if req.everyRepo {
		title = "Free steps — every repository"
	}
	dlg := adw.NewAlertDialog(title, body.String())
	dlg.SetPreferWideLayout(true)
	dlg.SetExtraChild(a.fixPlanWidget(plans))

	dlg.AddResponse("cancel", "Cancel")
	// The free-only plan is offered whenever the full one spends money, and it
	// is the default focus: "get this repository as far as free work can" is
	// the right answer often enough that it should not be the harder click.
	if metered {
		dlg.AddResponse("free", "Free steps only")
	}
	dlg.AddResponse("fix", confirm)
	dlg.SetResponseAppearance("fix", adw.ResponseSuggested)
	if metered {
		dlg.SetResponseAppearance("fix", adw.ResponseDestructive)
	}
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")

	// Same typed-count gate as every other batch: a fix over 40 checkouts is
	// the one shape of this button that can cost real money — and the
	// board-wide sweep is gated on the same count even though it cannot,
	// because "every repository" is the one press nobody sized by hand.
	var entry *gtk.Entry
	typed := (metered || req.everyRepo) && len(plans) > 1 && len(plans) >= set.ConfirmBatchAt
	if typed {
		entry = gtk.NewEntry()
		entry.SetPlaceholderText("type " + gfy.Itoa(len(plans)) + " to confirm")
		box := gtk.NewBox(gtk.OrientationVertical, 8)
		box.Append(a.fixPlanWidget(plans))
		box.Append(entry)
		dlg.SetExtraChild(box)
		dlg.SetResponseEnabled("fix", false)
		want := gfy.Itoa(len(plans))
		entry.ConnectChanged(func() {
			dlg.SetResponseEnabled("fix", strings.TrimSpace(entry.Text()) == want)
		})
	}

	dlg.ConnectResponse(func(resp string) {
		switch resp {
		case "fix":
			a.runFix(plans, allowMetered)
		case "free":
			// Re-planned rather than filtered: without the LLM the right first
			// step for an unextracted repository is `update`, which is not in
			// the metered plan at all.
			var free []rowPlan
			for _, rp := range plans {
				p := heal.For(a.freshGraph(rp.row), false)
				if !p.Empty() {
					free = append(free, rowPlan{row: rp.row, plan: p})
				}
			}
			a.runFix(free, false)
		}
	})
	dlg.Present(a.win)
}

// fixPlanWidget renders the plans as what they are: a numbered list of exact
// command lines, per repository, with the metered ones called out.
func (a *App) fixPlanWidget(plans []rowPlan) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	shown := 0
	for _, rp := range plans {
		if shown == 4 {
			box.Append(gtk.NewLabel("…and " +
				plural(len(plans)-shown, "more repository", "more repositories")))
			break
		}
		shown++

		if len(plans) > 1 {
			name := gtk.NewLabel(rp.row.Name)
			name.SetXAlign(0)
			name.AddCSSClass("heading")
			box.Append(name)
		}

		var text strings.Builder
		for _, i := range rp.plan.Issues {
			text.WriteString("✗ " + i.What + "\n")
		}
		for n, s := range rp.plan.Steps {
			p := a.params(rp.row)
			s.Apply(&p)
			tag := ""
			if s.Cost() == gfy.Metered {
				tag = "  ⚠ METERED"
			}
			text.WriteString("\n" + gfy.Itoa(n+1) + ". " + gfy.Title(s.Kind) + tag + "\n" +
				"   " + s.Why + "\n   $ " + gfy.Quote(gfy.Argv(s.Kind, p)) + "\n")
		}
		for _, i := range rp.plan.Unreachable {
			text.WriteString("\nAfterwards, still not fixed: " + i.What + "\n")
		}

		frame := gtk.NewFrame("")
		lbl := monoLabel(strings.TrimRight(text.String(), "\n"))
		lbl.SetMarginTop(8)
		lbl.SetMarginBottom(8)
		lbl.SetMarginStart(8)
		lbl.SetMarginEnd(8)
		frame.SetChild(lbl)
		box.Append(frame)
	}
	return box
}

// runFix queues one chain per repository.
//
// Per repository, not one chain across all of them: two checkouts have nothing
// to do with each other, and a failure on one must not stop the other nine.
// The runner's own per-repo serialization and metered lane still apply, so a
// batch fix does not become a way to run twelve extractions at once.
func (a *App) runFix(plans []rowPlan, metered bool) {
	if len(plans) == 0 {
		a.toast("nothing to fix")
		return
	}
	for _, rp := range plans {
		rp := rp
		// A fix by hand is also an instruction to the unattended loop: forget
		// whatever you concluded about this repository. Otherwise a checkout
		// the loop had given up on stays given-up-on even after the user has
		// gone and cleared the thing it kept tripping over.
		if a.autofix != nil {
			a.autofix.Retry(rp.row.Path)
		}
		total := len(rp.plan.Steps)
		steps := make([]jobs.ChainStep, 0, total)
		for n, s := range rp.plan.Steps {
			p := a.params(rp.row)
			s.Apply(&p)
			steps = append(steps, jobs.ChainStep{
				Kind:   s.Kind,
				Repo:   rp.row.Path,
				Label:  "Fix " + gfy.Itoa(n+1) + "/" + gfy.Itoa(total) + " · " + gfy.Title(s.Kind) + " · " + rp.row.Name,
				Params: p,
				Env:    a.opts.Store.Overlay(rp.row.Path),
			})
		}
		name := rp.row.Name
		a.runner.SubmitChain(steps, func(res jobs.ChainResult) {
			// The callback lands on a worker goroutine; everything it touches
			// here is GTK.
			idle(func() {
				switch {
				case res.Err != nil:
					a.toastf("%s: fix stopped after %d/%d — %v", name, res.Ran, res.Total, res.Err)
				case metered:
					a.toastf("%s is healthy", name)
				default:
					a.toastf("%s: free steps done (%d/%d)", name, res.Ran, res.Total)
				}
				a.refresh(false)
			})
		})
	}
	if len(plans) == 1 {
		a.toastf("fixing %s — %s", plans[0].row.Name,
			plural(len(plans[0].plan.Steps), "step", "steps"))
	} else {
		a.toastf("fixing %s", plural(len(plans), "repo", "repos"))
	}
	a.view.QueueDraw()
	a.refreshStatus()
}

// fixSubtitle is what the Fix row says about the selected repository: the
// defects themselves, not a description of the button. A row that reads
// "drift, unnamed, no-report" has already told you why the button is there.
func fixSubtitle(r *board.Row) string {
	if r == nil {
		return "Run whatever this repository needs to become healthy."
	}
	issues := r.Graph.Issues()
	if len(issues) == 0 {
		return "Nothing to fix — this graph is healthy."
	}
	what := make([]string, 0, len(issues))
	for _, i := range issues {
		what = append(what, i.Code)
	}
	return plural(len(issues), "issue", "issues") + ": " + strings.Join(what, ", ") +
		". Runs the commands that clear them, in order."
}
