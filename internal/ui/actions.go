package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

// params builds the gfy.Params for one row with no command in mind: the
// board's defaults, overlaid with whatever that repository overrides.
//
// Callers that know which command they are about to run want paramsFor
// instead — the backend a job runs against depends on how much of the model
// that command eats, and this one cannot know.
func (a *App) params(r board.Row) gfy.Params {
	return a.paramsFor("", r)
}

// paramsFor is params for a known kind: the same parameters, with the backend
// and model chosen for that command's weight class.
//
// The split is store.Settings.HeavyBackend's: an extraction that sends a
// request per chunk of the tree and a labelling pass that sends a few dozen
// short prompts do not have to share a backend. A kind of "" — or any command
// that talks to no model — resolves to the general pin, which is what every
// caller that only wants p.Out gets.
//
// BackendForKind and not BackendFor: a command may also carry a pin of its
// own, one tier finer than its weight class, and the kind is in hand here.
func (a *App) paramsFor(kind string, r board.Row) gfy.Params {
	set := a.opts.Store.Settings()
	o := a.opts.Store.Override(r.Path)

	backend, model := set.BackendForKind(kind)
	p := gfy.Params{
		Repo:      r.Path,
		Out:       r.Graph.Out,
		Backend:   backend,
		Model:     model,
		ClaudeDir: set.ClaudeAccount,
		Extra:     append([]string(nil), o.Extra...),
	}
	if p.Out == "" {
		// The row always carries a resolved output directory; this is the
		// belt-and-braces path for a row built before a scan completed, and it
		// resolves the location the same way the scan does.
		p.Out = a.opts.Store.Out(r.Path)
	}
	if o.Backend != "" {
		p.Backend = o.Backend
	}
	if o.Model != "" {
		p.Model = o.Model
	}
	// Resolve "auto-detect" to something graphify can actually act on. Its own
	// detection is key-based, so on a machine whose only credential is a
	// logged-in Claude Code install a blank backend would find nothing and the
	// run would refuse; there this names claude-cli. The resolved name is in
	// the argv the confirm dialog prints, so the substitution is on screen.
	p.Backend = gfy.EffectiveBackend(p.Backend)
	// Each OpenCode plan takes its model from its own setting, for the reason
	// store.Settings.OpenCodeModel documents: the shared Model field holds
	// whatever the last backend needed, and sending an ollama tag — or the
	// other plan's id — to a gateway is a failure per repository rather than
	// a substitution.
	if gfy.IsOpenCodeBackend(p.Backend) && strings.TrimSpace(o.Model) == "" {
		p.Model = gfy.DefaultModelFor(p.Backend)
		if m := strings.TrimSpace(set.OpenCodeModelFor(p.Backend)); m != "" {
			p.Model = m
		}
	}
	// Size the chunk to the local server's context slot. graphify derives a
	// num_ctx per request and sends it, but ollama's OpenAI-compatible
	// endpoint drops it, so the slot stays at whatever the server was started
	// with and an oversized prompt is truncated from the front — taking the
	// system prompt that asks for JSON with it, which is how an extraction
	// spends hours and writes nothing. gfy.LocalTokenBudget returns 0 when the
	// slot could not be measured, and a zero leaves graphify's own default
	// alone rather than guessing at it.
	//
	// It is set before p.Extra is appended, so a repository that overrides
	// --token-budget by hand still wins: graphify's parser takes the last
	// occurrence.
	// It also gives the reply time to arrive: graphify allows a request 600
	// seconds, and a local model answering a chunk at a few tokens a second
	// needs longer than that. A request killed on the deadline costs the whole
	// file — it is dropped from the graph, and enough of them fail the run on
	// the shrink guard.
	//
	// The lane count goes in first because the request timeout is derived from
	// it: with more than one job sharing this machine's model server, a chunk
	// waits in the server's queue behind the other jobs' chunks, and the
	// timeout has to cover the wait as well as the answer.
	_, _, p.LocalLanes = a.runner.Lanes()
	gfy.ApplyLocalSizing(&p)
	return p
}

// run submits one job for one row, after the cost gate.
//
// Every action in the board funnels through here, which is what makes the two
// invariants checkable in one place: a metered command is never submitted
// without an explicit confirm that names the backend, the model and the repo
// count; and the exact argv is always shown before it runs.
func (a *App) run(kind string, rows []board.Row, mutate func(*gfy.Params)) {
	if len(rows) == 0 {
		a.toast("nothing selected")
		return
	}
	spec, ok := gfy.Known[kind]
	if !ok {
		a.toastf("ggraphify has no command builder for %q", kind)
		return
	}

	if spec.NeedsGraph {
		var ready, blocked []board.Row
		for _, r := range rows {
			if a.hasGraph(r) {
				ready = append(ready, r)
			} else {
				blocked = append(blocked, r)
			}
		}
		if len(blocked) > 0 {
			a.offerExtract(kind, blocked)
		}
		if len(ready) == 0 {
			return
		}
		rows = ready
	}

	set := a.opts.Store.Settings()
	needsConfirm := a.costOf(kind, rows) == gfy.Metered ||
		(len(rows) > 1 && len(rows) >= set.ConfirmBatchAt)
	if !needsConfirm {
		a.submit(kind, rows, mutate)
		return
	}
	a.confirm(kind, rows, mutate)
}

