package ui

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

// showSettings presents the preferences dialog. It is instant-apply: there is
// no OK button, because a preferences dialog with one is a dialog you can
// cancel out of into a state the board is already in.
//
// One AdwPreferencesDialog is held, so pressing `,` twice presents the one
// already open rather than building a second.
func (a *App) showSettings() {
	if a.settings != nil {
		a.settings.Present(a.win)
		return
	}
	dlg := adw.NewPreferencesDialog()
	dlg.SetTitle("ggraphify")
	dlg.Add(a.settingsGeneral())
	dlg.Add(a.settingsJobs())
	dlg.Add(a.settingsAbout())
	a.settings = dlg
	dlg.Present(a.win)
}

// pinned marks a row whose value the command line decided for this run. The
// row is greyed out and says which flag did it, rather than silently
// disagreeing with what the board is actually doing.
func (a *App) pinned(row interface {
	SetSensitive(bool)
	SetTooltipText(string)
}, flag string) bool {
	if !a.opts.SetFlags[flag] {
		return false
	}
	row.SetSensitive(false)
	row.SetTooltipText("Pinned by -" + flag + " on the command line for this run. " +
		"Restart without the flag to edit it here.")
	return true
}

func (a *App) settingsGeneral() *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("General")
	page.SetIconName("preferences-system-symbolic")

	scan := adw.NewPreferencesGroup()
	scan.SetTitle("Scanning")
	scan.SetDescription("Where ggraphify looks for checkouts. Each root is walked independently; " +
		"a root that does not exist is skipped. Every root is also a folder the board can be " +
		"narrowed to, from the dropdown in the filter bar or with F.")

	roots := adw.NewEntryRow()
	roots.SetTitle("Roots, comma separated")
	set := a.opts.Store.Settings()
	rootList := set.Roots
	if len(rootList) == 0 {
		rootList = a.opts.Roots
	}
	roots.SetText(strings.Join(rootList, ", "))
	roots.ConnectApply(func() {
		s := a.opts.Store.Settings()
		s.Roots = splitList(roots.Text())
		a.opts.Store.SetSettings(s)
		a.refresh(true)
	})
	a.pinned(roots, "roots")
	scan.Add(roots)

	// setRoots is the one writer: the entry, the picker and the defaults
	// button all go through it, so the text shown and the preference stored
	// can never drift apart.
	setRoots := func(list []string) {
		roots.SetText(strings.Join(list, ", "))
		s := a.opts.Store.Settings()
		s.Roots = list
		a.opts.Store.SetSettings(s)
		a.refresh(true)
	}

	add := gtk.NewButtonWithLabel("Add folder…")
	add.AddCSSClass("flat")
	add.SetVAlign(gtk.AlignCenter)
	add.SetTooltipText("Pick another directory to scan for checkouts.")
	add.ConnectClicked(func() {
		if a.pinnedFlag("roots") {
			return
		}
		a.chooseFolder("Add a scan root", firstExisting(splitList(roots.Text())), func(path string) {
			list := splitList(roots.Text())
			for _, r := range list {
				if r == path {
					a.toastf("%s is already a root", path)
					return
				}
			}
			setRoots(append(list, path))
			a.toastf("added %s", path)
		})
	})

	defaults := gtk.NewButtonWithLabel("Use defaults")
	defaults.AddCSSClass("flat")
	defaults.SetVAlign(gtk.AlignCenter)
	defaults.SetTooltipText(strings.Join(discover.DefaultRoots(), "\n"))
	defaults.ConnectClicked(func() {
		if a.pinnedFlag("roots") {
			return
		}
		setRoots(discover.DefaultRoots())
	})
	rootsHelp := adw.NewActionRow()
	rootsHelp.SetTitle("Repository folders")
	rootsHelp.SetSubtitle("Defaults: ~/git, ~/tmp, ~/.graphify/repos")
	rootsHelp.AddSuffix(add)
	rootsHelp.AddSuffix(defaults)
	if a.opts.SetFlags["roots"] {
		rootsHelp.SetSensitive(false)
	}
	scan.Add(rootsHelp)

	depth := adw.NewSpinRow(gtk.NewAdjustment(float64(set.Depth), 1, 8, 1, 1, 0), 1, 0)
	depth.SetTitle("Search depth below each root")
	depth.SetSubtitle("Checkouts are leaves: ggraphify does not descend into one looking for submodules.")
	depth.NotifyProperty("value", func() {
		s := a.opts.Store.Settings()
		s.Depth = int(depth.Value())
		a.opts.Store.SetSettings(s)
		a.refresh(true)
	})
	a.pinned(depth, "depth")
	scan.Add(depth)

	dotted := adw.NewSwitchRow()
	dotted.SetTitle("Hide hidden directories")
	dotted.SetSubtitle("Skip directories whose name starts with a dot, and every checkout inside " +
		"them. A root that is itself hidden, like ~/.graphify/repos, is still scanned — naming it " +
		"is asking for what is in it.")
	dotted.SetActive(!set.ShowHidden)
	dotted.NotifyProperty("active", func() {
		s := a.opts.Store.Settings()
		s.ShowHidden = !dotted.Active()
		a.opts.Store.SetSettings(s)
		a.refresh(true)
	})
	a.pinned(dotted, "hidden")
	scan.Add(dotted)

	refresh := adw.NewSpinRow(gtk.NewAdjustment(float64(set.Refresh), 5, 3600, 5, 30, 0), 1, 0)
	refresh.SetTitle("Rescan interval (seconds)")
	refresh.NotifyProperty("value", func() {
		s := a.opts.Store.Settings()
		s.Refresh = int(refresh.Value())
		a.opts.Store.SetSettings(s)
	})
	a.pinned(refresh, "refresh")
	scan.Add(refresh)
	page.Add(scan)
	page.Add(a.settingsStorage())

	look := adw.NewPreferencesGroup()
	look.SetTitle("Appearance")
	scheme := adw.NewComboRow()
	scheme.SetTitle("Colour scheme")
	scheme.SetModel(gtk.NewStringList([]string{"Follow the system", "Light", "Dark"}))
	scheme.SetSelected(uint(schemeIndex(a.currentScheme())))
	scheme.NotifyProperty("selected", func() {
		if a.schemePinned() {
			return
		}
		a.opts.Store.SetScheme(schemeCycle[int(scheme.Selected())])
		a.applyColorScheme()
	})
	if a.schemePinned() {
		scheme.SetSensitive(false)
		scheme.SetSubtitle("pinned by the -dark/-light flag for this run")
	}
	look.Add(scheme)

	cols := adw.NewExpanderRow()
	cols.SetTitle("Columns")
	cols.SetSubtitle("Which columns the board shows")
	hidden := map[string]bool{}
	for _, id := range a.opts.Store.Hidden() {
		hidden[id] = true
	}
	for i := range columns {
		spec := columns[i]
		sw := adw.NewSwitchRow()
		sw.SetTitle(spec.Title)
		sw.SetSubtitle(spec.ID)
		sw.SetActive(!hidden[spec.ID])
		sw.NotifyProperty("active", func() {
			if col := a.cols[spec.ID]; col != nil {
				col.SetVisible(sw.Active())
			}
			a.saveHidden()
		})
		a.setCols[spec.ID] = sw
		cols.AddRow(sw)
	}
	look.Add(cols)
	page.Add(look)
	page.Add(a.settingsPanel())

	tools := adw.NewPreferencesGroup()
	tools.SetTitle("External tools")
	term := adw.NewEntryRow()
	term.SetTitle("Terminal (blank = $TERMINAL, then foot/alacritty/kitty)")
	term.SetText(set.Terminal)
	term.ConnectApply(func() {
		s := a.opts.Store.Settings()
		s.Terminal = strings.TrimSpace(term.Text())
		a.opts.Store.SetSettings(s)
	})
	tools.Add(term)
	ed := adw.NewEntryRow()
	ed.SetTitle("Editor (blank = $VISUAL, $EDITOR, then nvim/vim)")
	ed.SetText(set.Editor)
	ed.ConnectApply(func() {
		s := a.opts.Store.Settings()
		s.Editor = strings.TrimSpace(ed.Text())
		a.opts.Store.SetSettings(s)
	})
	tools.Add(ed)
	page.Add(tools)

	return page
}

