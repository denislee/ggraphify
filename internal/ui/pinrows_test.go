package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/store"
)

// Choosing a backend in a pin row must return. The row rebuilds its own
// dropdown model to retitle the "same as …" entry, and it does that from
// inside the ::selected handler that rebuild re-emits.
func TestPinRowSelectionReturns(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		t.Skip("no display")
	}
	if !gtk.InitCheck() {
		t.Skip("gtk will not initialise here")
	}
	adw.Init()
	st := store.Open(filepath.Join(t.TempDir(), "state.json"))
	a := &App{}
	a.opts.Store = st

	g := adw.NewPreferencesGroup()
	combo := a.pinRows(g, pin{
		title:     gfy.Title("extract"),
		what:      "x",
		follows:   "Heavy work",
		modelNote: "the Heavy work model",
		get:       func(s store.Settings) (string, string) { return s.KindPin("extract") },
		set:       func(s *store.Settings, b, m string) { s.SetKindPin("extract", b, m) },
		followed:  func(s store.Settings) string { b, _ := s.BackendFor(gfy.Heavy); return b },
		inForce:   func(s store.Settings) string { b, _ := s.BackendForKind("extract"); return b },
	})

	choices := weightChoices()
	pick := func(backend string) {
		t.Helper()
		for i, c := range choices {
			if c == backend {
				combo.SetSelected(uint(i))
				return
			}
		}
		t.Fatalf("no dropdown entry for %q", backend)
	}

	pick(gfy.ClaudeCLIBackend)
	if got, _ := st.Settings().KindPin("extract"); got != gfy.ClaudeCLIBackend {
		t.Fatalf("pin = %q, want %q", got, gfy.ClaudeCLIBackend)
	}

	// A refresh must not read as a click. This is the regression: the refresh
	// used to rebuild the dropdown's string list to retitle its first entry,
	// GTK re-emitted ::selected once that call had returned, the handler took
	// it for a choice and refreshed again — a board that froze on the first
	// backend anybody picked here.
	a.refreshPins()
	if got, _ := st.Settings().KindPin("extract"); got != gfy.ClaudeCLIBackend {
		t.Fatalf("a refresh moved the pin to %q", got)
	}

	pick(gfy.OllamaBackend)
	if got, _ := st.Settings().KindPin("extract"); got != gfy.OllamaBackend {
		t.Fatalf("second choice = %q, want ollama", got)
	}

	// And back to following the tier above, which is an absent key rather
	// than a remembered blank.
	pick("")
	if got, _ := st.Settings().KindPin("extract"); got != "" {
		t.Fatalf("cleared pin = %q, want blank", got)
	}
	if _, ok := st.Settings().KindBackend["extract"]; ok {
		t.Fatal("clearing the pin left a key behind")
	}
}
