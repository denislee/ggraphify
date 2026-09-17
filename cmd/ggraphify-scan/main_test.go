package main

import (
	"testing"

	"github.com/dns/ggraphify/internal/store"
)

// A board configured with an out-base keeps every repository's graph under one
// directory rather than inside the checkout. A scan that ignored that setting
// found no graphify-out/ in any checkout and reported every graphed repository
// as having "no graphify graph at all" — which inverted the whole -usage
// recommendation list into twelve metered extractions that were already done.
func TestResolveOutPrefersTheStoredBaseOverTheDefault(t *testing.T) {
	set := store.Settings{OutBase: "/home/u/knowledge"}
	name, base := resolveOut(set, map[string]bool{}, "", "")
	if base != "/home/u/knowledge" {
		t.Errorf("out-base = %q, want the stored one", base)
	}
	if name != "" {
		t.Errorf("out-name = %q, want empty", name)
	}
}

// An explicit flag still wins: a stored preference outranks a built-in default
// and nothing else.
func TestResolveOutLetsAnExplicitFlagWin(t *testing.T) {
	set := store.Settings{OutName: "stored-out", OutBase: "/home/u/knowledge"}
	flags := map[string]bool{"out-base": true, "out-name": true}
	name, base := resolveOut(set, flags, "flag-out", "/tmp/elsewhere")
	if base != "/tmp/elsewhere" {
		t.Errorf("out-base = %q, want the flag", base)
	}
	if name != "flag-out" {
		t.Errorf("out-name = %q, want the flag", name)
	}
}

// An explicitly-empty flag is still a choice: `-out-base=` means "in the
// checkout", and must not fall back to the stored value.
func TestResolveOutHonoursAnExplicitlyEmptyFlag(t *testing.T) {
	set := store.Settings{OutBase: "/home/u/knowledge"}
	_, base := resolveOut(set, map[string]bool{"out-base": true}, "", "")
	if base != "" {
		t.Errorf("out-base = %q, want empty — the flag said so", base)
	}
}