// costOf is what this action will actually cost, which is not always what its
// kind costs: graft's deep pass is free against a model on this machine and a
// real bill against anything else, so the answer depends on where the weight
// pin — and any per-repository override — points.
//
// It reads the pin rather than resolving params per row: paramsFor probes the
// local server, and forty probes on the main thread to decide whether to show
// one dialog is a visibly stalled board. The pin is what GraftDeepReady
// resolves from anyway, and it only ever rewrites one local backend into
// another, so the metered/free half of the answer is the same either way.
//
// The pin here is the kind's own when it has one, its weight class's when it
// does not — the same resolution paramsFor will apply to every row.
func (a *App) costOf(kind string, rows []board.Row) gfy.Cost {
	backend, _ := a.opts.Store.Settings().BackendForKind(kind)
	for _, r := range rows {
		b := backend
		if o := a.opts.Store.Override(r.Path).Backend; o != "" {
			b = o
		}
		if gfy.CostFor(kind, b) == gfy.Metered {
			return gfy.Metered
		}
	}
	return gfy.CostFor(kind, backend)
}

// submit queues the jobs. No gate here: run() and confirm() own that decision.
func (a *App) submit(kind string, rows []board.Row, mutate func(*gfy.Params)) {
	n := 0
	for _, r := range rows {
		p := a.paramsFor(kind, r)
		if mutate != nil {
			mutate(&p)
		}
		label := gfy.Title(kind) + " · " + r.Name
		if _, err := a.runner.SubmitCmd(kind, r.Path, label, p, a.opts.Store.Overlay(r.Path)); err != nil {
			// A refusal about the local model server is about the MACHINE,
			// not about this row: every remaining row would be refused for
			// the identical reason, so it is said once and the rest are not
			// attempted. Anything else is per-repository and the loop goes on.
			if a.submitRefused(r.Name, err) {
				return
			}
			continue
		}
		n++
	}
	if n == 0 {
		return
	}
	if n == 1 {
		a.toastf("queued %s on %s", gfy.Title(kind), rows[0].Name)
	} else {
		a.toastf("queued %s on %s", gfy.Title(kind), plural(n, "repo", "repos"))
	}
	a.view.QueueDraw()
	a.refreshStatus()
}

// submitRefused reports a precheck's refusal, and says whether it ends the
// whole submission rather than just this row.
//
// The needs-root case gets the command on the clipboard as well as in the
// toast, for the same reason the settings page's Start button does: a toast
// is gone in five seconds and `sudo systemctl start ollama` retyped from
// memory is `sudo systemctl start ollama.service` half the time.
func (a *App) submitRefused(name string, err error) (fatal bool) {
	if gfy.NeedsRoot(err) {
		applog.Errorf("submit refused: %v", err)
		a.win.Clipboard().SetText(gfy.SudoStartOllama)
		a.toastf("%v — copied: %s", err, gfy.SudoStartOllama)
		return true
	}
	a.toastf("%s: %v", name, err)
	return false
}