func (a *App) settingsJobs() *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("Jobs")
	page.SetIconName("system-run-symbolic")
	set := a.opts.Store.Settings()

	lanes := adw.NewPreferencesGroup()
	lanes.SetTitle("Concurrency")
	lanes.SetDescription("Free work (AST extraction, clustering, exports, reads) fans out. " +
		"Metered work dispatches LLM requests against your own API key and is deliberately " +
		"serialized — raising this lane is the one setting here that can cost money. " +
		"LLM work against a model on THIS machine has its own lane, because there the " +
		"limit is cores and memory rather than spend.")

	free, metered, local := a.runner.Lanes()
	freeRow := adw.NewSpinRow(gtk.NewAdjustment(float64(free), 1, 32, 1, 2, 0), 1, 0)
	freeRow.SetTitle("Free lanes")
	freeRow.NotifyProperty("value", func() {
		_, m, l := a.runner.Lanes()
		a.runner.SetLanes(int(freeRow.Value()), m, l)
		s := a.opts.Store.Settings()
		s.FreeLanes = int(freeRow.Value())
		a.opts.Store.SetSettings(s)
	})
	lanes.Add(freeRow)

	meteredRow := adw.NewSpinRow(gtk.NewAdjustment(float64(metered), 1, 16, 1, 1, 0), 1, 0)
	meteredRow.SetTitle("Metered lanes")
	meteredRow.SetSubtitle("Billed backends only. Default 1. Never raised implicitly.")
	meteredRow.AddCSSClass("metered")
	meteredRow.NotifyProperty("value", func() {
		f, _, l := a.runner.Lanes()
		a.runner.SetLanes(f, int(meteredRow.Value()), l)
		s := a.opts.Store.Settings()
		s.MeteredLanes = int(meteredRow.Value())
		a.opts.Store.SetSettings(s)
	})
	lanes.Add(meteredRow)

	// The local lane, and the arithmetic that makes its number mean something.
	// A lane count on its own is not a decision anybody can make: what matters
	// is lanes x chunks against the slots the server actually has, and that
	// product is three numbers the user would otherwise have to find in three
	// different places.
	localRow := adw.NewSpinRow(
		gtk.NewAdjustment(float64(local), 1, float64(gfy.MaxLocalLanes), 1, 1, 0), 1, 0)
	localRow.SetTitle("Local model lanes")
	localRow.SetSubtitleLines(0)
	localSubtitle := func(n int) string {
		s := "Jobs run at once against a model on this machine. Free — the limit here is " +
			"cores and memory, not money. Default " + gfy.Itoa(jobs.DefaultLocalLanes) +
			": a graphify run alternates between a CPU-bound AST pass that leaves the model " +
			"idle and a semantic pass that is nothing but requests to it, and a second job " +
			"fills the first job's idle stretches."
		set := a.opts.Store.Settings()
		eff := gfy.EffectiveBackend(set.Backend)
		if !gfy.IsLocalBackend(eff) {
			return s + " No local backend is selected, so nothing takes this lane right now."
		}
		p := gfy.ProbeLocal(eff)
		if !p.Reach {
			return s
		}
		chunks := gfy.LocalConcurrency(eff)
		if chunks < 1 {
			chunks = 1
		}
		s += " Right now: " + plural(n, "job", "jobs") + " x " +
			plural(chunks, "chunk", "chunks") + " = " +
			plural(n*chunks, "request", "requests") + " at "
		if p.Slots > 0 {
			s += plural(p.Slots, "server slot", "server slots") + "."
		} else {
			s += "a server whose slot count could not be read."
		}
		if p.Slots > 0 && n*chunks > p.Slots {
			// Not a warning. The surplus is the point — it is what keeps the
			// slots fed while another job is in its AST phase — but somebody
			// reading "8 jobs" should see what that actually queues.
			s += " The surplus queues inside the server; the request timeout " +
				"scales with the lane count so a queued chunk is not killed waiting."
		}
		return s
	}
	localRow.SetSubtitle(escapeMarkup(localSubtitle(local)))
	localRow.NotifyProperty("value", func() {
		n := int(localRow.Value())
		f, m, _ := a.runner.Lanes()
		a.runner.SetLanes(f, m, n)
		s := a.opts.Store.Settings()
		s.LocalLanes = n
		a.opts.Store.SetSettings(s)
		localRow.SetSubtitle(escapeMarkup(localSubtitle(n)))
	})
	lanes.Add(localRow)

	confirm := adw.NewSpinRow(gtk.NewAdjustment(float64(set.ConfirmBatchAt), 2, 500, 1, 5, 0), 1, 0)
	confirm.SetTitle("Type-to-confirm threshold")
	confirm.SetSubtitle("A batch of at least this many repositories requires the count to be typed.")
	confirm.NotifyProperty("value", func() {
		s := a.opts.Store.Settings()
		s.ConfirmBatchAt = int(confirm.Value())
		a.opts.Store.SetSettings(s)
	})
	lanes.Add(confirm)
	page.Add(lanes)

	llm := adw.NewPreferencesGroup()
	llm.SetTitle("LLM defaults")
	desc := "Used by the metered commands unless a repository overrides them. " +
		"A blank backend means auto-detect: graphify picks from whichever API key is in " +
		"the environment. ggraphify never stores an API key — only the backend's name."
	if !gfy.HasAPIKey() && gfy.HasClaudeCLI() {
		desc += " With no API key set and a Claude Code CLI installed here, auto-detect " +
			"resolves to claude-cli, which runs against your Claude Code login instead."
	}
	if eff := gfy.EffectiveBackend(""); eff == gfy.OpenCodeBackend {
		desc += " With " + gfy.OpenCodeKeyVar + " exported and no vendor API key, auto-detect " +
			"resolves to " + gfy.OpenCodeBackend + ": graphify has no built-in backend by that " +
			"name, so the board names it outright and registers it as a custom provider. Its " +
			"model is chosen in its own group below, not in the Model field here."
	}
	llm.SetDescription(desc)

	backend := adw.NewComboRow()
	backend.SetTitle("Backend")
	names := make([]string, len(gfy.Backends))
	for i, b := range gfy.Backends {
		switch b {
		case "":
			names[i] = "auto-detect"
			if eff := gfy.EffectiveBackend(""); eff != "" {
				names[i] += " (→ " + eff + ")"
			}
		case gfy.ClaudeCLIBackend:
			names[i] = b + " (Claude Code on this machine, no API key)"
		case gfy.OllamaBackend:
			names[i] = b + " (a model on this machine — free, slow, no API key)"
		case gfy.OpenCodeBackend:
			names[i] = b + " (OpenCode Go — a $10/month subscription, " + gfy.OpenCodeKeyVar + ")"
		default:
			names[i] = b
			// openai is the OpenAI API until OPENAI_BASE_URL says otherwise,
			// at which point it is whatever local server that names. Saying so
			// here is the only place a user would find out.
			if gfy.IsLocalBackend(b) {
				names[i] = b + " (→ " + gfy.LocalBaseURL(b) + ", local and free)"
			}
		}
	}
	backend.SetModel(gtk.NewStringList(names))
	for i, b := range gfy.Backends {
		if b == set.Backend {
			backend.SetSelected(uint(i))
		}
	}
	llm.Add(backend)

	model := adw.NewEntryRow()
	model.SetTitle("Model (blank = the backend's default)")
	// Every backend but one takes this as `--model` on the command line. The
	// claude-cli backend ignores that flag — graphify's CLI path reads
	// GRAPHIFY_CLAUDE_CLI_MODEL and nothing else — so for that backend the
	// board exports this value instead. Same field, same meaning, two
	// mechanisms; the row says so because a setting that appeared to do
	// nothing is what sent extractions to Opus for a JSON-shaped job.
	model.SetText(set.Model)
	model.ConnectApply(func() {
		s := a.opts.Store.Settings()
		s.Model = strings.TrimSpace(model.Text())
		a.opts.Store.SetSettings(s)
	})
	llm.Add(model)

	// The Model field is one field for every backend, and opencode-go is the
	// one backend it does not reach: that plan's model is a pick from a list
	// with prices attached, kept in its own group so the cost estimate can
	// move with it. Saying so on this row, as it happens, is the difference
	// between a dropdown nobody finds and a dropdown that is where the user
	// already is.
	syncBackend := func(b string) {
		if gfy.EffectiveBackend(b) == gfy.OpenCodeBackend {
			model.SetSensitive(false)
			model.SetTitle("Model — chosen under OpenCode Go, below")
		} else {
			model.SetSensitive(true)
			model.SetTitle("Model (blank = the backend's default)")
		}
		if a.ocApply != nil {
			a.ocApply(gfy.EffectiveBackend(b))
		}
	}
	backend.NotifyProperty("selected", func() {
		s := a.opts.Store.Settings()
		s.Backend = gfy.Backends[int(backend.Selected())]
		a.opts.Store.SetSettings(s)
		syncBackend(s.Backend)
	})

	key := adw.NewActionRow()
	key.SetTitle("API key")
	key.SetSubtitleLines(0)
	if gfy.HasAPIKey() {
		sub := "An API key is visible in this environment. It is passed through to " +
			"graphify and is never written to ggraphify's state file."
		if gfy.HasOpenCodeKey() {
			sub += " That includes " + gfy.OpenCodeKeyVar + " — see the OpenCode Go group below."
		}
		key.SetSubtitle(sub)
	} else if gfy.HasClaudeCLI() {
		key.SetSubtitle("No API key is visible in this environment — the claude-cli backend " +
			"will be used instead, and needs none.")
	} else {
		key.SetSubtitle("No API key is visible in this environment — metered commands will fail. " +
			"Export one before launching, install the Claude Code CLI, or run a local model: " +
			"see the group below.")
	}
	llm.Add(key)

	// The Claude Code CLI is the one backend whose availability is a fact about
	// this machine rather than about the environment, so the board reports where
	// it found it — a claude resolved outside $PATH is added to the PATH each job
	// inherits, which is otherwise an invisible thing to have happened.
	cli := adw.NewActionRow()
	cli.SetTitle("Claude Code CLI")
	cli.SetSubtitleLines(0)
	if p := gfy.ClaudeCLI(); p != "" {
		acc := gfy.ClaudeAccountFor(set.ClaudeAccount)
		sub := p + " — usable as the claude-cli backend, billed to the " + acc.Name +
			" login's plan (" + gfy.Tilde(acc.Dir) + ") rather than to an API key. " +
			"graphify runs it one request at a time unless GRAPHIFY_CLAUDE_CLI_PARALLEL=1. " +
			"The account is chosen under Claude Code integration, below."
		if m, origin := gfy.ClaudeCLIModelFor(a.opts.Store.Overlay(""), set.Model); m != "" {
			sub += " Model: " + m + " (from " + origin + ")."
		} else {
			sub += " Model: Claude Code's own default for that login — Opus on a Pro/Max " +
				"plan, at that account's effort level, for an extraction whose output is a " +
				"schema-constrained JSON object. Put a cheaper model in the Model field " +
				"above, or set " + gfy.ClaudeCLIModelVar + " in the overlay below."
		}
		cli.SetSubtitle(sub)
	} else {
		cli.SetSubtitle("Not found on this machine. Install it from https://claude.ai/code and " +
			"run `claude` once to authenticate, then the claude-cli backend needs no API key.")
	}
	llm.Add(cli)

	page.Add(llm)

	page.Add(a.settingsOpenCode())
	// After the group exists, so the first call reaches it rather than the
	// nil it would find while the page above was still being built.
	syncBackend(set.Backend)

	page.Add(a.settingsAutoFix())

	page.Add(a.settingsLocal())
	page.Add(a.settingsClaude())
	page.Add(a.settingsGraft())

	page.Add(a.settingsOverlay())
	return page
}

