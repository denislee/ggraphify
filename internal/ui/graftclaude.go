package ui

import (
	"context"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The graft integration group, directly below the graphify one and built the
// same way: read what the installer wrote, say which parts are wrong, and
// offer the one command that rewrites them.
//
// It is a second group rather than more rows in the first because the two
// integrations fail independently and are repaired by different commands —
// `graphify install --platform claude` and `graft init` — and a single group
// with one Fix button could only ever run one of them.
//
// What it checks is the USER-level wiring: the shim, the four hook entries and
// the MCP registration that apply to every project Claude Code opens. The
// repo-level half of graft's wiring belongs to a repository, not to this
// machine, and the board says which repositories have an index in the Graft
// column instead.

func (a *App) settingsGraft() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("graft integration")
	g.SetDescription("Whether the Claude Code CLI on this machine is configured to use graft: " +
		"the hooks shim, the SessionStart / UserPromptSubmit / PostToolUse / Stop entries that " +
		"run it, and the graft MCP server. All of it is written by `graft init`; ggraphify only " +
		"reads it and offers to re-run that.")

	summary := adw.NewActionRow()
	summary.SetTitle("Status")
	summary.SetSubtitle("Not checked yet.")
	summary.SetSubtitleLines(0)

	check := gtk.NewButtonWithLabel("Check")
	check.AddCSSClass("flat")
	check.SetVAlign(gtk.AlignCenter)
	check.SetTooltipText("Re-read the shim, the hook entries and the MCP registration.")
	check.ConnectClicked(func() { a.checkGraftSetup(true) })
	summary.AddSuffix(check)

	fix := gtk.NewButtonWithLabel("Fix")
	fix.AddCSSClass("suggested-action")
	fix.SetVAlign(gtk.AlignCenter)
	fix.SetSensitive(false)
	fix.ConnectClicked(func() { a.fixGraftSetup() })
	summary.AddSuffix(fix)

	g.Add(summary)

	a.graftGroup = g
	a.graftSummary = summary
	a.graftCheckBtn = check
	a.graftFixBtn = fix

	a.checkGraftSetup(false)
	return g
}

// checkGraftSetup runs the inspection off the main thread — it stats a handful
// of files and parses two JSON documents — and folds the result back in.
// announce toasts the verdict, for the button; the check at build time stays
// quiet, exactly as the graphify group's does.
func (a *App) checkGraftSetup(announce bool) {
	if a.graftGroup == nil {
		return
	}
	sel := a.opts.Store.Settings().ClaudeAccount
	go func() {
		// The version probe is cached for the process's lifetime, so this is
		// one subprocess on the first check and none after it — except from
		// the button, which is the one place re-asking is the point.
		v := gfy.GraftProbe(context.TODO())
		if announce {
			v = gfy.GraftReprobe(context.TODO())
		}
		s := gfy.InspectGraft(v, sel)
		idle(func() {
			a.graftVersion = v
			a.graftSetup = s
			a.fillGraftSetup(s)
			if announce {
				applog.Infof("checked Claude Code ↔ graft:\n%s", s.Report())
				a.toast(s.Summary())
			}
		})
	}()
}

