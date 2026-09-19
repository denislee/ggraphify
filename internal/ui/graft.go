package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graftstate"
)

// The graft sweep: bring every repository in the folder currently on the board
// up to date in graft's own index, with the one free command that does it.
//
// It is folder-scoped rather than board-wide because that is the unit people
// actually mean — "the checkouts under ~/git" — and because the folder filter
// is already on screen saying which one is in force. With no folder filter the
// target is the whole board, and the dialog says so in those words.
//
// Everything else here is the same machinery the graphify actions use: one job
// per repository through the runner's free lane, the exact argv on screen
// before anything runs, and the typed-count gate on a large batch.

// graftTargets is what the sync button acts on: every repository in the folder
// in force that batch actions may touch.
//
// Deliberately NOT the visible rows: the search box and the state chips narrow
// what is on screen, and a sweep that silently skipped whatever a half-typed
// filter happened to hide would be a trap. The folder is different — it is a
// statement about which checkouts are in scope, not about which are
// interesting right now.
func (a *App) graftTargets() []board.Row {
	var out []board.Row
	for _, r := range a.allRows() {
		if r.Excluded || !board.InGroup(r, a.groupID) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// graftTargetName is what the dialog calls the target directory.
func (a *App) graftTargetName() string {
	if a.groupID == "" {
		return "every folder on the board"
	}
	return board.Tilde(a.groupID)
}

// actSyncGraft is the button. It splits the target into what needs a build and
// what is already in step, then asks.
func (a *App) actSyncGraft() {
	rows := a.graftTargets()
	if len(rows) == 0 {
		a.toast("no repositories in this folder")
		return
	}

	var todo []board.Row
	inStep := 0
	for _, r := range rows {
		if r.Graft.NeedsBuild() {
			todo = append(todo, r)
			continue
		}
		inStep++
	}
	if len(todo) == 0 {
		a.toastf("graft is already in step with all %s in %s",
			plural(inStep, "repo", "repos"), a.graftTargetName())
		return
	}
	a.confirmSyncGraft(todo, inStep)
}

// confirmSyncGraft is the gate. graft build writes a directory *inside* every
// one of these checkouts and appends to their .gitignore, which is the one
// thing about this sweep that is not obvious from its name — so the dialog
// says it before the count, not after.
func (a *App) confirmSyncGraft(rows []board.Row, inStep int) {
	set := a.opts.Store.Settings()

	var body strings.Builder
	body.WriteString("Target: " + a.graftTargetName() + "\n")
	body.WriteString(plural(len(rows), "repository", "repositories") + " to sync")
	if inStep > 0 {
		body.WriteString(" (" + gfy.Itoa(inStep) + " already in step, skipped)")
	}
	body.WriteString("\n\n")
	body.WriteString(graftPlanSummary(rows))
	body.WriteString("\n\nThis is FREE: tree-sitter only, no API key and no LLM request. " +
		"graft's --deep concept pass is the separate deep sweep, which runs on this " +
		"machine's local model.\n\n" +
		"It writes graft/ inside each checkout and adds that directory to the " +
		"repository's .gitignore — graft's own write, the same one `graft build` " +
		"makes from a terminal.")

	if ok, why := gfy.GraftReady(); !ok {
		body.WriteString("\n\n⚠ " + why + "\nEvery one of these runs is likely to fail.")
	}

	dlg := adw.NewAlertDialog("Sync graft", body.String())
	dlg.SetPreferWideLayout(true)

	extra := gtk.NewBox(gtk.OrientationVertical, 8)
	argv := gfy.Argv("graft-build", a.params(rows[0]))
	cmd := "$ " + gfy.Quote(argv)
	if len(rows) > 1 {
		cmd += "\n  …and " + plural(len(rows)-1, "more like it", "more like it")
	}
	frame := gtk.NewFrame("")
	lbl := monoLabel(cmd)
	lbl.SetMarginTop(8)
	lbl.SetMarginBottom(8)
	lbl.SetMarginStart(8)
	lbl.SetMarginEnd(8)
	frame.SetChild(lbl)
	extra.Append(frame)

	// The same typed-count gate every other batch has. Forty subprocesses
	// walking forty trees is not a bill, but it is not a click to make by
	// accident either.
	var entry *gtk.Entry
	typed := len(rows) > 1 && len(rows) >= set.ConfirmBatchAt
	if typed {
		entry = gtk.NewEntry()
		entry.SetPlaceholderText("type " + gfy.Itoa(len(rows)) + " to confirm")
		extra.Append(entry)
	}
	dlg.SetExtraChild(extra)

	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("sync", "Sync "+plural(len(rows), "repo", "repos"))
	dlg.SetResponseAppearance("sync", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")

	if typed {
		dlg.SetResponseEnabled("sync", false)
		want := gfy.Itoa(len(rows))
		entry.ConnectChanged(func() {
			dlg.SetResponseEnabled("sync", strings.TrimSpace(entry.Text()) == want)
		})
	}

	queue := append([]board.Row(nil), rows...)
	dlg.ConnectResponse(func(resp string) {
		if resp == "sync" {
			a.submit("graft-build", queue, nil)
		}
	})
	dlg.Present(a.win)
}

// actSyncGraftDeep is the deep sweep: graft's LLM tier over every repository in
// the folder in force, on the model already running on this machine.
//
// Its target is wider than the free sweep's by exactly one state. NeedsBuild
// skips StateRaw — a wiring graph in step with the tree — because `graft
// build` has genuinely nothing to do there. That is precisely the repository
// this action exists for: current wiring, no prose tier at all.
func (a *App) actSyncGraftDeep() {
	rows := a.graftTargets()
	if len(rows) == 0 {
		a.toast("no repositories in this folder")
		return
	}

	set := a.opts.Store.Settings()
	backend, model, ok, why := gfy.GraftDeepReady(set.Backend, set.Model)
	if !ok {
		// Not a dialog. Nothing about this one is a choice the person still
		// has: there is no model to run it on, and the sentence says which
		// step is missing.
		a.toast(why)
		return
	}

	var todo []board.Row
	inStep := 0
	for _, r := range rows {
		if r.Graft.NeedsDeep() {
			todo = append(todo, r)
			continue
		}
		inStep++
	}
	if len(todo) == 0 {
		a.toastf("graft's deep tier is already current on all %s in %s",
			plural(inStep, "repo", "repos"), a.graftTargetName())
		return
	}
	a.confirmSyncGraftDeep(todo, inStep, backend, model)
}

// confirmSyncGraftDeep is the gate. It differs from the free sweep's in the
// two things that are actually different: this one dispatches LLM requests,
// and it says where they go — because "free" here is a claim about an endpoint
// on this machine, and a reader is entitled to check it.
func (a *App) confirmSyncGraftDeep(rows []board.Row, inStep int, backend, model string) {
	set := a.opts.Store.Settings()

	var body strings.Builder
	body.WriteString("Target: " + a.graftTargetName() + "\n")
	body.WriteString(plural(len(rows), "repository", "repositories") + " to index deeply")
	if inStep > 0 {
		body.WriteString(" (" + gfy.Itoa(inStep) + " already current, skipped)")
	}
	body.WriteString("\n\n")
	body.WriteString(graftDeepPlanSummary(rows))
	body.WriteString("\n\nThis runs graft's LLM pass — a concept map and a per-symbol " +
		"summary and crux — on " + model + " at " + gfy.LocalBaseURL(backend) + ".\n" +
		"No key and no bill: the corpus and every request stay on this machine. " +
		"It is far slower than the free wiring build, and it writes graft/ inside " +
		"each checkout the same way that one does.\n\n" +
		"A partial result is kept rather than discarded (--allow-partial): the " +
		"per-symbol crux needs a model that honors tool_choice, which ollama's " +
		"endpoint does not always pass through, and the concept map is worth " +
		"having on its own. A later run resumes from what is cached.")

	dlg := adw.NewAlertDialog("Deep graft sweep", body.String())
	dlg.SetPreferWideLayout(true)

	extra := gtk.NewBox(gtk.OrientationVertical, 8)
	p := a.params(rows[0])
	p.Backend, p.Model = backend, model
	gfy.GraftDeepParams(&p)
	cmd := "$ " + gfy.Quote(gfy.Argv(gfy.GraftDeepKind, p))
	if len(rows) > 1 {
		cmd += "\n  …and " + plural(len(rows)-1, "more like it", "more like it")
	}
	frame := gtk.NewFrame("")
	lbl := monoLabel(cmd)
	lbl.SetMarginTop(8)
	lbl.SetMarginBottom(8)
	lbl.SetMarginStart(8)
	lbl.SetMarginEnd(8)
	frame.SetChild(lbl)
	extra.Append(frame)

	var entry *gtk.Entry
	typed := len(rows) > 1 && len(rows) >= set.ConfirmBatchAt
	if typed {
		entry = gtk.NewEntry()
		entry.SetPlaceholderText("type " + gfy.Itoa(len(rows)) + " to confirm")
		extra.Append(entry)
	}
	dlg.SetExtraChild(extra)

	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("sync", "Index "+plural(len(rows), "repo", "repos"))
	dlg.SetResponseAppearance("sync", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")

	if typed {
		dlg.SetResponseEnabled("sync", false)
		want := gfy.Itoa(len(rows))
		entry.ConnectChanged(func() {
			dlg.SetResponseEnabled("sync", strings.TrimSpace(entry.Text()) == want)
		})
	}

	queue := append([]board.Row(nil), rows...)
	mutate := a.graftDeepParams()
	dlg.ConnectResponse(func(resp string) {
		if resp == "sync" {
			a.submit(gfy.GraftDeepKind, queue, mutate)
		}
	})
	dlg.Present(a.win)
}

// graftDeepPlanSummary is graftPlanSummary plus the state the free sweep has
// no name for: a current wiring graph with no concept layer over it.
func graftDeepPlanSummary(rows []board.Row) string {
	var raw int
	for _, r := range rows {
		if r.Graft.State == graftstate.StateRaw {
			raw++
		}
	}
	parts := graftPlanSummary(rows)
	if raw == 0 {
		return parts
	}
	rawPart := plural(raw, "has wiring but no concept layer", "have wiring but no concept layer")
	if parts == "" {
		return rawPart + "."
	}
	return strings.TrimSuffix(parts, ".") + ", " + rawPart + "."
}

// graftPlanSummary says what the sweep is actually about to do, in the terms
// the Graft column already showed: how many have no index at all, how many
// have one the tree has moved under, how many are unreadable.
func graftPlanSummary(rows []board.Row) string {
	var none, stale, broken int
	for _, r := range rows {
		switch r.Graft.State {
		case graftstate.StateNone:
			none++
		case graftstate.StateStale:
			stale++
		case graftstate.StateBroken:
			broken++
		}
	}
	var parts []string
	if none > 0 {
		parts = append(parts, plural(none, "has no index yet", "have no index yet"))
	}
	if stale > 0 {
		parts = append(parts, plural(stale, "has drifted", "have drifted"))
	}
	if broken > 0 {
		parts = append(parts, plural(broken, "is unreadable", "are unreadable"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}