// settingsOverlay is the GRAPHIFY_* environment editor: a per-job overlay,
// composed on top of the inherited environment rather than exported globally,
// so two concurrent jobs can target different backends.
func (a *App) settingsOverlay() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Environment overlay")
	g.SetDescription("GRAPHIFY_* variables applied to every job. Values whose name looks like a " +
		"credential are masked on screen; they are still written to the state file, so put a " +
		"key in your shell profile rather than here.")

	a.overlayGroup = g
	a.rebuildOverlayRows()
	return g
}

func (a *App) rebuildOverlayRows() {
	g := a.overlayGroup
	if g == nil {
		return
	}
	for _, r := range a.overlayRows {
		g.Remove(r)
	}
	a.overlayRows = nil

	set := a.opts.Store.Settings()
	keys := make([]string, 0, len(set.Overlay))
	for k := range set.Overlay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		row := adw.NewEntryRow()
		row.SetTitle(name)
		row.SetText(gfy.Mask(name, set.Overlay[name]))
		if gfy.IsSecret(name) {
			// Shown but not editable: the value is masked on screen, so an
			// edit would write the mask back over the real value.
			row.SetEditable(false)
			row.SetTooltipText("This value looks like a credential, so it is masked. " +
				"Delete and re-add it to change it.")
		} else {
			row.ConnectApply(func() {
				s := a.opts.Store.Settings()
				s.Overlay[name] = row.Text()
				a.opts.Store.SetSettings(s)
			})
		}
		del := gtk.NewButtonFromIconName("user-trash-symbolic")
		del.AddCSSClass("flat")
		del.SetVAlign(gtk.AlignCenter)
		del.ConnectClicked(func() {
			s := a.opts.Store.Settings()
			delete(s.Overlay, name)
			a.opts.Store.SetSettings(s)
			a.rebuildOverlayRows()
		})
		row.AddSuffix(del)
		g.Add(row)
		a.overlayRows = append(a.overlayRows, row)
	}

	// The add row, always last.
	add := adw.NewEntryRow()
	add.SetTitle("Add a variable: NAME=value")
	add.ConnectApply(func() {
		name, value, ok := strings.Cut(strings.TrimSpace(add.Text()), "=")
		name = strings.TrimSpace(name)
		if !ok || !gfy.ValidVar(name) {
			a.toast("expected NAME=value with a valid variable name")
			return
		}
		s := a.opts.Store.Settings()
		s.Overlay[name] = value
		a.opts.Store.SetSettings(s)
		add.SetText("")
		a.rebuildOverlayRows()
	})
	g.Add(add)
	a.overlayRows = append(a.overlayRows, add)
}

