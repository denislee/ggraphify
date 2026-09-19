package ui

import (
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The OpenCode Go gateway this board runs on 127.0.0.1:11437 is served by the
// GUI process itself (gfy.StartOpenCodeProxy). That endpoint is genuinely
// useful to other tools — a fleet-wide `graft build --deep` can be pointed at
// it through ~/.graphify/providers.json, and on this machine one has been —
// but it exists only for as long as this window does. An external consumer
// does not see "ggraphify quit"; it sees its model start refusing connections
// in the middle of a run.
//
// The minimum honest fix is this one: on quit, if the proxy is ours and has
// forwarded a request recently, say so and make it a decision. It converts a
// silent failure into a click. A headless `--serve-proxy` mode that could
// outlive the GUI is the better answer and is not this — see the README, which
// states plainly that the proxy is GUI-bound today.

// proxyIdleGrace is how long after the last forwarded request the proxy still
// counts as in use. It is generous on purpose: a semantic pass over a large
// repository can sit for minutes between chunks while a model thinks, and a
// grace period shorter than that would call a live run idle.
const proxyIdleGrace = 10 * time.Minute

// holdForProxy reports whether the close should be held back for a question.
// It returns true exactly once per quit: the dialog's own "Quit" response
// closes the window again, and by then quitting is confirmed.
func (a *App) holdForProxy() bool {
	if a.quitConfirmed {
		return false
	}
	base, owned, requests, last := gfy.OpenCodeProxyActivity()
	if !owned || requests == 0 || time.Since(last) > proxyIdleGrace {
		return false
	}
	a.confirmProxyQuit(base, requests, last)
	return true
}

// confirmProxyQuit is the dialog. It names the endpoint, the traffic and what
// stops, because "something may break" is not a thing anybody can decide on.
func (a *App) confirmProxyQuit(base string, requests int, last time.Time) {
	body := "This board is serving the OpenCode Go gateway at " + base + " itself, and it has " +
		"forwarded " + plural(requests, "request", "requests") + " — the last one " +
		shortDur(time.Since(last)) + " ago.\n\n" +
		"Quitting stops that endpoint. Anything pointed at it — another tool's " +
		"~/.graphify/providers.json entry, a `graft build --deep` running against this " +
		"machine — will start getting connection refusals mid-run, with nothing to say why.\n\n" +
		"Nothing of ggraphify's own is lost: jobs and settings are saved either way."

	dlg := adw.NewAlertDialog("The OpenCode gateway stops with this window", body)
	dlg.SetPreferWideLayout(true)
	dlg.AddResponse("stay", "Keep running")
	dlg.AddResponse("quit", "Quit anyway")
	dlg.SetResponseAppearance("quit", adw.ResponseDestructive)
	dlg.SetDefaultResponse("stay")
	dlg.SetCloseResponse("stay")
	dlg.ConnectResponse(func(resp string) {
		if resp != "quit" {
			return
		}
		a.quitConfirmed = true
		applog.Infof("quitting with the opencode-go proxy at %s in use (%d requests)", base, requests)
		a.win.Close()
	})
	dlg.Present(a.win)
}
