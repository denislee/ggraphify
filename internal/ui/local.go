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
// The lane still serializes them, because a local model is slow and graphify
// forces one request at a time for ollama, but the bill is zero.

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

// The board-wide local extraction: run the full LLM pass, against the model on
// this machine, on every repository that has never had one.
//
// It exists because the local backend changes what a fan-out over a hundred
// checkouts *means*. Against an API key that sweep is the single most
// expensive thing this application can do, which is why nothing offers it as
// one button. Against a local model it costs nothing but time, so the sweep
// that was never safe to offer becomes the obvious one — overnight work on the
// backlog of repositories nobody was going to pay to extract.
//
// Three things keep it honest anyway:
//
//   - It is scoped to rows that never ran, not to the whole board. A fresh or
//     stale graph already has its semantic layer; re-extracting it would be
//     hours of CPU to rebuild what is there (see board.NeverExtracted).
//   - It pins the backend to the local one explicitly rather than relying on
//     auto-detect, so an exported API key cannot turn a sweep the user asked
//     to run locally into a bill. The pinned name is in the argv the confirm
//     dialog prints.
//   - It goes through the ordinary confirm, which for a batch this size means
//     the typed-count gate as well.
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

	var rows []board.Row
	var running, done, excluded int
	for _, r := range a.allRows() {
		// One read of the job table per row: jobFor takes the lock, and asking
		// it twice in one switch would both cost double and let the answer
		// change between the two arms.
		job := a.jobFor(r.Path)
		switch {
		case r.Excluded:
			excluded++
		case !r.NeverExtracted():
			done++
		case job != nil && !job.Status.Done():
			// Already building. The runner serializes mutating commands per
			// repository, so a second extract would not corrupt anything — it
			// would just queue an hours-long job to redo what is in flight.
			running++
		default:
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		switch {
		case done > 0 && running == 0 && excluded == 0:
			a.toastf("every repository on the board has already been extracted (%d)", done)
		default:
			a.toastf("nothing to extract: %d already done, %d running, %d kept out of batches",
				done, running, excluded)
		}
		return
	}

	applog.Infof("local sweep: %d never extracted, %d already done, %d running, %d excluded — backend %s, model %s",
		len(rows), done, running, excluded, backend, gfy.LocalModel(backend, model))

	a.run("extract", rows, func(p *gfy.Params) {
		p.Backend = backend
		if model != "" {
			p.Model = model
		}
	})
}
