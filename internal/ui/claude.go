package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The Claude Code integration group answers a question that used to have no
// answer anywhere in this application: is the Claude Code on this machine
// actually set up to use graphify?
//
// It is a fair question to have, because the wiring is invisible when it is
// broken. A missing skill, or a skill from a graphify three releases old, or
// a CLAUDE.md that lost its pointer line during a hand edit, all present the
// same way: an agent that simply never mentions the graph. Nothing errors.
//
// So the group reads the artefacts `graphify install --platform claude`
// writes, says which of them are wrong, and offers the one command that
// rewrites them. The check is a filesystem read and costs nothing; the fix is
// an ordinary supervised job, confirmed first and named in full, like every
// other mutation the board performs.

func (a *App) settingsClaude() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Claude Code integration")
	g.SetDescription("Whether the Claude Code on this machine can use graphify: the agent " +
		"skill, its reference pages, and the pointer in your user-level CLAUDE.md that makes " +
		"an agent reach for it. All of it is written by `graphify install --platform claude`; " +
		"ggraphify only reads it and offers to re-run that.")

	g.Add(a.claudeAccountRow())

	summary := adw.NewActionRow()
	summary.SetTitle("Status")
	summary.SetSubtitle("Not checked yet.")
	summary.SetSubtitleLines(0)

	check := gtk.NewButtonWithLabel("Check")
	check.AddCSSClass("flat")
	check.SetVAlign(gtk.AlignCenter)
	check.SetTooltipText("Re-read the skill, its version stamp and your CLAUDE.md.")
	check.ConnectClicked(func() { a.checkClaude(true) })
	summary.AddSuffix(check)

	fix := gtk.NewButtonWithLabel("Fix")
	fix.AddCSSClass("suggested-action")
	fix.SetVAlign(gtk.AlignCenter)
	fix.SetSensitive(false)
	fix.SetTooltipText(gfy.FixArgv())
	fix.ConnectClicked(func() { a.fixClaude() })
	summary.AddSuffix(fix)

	g.Add(summary)

	a.claudeGroup = g
	a.claudeSummary = summary
	a.claudeCheckBtn = check
	a.claudeFixBtn = fix

	// Checked as the page is built, so the group is already answered by the
	// time it is scrolled to rather than being a button that says "ask me".
	a.checkClaude(false)
	return g
}

// claudeAccountRow is the account picker: which of this machine's Claude Code
// logins the board uses.
//
// It exists because a machine can hold several. Claude Code keeps one login
// per configuration directory and switches between them through
// $CLAUDE_CONFIG_DIR, so ~/.claude and ~/.claude-work are two accounts and
// there is no other handle on them. The choice is worth surfacing rather than
// inheriting silently, for two reasons that pull in the same direction: a
// claude-cli extraction is billed to the chosen login's plan, and `graphify
// install --platform claude` writes the skill into the chosen login's
// directory. Picking here moves both together.
//
// The first entry keeps the old behaviour — inherit whatever the environment
// says — so a user who already exports CLAUDE_CONFIG_DIR in their shell
// profile is not overridden by a setting they never touched.
func (a *App) claudeAccountRow() *adw.ComboRow {
	set := a.opts.Store.Settings()
	accounts := gfy.ClaudeAccounts(set.ClaudeAccount)

	// Index 0 is "inherit"; everything after it is an account directory, so
	// dirs[i] is the value the setting takes for row i.
	dirs := make([]string, 0, len(accounts)+1)
	names := make([]string, 0, len(accounts)+1)
	inherit := "Inherit the environment"
	if d := gfy.ClaudeDir(); d != "" {
		inherit += " (" + gfy.Tilde(d) + ")"
	}
	dirs = append(dirs, "")
	names = append(names, inherit)
	for _, acc := range accounts {
		dirs = append(dirs, acc.Dir)
		names = append(names, acc.Label())
	}

	row := adw.NewComboRow()
	row.SetTitle("Account")
	row.SetSubtitle("Which Claude Code login jobs run as: the plan a claude-cli extraction " +
		"is billed to, and the ~/.claude* the skill is installed into. Passed as " +
		gfy.ClaudeConfigDirVar + " on every job.")
	row.SetSubtitleLines(0)
	row.SetModel(gtk.NewStringList(names))
	for i, d := range dirs {
		if i > 0 && d == set.ClaudeAccount {
			row.SetSelected(uint(i))
		}
	}
	row.NotifyProperty("selected", func() {
		i := int(row.Selected())
		if i < 0 || i >= len(dirs) {
			return
		}
		s := a.opts.Store.Settings()
		if s.ClaudeAccount == dirs[i] {
			return
		}
		s.ClaudeAccount = dirs[i]
		a.opts.Store.SetSettings(s)
		applog.Infof("Claude Code account set to %s", accountLabel(dirs[i]))
		a.toast("Claude Code account: " + accountLabel(dirs[i]))
		// Everything below this row described the previous account, and the
		// running jobs keep the one they were launched with; re-checking is
		// what stops the group from answering about a directory nobody uses.
		a.checkClaude(false)
	})
	a.claudeAccountCombo = row
	return row
}

