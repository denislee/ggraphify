package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
)

// The local-model group: run the metered commands against a model on this
// machine instead of against a vendor API.
//
// It is its own group rather than three more rows in "LLM defaults" because it
// answers a different question. The LLM group asks which backend to use; this
// one asks whether the machine can host one at all — is a server installed, is
// it running, has a model been pulled — and those are facts about hardware and
// disk, not about a preference. Each of the three fails independently, and
// each has a different fix.
//
// It also carries the one piece of good news the rest of the settings page
// cannot give: against a local model the metered commands are not metered.
// They are still slow — the metered lane runs one at a time, and a run is
// bounded by how many requests the server answers at once — but the bill is
// zero, and the slot count is on the server row so that the second of those
// two is a number the user can change rather than a fact they must accept.

func (a *App) settingsLocal() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Local model")
	g.SetDescription("A model served from this machine — ollama, or any OpenAI-compatible " +
		"server (llama.cpp's llama-server, vLLM, LM Studio) named by " + gfy.OpenAIBaseURLVar + ". " +
		"Extraction against one costs no money and sends nothing off this machine; it is slower " +
		"and weaker than a frontier model, which is the trade. Select it as the backend above, " +
		"or leave the backend on auto-detect and it is what a machine with no API key and no " +
		"Claude Code login falls back to.")

	server := adw.NewActionRow()
	server.SetTitle("ollama")
	server.SetSubtitleLines(0)
	server.SetSubtitle("Checking…")

	refresh := gtk.NewButtonWithLabel("Refresh")
	refresh.AddCSSClass("flat")
	refresh.SetVAlign(gtk.AlignCenter)
	refresh.SetTooltipText("Re-probe the local server for its model list.")
	refresh.ConnectClicked(func() { a.checkLocalLLM(true) })
	server.AddSuffix(refresh)

	start := gtk.NewButtonWithLabel("Start")
	start.AddCSSClass("suggested-action")
	start.SetVAlign(gtk.AlignCenter)
	start.SetSensitive(false)
	start.SetTooltipText("Start the ollama server — its systemd unit when there is one, " +
		"otherwise a detached `ollama serve`.")
	start.ConnectClicked(func() { a.startOllama() })
	server.AddSuffix(start)
	g.Add(server)

	// The model row is a combo when the server has models and a plain row when
	// it has none, so it is never a picker with nothing to pick. adw has no
	// way to swap a row's type in place, so both exist and one is hidden.
	models := adw.NewComboRow()
	models.SetTitle("Model on this machine")
	models.SetSubtitleLines(0)
	models.SetVisible(false)
	models.NotifyProperty("selected", func() {
		if a.localFilling || a.localModelNames == nil {
			return
		}
		i := int(models.Selected())
		if i < 0 || i >= len(a.localModelNames) {
			return
		}
		s := a.opts.Store.Settings()
		if s.Model == a.localModelNames[i] {
			return
		}
		s.Model = a.localModelNames[i]
		a.opts.Store.SetSettings(s)
		a.toastf("model set to %s", s.Model)
	})
	g.Add(models)

	pull := adw.NewActionRow()
	pull.SetTitle("No model pulled")
	pull.SetSubtitleLines(0)
	pull.SetVisible(false)

	copyBtn := gtk.NewButtonWithLabel("Copy command")
	copyBtn.AddCSSClass("flat")
	copyBtn.SetVAlign(gtk.AlignCenter)
	copyBtn.SetTooltipText("A pull is gigabytes over your network; the board hands you the " +
		"command rather than starting a download it could not show progress for.")
	copyBtn.ConnectClicked(func() {
		cmd := gfy.PullOllamaCommand(a.opts.Store.Settings().Model)
		a.win.Clipboard().SetText(cmd)
		a.toastf("copied: %s", cmd)
	})
	pull.AddSuffix(copyBtn)
	g.Add(pull)

	// The OpenAI-compatible row is informational only: whether OPENAI_BASE_URL
	// points at a local server is a property of the environment the board was
	// launched in, and the overlay editor below is where it is changed.
	compat := adw.NewActionRow()
	compat.SetTitle("OpenAI-compatible server")
	compat.SetSubtitleLines(0)
	g.Add(compat)

	a.localGroup = g
	a.localServerRow = server
	a.localStartBtn = start
	a.localModelsRow = models
	a.localPullRow = pull
	a.localCompatRow = compat

	a.checkLocalLLM(false)
	return g
}