// confirm is the dialog that stands between a click and a bill.
//
// It shows the literal command line, the environment overlay with secrets
// masked, the repository count and — for a metered command — the backend and
// model that will actually be used. A large batch additionally requires the
// count to be typed, because "click through" is exactly the failure mode the
// gate exists to prevent.
func (a *App) confirm(kind string, rows []board.Row, mutate func(*gfy.Params)) {
	set := a.opts.Store.Settings()

	sample := a.paramsFor(kind, rows[0])
	if mutate != nil {
		mutate(&sample)
	}
	argv := gfy.Argv(kind, sample)

	// The sample has been through paramsFor and the kind's own mutation, so
	// its backend is the one the run will use — the last point at which the
	// price of THIS run, rather than of the kind, is still knowable.
	cost := gfy.CostFor(kind, sample.Backend)

	heading := gfy.Title(kind)
	var body strings.Builder
	if len(rows) == 1 {
		body.WriteString(rows[0].Name + "\n" + rows[0].Path)
	} else {
		body.WriteString(plural(len(rows), "repository", "repositories") + ":\n")
		for i, r := range rows {
			if i == 8 {
				body.WriteString("…and " + plural(len(rows)-8, "more", "more"))
				break
			}
			body.WriteString("· " + r.Name + "\n")
		}
	}

	if cost == gfy.Metered {
		body.WriteString("\n\n" + a.meteredNotice(sample))
	} else if note := gfy.AccountNote(kind, sample.Backend, sample.ClaudeDir); note != "" {
		// A free command can still write into a Claude Code configuration
		// directory — `install` does, which is the whole of what it does — and
		// which directory that is is a setting, not the default. The metered
		// path says so inside meteredNotice; this is the other half, so the
		// account is named on every dialog it bears on and on none it does
		// not.
		body.WriteString("\n\n" + note)
	}

	dlg := adw.NewAlertDialog(heading, body.String())
	dlg.SetPreferWideLayout(true)

	extra := gtk.NewBox(gtk.OrientationVertical, 8)
	cmdLabel := monoLabel("$ " + gfy.Quote(argv))
	if len(rows) > 1 {
		cmdLabel.SetText("$ " + gfy.Quote(argv) + "\n  …and " +
			plural(len(rows)-1, "more like it", "more like it"))
	}
	frame := gtk.NewFrame("")
	frame.SetChild(cmdLabel)
	cmdLabel.SetMarginTop(8)
	cmdLabel.SetMarginBottom(8)
	cmdLabel.SetMarginStart(8)
	cmdLabel.SetMarginEnd(8)
	extra.Append(frame)

	if env := a.opts.Store.Overlay(rows[0].Path).Display(); len(env) > 0 {
		extra.Append(monoLabel(strings.Join(env, "\n")))
	}

	copyBtn := gtk.NewButtonWithLabel("Copy command line")
	copyBtn.AddCSSClass("flat")
	copyBtn.ConnectClicked(func() {
		a.win.Clipboard().SetText(gfy.Quote(argv))
		a.toast("command line copied")
	})
	extra.Append(copyBtn)

	// The typed-count gate for a large batch. It is deliberately friction:
	// a fan-out extraction over every checkout on the machine is the one way
	// this GUI could do real damage, and a dialog you can dismiss with the
	// space bar is not a gate.
	var entry *gtk.Entry
	typed := len(rows) >= set.ConfirmBatchAt && len(rows) > 1
	if typed {
		entry = gtk.NewEntry()
		entry.SetPlaceholderText("type " + gfy.Itoa(len(rows)) + " to confirm")
		extra.Append(entry)
	}
	dlg.SetExtraChild(extra)

	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("run", "Run")
	dlg.SetResponseAppearance("run", adw.ResponseSuggested)
	if cost == gfy.Metered {
		dlg.SetResponseAppearance("run", adw.ResponseDestructive)
		// `--code-only` is the free way to get most of what `extract` gives,
		// offered right here rather than buried in settings: the moment
		// somebody is looking at a bill is the moment to mention the
		// alternative.
		if kind == "extract" {
			dlg.AddResponse("free", "Run --code-only (free)")
		}
	}
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")

	if typed {
		dlg.SetResponseEnabled("run", false)
		want := gfy.Itoa(len(rows))
		entry.ConnectChanged(func() {
			dlg.SetResponseEnabled("run", strings.TrimSpace(entry.Text()) == want)
		})
	}

	dlg.ConnectResponse(func(resp string) {
		switch resp {
		case "run":
			a.submit(kind, rows, mutate)
		case "free":
			a.submit(kind, rows, func(p *gfy.Params) {
				if mutate != nil {
					mutate(p)
				}
				p.CodeOnly = true
			})
		}
	})
	dlg.Present(a.win)
}