func (a *App) settingsAbout() *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("About")
	page.SetIconName("help-about-symbolic")

	g := adw.NewPreferencesGroup()
	g.SetTitle("graphify")
	g.SetDescription("ggraphify never re-implements graphify: every mutation is a supervised " +
		"subprocess, and every fact on the board is read from graphify-out/ on disk.")

	binRow := adw.NewActionRow()
	binRow.SetTitle("Binary")
	binRow.SetSubtitle(gfy.Bin())
	g.Add(binRow)

	verRow := adw.NewActionRow()
	verRow.SetTitle("Version")
	if a.version.Found {
		verRow.SetSubtitle(a.version.Number + "  (ggraphify's command builders target " + gfy.BuiltAgainst + ")")
	} else {
		verRow.SetSubtitle("not found: " + a.version.Err)
	}
	reprobe := gtk.NewButtonWithLabel("Re-probe")
	reprobe.AddCSSClass("flat")
	reprobe.SetVAlign(gtk.AlignCenter)
	reprobe.ConnectClicked(func() {
		go func() {
			v := gfy.Reprobe(context.TODO())
			idle(func() {
				a.version = v
				if v.Found {
					verRow.SetSubtitle(v.Number)
				} else {
					verRow.SetSubtitle("not found: " + v.Err)
				}
				a.refreshStatus()
			})
		}()
	})
	verRow.AddSuffix(reprobe)
	g.Add(verRow)

	if a.version.SkillSkew != "" {
		skew := adw.NewActionRow()
		skew.SetTitle("Agent skill is out of date")
		skew.SetSubtitle(a.version.SkillSkew)
		fix := gtk.NewButtonWithLabel("Update")
		fix.AddCSSClass("suggested-action")
		fix.SetVAlign(gtk.AlignCenter)
		fix.ConnectClicked(func() { a.updateSkills() })
		skew.AddSuffix(fix)
		g.Add(skew)
	}
	page.Add(g)

	self := adw.NewPreferencesGroup()
	self.SetTitle("ggraphify")
	v := adw.NewActionRow()
	v.SetTitle("Version")
	v.SetSubtitle(a.opts.Version)
	self.Add(v)
	state := adw.NewActionRow()
	state.SetTitle("State file")
	state.SetSubtitle(a.opts.Store.Path())
	self.Add(state)
	page.Add(self)
	return page
}