// checkLocalLLM probes both local endpoints off the main thread — each is one
// HTTP request with a two-second timeout, which is two seconds too many to
// spend on the thread that draws — and folds the answer back in.
func (a *App) checkLocalLLM(announce bool) {
	if a.localGroup == nil {
		return
	}
	model := a.opts.Store.Settings().Model
	go func() {
		if announce {
			gfy.InvalidateLocalProbe()
		}
		ollama := gfy.ProbeLocal(gfy.OllamaBackend)
		compat := gfy.ProbeLocal(gfy.OpenAIBackend)
		idle(func() {
			a.fillLocalLLM(ollama, compat, model)
			if announce {
				ok, why := gfy.LocalReady(gfy.OllamaBackend, model)
				applog.Infof("probed local model server: %s", why)
				if ok {
					a.toastf("%s — %d model(s)", ollama.BaseURL, len(ollama.Models))
				} else {
					a.toast(why)
				}
			}
		})
	}()
}

// fillLocalLLM renders one probe into the four rows. Main thread.
func (a *App) fillLocalLLM(ollama, compat gfy.LocalProbe, model string) {
	if a.localGroup == nil {
		return
	}

	// ollama: installed / running / serving what.
	bin := gfy.Ollama()
	switch {
	case ollama.Reach:
		sub := "Running at " + ollama.BaseURL + " — " +
			plural(len(ollama.Models), "model", "models") + " served"
		if bin != "" {
			sub += ", binary at " + bin
		}
		sub += ". Requests are free and never leave this machine."
		// The context slot, whenever a model is loaded and it could be
		// measured. It belongs on this row rather than in a tooltip because it
		// is the one property of a local server that decides whether an
		// extraction produces a graph or produces prose.
		if ollama.Ctx > 0 {
			sub += " Serving " + ollama.CtxModel + " in a " + gfy.Itoa(ollama.Ctx) +
				"-token context"
			if ollama.CtxMax > 0 {
				sub += " of a possible " + gfy.Itoa(ollama.CtxMax)
			}
			sub += "."
		}
		// And how many of those requests it takes at once, which is the
		// difference between an extraction that uses this machine and one that
		// uses a corner of it. It sits next to the context slot because the
		// two are one setting in practice: ollama divides the configured
		// context across the slots.
		if ollama.Slots > 0 {
			sub += " Answering " + plural(ollama.Slots, "request", "requests") +
				" at a time (" + ollama.SlotsWhy + ")"
			if n := gfy.LocalConcurrency(gfy.OllamaBackend); n > 1 {
				sub += ", so the board sends " + gfy.Itoa(n) + " chunks at once"
			}
			sub += "."
		}
		if adv := ollama.ContextAdvice(); adv != "" {
			sub += " ⚠ " + adv
		}
		if adv := ollama.ThroughputAdvice(); adv != "" {
			sub += " ⚡ " + adv
		}
		a.localServerRow.SetSubtitle(escapeMarkup(sub))
		a.localStartBtn.SetVisible(false)
	case bin != "":
		a.localServerRow.SetSubtitle(escapeMarkup("Installed at " + bin +
			", but nothing is answering at " + ollama.BaseURL + " (" + ollama.Err + ")."))
		a.localStartBtn.SetVisible(true)
		a.localStartBtn.SetSensitive(true)
	default:
		a.localServerRow.SetSubtitle(escapeMarkup("Not installed on this machine, and nothing " +
			"is answering at " + ollama.BaseURL + ". Install it from https://ollama.com/download, " +
			"or set " + gfy.OllamaHostVar + " in the overlay below to a server elsewhere."))
		a.localStartBtn.SetVisible(false)
	}

	// The model picker, and the pull hint that replaces it when there is
	// nothing to pick.
	a.localModelNames = append([]string(nil), ollama.Models...)
	if len(ollama.Models) > 0 {
		a.localFilling = true
		a.localModelsRow.SetModel(gtk.NewStringList(ollama.Models))
		want := gfy.LocalModel(gfy.OllamaBackend, model)
		for i, m := range ollama.Models {
			if m == want || strings.TrimSuffix(m, ":latest") == want {
				a.localModelsRow.SetSelected(uint(i))
				break
			}
		}
		a.localFilling = false
		sub := "Sets the Model field above. "
		if want != "" && !ollama.Has(want) {
			sub += "The board is currently set to " + want + ", which this server does not have."
		} else {
			sub += "These are the models this server can serve right now."
		}
		a.localModelsRow.SetSubtitle(escapeMarkup(sub))
		a.localModelsRow.SetVisible(true)
		a.localPullRow.SetVisible(false)
	} else {
		a.localModelsRow.SetVisible(false)
		if ollama.Reach || bin != "" {
			want := gfy.LocalModel(gfy.OllamaBackend, model)
			a.localPullRow.SetSubtitle(escapeMarkup("Nothing to run yet. " +
				gfy.PullOllamaCommand(want) + " fetches graphify's default coding model " +
				"(a few gigabytes); the board copies the command rather than downloading for you."))
			a.localPullRow.SetVisible(true)
		} else {
			a.localPullRow.SetVisible(false)
		}
	}

	// The OpenAI-compatible half.
	switch {
	case compat.Reach:
		a.localCompatRow.SetSubtitle(escapeMarkup(gfy.OpenAIBaseURLVar + " points at " +
			compat.BaseURL + ", which is answering with " + plural(len(compat.Models), "model", "models") +
			". The openai backend is therefore local here: free, and billed to nobody."))
	case compat.BaseURL != "":
		a.localCompatRow.SetSubtitle(escapeMarkup(gfy.OpenAIBaseURLVar + " points at " +
			compat.BaseURL + ", but nothing is answering there (" + compat.Err + ")."))
	default:
		a.localCompatRow.SetSubtitle(escapeMarkup("Unset, so the openai backend is the OpenAI API " +
			"and is billed. Set " + gfy.OpenAIBaseURLVar + " in the overlay below to a loopback " +
			"address — llama-server's http://127.0.0.1:8080/v1, LM Studio's " +
			"http://127.0.0.1:1234/v1 — to point it at a model here instead."))
	}
}

