package ui

import (
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/store"
)

// --- the bottom panel's visibility -------------------------------------------

// applyDockSettings pushes the stored preference into the panel: which pages
// exist, which one is on top, how tall it is. It is the single writer, so the
// settings switches, the `b` key and the panel's own hide button cannot leave
// the widget and the preference disagreeing.
func (a *App) applyDockSettings() {
	if a.dock == nil {
		return
	}
	set := a.opts.Store.Settings()
	jobsOn := !set.HideJobsBar
	logOn := !set.HideLogBar
	a.dock.pageJobs.SetVisible(jobsOn)
	a.dock.pageLog.SetVisible(logOn)

	// A panel with no pages is not a panel. Switching the last page off is
	// therefore the same statement as switching the panel off, and the master
	// preference is left alone so that switching a page back on brings the
	// panel with it.
	show := !set.HideBottom && (jobsOn || logOn)
	a.dock.widget.SetVisible(show)
	if !show {
		return
	}

	want := set.BottomPage
	if want == dockPageJobs && !jobsOn {
		want = dockPageLog
	}
	if want == dockPageLog && !logOn {
		want = dockPageJobs
	}
	if want != dockPageJobs && want != dockPageLog {
		want = dockPageJobs
	}
	a.dock.stack.SetVisibleChildName(want)
	a.dock.actions.SetVisibleChildName(want)

	a.applyDockHeight(set.BottomHeight)
	a.dock.key = ""
	a.dock.reloadJobs()
	a.dock.renderLog(true)
}

// applyDockHeight positions the splitter so the panel is h pixels tall.
//
// The position has to be computed from the paned's own allocation, which is
// zero until the window has been laid out — so a first call before the first
// frame reschedules itself rather than guessing, and the guess is only used
// if the allocation never arrives.
func (a *App) applyDockHeight(h int) {
	if h < dockMinHeight {
		h = dockDefaultHeight
	}
	tries := 0
	var try func() bool
	try = func() bool {
		if a.vsplit == nil || !a.dock.widget.Visible() {
			return false
		}
		total := a.vsplit.AllocatedHeight()
		if total <= 0 {
			tries++
			return tries < 40 // ~2s, then give up and leave GTK's own position
		}
		// A tiling compositor can hand this window a very short cell, and a
		// remembered height from a tall one would leave the board itself with
		// two rows. The panel never takes more than half of what there is.
		if h > total/2 {
			h = total / 2
		}
		pos := total - h
		if pos < dockMinHeight {
			pos = total / 2
		}
		a.vsplit.SetPosition(pos)
		return false
	}
	if try() {
		timeoutAdd(50, try)
	}
}

// saveDockHeight records the height the splitter was left at.
func (a *App) saveDockHeight() {
	if a.dock == nil || a.vsplit == nil || !a.dock.widget.Visible() {
		return
	}
	h := a.dock.widget.AllocatedHeight()
	if h < dockMinHeight {
		return
	}
	set := a.opts.Store.Settings()
	if set.BottomHeight == h {
		return
	}
	set.BottomHeight = h
	a.opts.Store.SetSettings(set)
}

// setDockVisible shows or hides the whole panel, persisting the choice. `b`
// and the panel's hide button both land here.
func (a *App) setDockVisible(on bool) {
	if a.dock == nil {
		return
	}
	if on {
		a.saveDockHeight()
	}
	set := a.opts.Store.Settings()
	if on && set.HideJobsBar && set.HideLogBar {
		// Asking for the panel back when both its pages are off means the
		// jobs strip: it is the default page and the one `b` is for.
		set.HideJobsBar = false
	}
	set.HideBottom = !on
	a.opts.Store.SetSettings(set)
	a.applyDockSettings()
	a.syncDockSwitches()
}

// toggleDock is the `b` key.
func (a *App) toggleDock() {
	a.setDockVisible(a.opts.Store.Settings().HideBottom)
}

// showDockPage brings the panel up on one of its pages, switching that page
// back on if it had been turned off — asking for the log is a statement that
// the log should be on screen.
func (a *App) showDockPage(name string) {
	set := a.opts.Store.Settings()
	switch name {
	case dockPageLog:
		set.HideLogBar = false
	case dockPageJobs:
		set.HideJobsBar = false
	}
	set.HideBottom = false
	set.BottomPage = name
	a.opts.Store.SetSettings(set)
	a.applyDockSettings()
	a.syncDockSwitches()
}

// selectRepo moves the board's cursor to a repository, which is what clicking
// a job in the strip does.
func (a *App) selectRepo(path string) {
	n := a.sorted.NItems()
	for i := uint(0); i < n; i++ {
		if r := a.rowFor(a.sorted.Item(i)); r != nil && r.Path == path {
			a.sel.SetSelected(i)
			a.view.ScrollTo(i, nil, gtk.ListScrollFocus, nil)
			return
		}
	}
	a.toastf("%s is not on the board right now", board.Tilde(path))
}

// --- the diagnostics report ---------------------------------------------------

