package ui

import (
	"context"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
)

// The OpenCode group is the settings for the two backends graphify does not
// ship — the Go subscription and the pay-as-you-go Zen gateway. It shows one
// at a time: whichever the Backend picker has selected, because that picker is
// the control that decides which bill a run lands on.
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

	// Which plan this group is configuring. It follows the Backend picker
	// rather than carrying a plan switch of its own: choosing between Go and
	// Zen is choosing which bill a run lands on, and two controls that both
	// decide that is how a board ends up disagreeing with itself about where
	// the money went. Before either is selected it shows Go, which is what it
	// showed when Go was the only plan there was.
	plan := gfy.OpenCodePlanFor(gfy.EffectiveBackend(a.opts.Store.Settings().Backend))
	if !gfy.IsOpenCodeBackend(gfy.EffectiveBackend(a.opts.Store.Settings().Backend)) {
		plan = gfy.OpenCodePlanFor(gfy.OpenCodeBackend)
	}

	status := adw.NewActionRow()
	status.SetTitle("Status")
	status.SetSubtitleLines(0)

	model := adw.NewComboRow()
	model.SetTitle("Model")
	model.SetSubtitleLines(0)

	// ids[i] is the model id row i stands for, so a dropdown reordered by a
	// catalogue refresh cannot silently change what the stored setting means.
	var ids []string
	filling := false

	// What the gateway said when asked about each model, by id. A model is
	// "offline" here only when the gateway itself refused it — never when the
	// network did, which says nothing about the model; see
	// gfy.VerifyOpenCodeModel.
	type verdict struct {
		known  bool
		ok     bool
		detail string
	}
	verdicts := map[string]verdict{}
	sweeping := false
	var lastPaint time.Time

	// Adw's default list item is a single ellipsized line, and every entry
	// here is one long line by design — id, then name, then two prices, then
	// the context window, and now whether the gateway answers for it at all.
	// Ellipsized, all of it after the id is "…", which is exactly the half
	// that makes one model a better choice than another. A list factory whose
	// label neither ellipsizes nor shortens lets the popup take the width its
	// widest entry needs, and a wrapping value label does the same for the row
	// — the chosen model's price and context window are as worth reading after
	// the choice as before it, and wrapping grows the row by a line rather
	// than stretching the preferences dialog by a column.
	//
	// The factory is also what fades a model the gateway refuses. It is faded
	// rather than removed: a model that is listed and refused is a fact about
	// this account and region, and a list that silently dropped it would look
	// like a list that had never heard of it — which is a different problem
	// with a different fix. It stays selectable for the same reason, and the
	// label says so in words as well as in colour, because a fade alone is
	// not a thing a screen reader or a monochrome display conveys.
	model.SetListFactory(&wideTextFactory(func(pos uint) bool {
		if int(pos) >= len(ids) {
			return true
		}
		v := verdicts[ids[pos]]
		return !v.known || v.ok
	}).ListItemFactory)
	model.SetFactory(&valueFactory().ListItemFactory)

	refreshStatus := func() {
		_, why := gfy.OpenCodeReady(plan.Backend, a.opts.Store.Settings().OpenCodeModelFor(plan.Backend))
		status.SetSubtitle(escapeMarkup(why))
	}

	// Asking the gateway for one token is the only way to find out whether a
	// model it lists will actually answer this account: the Muse Spark
	// Contributor models are served to some regions and return 500 to the
	// rest, and nothing readable distinguishes them. Doing it as the model is
	// chosen puts the answer in front of the person choosing, rather than in
	// a failed run twenty minutes later.
	verify := func(id string) {
		if !gfy.HasOpenCodeKey() || id == "" {
			return
		}
		backend := plan.Backend
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			ok, detail := gfy.VerifyOpenCodeModel(ctx, backend, id)
			idle(func() {
				if plan.Backend != backend {
					return
				}
				verdicts[id] = verdict{known: true, ok: ok, detail: detail}
				refreshStatus()
				if !ok && detail != "" {
					applog.Infof("%s: %s", backend, detail)
					a.toastf("%s", detail)
				}
			})
		}()
	}

	// avail is the sweep's own row: how much of the catalogue has been asked
	// about, and how much of it answers.
	avail := adw.NewActionRow()
	avail.SetTitle("Availability")
	avail.SetSubtitleLines(0)

	availSummary := func() string {
		total := len(ids)
		asked, live := 0, 0
		for _, id := range ids {
			if v, hit := verdicts[id]; hit && v.known {
				asked++
				if v.ok {
					live++
				}
			}
		}
		switch {
		case !gfy.HasOpenCodeKey():
			return "Export " + gfy.OpenCodeKeyVar + " and the board can ask the gateway which " +
				"of these models actually answer for this account. Until then every model is " +
				"shown as listed, which is all " + plan.Name + "'s /models can say."
		case total == 0:
			return "No models to check yet — refresh " + plan.Name + "'s list first."
		case sweeping:
			return "Asking the gateway about each model, " + gfy.Itoa(asked) + " of " +
				gfy.Itoa(total) + " answered so far. Eight tokens per model."
		case asked == 0:
			return "The gateway lists these models; whether it will ANSWER for this account " +
				"and region is a different question, and only a request can settle it. " +
				"Checking costs eight tokens per model."
		default:
			return gfy.Itoa(live) + " of " + gfy.Itoa(asked) + " checked models answered. " +
				"A faded entry was refused by the gateway itself — it stays selectable, " +
				"because that verdict is about this account rather than about the model."
		}
	}

	fill := func(models []gfy.OpenCodeModel) {
		set := a.opts.Store.Settings()
		want := set.OpenCodeModelFor(plan.Backend)
		if want == "" {
			want = gfy.DefaultModelFor(plan.Backend)
		}
		def := gfy.DefaultModelFor(plan.Backend)

		ids = ids[:0]
		names := make([]string, 0, len(models)+1)
		for _, m := range models {
			ids = append(ids, m.ID)
			label := m.Label()
			if m.ID == def && def != "" {
				if plan.DefaultModel == "" {
					label += " · what a run with nothing chosen would use"
				} else {
					label += " · the board's default"
				}
			}
			if v, hit := verdicts[m.ID]; hit && v.known && !v.ok {
				label += " · OFFLINE for this account — " + shortVerdict(v.detail)
			}
			names = append(names, label)
		}
		// A model chosen before this build's catalogue knew it — or after the
		// plan dropped it — stays selectable rather than being silently
		// replaced by whatever happens to be first in the list.
		if want != "" && indexOf(ids, want) < 0 {
			ids = append(ids, want)
			names = append(names, want+" — not in the catalogue; the gateway decides")
		}
		if len(ids) == 0 {
			// Zen, before the first refresh. An empty dropdown with no
			// explanation reads as a broken board; this one says what is
			// missing and what fetches it.
			ids = append(ids, "")
			names = append(names, "no models yet — press Refresh models to ask "+plan.Name)
		}

		filling = true
		model.SetModel(gtk.NewStringList(names))
		if i := indexOf(ids, want); i >= 0 {
			model.SetSelected(uint(i))
		}
		filling = false

		model.SetSubtitle(escapeMarkup("Which model a " + plan.Backend +
			" extraction runs. Sorted cheapest first — and on this plan the free models, " +
			"when it has any, sort first of all: an extraction is a schema-constrained " +
			"JSON job over one file at a time, which is the shape a small fast model does well " +
			"and a frontier model only does expensively. The choice is passed as --model and " +
			"is written into the provider entry with its own prices, so graphify's cost " +
			"estimate is this model's rather than the last one's. A repository that overrides " +
			"the model in its own detail page still wins over this."))
		avail.SetSubtitle(escapeMarkup(availSummary()))
		lastPaint = time.Now()
		refreshStatus()
	}

	model.NotifyProperty("selected", func() {
		if filling {
			return
		}
		i := int(model.Selected())
		if i < 0 || i >= len(ids) || ids[i] == "" {
			return
		}
		s := a.opts.Store.Settings()
		s.SetOpenCodeModelFor(plan.Backend, ids[i])
		a.opts.Store.SetSettings(s)
		refreshStatus()
		verify(ids[i])
		a.checkBackend()
	})

	// sweep asks the gateway about every model in the list that has not been
	// asked about yet.
	//
	// It runs by itself only for the plan that is actually SELECTED, and only
	// with a key present. Zen bills per token, and a board that swept both
	// catalogues every time the settings dialog opened would be spending
	// somebody's money to populate a dropdown they were not looking at. For
	// the plan in use it is worth it: the alternative is finding out mid-run.
	sweep := func(announce bool) {
		if sweeping || !gfy.HasOpenCodeKey() {
			if announce && !gfy.HasOpenCodeKey() {
				a.toastf("%s is not set, so the gateway cannot be asked", gfy.OpenCodeKeyVar)
			}
			return
		}
		backend := plan.Backend
		models := gfy.OpenCodeCatalog(backend)
		if len(models) == 0 {
			return
		}
		sweeping = true
		avail.SetSubtitle(escapeMarkup(availSummary()))
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			gfy.VerifyOpenCodeCatalog(ctx, backend, models, func(id string, ok bool, detail string) {
				idle(func() {
					if plan.Backend != backend {
						return
					}
					verdicts[id] = verdict{known: true, ok: ok, detail: detail}
					// Repainting the whole list on every answer would rebuild
					// a forty-entry model forty times, and would reset the
					// popup under anybody scrolling it. Coalesced: the list
					// catches up a few times a second, and once at the end.
					if time.Since(lastPaint) > 700*time.Millisecond {
						fill(gfy.OpenCodeCatalog(backend))
					} else {
						avail.SetSubtitle(escapeMarkup(availSummary()))
					}
				})
			})
			idle(func() {
				if plan.Backend != backend {
					return
				}
				sweeping = false
				fill(gfy.OpenCodeCatalog(backend))
				if announce {
					a.toastf("%s", availSummary())
				}
			})
		}()
	}

	check := gtk.NewButtonWithLabel("Check availability")
	check.AddCSSClass("flat")
	check.SetVAlign(gtk.AlignCenter)
	check.SetTooltipText("Ask the gateway to answer one token with each listed model, and fade " +
		"the ones it refuses.")
	check.ConnectClicked(func() { sweep(true) })
	avail.AddSuffix(check)

	// The catalogue is baked in for the Go plan, so its dropdown is populated
	// before any network call and stays populated on a machine that cannot
	// reach models.dev. The refresh is what keeps it current — the plan's
	// model list changes monthly — and it is the same fetch the button
	// repeats by hand. Zen has no baked-in list at all, so for Zen this is
	// not a refresh but the only way the list ever arrives.
	reload := func(announce bool) {
		backend := plan.Backend
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			models, err := gfy.RefreshOpenCodeCatalog(ctx, backend)
			idle(func() {
				if plan.Backend != backend {
					return
				}
				fill(models)
				if err != nil {
					applog.Infof("%s: keeping the built-in model catalogue: %v", backend, err)
					if announce {
						a.toastf("could not refresh the model list: %v", err)
					}
					return
				}
				if announce {
					a.toastf("%s", plural(len(models), "model", "models"))
				}
				if gfy.EffectiveBackend(a.opts.Store.Settings().Backend) == backend {
					sweep(false)
				}
			})
		}()
	}

	refresh := gtk.NewButtonWithLabel("Refresh models")
	refresh.AddCSSClass("flat")
	refresh.SetVAlign(gtk.AlignCenter)
	refresh.SetTooltipText("Re-read the plan's model list from the gateway, and its prices from " +
		gfy.ModelsDevURL + ".")
	refresh.ConnectClicked(func() { reload(true) })
	status.AddSuffix(refresh)

	g.Add(status)
	g.Add(model)
	g.Add(avail)

	caveat := adw.NewActionRow()
	caveat.SetTitle("How jobs reach the gateway")
	caveat.SetSubtitleLines(0)
	caveat.SetSubtitle(escapeMarkup(gfy.OpenCodeCaveat))
	g.Add(caveat)

	// describe paints everything that names the plan. Which of the two
	// sentences the group opens with depends on the Backend picker two groups
	// up, and it changes as that picker changes: a settings page that
	// describes a backend as selectable while it IS selected is how a user
	// concludes the dropdown they are looking at does nothing.
	describe := func(selected bool) {
		g.SetTitle(plan.Name)
		blurb := planBlurb(plan)
		if selected {
			g.SetDescription(blurb + " It is the selected backend: the model below is what " +
				"every metered command runs against.")
		} else {
			g.SetDescription(blurb + " Select " + plan.Backend + " under Backend, above, to use it " +
				"— and to configure it here, since which plan this group shows is the one " +
				"that is selected.")
		}
	}

	a.ocApply = func(backend string) {
		was := plan.Backend
		if gfy.IsOpenCodeBackend(backend) {
			plan = gfy.OpenCodePlanFor(backend)
		}
		describe(gfy.IsOpenCodeBackend(backend) && plan.Backend == backend)
		if plan.Backend != was {
			// A different plan is a different catalogue, a different price
			// list and a different set of verdicts. Nothing from the old one
			// survives the switch.
			verdicts = map[string]verdict{}
			fill(gfy.OpenCodeCatalog(plan.Backend))
			reload(false)
			return
		}
		refreshStatus()
	}
	a.ocApply(gfy.EffectiveBackend(a.opts.Store.Settings().Backend))

	a.ocGroup, a.ocModelRow = g, model
	fill(gfy.OpenCodeCatalog(plan.Backend))
	reload(false)
	if m := a.opts.Store.Settings().OpenCodeModelFor(plan.Backend); m != "" {
		verify(m)
	} else {
		verify(gfy.DefaultModelFor(plan.Backend))
	}
	return g
}

// planBlurb is the sentence that opens the group: what the plan is, and why
// the board has to register it as a provider for the name to mean anything.
func planBlurb(plan gfy.OpenCodePlan) string {
	what := "A $10/month subscription to a curated set of open coding models, " +
		"reached through one OpenAI-compatible endpoint."
	if plan.Backend == gfy.OpenCodeZenBackend {
		what = "Pay-as-you-go over the same key and a wider estate — the open models, the " +
			"frontier ones, and whichever models the gateway is currently serving at no " +
			"charge, which are labelled free below and sort first."
	}
	return what + " graphify has no backend by this name — the board registers it as a " +
		"custom provider, which is what makes `--backend " + plan.Backend + "` work at all."
}

// shortVerdict trims the gateway's refusal to something that fits on the end
// of a dropdown entry. The full sentence is in the status row and the log.
func shortVerdict(detail string) string {
	if i := strings.Index(detail, ": "); i >= 0 && i+2 < len(detail) {
		detail = detail[i+2:]
	}
	const max = 60
	if len(detail) > max {
		return detail[:max] + "…"
	}
	return detail
}
