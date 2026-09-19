package ui

import (
	"context"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The OpenCode Go group is the settings for a backend graphify does not ship.
//
// Everything else in "LLM defaults" is a value passed to a backend graphify
// already knows; this one is the backend. The name only means something
// because the board registers a provider under it in
// ~/.graphify/providers.json, and that entry carries the endpoint, the key
// variable, the default model and — the reason it is rewritten rather than
// written once — the price pair graphify estimates a run's cost from.
//
// So the model picker here is not a convenience over the free-text Model
// field above. It is the thing that keeps the estimate honest: choosing a
// model moves the price pair with it, which typing an id into a box shared
// with every other backend could not do.
func (a *App) settingsOpenCode() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("OpenCode Go")
	const blurb = "A $10/month subscription to a curated set of open coding models, " +
		"reached through one OpenAI-compatible endpoint. graphify has no backend by this " +
		"name — the board registers it as a custom provider, which is what makes " +
		"`--backend " + gfy.OpenCodeBackend + "` work at all."

	status := adw.NewActionRow()
	status.SetTitle("Status")
	status.SetSubtitleLines(0)

	model := adw.NewComboRow()
	model.SetTitle("Model")
	model.SetSubtitleLines(0)
	// Adw's default list item is a single ellipsized line, and every entry
	// here is one long line by design — id, then name, then two prices, then
	// the context window. Ellipsized, all of it after the id is "…", which is
	// exactly the half that makes one model a better choice than another. A
	// list factory whose label neither ellipsizes nor shortens lets the popup
	// take the width its widest entry needs; the row itself keeps Adw's own
	// display, so the selected value still shortens instead of stretching the
	// preferences dialog.
	model.SetListFactory(&wideTextFactory().ListItemFactory)

	// ids[i] is the model id row i stands for, so a dropdown reordered by a
	// catalogue refresh cannot silently change what the stored setting means.
	var ids []string
	filling := false

	refreshStatus := func() {
		_, why := gfy.OpenCodeReady(a.opts.Store.Settings().OpenCodeModel)
		status.SetSubtitle(escapeMarkup(why))
	}

	// Asking the gateway for one token is the only way to find out whether a
	// model it lists will actually answer this account: the Muse Spark
	// Contributor models are served to some regions and return 500 to the
	// rest, and nothing readable distinguishes them. Doing it as the model is
	// chosen puts the answer in front of the person choosing, rather than in
	// a failed run twenty minutes later.
	verify := func(model string) {
		if !gfy.HasOpenCodeKey() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			ok, detail := gfy.VerifyOpenCodeModel(ctx, model)
			idle(func() {
				refreshStatus()
				if !ok && detail != "" {
					applog.Infof("opencode-go: %s", detail)
					a.toastf("%s", detail)
				}
			})
		}()
	}

	fill := func(models []gfy.OpenCodeModel) {
		set := a.opts.Store.Settings()
		want := set.OpenCodeModel
		if want == "" {
			want = gfy.DefaultOpenCodeModel
		}

		ids = ids[:0]
		names := make([]string, 0, len(models)+1)
		for _, m := range models {
			ids = append(ids, m.ID)
			label := m.Label()
			if m.ID == gfy.DefaultOpenCodeModel {
				label += " · the board's default"
			}
			names = append(names, label)
		}
		// A model chosen before this build's catalogue knew it — or after the
		// plan dropped it — stays selectable rather than being silently
		// replaced by whatever happens to be first in the list.
		if indexOf(ids, want) < 0 {
			ids = append(ids, want)
			names = append(names, want+" — not in the catalogue; the gateway decides")
		}

		filling = true
		model.SetModel(gtk.NewStringList(names))
		if i := indexOf(ids, want); i >= 0 {
			model.SetSelected(uint(i))
		}
		filling = false

		model.SetSubtitle(escapeMarkup("Which model an " + gfy.OpenCodeBackend +
			" extraction runs. Sorted cheapest first: an extraction is a schema-constrained " +
			"JSON job over one file at a time, which is the shape a small fast model does well " +
			"and a frontier model only does expensively. The choice is passed as --model and " +
			"is written into the provider entry with its own prices, so graphify's cost " +
			"estimate is this model's rather than the last one's. A repository that overrides " +
			"the model in its own detail page still wins over this."))
		refreshStatus()
	}

	model.NotifyProperty("selected", func() {
		if filling {
			return
		}
		i := int(model.Selected())
		if i < 0 || i >= len(ids) {
			return
		}
		s := a.opts.Store.Settings()
		s.OpenCodeModel = ids[i]
		a.opts.Store.SetSettings(s)
		refreshStatus()
		verify(ids[i])
		a.checkBackend()
	})

	// The catalogue is baked in, so the dropdown is populated before any
	// network call and stays populated on a machine that cannot reach
	// models.dev. The refresh is what keeps it current — the plan's model list
	// changes monthly — and it is the same fetch the button repeats by hand.
	reload := func(announce bool) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			models, err := gfy.RefreshOpenCodeCatalog(ctx)
			idle(func() {
				fill(models)
				if err != nil {
					applog.Infof("opencode-go: keeping the built-in model catalogue: %v", err)
					if announce {
						a.toastf("could not refresh the model list: %v", err)
					}
					return
				}
				if announce {
					a.toastf("%s", plural(len(models), "model", "models"))
				}
			})
		}()
	}

	refresh := gtk.NewButtonWithLabel("Refresh models")
	refresh.AddCSSClass("flat")
	refresh.SetVAlign(gtk.AlignCenter)
	refresh.SetTooltipText("Re-read the plan's model list and prices from " + gfy.ModelsDevURL + ".")
	refresh.ConnectClicked(func() { reload(true) })
	status.AddSuffix(refresh)

	g.Add(status)
	g.Add(model)

	caveat := adw.NewActionRow()
	caveat.SetTitle("How jobs reach the gateway")
	caveat.SetSubtitleLines(0)
	caveat.SetSubtitle(escapeMarkup(gfy.OpenCodeCaveat))
	g.Add(caveat)

	// Which of the two sentences the group opens with depends on the Backend
	// picker two groups up, and it changes as that picker changes: a settings
	// page that describes a backend as selectable while it IS selected is how
	// a user concludes the dropdown they are looking at does nothing.
	a.ocApply = func(backend string) {
		if backend == gfy.OpenCodeBackend {
			g.SetDescription(blurb + " It is the selected backend: the model below is what " +
				"every metered command runs against.")
		} else {
			g.SetDescription(blurb + " Select it under Backend, above, to use it.")
		}
		refreshStatus()
	}
	a.ocApply(gfy.EffectiveBackend(a.opts.Store.Settings().Backend))

	a.ocGroup, a.ocModelRow = g, model
	fill(gfy.OpenCodeCatalog())
	reload(false)
	if m := a.opts.Store.Settings().OpenCodeModel; m != "" {
		verify(m)
	} else {
		verify(gfy.DefaultOpenCodeModel)
	}
	return g
}