// meteredNotice is the block that stands between a click and a bill: which
// backend, which model, how many of these can run at once, and whether that
// backend is actually usable on this machine.
//
// One function rather than one per dialog. A second confirm path that
// described the cost slightly differently — or forgot to mention the backend
// was not logged in — would be the gap the whole gate exists to close.
func (a *App) meteredNotice(sample gfy.Params) string {
	backend := sample.Backend
	shown := backend
	if shown == "" {
		shown = "auto-detected from whichever API key is set"
	}
	model := sample.Model
	if gfy.IsOpenCodeBackend(backend) {
		// Never "the backend's default" for this one: the default lives in a
		// provider entry this board wrote, so it can be named exactly, along
		// with what it costs.
		m, origin := gfy.OpenCodeModelFor(backend, a.opts.Store.Overlay(sample.Repo), sample.Model)
		model = m.Label() + " (from " + origin + ")"
	}
	if model == "" {
		model = "the backend's default"
		if backend == gfy.ClaudeCLIBackend {
			if m := strings.TrimSpace(os.Getenv(gfy.ClaudeCLIModelVar)); m != "" {
				model = m + " (" + gfy.ClaudeCLIModelVar + ")"
			} else {
				model = "Claude Code's own default for this login — Opus on a Pro/Max plan, " +
					"at that account's effort level (set the Model field, or " +
					gfy.ClaudeCLIModelVar + ", to change it)"
			}
		}
	}

	var b strings.Builder
	// A local model spends no money at all, and saying otherwise would make
	// the one warning this application exists to give mean nothing. The lane
	// is still the metered one — a local run is slow and graphify serializes
	// it — but the paragraph tells the truth about the bill.
	if gfy.IsLocalBackend(backend) {
		b.WriteString(gfy.LocalNotice(backend, sample.Model))
		_, meteredLanes, _ := a.runner.Lanes()
		b.WriteString("Metered lane concurrency: " + gfy.Itoa(meteredLanes) + "\n")
		if ok, why := gfy.LocalReady(backend, sample.Model); !ok {
			b.WriteString("\n⚠ " + why + "\nEvery one of these runs is likely to fail.")
		} else {
			b.WriteString("\n" + why)
		}
		return b.String()
	}
	switch backend {
	case gfy.ClaudeCLIBackend:
		acc := gfy.ClaudeAccountFor(sample.ClaudeDir)
		b.WriteString("This is METERED. It dispatches LLM requests through the " +
			"Claude Code CLI on this machine, billed to the " + acc.Name +
			" login's plan rather than to an API key.\n")
		b.WriteString("Claude Code account: " + acc.Name + " (" + acc.Dir + ")\n")
	case gfy.OpenCodeBackend, gfy.OpenCodeZenBackend:
		plan := gfy.OpenCodePlanFor(backend)
		b.WriteString("This is METERED. It dispatches LLM requests to " + plan.Name +
			" against " + gfy.OpenCodeKeyVar + ", " + plan.Billing + ".\n")
		b.WriteString("Endpoint: " + gfy.OpenCodeUpstream(backend) + " (through this board's loopback " +
			"proxy, which adds the session header the gateway requires)\n")
	default:
		b.WriteString("This is METERED. It dispatches LLM requests against your " +
			"own API key and will be billed.\n")
	}
	b.WriteString("Backend: " + shown + "\nModel: " + model + "\n")
	_, meteredLanes, _ := a.runner.Lanes()
	b.WriteString("Metered lane concurrency: " + gfy.Itoa(meteredLanes) + "\n")
	ready, why := gfy.BackendReady(backend)
	if gfy.IsOpenCodeBackend(backend) {
		// BackendReady answers for a job with nothing chosen; here the model
		// is known, and it is half of what this backend's readiness means.
		ready, why = gfy.OpenCodeReady(backend, sample.Model)
	}
	if !ready {
		b.WriteString("\n⚠ " + why + "\nEvery one of these runs is likely to fail.")
	} else if backend == gfy.ClaudeCLIBackend || gfy.IsOpenCodeBackend(backend) {
		b.WriteString("\n" + why)
	}
	return b.String()
}

// hasGraph reports whether a row's output directory holds a graph a command
// could read, asked of the filesystem rather than of the last scan: a build
// that finished twenty seconds ago must not leave its own repository looking
// ungraphable, and a graph deleted by hand must not look present.
func (a *App) hasGraph(r board.Row) bool { return graphstate.HasGraph(a.params(r).Out) }

