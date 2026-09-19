package ui

import (
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The backend banner answers a failure mode the job-time gate cannot.
//
// The gate itself is correct: BackendReady is consulted before every metered
// job and the job refuses, with the confirm dialog explaining exactly why. But
// a refusal only appears at the moment somebody tries to spend money. A board
// mostly driving the free lane can sit on a backend that has been dead for
// weeks — a pinned `ollama` with no server, an OpenCode key that expired — and
// the only symptom is indirect and easy to misread: every graph stays at the
// AST tier, communities keep placeholder names like `testing.T`, and nothing
// anywhere says the semantic pass has never once run.
//
// So the readiness question is asked on startup and again whenever the setting
// moves, and a dead pin is said out loud. What the banner must NOT do is fix
// itself: the backend is who pays for an extraction, and an application that
// silently re-pointed that at another account — or at a subscription with a
// monthly allowance — would be making a spend decision on the user's behalf.
// It offers; one click applies.

// backendRecheck is how often a showing banner re-asks. Slow on purpose: what
// it is waiting for is a human starting a server or exporting a key in another
// window, which is not a thing to poll a port for every second.
const backendRecheck = 30 * time.Second

// checkBackend probes the configured backend off the main thread and shows or
// hides the banner accordingly.
func (a *App) checkBackend() {
	if a.backendBanner == nil || a.backendBusy {
		return
	}
	a.backendBusy = true
	a.backendAt = time.Now()
	set := a.opts.Store.Settings()
	backend, model := set.Backend, set.Model
	if gfy.EffectiveBackend(backend) == gfy.OpenCodeBackend {
		// This backend's model lives in its own setting, and it is half of
		// what its readiness means — a model the plan will not serve is as
		// broken a pin as a missing key.
		model = set.OpenCodeModel
	}
	go func() {
		eff, ready, why := gfy.BackendVerdict(backend, model)
		var alts []gfy.BackendChoice
		if !ready {
			alts = gfy.ReadyBackends(eff, model)
		}
		coreglib.IdleAdd(func() { a.showBackendVerdict(eff, ready, why, alts) })
	}()
}

// showBackendVerdict paints the result. Main thread only.
func (a *App) showBackendVerdict(eff string, ready bool, why string, alts []gfy.BackendChoice) {
	if a.backendBanner == nil {
		a.backendBusy = false
		return
	}
	a.backendBusy = false
	a.backendReady, a.backendWhy, a.backendAlts = ready, why, alts
	if ready {
		a.backendBanner.SetRevealed(false)
		return
	}
	name := eff
	if name == "" {
		name = "auto-detect"
	}
	applog.Errorf("the %s backend is not ready: %s", name, why)
	a.backendBanner.SetTitle("Metered work cannot run: the " + name + " backend is not ready — " +
		ellipsize(why, 120))
	switch {
	case len(alts) > 0:
		a.backendBanner.SetButtonLabel("Choose a backend")
	default:
		a.backendBanner.SetButtonLabel("Details")
	}
	a.backendBanner.SetRevealed(true)
}

// backendDialog is what the banner's button opens: the verdict in full, and
// every backend this machine could run instead, each with the reason it would
// work. Selecting one writes the setting and nothing else — no job is started,
// because the dialog's whole point is that this is a spend decision.
func (a *App) backendDialog() {
	why := a.backendWhy
	if why == "" {
		why = "this backend has no credential, server or model that a metered job could use."
	}
	body := why
	if len(a.backendAlts) == 0 {
		body += "\n\nNothing else on this machine is ready either: no API key is exported, " +
			"no Claude Code CLI was found, no OpenCode key is set and no local model server " +
			"answered. Until one of those is true, every metered command — extraction's " +
			"semantic pass, community naming — will refuse to run, and graphs will stay at " +
			"the free AST tier."
	}

	dlg := adw.NewAlertDialog("The configured backend is not ready", body)
	dlg.SetPreferWideLayout(true)

	if len(a.backendAlts) > 0 {
		list := gtk.NewBox(gtk.OrientationVertical, 6)
		group := adw.NewPreferencesGroup()
		group.SetTitle("Ready on this machine")
		group.SetDescription("Picking one changes the backend setting. It decides who pays for " +
			"an extraction, so nothing is switched until you say so, and no job is started here.")
		for _, alt := range a.backendAlts {
			c := alt
			row := adw.NewActionRow()
			row.SetTitle(c.Label)
			row.SetSubtitle(c.Why)
			row.SetSubtitleLines(0)
			btn := gtk.NewButtonWithLabel("Use this")
			btn.AddCSSClass("suggested-action")
			btn.SetVAlign(gtk.AlignCenter)
			btn.ConnectClicked(func() {
				a.useBackend(c)
				dlg.Close()
			})
			row.AddSuffix(btn)
			row.SetActivatableWidget(btn)
			group.Add(row)
		}
		list.Append(group)
		dlg.SetExtraChild(list)
	}

	dlg.AddResponse("close", "Close")
	dlg.AddResponse("settings", "Open settings")
	dlg.SetDefaultResponse("close")
	dlg.SetCloseResponse("close")
	dlg.ConnectResponse(func(resp string) {
		if resp == "settings" {
			a.showSettings()
		}
	})
	dlg.Present(a.win)
}

// useBackend applies a chosen alternative: the setting, a line in the log
// saying what moved and why, and a re-probe so the banner reflects the new
// choice rather than the old verdict.
func (a *App) useBackend(c gfy.BackendChoice) {
	s := a.opts.Store.Settings()
	was := s.Backend
	s.Backend = c.Name
	a.opts.Store.SetSettings(s)
	name := c.Name
	if name == "" {
		name = "auto-detect"
	}
	from := was
	if from == "" {
		from = "auto-detect"
	}
	applog.Infof("backend changed from %s to %s (%s)", from, name, strings.TrimSpace(c.Why))
	a.toastf("backend is now %s", name)
	a.checkBackend()
	a.refreshStatus()
}