// settingsPanel is the bottom panel: the jobs strip and the application log.
//
// Three switches rather than two, because "which pages exist" and "is the
// panel on screen right now" are different questions — b answers the second
// one a dozen times an hour and must not forget the answer to the first.
func (a *App) settingsPanel() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Bottom panel")
	g.SetDescription("A strip across the bottom of the window. Both of its pages are on by " +
		"default; b hides and shows the whole panel, and its height is remembered.")

	row := func(id, title, subtitle string, active bool, apply func(*store.Settings, bool)) *adw.SwitchRow {
		sw := adw.NewSwitchRow()
		sw.SetTitle(title)
		sw.SetSubtitle(subtitle)
		sw.SetSubtitleLines(0)
		sw.SetActive(active)
		sw.NotifyProperty("active", func() {
			if a.dockGuard {
				return
			}
			set := a.opts.Store.Settings()
			apply(&set, sw.Active())
			a.opts.Store.SetSettings(set)
			a.applyDockSettings()
			a.syncDockSwitches()
		})
		a.dockRows[id] = sw
		g.Add(sw)
		return sw
	}

	set := a.opts.Store.Settings()
	row("panel", "Show the bottom panel", "Toggle it any time with b.",
		!set.HideBottom, func(s *store.Settings, on bool) {
			s.HideBottom = !on
			if on && s.HideJobsBar && s.HideLogBar {
				s.HideJobsBar = false
			}
		})
	row("jobs", "Jobs", "Everything queued, running and recently finished, across every "+
		"repository — the same queue the jobs dialog (J) shows in full, with each job's log.",
		!set.HideJobsBar, func(s *store.Settings, on bool) {
			s.HideJobsBar = !on
			if on {
				s.HideBottom = false
			}
		})
	row("log", "Application log", "What ggraphify itself has to say: scans, job transitions, "+
		"failures, and anything it toasted. One button copies the whole log under a description "+
		"of this machine's setup — the thing worth pasting into an issue, or handing to an agent.",
		!set.HideLogBar, func(s *store.Settings, on bool) {
			s.HideLogBar = !on
			if on {
				s.HideBottom = false
			}
		})
	return g
}