// accountLabel names a selection for a log line or a toast.
func accountLabel(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "inherited from the environment (" + gfy.Tilde(gfy.ClaudeDir()) + ")"
	}
	return gfy.ClaudeAccountFor(dir).Name + " (" + gfy.Tilde(dir) + ")"
}

// checkClaude runs the inspection off the main thread — it stats a handful of
// files and reads one CLAUDE.md, which is fast but is still I/O — and folds
// the result back in. announce toasts the verdict, for the button; the
// automatic check at build time stays quiet.
func (a *App) checkClaude(announce bool) {
	if a.claudeGroup == nil {
		return
	}
	v := a.version
	sel := a.opts.Store.Settings().ClaudeAccount
	go func() {
		s := gfy.InspectClaudeIn(v, sel)
		idle(func() {
			a.claudeSetup = s
			a.fillClaude(s)
			if announce {
				applog.Infof("checked Claude Code integration:\n%s", s.Report())
				a.toast(s.Summary())
			}
		})
	}()
}

// fillClaude rebuilds the per-check rows. Main thread.
func (a *App) fillClaude(s gfy.ClaudeSetup) {
	if a.claudeGroup == nil {
		return
	}
	for _, r := range a.claudeRows {
		a.claudeGroup.Remove(r)
	}
	a.claudeRows = nil

	for _, c := range s.Checks {
		row := adw.NewActionRow()
		row.SetTitle(c.Name)
		row.SetSubtitle(c.Detail)
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
		a.claudeGroup.Add(row)
		a.claudeRows = append(a.claudeRows, row)
	}

	a.claudeSummary.SetSubtitle(s.Summary() + "  ·  " + s.Dir)
	a.claudeFixBtn.SetSensitive(s.Fixable())
	if s.Fixable() {
		a.claudeFixBtn.SetTooltipText("Re-run " + gfy.FixArgv() +
			", which rewrites the skill, its references and the CLAUDE.md pointer.")
	} else if s.OK() {
		a.claudeFixBtn.SetTooltipText("Nothing to fix — everything the installer writes is in place.")
	} else {
		a.claudeFixBtn.SetTooltipText("What is wrong here is not something the installer can fix: " +
			"install graphify, or the Claude Code CLI, first.")
	}
}

func checkClass(st gfy.CheckState) string {
	switch st {
	case gfy.CheckWarn:
		return "check-warn"
	case gfy.CheckBad:
		return "check-bad"
	default:
		return "check-ok"
	}
}

// fixClaude confirms and then runs the installer.
//
// It is confirmed because it writes outside any repository — into your
// ~/.claude — which is a thing ggraphify does exactly here and nowhere else,
// and the dialog says so in those words rather than calling it "fixing".
func (a *App) fixClaude() {
	s := a.claudeSetup
	body := "graphify will write:\n\n" +
		"  • " + s.SkillDir + "/SKILL.md and its references/\n" +
		"  • the graphify section of " + s.Dir + "/CLAUDE.md\n\n" +
		"Existing files are overwritten with this graphify's own copies. Nothing " +
		"inside any repository is touched.\n\n" + gfy.FixArgv()

	dlg := adw.NewAlertDialog("Set up Claude Code for graphify", body)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("install", "Run the installer")
	dlg.SetResponseAppearance("install", adw.ResponseSuggested)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "install" {
			a.installClaudeSkill("Set up Claude Code")
		}
	})
	dlg.Present(a.win)
}

// installClaudeSkill submits `graphify install --platform claude`. Both the
// settings group's Fix button and the version banner's "Update agent skills"
// come through here, so there is one command and one place it is built.
func (a *App) installClaudeSkill(label string) {
	// No repository: this is a user-level install, and passing the selected
	// checkout would make graphify's --project heuristics the difference
	// between two invocations that look identical in the log.
	p := gfy.Params{Platform: "claude", ClaudeDir: a.opts.Store.Settings().ClaudeAccount}
	if _, err := a.runner.SubmitCmd("install", "", label, p, nil); err != nil {
		applog.Errorf("could not start the Claude Code installer: %v", err)
		a.toastf("%v", err)
		return
	}
	a.toast(label + "…")
}

// onSkillInstalled is called when an install job succeeds: the version probe's
// skew warning and every check in the group above are now stale, so both are
// redone rather than left saying what was true a minute ago.
func (a *App) onSkillInstalled() {
	go func() {
		v := gfy.Reprobe(nil)
		idle(func() {
			a.version = v
			a.refreshStatus()
			if v.SkillSkew == "" && a.banner != nil {
				a.banner.SetRevealed(false)
			}
			a.checkClaude(false)
		})
	}()
}