// offerExtract explains a blocked action and offers the one command that
// unblocks it, because a refusal that does not name the next step is only
// half an answer. Extract is metered, so choosing it lands on the ordinary
// cost confirm rather than starting anything.
func (a *App) offerExtract(kind string, blocked []board.Row) {
	what := gfy.Title(kind)
	var heading, body string
	if len(blocked) == 1 {
		r := blocked[0]
		heading = r.Name + " has no graph yet"
		body = what + " reads " + filepath.Join(a.params(r).Out, "graph.json") + ", which does not exist.\n\n"
		if r.Graph.State == graphstate.StateBroken {
			heading = r.Name + "'s graph is unreadable"
			body = what + " reads " + filepath.Join(a.params(r).Out, "graph.json") +
				", which is missing or broken: " + r.Graph.Err + "\n\n"
		}
	} else {
		heading = plural(len(blocked), "repository", "repositories") + " with no graph"
		body = what + " reads each repository's graph.json, and " +
			plural(len(blocked), "of them has", "of them have") + " none.\n\n"
	}
	body += "Extract builds one from the checkout (AST + semantic — METERED). " +
		"Update builds the AST half for free."

	dlg := adw.NewAlertDialog(heading, body)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("update", "Run Update (free)")
	dlg.AddResponse("extract", "Run Extract")
	dlg.SetResponseAppearance("extract", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	rows := append([]board.Row(nil), blocked...)
	dlg.ConnectResponse(func(resp string) {
		switch resp {
		case "extract":
			a.run("extract", rows, nil)
		case "update":
			a.run("update", rows, nil)
		}
	})
	dlg.Present(a.win)
}

// --- the individual actions ------------------------------------------------

func (a *App) actUpdate()  { a.run("update", a.batch(), nil) }
func (a *App) actExtract() { a.run("extract", a.batch(), nil) }

// actCluster re-runs clustering *without* the LLM naming pass, which is what
// keeps it free. Naming is the `label` action, and the two are kept apart on
// purpose: `cluster-only` with naming on costs exactly what `label` costs.
func (a *App) actCluster() {
	a.run("cluster-only", a.batch(), func(p *gfy.Params) { p.NoLabel = true })
}

func (a *App) actLabel() {
	a.run("label", a.batch(), func(p *gfy.Params) { p.MissingOnly = true })
}

func (a *App) actCheckUpdate() { a.run("check-update", a.batch(), nil) }

// actGraftBuild syncs the selected repositories' graft index — the other
// indexer's free, tree-sitter-only build. The board-wide form of the same
// command is the folder sweep in graft.go.
func (a *App) actGraftBuild() { a.run("graft-build", a.batch(), nil) }

// actGraftDeep runs graft's LLM tier on the selected repositories against the
// model on this machine. It is the same command the sweep in graft.go queues
// board-wide, and it costs nothing for the same reason: the corpus and the
// bill both stay here.
//
// The readiness check is up front and once, not per repository: every way this
// fails — no graft, no server, no model — fails identically for all of them,
// and a refusal repeated forty times is a refusal nobody reads.
func (a *App) actGraftDeep() {
	rows := a.batch()
	if len(rows) == 0 {
		a.toast("nothing selected")
		return
	}
	if _, _, ok, why := a.graftDeepPin(); !ok {
		a.toast(why)
		return
	}
	a.run(gfy.GraftDeepKind, rows, a.graftDeepParams())
}

// graftDeepPin is where the deep pass will run: its own pin if it has been
// given one, the Heavy work pin otherwise, resolved through graft's own view
// of whichever it is.
//
// Heavy and not the general Backend it used to read — see the weight table:
// the deep index is the other pass that reads every file in the checkout. And
// its own pin above that, because the two heavy kinds are the same shape of
// job and still not the same job.
func (a *App) graftDeepPin() (backend, model string, ok bool, why string) {
	set := a.opts.Store.Settings()
	b, m := set.BackendForKind(gfy.GraftDeepKind)
	// Each OpenCode plan takes its model from its own setting, for the reason
	// paramsFor does the same: the weight pin's model field is blank for those
	// two backends — the settings page greys it out and sends people to the
	// plan's own group — and a blank there would fall back to a plan default
	// nobody picked.
	if eff := gfy.EffectiveBackend(b); gfy.IsOpenCodeBackend(eff) && strings.TrimSpace(m) == "" {
		m = set.OpenCodeModelFor(eff)
	}
	return gfy.GraftDeepReady(b, m)
}

// graftDeepParams is the mutation every deep submit applies: the backend Heavy
// work names, a model it serves, and — locally — the concurrency its slots
// hold. Resolved once per action rather than once per repository — ProbeLocal
// does network I/O, and forty identical probes on the main thread is a visibly
// stalled board.
func (a *App) graftDeepParams() func(*gfy.Params) {
	backend, model, ok, _ := a.graftDeepPin()
	return func(p *gfy.Params) {
		if !ok {
			return
		}
		p.Backend, p.Model = backend, model
		// The re-check inside is not redundant: the server can go away between
		// the dialog and the submit, and a refusal here leaves the argv
		// unbuildable, which SubmitCmd reports per repository rather than
		// sending the corpus somewhere nobody named.
		gfy.GraftDeepParams(p)
	}
}

func (a *App) actExportHTML() { a.run("export-html", a.batch(), nil) }
func (a *App) actExportWiki() { a.run("export-wiki", a.batch(), nil) }

func (a *App) actTree() {
	a.run("tree", a.batch(), func(p *gfy.Params) {
		p.OutFile = filepath.Join(p.Out, "GRAPH_TREE.html")
	})
}

// actWatch starts or stops `graphify watch` for the selected repository. A
// watcher is a long-running child of the board and is cancelled when the board
// exits — the runner's Close does it.
func (a *App) actWatch() {
	r := a.current()
	if r == nil {
		return
	}
	if s := a.jobFor(r.Path); s != nil && s.Kind == "watch" && !s.Status.Done() {
		a.runner.Cancel(s.ID)
		a.toastf("stopped watching %s", r.Name)
		return
	}
	a.run("watch", []board.Row{*r}, nil)
}

// actGlobalAdd merges the selected repositories' graphs into the global graph
// under a tag, which is the cross-repo answer the CLI makes tedious.
func (a *App) actGlobalAdd() { a.globalAdd(a.batch()) }

// actGlobalToggle is the membership switch: one key that puts the selection
// into the global graph or takes it out again.
//
// A mixed batch adds rather than removes. The two are not symmetrical — an add
// is free and idempotent, a remove throws away nodes that cost an extraction —
// so the safe reading of "some of these are in and some are not" is "put the
// rest in", and removing is left to say so explicitly.
func (a *App) actGlobalToggle() {
	rows := a.batch()
	if len(rows) == 0 {
		a.toast("nothing selected")
		return
	}
	var out []board.Row
	for _, r := range rows {
		if !r.Global.In {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		a.globalRemove(rows)
		return
	}
	a.globalAdd(out)
}

// globalTag is the name a row is, or would be, in the global graph.
//
// The manifest's own tag wins whenever there is one: it is the only handle
// `graphify global remove` accepts, and a repository that was added under a
// different name must not be removed by guessing its directory's.
func globalTag(r board.Row) string {
	if t := strings.TrimSpace(r.Global.Tag); t != "" {
		return t
	}
	return filepath.Base(r.Path)
}

// globalAdd merges rows into the global graph, one command at a time.
//
// The sequencing is not a nicety. `graphify global add` is a read-modify-write
// of a single shared file with no cross-process lock, so two of them running
// in different free lanes would race and one repository's nodes would vanish
// into the loser's copy. Every membership change on this board therefore goes
// through a chain, and this is the only place that submits one.
func (a *App) globalAdd(rows []board.Row) {
	if len(rows) == 0 {
		a.toast("nothing selected")
		return
	}
	var ready, blocked []board.Row
	for _, r := range rows {
		if a.hasGraph(r) {
			ready = append(ready, r)
		} else {
			blocked = append(blocked, r)
		}
	}
	if len(blocked) > 0 {
		a.offerExtract("global-add", blocked)
	}
	if len(ready) == 0 {
		return
	}
	if clash := a.globalTagClashes(ready); clash != "" {
		a.confirmGlobal("Tag already taken", clash, "Add anyway", adw.ResponseDestructive,
			func() { a.runGlobalChain("global-add", ready) })
		return
	}
	if len(ready) == 1 {
		a.runGlobalChain("global-add", ready)
		return
	}
	a.confirmGlobal("Add to the global graph",
		"graphify will merge "+plural(len(ready), "repository", "repositories")+
			" into ~/.graphify/global-graph.json, one at a time.\n\n"+
			"They are run in sequence rather than in parallel: `graphify global add` "+
			"rewrites one shared file, and two at once would lose one of them.",
		"Add", adw.ResponseSuggested,
		func() { a.runGlobalChain("global-add", ready) })
}

// globalRemove takes rows back out of the global graph.
//
// Always behind a confirm, even for one row: the nodes it drops were paid for
// by an extraction, and putting them back means running that command again.
func (a *App) globalRemove(rows []board.Row) {
	var in []board.Row
	for _, r := range rows {
		if r.Global.In {
			in = append(in, r)
		}
	}
	if len(in) == 0 {
		a.toast("none of these are in the global graph")
		return
	}
	var tags []string
	for _, r := range in {
		tags = append(tags, globalTag(r))
	}
	a.confirmGlobal("Remove from the global graph",
		"graphify will drop "+plural(len(in), "repository's", "repositories'")+
			" nodes from ~/.graphify/global-graph.json:\n\n"+
			strings.Join(namesUpTo(tags, 8), ", ")+"\n\n"+
			"Nothing inside any checkout is touched — the repositories' own graphs stay "+
			"where they are, and adding them back is free.",
		"Remove", adw.ResponseDestructive,
		func() { a.runGlobalChain("global-remove", in) })
}

// globalTagClashes names the rows whose tag is already taken in the manifest
// by a different graph, which is the one way an add can quietly destroy
// something: graphify prunes whatever is under that tag before it merges.
func (a *App) globalTagClashes(rows []board.Row) string {
	m := a.globals.Load()
	var lines []string
	for _, r := range rows {
		tag := globalTag(r)
		e, ok := m.ByTag(tag)
		if !ok || e.Source == globalgraph.GraphFor(r.Graph.Out) {
			continue
		}
		lines = append(lines, "“"+tag+"” currently holds "+board.Tilde(e.Source))
	}
	// Two rows in the same batch wanting one tag is the same accident one step
	// earlier — two checkouts of the same name under different roots.
	seen := map[string]string{}
	for _, r := range rows {
		tag := globalTag(r)
		if other, dup := seen[tag]; dup {
			lines = append(lines, "“"+tag+"” is wanted by both "+board.Tilde(other)+
				" and "+board.Tilde(r.Path))
			continue
		}
		seen[tag] = r.Path
	}
	if len(lines) == 0 {
		return ""
	}
	return "Adding these would replace the nodes already under those tags:\n\n" +
		strings.Join(lines, "\n") + "\n\n" +
		"A tag is the only handle the global graph has on a repository, so the two " +
		"cannot both be in it under that name."
}

// runGlobalChain submits one membership command per row, in sequence, and
// repaints the board when the chain ends. See globalAdd for why it is a chain.
func (a *App) runGlobalChain(kind string, rows []board.Row) {
	steps := make([]jobs.ChainStep, 0, len(rows))
	for _, r := range rows {
		p := a.paramsFor(kind, r)
		p.Tag = globalTag(r)
		steps = append(steps, jobs.ChainStep{
			Kind:   kind,
			Repo:   r.Path,
			Label:  gfy.Title(kind) + " · " + r.Name,
			Params: p,
			Env:    a.opts.Store.Overlay(r.Path),
		})
	}
	a.submitGlobalChain(steps, gfy.Title(kind))
}

// submitGlobalChain is the tail both the board and the Global screen share:
// run the steps in order, then drop the membership memo and repaint.
func (a *App) submitGlobalChain(steps []jobs.ChainStep, what string) {
	if len(steps) == 0 {
		return
	}
	a.toastf("queued %s on %s", what, plural(len(steps), "repo", "repos"))
	a.runner.SubmitChain(steps, func(res jobs.ChainResult) {
		// The runner calls this on a worker goroutine; everything below is GTK.
		idle(func() {
			// Unconditionally, and before the scan: a chain that stopped
			// halfway still changed the manifest for the steps that ran.
			a.globals.Invalidate()
			a.refresh(false)
			if a.globalPane != nil {
				a.globalPane.reload()
			}
			if res.Err != nil {
				applog.Errorf("%s: stopped after %d/%d — %v", what, res.Ran, res.Total, res.Err)
				a.toastf("%s: stopped after %d/%d — %v", what, res.Ran, res.Total, res.Err)
				return
			}
			a.toastf("%s: %d/%d done", what, res.Ran, res.Total)
		})
	})
}

// confirmGlobal is the alert every membership change goes through. It is not
// a.confirm: that one is about cost and prints an argv, and these commands are
// free — what they need saying is what happens to the shared file.
func (a *App) confirmGlobal(title, body, verb string, look adw.ResponseAppearance, do func()) {
	dlg := adw.NewAlertDialog(title, body)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("go", verb)
	dlg.SetResponseAppearance("go", look)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "go" {
			do()
		}
	})
	dlg.Present(a.win)
}