// fillGraftSetup rebuilds the per-check rows. Main thread.
func (a *App) fillGraftSetup(s gfy.GraftSetup) {
	if a.graftGroup == nil {
		return
	}
	for _, r := range a.graftSetupRows {
		a.graftGroup.Remove(r)
	}
	a.graftSetupRows = nil

	for _, c := range s.Checks {
		row := adw.NewActionRow()
		row.SetTitle(c.Name)
		row.SetSubtitle(escapeMarkup(c.Detail))
		row.SetSubtitleLines(0)

		mark := gtk.NewLabel(c.State.Glyph())
		mark.AddCSSClass(checkClass(c.State))
		mark.SetTooltipText(c.State.String())
		row.AddPrefix(mark)
		if c.Fixable && c.State != gfy.CheckOK {
			hint := gtk.NewLabel("fixable")
			hint.AddCSSClass("dim-label")
			row.AddSuffix(hint)
		}
		a.graftGroup.Add(row)
		a.graftSetupRows = append(a.graftSetupRows, row)
	}

	a.graftSummary.SetSubtitle(escapeMarkup(s.Summary() + "  ·  " + s.Dir))
	a.graftFixBtn.SetSensitive(s.Fixable())
	switch {
	case s.Fixable():
		a.graftFixBtn.SetTooltipText("Run `graft init`, which rewrites the shim, the hook " +
			"entries and the MCP registration.")
	case s.OK():
		a.graftFixBtn.SetTooltipText("Nothing to fix — everything `graft init` writes is in place.")
	case !s.InitWrites:
		a.graftFixBtn.SetTooltipText("`graft init` ignores " + gfy.ClaudeConfigDirVar +
			" and would write " + s.InitDir + ", not " + s.Dir +
			" — it cannot repair the account selected in settings.")
	default:
		a.graftFixBtn.SetTooltipText("What is wrong here is not something `graft init` can fix: " +
			"install graft first, or repair the JSON it declined to touch.")
	}
}

// fixGraftSetup confirms and then runs `graft init`.
//
// The confirm is not a formality. `graft init` has no user-level-only mode:
// the writes into ~/.claude that this group is about are made alongside writes
// into whichever repository it is pointed at. So the dialog names that
// repository first, then lists both sets of files — anything less would be a
// button that quietly wired a checkout the user was not thinking about.
func (a *App) fixGraftSetup() {
	r := a.current()
	if r == nil {
		a.toast("select a repository first — `graft init` writes this machine's wiring " +
			"alongside that repository's own")
		return
	}
	s := a.graftSetup

	// The paths graft init actually writes, not the ones this report read.
	// They differ in two ordinary situations — an account other than the
	// default is selected, and a settings.json that names another account's
	// shim by absolute path — and a dialog that listed the inspected paths
	// would be naming files the command is not going to touch.
	initShim, initSettings, initMCP := gfy.GraftInitTargets()

	body := "graft will write, for every project on this machine:\n\n" +
		"  • " + initShim + "\n" +
		"  • the graft hook entries in " + initSettings + "\n" +
		"  • mcpServers." + gfy.GraftMCPKey + " in " + initMCP + "\n\n" +
		"and, inside " + r.Name + " (" + r.Path + "):\n\n" +
		"  • .claude/settings.json, .claude/helpers/ and .claude/skills/graft/SKILL.md\n" +
		"  • .mcp.json\n\n" +
		"`graft init` has no machine-only mode — the wiring above is written alongside one " +
		"repository's, and " + r.Name + " is the row selected on the board. Existing files are " +
		"merged, not replaced: other MCP servers and other hooks are preserved. No index is " +
		"built (--no-build) and no other agent's files are touched (--no-agents).\n\n"

	// Only reachable defensively — the Fix button is insensitive when graft
	// init cannot write the inspected account — but a dialog that is one
	// click from writing the wrong directory says so rather than relying on
	// the button's state to have been right.
	if !s.InitWrites {
		body += "⚠ Settings has " + s.Account.Name + " (" + s.Dir + ") selected, and `graft init` " +
			"ignores " + gfy.ClaudeConfigDirVar + ": the files above are the ones it will write, " +
			"and that account is not one of them.\n\n"
	}
	body += gfy.GraftFixArgv(r.Path)

	dlg := adw.NewAlertDialog("Configure Claude Code for graft", body)
	dlg.SetPreferWideLayout(true)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("init", "Run graft init")
	dlg.SetResponseAppearance("init", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")

	repo := r.Path
	name := r.Name
	dlg.ConnectResponse(func(resp string) {
		if resp != "init" {
			return
		}
		p := a.params(*r)
		if _, err := a.runner.SubmitCmd("graft-init", repo,
			gfy.Title("graft-init")+" · "+name, p, a.opts.Store.Overlay(repo)); err != nil {
			applog.Errorf("could not start graft init: %v", err)
			a.toastf("%v", err)
			return
		}
		a.toast("wiring Claude Code for graft…")
	})
	dlg.Present(a.win)
}
