package ui

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
)

// The key table is the documentation and dispatchKey is the behaviour. They
// are deliberately two things — a function pointer in the table would create
// an initialisation cycle with the help dialog — so this test is what keeps
// them from drifting apart.
func TestEveryDocumentedKeyIsDispatched(t *testing.T) {
	handled := dispatchedKeys(t)
	for _, k := range keyBindings {
		for _, name := range accelNames(k.Keys) {
			if name == "" {
				continue
			}
			if !handled[name] {
				t.Errorf("the help overlay documents %q (%s) but dispatchKey does not handle it",
					k.Keys, k.What)
			}
		}
	}
}

// dispatchedKeys reads the gdk.KEY_* constants named in the dispatchKey switch
// out of the source, which is the only way to assert on it without a display.
func dispatchedKeys(t *testing.T) map[string]bool {
	t.Helper()
	src := readSource(t, "keys.go")
	out := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "case gdk.KEY_") {
			continue
		}
		for _, part := range strings.Split(strings.TrimPrefix(line, "case "), ",") {
			part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ":"))
			out[strings.TrimPrefix(part, "gdk.KEY_")] = true
		}
	}
	if len(out) < 10 {
		t.Fatalf("only %d keys parsed out of dispatchKey — the parser is wrong", len(out))
	}
	return out
}

// accelNames maps a help-table key string to the gdk.KEY_ constant names it
// should correspond to.
func accelNames(keys string) []string {
	switch keys {
	case "j / ↓":
		return []string{"j"}
	case "k / ↑":
		return []string{"k"}
	case "g / G":
		return []string{"g", "G"}
	case "Enter", "Ctrl+Q", "Ctrl+H", "Ctrl+G", "Ctrl+L", "Ctrl+U", "Ctrl+F / Ctrl+B":
		return nil // handled by the view's activate signal / the ctrl branch
	case "/":
		return []string{"slash"}
	case "Esc":
		return []string{"Escape"}
	case "?":
		return []string{"question"}
	case ",":
		return []string{"comma"}
	case "Space":
		return []string{"space"}
	}
	return []string{keys}
}

// The help overlay's accelerator strings must be ones AdwShortcutsItem can
// parse. A bare "↓" is not.
func TestAccelForProducesParseableStrings(t *testing.T) {
	for _, k := range keyBindings {
		got := accelFor(k.Keys)
		if got == "" {
			t.Errorf("accelFor(%q) is empty", k.Keys)
		}
		for _, bad := range []string{"↓", "↑", "/", "?", ","} {
			if strings.Contains(got, bad) {
				t.Errorf("accelFor(%q) = %q, which GTK cannot parse", k.Keys, got)
			}
		}
	}
}

func TestUsageKeysListsEveryBinding(t *testing.T) {
	out := UsageKeys()
	for _, k := range keyBindings {
		if !strings.Contains(out, k.What) {
			t.Errorf("`ggraphify -h` does not mention %q", k.What)
		}
	}
}

// Every column in the registry must have a unique id — the sidecar keys widths
// and visibility on it, and two columns sharing one would silently overwrite
// each other's stored width.
func TestColumnIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range columns {
		if seen[c.ID] {
			t.Errorf("duplicate column id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Title == "" {
			t.Errorf("column %q has no title", c.ID)
		}
		if c.render == nil {
			t.Errorf("column %q has no renderer", c.ID)
		}
	}
}

// Every action the UI offers must name a kind that internal/gfy can build, or
// the board offers a button that cannot run.
func TestEveryActionKindIsKnown(t *testing.T) {
	src := readSource(t, "detail.go") + readSource(t, "actions.go") +
		readSource(t, "viz.go") + readSource(t, "graft.go")
	for _, kind := range kindsNamedIn(src) {
		if _, ok := gfy.Known[kind]; !ok {
			t.Errorf("the UI names the kind %q, which internal/gfy does not know", kind)
		}
	}
}

// kindsNamedIn pulls the quoted first argument out of a.run(…) / add(…, kind, …)
// call sites. It is a blunt instrument on purpose: it over-collects, and every
// string it collects has to be a real kind anyway.
func kindsNamedIn(src string) []string {
	var out []string
	for _, marker := range []string{`a.run("`, `d.a.run("`, `SubmitCmd("`} {
		rest := src
		for {
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}
			rest = rest[i+len(marker):]
			j := strings.IndexByte(rest, '"')
			if j < 0 {
				break
			}
			out = append(out, rest[:j])
		}
	}
	return out
}