// namesUpTo is a list a dialog can carry: the first n, and a count for the
// rest. A confirm that names forty repositories is a confirm nobody reads.
func namesUpTo(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	out := append([]string(nil), names[:n]...)
	return append(out, sprintf("and %d more", len(names)-n))
}

// actHookInstall installs graphify's git hooks in the selected repositories.
//
// This is one of the few places ggraphify causes a write *inside* a repository
// (the graft sync is another),
// and it is always explicit and always confirmed — never on a batch path taken
// by accident. The write is graphify's, not the board's.
func (a *App) actHookInstall() {
	rows := a.batch()
	if len(rows) == 0 {
		return
	}
	dlg := adw.NewAlertDialog("Install git hooks",
		"graphify will write post-commit and post-checkout hooks into "+
			plural(len(rows), "repository", "repositories")+".\n\n"+
			"This is one of the few things ggraphify does that changes a repository "+
			"on disk. The write is graphify's own (`graphify hook install`).")
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("install", "Install")
	dlg.SetResponseAppearance("install", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "install" {
			a.submit("hook-install", rows, nil)
		}
	})
	dlg.Present(a.win)
}

// updateSkills re-runs graphify's own installer for the Claude Code platform,
// which is the fix for the skew warning the banner reports. It is the same
// command the settings dialog's Claude Code group offers, built in one place.
func (a *App) updateSkills() {
	a.installClaudeSkill("Update agent skills")
	a.banner.SetRevealed(false)
}

