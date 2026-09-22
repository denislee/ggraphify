package ui

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/store"
)

// The backend pin has three tiers, and this file is the second and the third.
//
//	Backend            — one answer for everything (settings.go)
//	  Heavy / Light    — one answer per weight class (settingsWeights)
//	    per command    — one answer for one command (settingsKindPins)
//
// Each tier is optional and falls through to the one above it, so a board
// nobody has opened these groups on behaves exactly as it did before they
// existed. store.Settings.BackendForKind is where the fall-through is read
// back, and it is the only place that resolution lives.

// settingsWeights is the group that splits the LLM pin in two: one backend for
// the passes that read every file, another for the community labelling that
// sends a few dozen short prompts.
//
// It exists because one pin for both is a setting nobody chose. `extract`
// sends a request per chunk of every source file — the run that takes hours
// and empties a quota — while `label` names communities from a list of symbols
// it already has. Pointing both at a frontier model pays frontier prices for
// community names; pointing both at a 7B model on this machine hands it the
// extraction the entire graph is built out of. The split lets the expensive
// half be metered and the cheap half be free, or the other way round.
//
// A fresh install has both on ollama — see store.Defaults. That is the only
// pair of values that cannot surprise anybody with a bill on a first run.
func (a *App) settingsWeights() *adw.PreferencesGroup {
	// The dialog is built once and held, so this list is filled once. Cleared
	// anyway, because the day it is rebuilt the stale closures would be
	// pointing at destroyed widgets and the symptom would not name this line.
	a.pinRefresh = nil

	g := adw.NewPreferencesGroup()
	g.SetTitle("Heavy and light work")
	g.SetDescription("Which backend runs which kind of LLM command. " +
		"Heavy is " + gfy.Title("extract") + " and " + gfy.Title(gfy.GraftDeepKind) + ": " +
		"a request per file of the whole checkout, in graphify and in graft. Light is " +
		gfy.Title("label") + ": one short prompt per community, a few dozen for a whole " +
		"repository. Either can be left on the Backend above, either can be overridden " +
		"per command in the next group, and a repository's own backend override still " +
		"wins over all of them. The deep graft index is free on a model on this machine " +
		"and metered anywhere else; the board says which before it queues anything.")

	a.pinRows(g, pin{
		title: "Heavy work",
		what: gfy.Title("extract") + " and " + gfy.Title(gfy.GraftDeepKind) +
			" — the passes the indexes are built out of.",
		follows:   "the Backend above",
		modelNote: "the Model field above",
		get:       func(s store.Settings) (string, string) { return s.HeavyBackend, s.HeavyModel },
		set: func(s *store.Settings, b, m string) {
			s.HeavyBackend, s.HeavyModel = b, m
		},
		followed: func(s store.Settings) string { return s.Backend },
		inForce:  func(s store.Settings) string { b, _ := s.BackendFor(gfy.Heavy); return b },
	})
	a.pinRows(g, pin{
		title:     "Light work",
		what:      gfy.Title("label") + " — short prompts over a list of symbols.",
		follows:   "the Backend above",
		modelNote: "the Model field above",
		get:       func(s store.Settings) (string, string) { return s.LightBackend, s.LightModel },
		set: func(s *store.Settings, b, m string) {
			s.LightBackend, s.LightModel = b, m
		},
		followed: func(s store.Settings) string { return s.Backend },
		inForce:  func(s store.Settings) string { b, _ := s.BackendFor(gfy.Light); return b },
	})

	// The "same as …" label names the backend it follows, so every row has to
	// be retitled when a tier above it moves — a row that says it follows
	// claude after the user has switched to ollama is worse than one that
	// says nothing. The Backend picker calls this; so does any pin change.
	a.weightApply = func(string) { a.refreshPins() }
	return g
}

// settingsKindPins is the third tier: one command, one backend.
//
// The weight split answers the question most people have — expensive pass
// here, cheap pass there — and cannot answer the one the two heavy kinds
// raise. `extract` and graft's deep pass are the same SHAPE of job, a request
// per file of the whole checkout, and still not the same job: one fills a
// graph graphify's own commands read, the other writes prose an agent reads.
// A 7B model on this machine is a fine answer for one of those and a poor one
// for the other, and which way round that falls is not the board's to decide.
//
// Every row here is optional and starts blank. A board nobody opens this group
// on runs exactly the two-pin way it did before the group existed.
func (a *App) settingsKindPins() *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	g.SetTitle("Per command")
	g.SetDescription("A backend for one command, overriding the weight class it " +
		"belongs to. Every row starts on its weight class and can be left there; " +
		"a repository's own backend override still wins over all of it.")

	for _, kind := range gfy.LLMKinds() {
		k := kind
		w := gfy.WeightOf(k)
		class := weightTitle(w)
		a.pinRows(g, pin{
			title:     gfy.Title(k),
			what:      kindNote(k),
			follows:   class,
			modelNote: "the " + class + " model",
			get:       func(s store.Settings) (string, string) { return s.KindPin(k) },
			set:       func(s *store.Settings, b, m string) { s.SetKindPin(k, b, m) },
			followed: func(s store.Settings) string {
				b, _ := s.BackendFor(w)
				return b
			},
			inForce: func(s store.Settings) string { b, _ := s.BackendForKind(k); return b },
		})
	}
	return g
}