// wideTextFactory renders a GtkStringList entry as a full-width, non-truncated
// label: a dropdown whose items are sentences rather than words needs the
// popup sized to the text instead of the text clipped to the row.
//
// The label wraps rather than growing without bound, because a model id this
// board has never seen could be arbitrarily long and a popover wider than the
// monitor is its own kind of unreadable.
func wideTextFactory() *gtk.SignalListItemFactory {
	f := gtk.NewSignalListItemFactory()
	// GTK recycles list items, so the label is kept per item rather than
	// looked up from the item on bind — the same reason the board's column
	// factories keep a map.
	labels := map[uintptr]*gtk.Label{}
	f.ConnectSetup(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := gtk.NewLabel("")
		l.SetXAlign(0)
		l.SetWrap(true)
		l.SetWrapMode(2) // PANGO_WRAP_WORD_CHAR
		l.SetMaxWidthChars(72)
		li.SetChild(l)
		labels[li.Native()] = l
	})
	f.ConnectBind(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := labels[li.Native()]
		if l == nil {
			return
		}
		if so, ok := li.Item().Cast().(*gtk.StringObject); ok {
			l.SetText(so.String())
		}
	})
	f.ConnectTeardown(func(obj *coreglib.Object) {
		if li := asCell(obj); li != nil {
			delete(labels, li.Native())
		}
	})
	return f
}