// syncDockSwitches puts the dialog back in step after the panel's state moved
// somewhere else — the b key, or the panel's own hide button.
func (a *App) syncDockSwitches() {
	if len(a.dockRows) == 0 {
		return
	}
	set := a.opts.Store.Settings()
	a.dockGuard = true
	defer func() { a.dockGuard = false }()
	for id, want := range map[string]bool{
		"panel": !set.HideBottom,
		"jobs":  !set.HideJobsBar,
		"log":   !set.HideLogBar,
	} {
		if sw := a.dockRows[id]; sw != nil && sw.Active() != want {
			sw.SetActive(want)
		}
	}
}

func (a *App) saveHidden() {
	var ids []string
	for id, sw := range a.setCols {
		if !sw.Active() {
			ids = append(ids, id)
		}
	}
	a.opts.Store.SetHidden(ids)
}

func schemeIndex(v string) int {
	for i, s := range schemeCycle {
		if s == v {
			return i
		}
	}
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// settingsStorage is where graphify's knowledge lives: the directory name used
// inside every checkout, or one directory outside them all holding a
// subdirectory per repository.
//
// It is one setting with two shapes rather than two settings, because they are
// mutually exclusive in practice and a board configured with both would have
// to pick one silently. The board reads from whatever this resolves to and
// every job is launched with GRAPHIFY_OUT pointing at the same place, so the
// two halves cannot disagree.
func (a *App) settingsStorage() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Graph storage")
	g.SetDescription("Where each repository's graphify knowledge — graph.json, the manifest, " +
		"the report, the wiki — is kept. Changing this changes where ggraphify looks and where " +
		"its jobs write; it does not move graphs that already exist.")

	set := a.opts.Store.Settings()

	where := adw.NewComboRow()
	where.SetTitle("Location")
	where.SetModel(gtk.NewStringList([]string{
		"Inside each checkout",
		"In one directory, outside the checkouts",
	}))

	name := adw.NewEntryRow()
	name.SetTitle("Directory name inside each checkout")
	name.SetText(set.OutName)

	base := adw.NewEntryRow()
	base.SetTitle("Directory holding every graph")
	base.SetText(set.OutBase)

	example := adw.NewActionRow()
	example.SetTitle("A repository's graph resolves to")
	example.AddCSSClass("dim-label")

	central := strings.TrimSpace(set.OutBase) != ""
	if central {
		where.SetSelected(1)
	}

	// apply is the single writer, and the single place the two rows' relevance
	// is decided: a central directory is only a setting while it is selected,
	// so switching back to in-tree clears it rather than leaving a value that
	// silently does nothing.
	apply := func() {
		s := a.opts.Store.Settings()
		s.OutName = strings.TrimSpace(name.Text())
		if where.Selected() == 1 {
			s.OutBase = strings.TrimSpace(base.Text())
		} else {
			s.OutBase = ""
		}
		a.opts.Store.SetSettings(s)
		name.SetVisible(where.Selected() == 0)
		base.SetVisible(where.Selected() == 1)
		example.SetSubtitle(a.outExample(s.OutName, s.OutBase))
		a.refresh(true)
	}

	name.ConnectApply(func() { apply() })
	base.ConnectApply(func() { apply() })
	where.NotifyProperty("selected", func() { apply() })

	pick := gtk.NewButtonWithLabel("Choose…")
	pick.AddCSSClass("flat")
	pick.SetVAlign(gtk.AlignCenter)
	pick.ConnectClicked(func() {
		a.chooseFolder("Directory for every graph", discover.Expand(base.Text()), func(path string) {
			base.SetText(path)
			apply()
		})
	})
	base.AddSuffix(pick)

	reset := gtk.NewButtonWithLabel("Use default")
	reset.AddCSSClass("flat")
	reset.SetVAlign(gtk.AlignCenter)
	reset.SetTooltipText("graphify's own default: " + discover.DefaultOutName + "/ inside each checkout")
	reset.ConnectClicked(func() {
		name.SetText("")
		where.SetSelected(0)
		apply()
	})
	name.AddSuffix(reset)

	g.Add(where)
	g.Add(name)
	g.Add(base)
	g.Add(example)

	// The environment overlay can name the same two variables, and a board
	// that read one location while its jobs were handed another would show
	// permanent drift. The storage setting wins — GRAPHIFY_OUT is composed
	// last — so what is left to do is say so rather than let the overlay look
	// like it is in charge.
	if clash := overlayClash(set.Overlay); clash != "" {
		warn := adw.NewActionRow()
		warn.SetTitle("The environment overlay also sets " + clash)
		warn.SetSubtitle("This setting wins for every job ggraphify launches. " +
			"Remove it from the overlay on the Jobs page to keep one answer.")
		warn.AddCSSClass("warning")
		g.Add(warn)
	}

	name.SetVisible(!central)
	base.SetVisible(central)
	example.SetSubtitle(a.outExample(set.OutName, set.OutBase))

	for _, w := range []interface {
		SetSensitive(bool)
		SetTooltipText(string)
	}{where, name, base} {
		a.pinned(w, "out-name")
		a.pinned(w, "out-base")
	}
	return g
}