// cancelCurrent cancels whatever is running for the selected row.
func (a *App) cancelCurrent() {
	r := a.current()
	if r == nil {
		return
	}
	s := a.jobFor(r.Path)
	if s == nil || s.Status.Done() {
		a.toast("nothing running on this row")
		return
	}
	a.runner.Cancel(s.ID)
}

// togglePause stops or continues a running job's process group.
//
// It is one call rather than a Pause and a Resume button because the two are
// never both meaningful: a job is either stopped or it is not, and a control
// that shows the state it will move to is the one that can live in a row.
// Both halves run off the main thread, because both can block: a pause stops
// the local model server it was holding up, and a resume waits for that server
// to answer again before the process is woken — up to about ten seconds of a
// cold start, which is ten seconds this thread owes to drawing. The row
// redraws from the Pause event either way, so there is nothing here to wait
// for.
func (a *App) togglePause(id uint64) {
	// One transition per job at a time. The state is read here, on the main
	// thread, and acted on in the goroutine below — so two quick clicks would
	// otherwise both read the same pre-click state, both spawn, and the
	// outcome would be whichever reached setPaused first. A resume can sit
	// inside lease.Resume for the ten seconds a cold model server takes, which
	// is ample room for the second click.
	a.mu.Lock()
	if a.pausing[id] {
		a.mu.Unlock()
		return
	}
	a.pausing[id] = true
	a.mu.Unlock()

	resume := a.runner.Paused(id)
	// A resume against the local model server has to wait for that server to
	// come back before the process is woken, and the row cannot show the
	// change until it has: the Pause event is emitted at the end. So say so
	// now, or a click would look ignored for ten seconds.
	if resume && a.resumeWaitsForOllama(id) {
		a.toast("resuming — starting ollama first")
	}
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.pausing, id)
			a.mu.Unlock()
		}()
		if resume {
			a.runner.Resume(id)
			return
		}
		a.runner.Pause(id)
	}()
}