// weightTitle is a weight class's name as the settings page writes it, which
// is the name of the row that holds its pin.
func weightTitle(w gfy.Weight) string {
	if w == gfy.Light {
		return "Light work"
	}
	return "Heavy work"
}

// kindNote is the one line under a per-command row: what makes this command a
// different job from the other one in its weight class, since that difference
// is the entire reason to give it a pin of its own.
func kindNote(kind string) string {
	switch kind {
	case "extract":
		return "graphify's semantic pass: one schema-constrained JSON request per chunk, " +
			"and the graph every other graphify command reads."
	case gfy.GraftDeepKind:
		return "graft's concept map and per-symbol crux: prose an agent reads, one pass " +
			"per file. Free on a model on this machine, metered anywhere else."
	case "label":
		return "names for the communities the clustering found, from a list of symbols."
	}
	return gfy.Title(kind)
}

// pin is one backend/model pair the settings page can edit, and everything the
// rows need to know about the tier it falls through to.
type pin struct {
	title string // the row's title
	what  string // the row's subtitle
	// follows names the tier this pin falls back to, as it is written in the
	// dropdown's first entry: "the Backend above", "Heavy work".
	follows string
	// modelNote names the model that applies when this pin's own model is
	// blank, for the model row's title.
	modelNote string

	get func(store.Settings) (backend, model string)
	set func(s *store.Settings, backend, model string)
	// followed is the raw backend of the tier above, for the "(→ x)" suffix
	// on the first dropdown entry.
	followed func(store.Settings) string
	// inForce is the backend that actually runs this pin's work once the
	// fall-through is followed. It decides whether the model entry is
	// editable at all.
	inForce func(store.Settings) string
}

// pinRows adds one pin's two rows — the backend dropdown and the model that
// goes with it — and registers the closure that retitles them when a tier
// above changes.
//
// The model is per-pin because Model is one field shared by every backend and
// a model id is not portable between them: the moment two pins name different
// backends, one shared id is wrong for one of them.
func (a *App) pinRows(g *adw.PreferencesGroup, p pin) *adw.ComboRow {
	choices := weightChoices()
	set := a.opts.Store.Settings()
	backend, model := p.get(set)

	combo := adw.NewComboRow()
	combo.SetTitle(p.title)
	combo.SetListFactory(&wideTextFactory(nil).ListItemFactory)
	combo.SetFactory(&valueFactory().ListItemFactory)
	combo.SetSubtitleLines(0)
	combo.SetSubtitle(p.what)
	combo.SetModel(gtk.NewStringList(weightLabels(choices, p.follows)))
	for i, c := range choices {
		if c == backend {
			combo.SetSelected(uint(i))
		}
	}
	g.Add(combo)

	entry := adw.NewEntryRow()
	entry.SetTitle(p.title + " model (blank = " + p.modelNote + ")")
	entry.SetText(model)
	entry.SetShowApplyButton(true)
	entry.ConnectApply(func() {
		s := a.opts.Store.Settings()
		b, _ := p.get(s)
		p.set(&s, b, strings.TrimSpace(entry.Text()))
		a.opts.Store.SetSettings(s)
		a.checkBackend()
	})
	g.Add(entry)

	combo.NotifyProperty("selected", func() {
		s := a.opts.Store.Settings()
		was, m := p.get(s)
		now := choices[int(combo.Selected())]
		// A notification that carries the value already stored is not a
		// choice — it is GTK restating one. Returning here rather than
		// writing it back is what keeps a refresh from looking like a click.
		if now == was {
			return
		}
		p.set(&s, now, m)
		a.opts.Store.SetSettings(s)
		// The pin a job of this kind will actually run against just moved, so
		// every row that follows this one has a new answer to print, and the
		// banner's question has a new answer too.
		a.refreshPins()
		a.checkBackend()
	})

	a.pinRefresh = append(a.pinRefresh, func() {
		s := a.opts.Store.Settings()
		// Subtitle and not the dropdown's own entries. Naming the followed
		// backend inside entry 0 means rebuilding the string list to retitle
		// it, and a GtkStringList swap re-emits ::selected AFTER the call
		// that caused it has returned — a rebuild triggered from a selection
		// is then a selection that triggers a rebuild, which is a hung board
		// and was one. Nothing here touches the model or the selection, so
		// the cycle has no first step.
		combo.SetSubtitle(p.what + "\n" + pinNote(p, s))
		a.syncPinModel(entry, p, s)
	})
	// Once now, so the row is built with the suffix and the model-row state
	// it would otherwise only acquire on the first change somewhere above it.
	a.refreshPins()
	// The dropdown is returned for the test that drives it: a selection made
	// programmatically goes through the same ::selected handler a click does.
	return combo
}