// copyDiagnostics puts the whole application log on the clipboard under a
// description of this machine's setup.
//
// This is the pane's reason to exist. A log on its own answers "what
// happened"; the header answers "to what" — which graphify, which backend,
// which output layout, whether Claude Code is wired up — and those are
// exactly the questions whoever reads the paste would otherwise have to ask
// before they could start.
func (a *App) copyDiagnostics() {
	text := a.diagnostics()
	a.win.Clipboard().SetText(text)
	a.toastf("copied %s of diagnostics — paste it into an issue or to an agent",
		board.Bytes(int64(len(text))))
}

// diagnostics renders the report. It is markdown because the likeliest
// destination is a text box that renders it, and the least likely reader
// minds a few hashes.
func (a *App) diagnostics() string {
	set := a.opts.Store.Settings()
	c := board.Summarize(a.allRows())
	queued, running := a.runner.Active()
	free, metered := a.runner.Lanes()
	outName, outBase := a.outLocation()

	var b strings.Builder
	b.WriteString("# ggraphify diagnostics\n\n")
	line := func(k, v string) {
		b.WriteString("- **")
		b.WriteString(k)
		b.WriteString(":** ")
		b.WriteString(v)
		b.WriteByte('\n')
	}
	line("generated", time.Now().Format(time.RFC3339))
	line("ggraphify", a.opts.Version)
	if a.version.Found {
		line("graphify", a.version.Number+" at "+a.version.Bin+
			" (builders target "+gfy.BuiltAgainst+")")
	} else {
		line("graphify", "NOT FOUND — "+a.version.Err)
	}
	line("platform", runtime.GOOS+"/"+runtime.GOARCH+", Go "+runtime.Version())
	line("gtk", sprintf("%d.%d.%d, libadwaita %d.%d.%d",
		gtk.GetMajorVersion(), gtk.GetMinorVersion(), gtk.GetMicroVersion(),
		adw.GetMajorVersion(), adw.GetMinorVersion(), adw.GetMicroVersion()))
	line("session", envOr("XDG_SESSION_TYPE", "?")+", desktop "+envOr("XDG_CURRENT_DESKTOP", "?")+
		", renderer "+envOr("GSK_RENDERER", "default"))
	line("state file", a.opts.Store.Path())
	line("roots", strings.Join(a.opts.Roots, ", ")+sprintf("  (depth %d)", a.opts.Depth))
	line("graph storage", describeOut(outName, outBase))
	line("backend", describeBackend(set))
	line("lanes", sprintf("%d free / %d metered", free, metered))
	line("board", sprintf("%d repos, %d graphed, %d fresh, %d stale, %d unlabeled, %d broken, %d behind HEAD",
		c.Repos, c.Graphed, c.Fresh, c.Stale, c.Raw, c.Broken, c.Behind))
	line("queue", sprintf("%d running, %d queued", running, queued))

	b.WriteString("\n## Claude Code integration\n\n```\n")
	b.WriteString(gfy.InspectClaudeIn(a.version, set.ClaudeAccount).Report())
	b.WriteString("```\n")

	if hist := a.opts.Store.History(); len(hist) > 0 {
		b.WriteString("\n## Recent jobs\n\n```\n")
		for i, h := range hist {
			if i >= 15 {
				break
			}
			b.WriteString(h.Started.Format("2006-01-02 15:04:05"))
			b.WriteString("  ")
			b.WriteString(h.Status)
			if h.Exit != 0 {
				b.WriteString(sprintf(" (%d)", h.Exit))
			}
			b.WriteString("  ")
			b.WriteString(shortDur(time.Duration(h.Duration * float64(time.Second))))
			b.WriteString("  ")
			b.WriteString(gfy.Quote(h.Argv))
			b.WriteByte('\n')
		}
		b.WriteString("```\n")
	}

	b.WriteString("\n## Application log\n\n```\n")
	b.WriteString(applog.Default.Text())
	b.WriteString("```\n")
	return b.String()
}

func describeOut(name, base string) string {
	if strings.TrimSpace(base) != "" {
		return "one directory: " + base
	}
	if strings.TrimSpace(name) != "" {
		return name + "/ inside each checkout"
	}
	return "graphify's default inside each checkout"
}

// describeBackend names the backend a metered job would use, resolved the way
// the confirm dialog resolves it, plus whether it has the credential it needs.
func describeBackend(set store.Settings) string {
	name := set.Backend
	eff := gfy.EffectiveBackend(set.Backend)
	if name == "" {
		name = "auto-detect"
		if eff != "" {
			name += " → " + eff
		}
	}
	if set.Model != "" {
		name += ", model " + set.Model
	}
	if eff == gfy.ClaudeCLIBackend {
		acc := gfy.ClaudeAccountFor(set.ClaudeAccount)
		name += ", account " + acc.Name + " (" + acc.Dir + ")"
	}
	if ok, why := gfy.BackendReady(eff); !ok {
		name += " — NOT READY: " + why
	}
	return name
}

// envOr is one environment variable with a stand-in for "unset", so the
// report never has a blank where a fact should be.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// firstNonEmpty is the first of its arguments with something in it.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