// outExample renders the setting against a real row, because "one directory
// per checkout, keyed by a digest of its path" is a sentence and a resolved
// path is an answer. It falls back to a plausible path when the board has no
// rows yet.
func (a *App) outExample(outName, outBase string) string {
	repo := "/path/to/repo"
	if r := a.current(); r != nil {
		repo = r.Path
	} else if rows := a.allRows(); len(rows) > 0 {
		repo = rows[0].Path
	}
	return discover.OutSpec{Name: outName, Base: outBase, Repo: a.opts.Store.Override(repo).Out}.For(repo)
}

// pinnedFlag reports whether the command line decided this setting for the
// run, toasting the reason. A button behind a pinned row would otherwise write
// a preference the board is not using.
func (a *App) pinnedFlag(flag string) bool {
	if !a.opts.SetFlags[flag] {
		return false
	}
	a.toastf("-%s was given on the command line; restart without it to change this here", flag)
	return true
}

// firstExisting is the folder picker's starting point: the first configured
// path that is actually a directory, else the home directory.
func firstExisting(paths []string) string {
	for _, p := range paths {
		p = discover.Expand(p)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// overlayClash names the output variables the environment overlay sets, if
// any, for the warning in the storage group.
func overlayClash(overlay map[string]string) string {
	var found []string
	for _, k := range []string{"GRAPHIFY_OUT", "GRAPHIFY_OUT_NAME"} {
		if _, ok := overlay[k]; ok {
			found = append(found, k)
		}
	}
	return strings.Join(found, " and ")
}

// settingsAutoFix is the unattended repair loop's switch board.
//
// It is on the Jobs page rather than General because everything it does is
// queue jobs, and the two numbers below it — the lanes — are what decides how
// much of the machine it may take while doing so.
//
// The group's job is to make one thing unmissable: the loop is on, and it does
// not spend money. Every LLM step it runs goes to a model on this machine, and
// the one switch that changes that is off, marked, and says what it costs.
func (a *App) settingsAutoFix() *adw.PreferencesGroup {
	set := a.opts.Store.Settings()

	g := adw.NewPreferencesGroup()
	g.SetTitle("Automatic fixes")
	g.SetDescription("The board repairs unhealthy repositories on its own, on the same tick " +
		"that scans them — the Fix button with the click taken out. Same plan, same commands, " +
		"same order. On by default, because with a model on this machine it costs nothing but " +
		"time; it never reaches for a billed backend unless the switch below says it may.")

	// Everything under the master switch is desensitized with it, so a page
	// with auto-fix off does not read as a page of live settings.
	var dependents []interface{ SetSensitive(bool) }
	sensitize := func(on bool) {
		for _, w := range dependents {
			w.SetSensitive(on)
		}
	}

	enabled := adw.NewSwitchRow()
	enabled.SetTitle("Fix repositories automatically")
	enabled.SetSubtitleLines(0)
	enabled.SetSubtitle("Unhealthy repositories are repaired without being selected or " +
		"confirmed. Repositories kept out of batch actions (the X flag) are left alone, and " +
		"so is any repository with a job already running.")
	enabled.SetActive(set.AutoFix())
	g.Add(enabled)

	local := adw.NewSwitchRow()
	local.SetTitle("Use a local model for the LLM steps")
	local.SetSubtitleLines(0)
	local.SetActive(set.AutoFixLocal())
	local.SetSubtitle(a.autoFixLocalSubtitle())
	g.Add(local)
	dependents = append(dependents, local)

	metered := adw.NewSwitchRow()
	metered.SetTitle("Allow metered fixes")
	metered.AddCSSClass("metered")
	metered.SetSubtitleLines(0)
	metered.SetSubtitle("Off. Lets the loop run the LLM steps against the billed backend when " +
		"no local model is available — a full extraction and a community naming per repository, " +
		"unattended, on your own key. Leave it off unless you have read what that costs over a " +
		"board of this size.")
	metered.SetActive(set.AutoFixMetered)
	g.Add(metered)
	dependents = append(dependents, metered)

	maxRow := adw.NewSpinRow(
		gtk.NewAdjustment(float64(autoFixOr(set.AutoFixMax, autofix.DefaultMax)), 1, 16, 1, 1, 0), 1, 0)
	maxRow.SetTitle("Repositories at once")
	maxRow.SetSubtitleLines(0)
	maxRow.SetSubtitle("How many repairs the loop keeps in flight. Default " +
		gfy.Itoa(autofix.DefaultMax) + ". The lanes above are the real limit on what runs at " +
		"the same time; this one stops a large board from queueing a chain per checkout the " +
		"moment it starts.")
	g.Add(maxRow)
	dependents = append(dependents, maxRow)

	coolRow := adw.NewSpinRow(gtk.NewAdjustment(
		float64(autoFixOr(set.AutoFixCooldown, int(autofix.DefaultCooldown/time.Minute)*60))/60, 1, 720, 1, 5, 0), 1, 0)
	coolRow.SetTitle("Cooldown (minutes)")
	coolRow.SetSubtitleLines(0)
	coolRow.SetSubtitle("The least time before the same repository is attempted again, doubled " +
		"and tripled as attempts fail. Default " +
		gfy.Itoa(int(autofix.DefaultCooldown/time.Minute)) + " minutes.")
	g.Add(coolRow)
	dependents = append(dependents, coolRow)

	triesRow := adw.NewSpinRow(
		gtk.NewAdjustment(float64(autoFixOr(set.AutoFixAttempts, autofix.DefaultAttempts)), 1, 10, 1, 1, 0), 1, 0)
	triesRow.SetTitle("Attempts before giving up")
	triesRow.SetSubtitleLines(0)
	triesRow.SetSubtitle("How many times the same set of defects may be attacked before the " +
		"repository is left alone and said so in the log. Default " +
		gfy.Itoa(autofix.DefaultAttempts) + ". A repository whose defects CHANGE gets a fresh " +
		"count, so a multi-stage repair is never mistaken for a loop.")
	g.Add(triesRow)
	dependents = append(dependents, triesRow)

	// One writer for the whole group. Each row edits its own field of a fresh
	// copy of the settings, and the engine's memory is cleared afterwards:
	// whatever it was refusing to retry under the old policy it must be
	// willing to retry under the new one, or a setting changed to unblock a
	// repository would leave it blocked.
	save := func(f func(*store.Settings)) {
		s := a.opts.Store.Settings()
		f(&s)
		a.opts.Store.SetSettings(s)
		if a.autofix != nil {
			a.autofix.Reset()
		}
	}

	enabled.NotifyProperty("active", func() {
		on := enabled.Active()
		save(func(s *store.Settings) { s.NoAutoFix = !on })
		sensitize(on)
		if on {
			a.toast("automatic fixes on — the next scan repairs what it finds")
		} else {
			a.toast("automatic fixes off — nothing runs unless you ask")
		}
	})
	local.NotifyProperty("active", func() {
		save(func(s *store.Settings) { s.NoAutoFixLocal = !local.Active() })
		local.SetSubtitle(a.autoFixLocalSubtitle())
	})
	metered.NotifyProperty("active", func() {
		on := metered.Active()
		save(func(s *store.Settings) { s.AutoFixMetered = on })
		if on {
			a.toast("automatic fixes may now use the billed backend")
		}
	})
	maxRow.NotifyProperty("value", func() {
		save(func(s *store.Settings) { s.AutoFixMax = int(maxRow.Value()) })
	})
	coolRow.NotifyProperty("value", func() {
		save(func(s *store.Settings) { s.AutoFixCooldown = int(coolRow.Value()) * 60 })
	})
	triesRow.NotifyProperty("value", func() {
		save(func(s *store.Settings) { s.AutoFixAttempts = int(triesRow.Value()) })
	})

	sensitize(set.AutoFix())
	return g
}

// autoFixOr is the stored value, or the package default when nothing was ever
// stored. The zero in the state file means "whatever autofix decides", and
// that has to survive a trip through a spin button that cannot show it.
func autoFixOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// autoFixLocalSubtitle says what the local half of the loop will actually do
// on THIS machine right now — which model it would use, or why it cannot — so
// the switch is not a promise the hardware cannot keep.
//
// It reads the cached probe rather than forcing one: the settings page is
// redrawn on every open and an HTTP round trip per redraw would be felt.
func (a *App) autoFixLocalSubtitle() string {
	const lead = "The steps that need an LLM — a full extraction, naming communities — run " +
		"against a model on this machine instead of a billed backend. No API key, nothing " +
		"leaves here, and no bill: this is what lets the loop be on by default. "

	set := a.opts.Store.Settings()
	eff := gfy.EffectiveBackend(set.Backend)
	backend, preferred := eff, set.Model
	if !gfy.IsLocalBackend(eff) {
		backend, preferred = gfy.OllamaBackend, ""
	}
	model, ok, why := gfy.AutoLocalModel(backend, preferred)
	if !ok {
		return lead + "Right now there is none: " + why +
			" Until there is, the loop runs the free steps only and leaves the rest undone."
	}
	s := lead + "Right now: " + backend + " / " + model + "."
	if !gfy.IsLocalBackend(eff) {
		s += " The board's backend is " + eff + ", so this is the fallback the loop uses " +
			"for its own runs only — nothing you start by hand changes."
	}
	return s
}