// startOllama runs the start off the main thread: it waits for the port to
// answer, which is up to ten seconds of a cold start.
func (a *App) startOllama() {
	a.localStartBtn.SetSensitive(false)
	go func() {
		how, err := gfy.StartOllama()
		idle(func() {
			// A system-wide unit is not a failure, it is a command the board
			// is not allowed to run. Put it on the clipboard rather than
			// reporting an error the user can do nothing with.
			if gfy.NeedsRoot(err) {
				applog.Infof("ollama is owned by a system unit; offering %q", gfy.SudoStartOllama)
				a.win.Clipboard().SetText(gfy.SudoStartOllama)
				a.toastf("needs root — copied: %s", gfy.SudoStartOllama)
				a.localStartBtn.SetSensitive(true)
				return
			}
			if err != nil {
				applog.Errorf("start ollama (%s): %v", how, err)
				a.toastf("could not start ollama: %v", err)
				a.localStartBtn.SetSensitive(true)
				return
			}
			applog.Infof("started ollama via %s", how)
			a.toastf("ollama started (%s)", how)
			a.checkLocalLLM(false)
		})
	}()
}

// The local extraction sweep: force a full LLM extraction, against the model on
// this machine, on every repository currently listed on the board.
//
// It exists because the local backend changes what a fan-out over a hundred
// checkouts *means*. Against an API key this is the single most expensive
// thing this application can do, and no button should make it one click.
// Against a local model it costs nothing but time, so the sweep that was never
// safe to offer becomes the one worth having — one overnight pass that leaves
// every row on the board fully extracted.
//
// It is the blunt instrument, and it is blunt in both directions on purpose:
// `--force` is passed, and NO row is skipped for already being healthy. A
// graph that is fresh, labelled and reported is re-extracted from scratch
// along with the ones that were never built. That is hours of CPU spent
// rebuilding what was already correct, and it is only defensible because the
// local backend charges nothing for it. The free Fix sweep (Ctrl+H) plans the
// minimum command each row actually needs and remains the right first choice;
// this one is for "redo everything, I do not care what it costs in time".
//
// What bounds it is the board's own listing. It takes visibleRows — what the
// folder filter, the state filter and the search box currently admit, in sort
// order — and not every checkout on the machine. That is what makes a sweep
// this heavy usable at all: narrow the board to one folder and the sweep is
// that folder, with the count in the confirm to prove it. With no filter set
// the listing IS the whole board, so "everything" remains one Esc away.
//
// Two exclusions survive, and both are the user's own instruction rather than
// a judgement about state:
//
//   - rows flagged to stay out of batch actions (⊘), which means exactly this;
//   - rows with a job already in flight, because the runner serializes
//     mutating commands per repository and a second extract would only queue
//     an hours-long job to redo what is already running.
//
// Three things keep it honest:
//
//   - Readiness is checked before anything is queued. A hundred jobs each
//     failing against a stopped server is a log nobody can read.
//   - It pins the backend to the local one explicitly rather than relying on
//     auto-detect, so an exported API key cannot turn a sweep the user asked
//     to run locally into a bill. The pinned name is in the argv the confirm
//     dialog prints.
//   - It goes through the ordinary confirm, which at this batch size means the
//     typed-count gate as well.
func (a *App) actExtractLocalAll() {
	backend := gfy.OllamaBackend
	model := a.opts.Store.Settings().Model

	// Refuse before queueing rather than after. A hundred jobs that each fail
	// on a server that is not running is a log nobody can read and a board
	// full of red rows, when the answer was one sentence.
	if ok, why := gfy.LocalReady(backend, model); !ok {
		a.toast(why)
		applog.Errorf("local sweep refused: %s", why)
		return
	}

	listed := a.visibleRows()
	if len(listed) == 0 {
		a.toast("nothing is listed — clear the filter first")
		return
	}

	var rows []board.Row
	var running, excluded int
	for _, r := range listed {
		// One read of the job table per row: jobFor takes the lock, and asking
		// it twice in one switch would both cost double and let the answer
		// change between the two arms.
		job := a.jobFor(r.Path)
		switch {
		case r.Excluded:
			excluded++
		case job != nil && !job.Status.Done():
			running++
		default:
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		a.toastf("nothing to extract in %s: %d running, %d kept out of batches",
			a.sweepScopeName(), running, excluded)
		return
	}

	applog.Infof("local sweep over %s: forced extract on %d of %d listed repositories (%d running, %d excluded) — backend %s, model %s",
		a.sweepScopeName(), len(rows), len(listed), running, excluded, backend, gfy.LocalModel(backend, model))
	// Worth one line in the log at the top of a sweep that will run for hours:
	// a stock 4096-token slot is why every chunk of the last one came back as
	// prose, and the number the board is about to cap chunks at is the reason
	// this one will not. The cap bounds a chunk the packer builds and not a
	// file it cannot split, so on a narrow slot the advice below is a warning
	// about files that will go missing, not about speed.
	if p := gfy.ProbeLocal(backend); p.Ctx > 0 {
		applog.Infof("local sweep chunk budget: %d-token context slot on %s, --token-budget %d",
			p.Ctx, p.CtxModel, gfy.LocalTokenBudget(backend))
		if adv := p.ContextAdvice(); adv != "" {
			applog.Infof("local sweep: %s", adv)
			a.toast("narrow context slot — files larger than the chunk cap will be dropped; see the log")
		}
	}

	a.run("extract", rows, func(p *gfy.Params) {
		p.Backend = backend
		if model != "" {
			p.Model = model
		}
		// Force, because this sweep rebuilds graphs that already exist as well
		// as building the ones that do not. Without it graphify declines to
		// overwrite a graph.json whose rebuild came out smaller.
		p.Force = true
	})
}

// sweepScopeName says what the sweep is currently bounded to, for the log and
// the toasts. The folder filter is the one people set deliberately and the one
// they forget, so it is named; a search or state filter narrowing the listing
// further is reported as "the current listing" rather than quoted back, since
// the confirm dialog lists the repositories by name anyway.
func (a *App) sweepScopeName() string {
	if a.groupID == "" {
		return "the whole board"
	}
	return board.Tilde(a.groupID)
}