// refreshPins restates every pin row from the settings as they now stand: each
// row says which tier it follows and where that lands, and a change anywhere
// above moves what it lands on.
//
// It needs no re-entrancy guard because it writes no widget property anything
// listens to — subtitles and sensitivity, never a model or a selection.
func (a *App) refreshPins() {
	for _, f := range a.pinRefresh {
		f()
	}
}

// pinNote is the line under a pin row that says where its work actually goes:
// the tier it is following when it holds nothing of its own, and the backend
// in force when it does.
func pinNote(p pin, s store.Settings) string {
	b, _ := p.get(s)
	if strings.TrimSpace(b) == "" {
		note := "Follows " + p.follows
		if eff := gfy.EffectiveBackend(p.followed(s)); eff != "" {
			note += " (→ " + eff + ")"
		}
		return note
	}
	eff := gfy.EffectiveBackend(p.inForce(s))
	if eff == "" {
		// An explicit auto-detect that this machine has nothing to detect
		// from. Saying "runs on " and stopping would read as a bug.
		return "Set to auto-detect, and nothing here detects a backend"
	}
	return "Runs on " + eff
}

// syncPinModel decides whether a pin's model entry can be typed in at all.
//
// Two reasons it cannot. The pin may be following the tier above, in which
// case its own model is not the one that will be used — BackendForKind takes
// the model that was typed under the backend that is actually in force, and a
// field that silently did nothing is worse than a greyed one. Or the backend
// in force may be one of the two OpenCode plans, whose model is picked from a
// priced list in their own group and where a typed id is a 404.
func (a *App) syncPinModel(entry *adw.EntryRow, p pin, s store.Settings) {
	b, _ := p.get(s)
	if strings.TrimSpace(b) == "" {
		entry.SetSensitive(false)
		entry.SetTitle(p.title + " model — follows " + p.follows)
		return
	}
	entry.SetTitle(p.title + " model (blank = the backend's own default)")
	entry.SetSensitive(!gfy.IsOpenCodeBackend(gfy.EffectiveBackend(p.inForce(s))))
}

// weightChoices is what a pin can hold, in dropdown order: follow the tier
// above, then every backend by name with auto-detect under its own sentinel.
// See gfy.AutoBackend for why the two blanks cannot share one entry.
func weightChoices() []string {
	out := make([]string, 0, len(gfy.Backends)+1)
	out = append(out, "")
	for _, b := range gfy.Backends {
		if b == "" {
			out = append(out, gfy.AutoBackend)
			continue
		}
		out = append(out, b)
	}
	return out
}

// weightLabels names those choices for the dropdown, with the tier this pin
// falls back to named on the first entry.
//
// Deliberately free of anything that moves: the list is built once per row and
// never rebuilt, because rebuilding it is what re-emits ::selected. Where the
// fall-through currently lands is in the row's subtitle instead — see pinNote.
func weightLabels(choices []string, follows string) []string {
	names := make([]string, len(choices))
	for i, c := range choices {
		switch c {
		case "":
			names[i] = "Same as " + follows
		case gfy.AutoBackend:
			names[i] = "auto-detect"
			if eff := gfy.EffectiveBackend(""); eff != "" {
				names[i] += " (→ " + eff + ")"
			}
		case gfy.ClaudeCLIBackend:
			names[i] = c + " (Claude Code on this machine, no API key)"
		case gfy.OllamaBackend:
			names[i] = c + " (a model on this machine — free, slow, no API key)"
		case gfy.OpenCodeBackend:
			names[i] = c + " (OpenCode Go — a $10/month subscription)"
		case gfy.OpenCodeZenBackend:
			names[i] = c + " (OpenCode Zen — pay-as-you-go, including its free models)"
		default:
			names[i] = c
			if gfy.IsLocalBackend(c) {
				names[i] = c + " (→ " + gfy.LocalBaseURL(c) + ", local and free)"
			}
		}
	}
	return names
}
