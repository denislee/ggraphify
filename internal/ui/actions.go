package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

// params builds the gfy.Params for one row: the board's defaults, overlaid
// with whatever that repository overrides.
func (a *App) params(r board.Row) gfy.Params {
	set := a.opts.Store.Settings()
	o := a.opts.Store.Override(r.Path)

	p := gfy.Params{
		Repo:      r.Path,
		Out:       r.Graph.Out,
		Backend:   set.Backend,
		Model:     set.Model,
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
	needsConfirm := spec.Cost == gfy.Metered ||
		(len(rows) > 1 && len(rows) >= set.ConfirmBatchAt)
	if !needsConfirm {
		a.submit(kind, rows, mutate)
		return
	}
	a.confirm(kind, rows, mutate)
}

// submit queues the jobs. No gate here: run() and confirm() own that decision.
func (a *App) submit(kind string, rows []board.Row, mutate func(*gfy.Params)) {
	n := 0
	for _, r := range rows {
		p := a.params(r)
		if mutate != nil {
			mutate(&p)
		}
		label := gfy.Title(kind) + " · " + r.Name
		if _, err := a.runner.SubmitCmd(kind, r.Path, label, p, a.opts.Store.Overlay(r.Path)); err != nil {
			a.toastf("%s: %v", r.Name, err)
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

// confirm is the dialog that stands between a click and a bill.
//
// It shows the literal command line, the environment overlay with secrets
// masked, the repository count and — for a metered command — the backend and
// model that will actually be used. A large batch additionally requires the
// count to be typed, because "click through" is exactly the failure mode the
// gate exists to prevent.
func (a *App) confirm(kind string, rows []board.Row, mutate func(*gfy.Params)) {
	spec := gfy.Known[kind]
	set := a.opts.Store.Settings()

	sample := a.params(rows[0])
	if mutate != nil {
		mutate(&sample)
	}
	argv := gfy.Argv(kind, sample)

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

	if spec.Cost == gfy.Metered {
		body.WriteString("\n\n" + a.meteredNotice(sample))
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
	if spec.Cost == gfy.Metered {
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
	if model == "" {
		model = "the backend's default"
		if backend == gfy.ClaudeCLIBackend {
			if m := strings.TrimSpace(os.Getenv(gfy.ClaudeCLIModelVar)); m != "" {
				model = m + " (" + gfy.ClaudeCLIModelVar + ")"
			} else {
				model = "Claude Code's own default (set " + gfy.ClaudeCLIModelVar + " to change it)"
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
	if backend == gfy.ClaudeCLIBackend {
		acc := gfy.ClaudeAccountFor(sample.ClaudeDir)
		b.WriteString("This is METERED. It dispatches LLM requests through the " +
			"Claude Code CLI on this machine, billed to the " + acc.Name +
			" login's plan rather than to an API key.\n")
		b.WriteString("Claude Code account: " + acc.Name + " (" + acc.Dir + ")\n")
	} else {
		b.WriteString("This is METERED. It dispatches LLM requests against your " +
			"own API key and will be billed.\n")
	}
	b.WriteString("Backend: " + shown + "\nModel: " + model + "\n")
	_, meteredLanes, _ := a.runner.Lanes()
	b.WriteString("Metered lane concurrency: " + gfy.Itoa(meteredLanes) + "\n")
	if ok, why := gfy.BackendReady(backend); !ok {
		b.WriteString("\n⚠ " + why + "\nEvery one of these runs is likely to fail.")
	} else if backend == gfy.ClaudeCLIBackend {
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

func (a *App) actExportHTML() { a.run("export-html", a.batch(), nil) }
func (a *App) actExportWiki() { a.run("export-wiki", a.batch(), nil) }

func (a *App) actTree() {
	a.run("tree", a.batch(), func(p *gfy.Params) {
		p.OutFile = filepath.Join(p.Out, "GRAPH_TREE.html")
	})
}

func (a *App) actCallflow() { a.run("export-callflow", a.batch(), nil) }

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
func (a *App) actGlobalAdd() {
	rows := a.batch()
	a.run("global-add", rows, func(p *gfy.Params) {
		if p.Tag == "" {
			p.Tag = filepath.Base(p.Repo)
		}
	})
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

func (a *App) actHookStatus() { a.run("hook-status", a.batch(), nil) }

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
func (a *App) togglePause(id uint64) {
	if a.runner.Paused(id) {
		a.runner.Resume(id)
		return
	}
	a.runner.Pause(id)
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