// resumeWaitsForOllama reports whether resuming this job means waiting for the
// local model server, which is true exactly when the board is managing that
// server for a job that talks to it.
func (a *App) resumeWaitsForOllama(id uint64) bool {
	if !a.opts.Store.Settings().AutoOllama() {
		return false
	}
	s, ok := a.runner.Get(id)
	return ok && s.Local && gfy.ArgvBackend(s.Argv) == gfy.OllamaBackend
}

// pauseCurrent pauses or resumes whatever is running for the selected row.
func (a *App) pauseCurrent() {
	r := a.current()
	if r == nil {
		return
	}
	s := a.jobFor(r.Path)
	if s == nil || s.Status != jobs.Running {
		a.toast("nothing running on this row")
		return
	}
	a.togglePause(s.ID)
}

// pauseIconButton is the pause/resume affordance for a job row, beside the
// cancel button it shares a column with. Only a running job gets one: a queued
// job has no process to stop, and pausing it would mean holding a lane, which
// is a different thing entirely.
func (a *App) pauseIconButton(s jobs.Snapshot) *gtk.Button {
	icon, tip := "media-playback-pause-symbolic", pauseTip
	if s.Paused {
		icon, tip = "media-playback-start-symbolic", resumeTip
	}
	b := gtk.NewButtonFromIconName(icon)
	b.AddCSSClass("flat")
	b.SetTooltipText(tip)
	id := s.ID
	b.ConnectClicked(func() { a.togglePause(id) })
	return b
}

// setPauseButton points a labelled Pause/Resume button at a job, and hides it
// when there is no running job for it to act on — a dead "Pause" next to a
// finished job's log would be a control that lies about what is possible.
func setPauseButton(b *gtk.Button, s *jobs.Snapshot) {
	if s == nil || s.Status != jobs.Running {
		b.SetVisible(false)
		return
	}
	b.SetVisible(true)
	if s.Paused {
		b.SetLabel("Resume")
		b.SetTooltipText(resumeTip)
		return
	}
	b.SetLabel("Pause")
	b.SetTooltipText(pauseTip)
}

const (
	pauseTip = "Stop this job where it stands (SIGSTOP to its whole process group). " +
		"It keeps its log, its lane and its output directory, and resumes from the same place."
	resumeTip = "Continue this job (SIGCONT to its process group)."
)

// buttonSpacer is a fixed-width stand-in for a button a row does not get, so
// the buttons that other rows do get stay in one column.
func buttonSpacer(n int) *gtk.Box {
	sp := gtk.NewBox(gtk.OrientationHorizontal, 0)
	sp.SetSizeRequest(24*n, -1)
	return sp
}

// toggleExclude flips the "keep out of batch actions" override.
func (a *App) toggleExclude() {
	r := a.current()
	if r == nil {
		return
	}
	o := a.opts.Store.Override(r.Path)
	o.ExcludeBatch = !o.ExcludeBatch
	a.opts.Store.SetOverride(r.Path, o)
	if o.ExcludeBatch {
		a.toastf("%s is excluded from batch actions", r.Name)
	} else {
		a.toastf("%s is back in batch actions", r.Name)
	}
	a.refresh(false)
}

// setOverride is the detail pane's writer, kept here so every override change
// goes through one path and triggers one re-derive.
func (a *App) setOverride(path string, o store.RepoOverride) {
	a.opts.Store.SetOverride(path, o)
	a.refresh(false)
}
