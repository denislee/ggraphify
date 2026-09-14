package gfy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// graft is the second indexer the board knows about. It is a Node CLI rather
// than a Python one, it writes into `<repo>/graft` rather than into
// graphify-out/, and the only thing ggraphify asks of it is the free wiring
// build — so the whole of that coupling is this file plus the `graft-*` arms
// of Argv.
//
// Deliberately NOT wired up: `graft build --deep`, which dispatches LLM
// requests. The board would have to describe that bill in the same terms it
// describes graphify's, and the sync this GUI offers is the free one.

// GraftBinVar overrides the executable, the same escape hatch GRAPHIFY_BIN is.
const GraftBinVar = "GRAFT_BIN"

// GraftBin resolves the graft executable: $GRAFT_BIN, then PATH, then the
// usual global-npm locations. As with Bin, the path is resolved rather than
// left to exec: a board launched from a .desktop entry inherits the session's
// PATH, which on a machine that installs node through nvm does not have graft
// on it.
func GraftBin() string {
	if v := strings.TrimSpace(os.Getenv(GraftBinVar)); v != "" {
		return v
	}
	if p, err := exec.LookPath("graft"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "graft"
	}
	candidates := []string{
		filepath.Join(home, ".local", "bin", "graft"),
		filepath.Join(home, ".npm-global", "bin", "graft"),
		filepath.Join("/usr", "local", "bin", "graft"),
	}
	// nvm keeps one bin directory per installed node version and puts none of
	// them on a desktop session's PATH; the newest one that has graft wins.
	if ents, err := os.ReadDir(filepath.Join(home, ".nvm", "versions", "node")); err == nil {
		for i := len(ents) - 1; i >= 0; i-- {
			if ents[i].IsDir() {
				candidates = append(candidates,
					filepath.Join(home, ".nvm", "versions", "node", ents[i].Name(), "bin", "graft"))
			}
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	return "graft" // let exec fail with a clear message
}

// GraftReady reports whether graft can be run on this machine, and why not
// when it cannot. The sync dialog says so before queueing anything, because a
// fan-out of forty jobs that all fail with "executable file not found" is a
// worse way to learn it.
func GraftReady() (bool, string) {
	bin := GraftBin()
	if filepath.Base(bin) == bin {
		return false, "graft is not installed on this machine, or not on this session's PATH. " +
			"Install it with `npm i -g @nanonets/graft`, or point " + GraftBinVar + " at it."
	}
	return true, bin
}

// GraftVersion is what a graft version probe found. It is deliberately
// thinner than Version: ggraphify runs exactly one graft command, and there is
// no pinned argv contract to warn about the way there is for graphify.
type GraftVersion struct {
	Bin    string
	Number string
	Found  bool
	Err    string
}

// GraftProbe runs `graft --version` once and caches the answer for the
// process's lifetime, the same shape Probe has.
//
// `graft --version` and not `graft version`: the latter also asks npm what the
// latest published release is, which is a network call this must not make.
func GraftProbe(ctx context.Context) GraftVersion {
	graftProbeMu.Lock()
	defer graftProbeMu.Unlock()
	if graftProbeDone {
		return graftProbed
	}
	graftProbed, graftProbeDone = probeGraft(ctx), true
	return graftProbed
}

// GraftReprobe discards the cached answer and probes again — what the settings
// group's Check button calls after an install.
func GraftReprobe(ctx context.Context) GraftVersion {
	graftProbeMu.Lock()
	graftProbeDone = false
	graftProbeMu.Unlock()
	return GraftProbe(ctx)
}

var (
	graftProbeMu   sync.Mutex
	graftProbeDone bool
	graftProbed    GraftVersion
)

func probeGraft(ctx context.Context) GraftVersion {
	bin := GraftBin()
	v := GraftVersion{Bin: bin}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		v.Err = err.Error()
		return v
	}
	for _, line := range strings.Split(string(out), "\n") {
		if n := versionNumber(strings.TrimSpace(line)); n != "" {
			v.Number, v.Found = n, true
			break
		}
	}
	if !v.Found && v.Err == "" {
		v.Err = "could not parse a version out of `graft --version`"
	}
	return v
}
