package jobs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
)

// withGraph builds an output directory holding a plausible graph.json.
func withGraph(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "graphify-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(`{"nodes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// The regression this whole gate exists for: `label` on a checkout that was
// never extracted. graphify answers "no graph found at …/graph.json — run
// /graphify first" only after the job has been queued, and for a metered
// command only after a confirm dialog has already named the bill.
func TestPrecheckRefusesLabelWithoutAGraph(t *testing.T) {
	err := RequireGraph(&Job{Kind: "label", Repo: "/repo", Out: filepath.Join(t.TempDir(), "never-built")})
	if err == nil {
		t.Fatal("label was allowed against an output directory with no graph.json")
	}
	if !strings.Contains(err.Error(), "graph.json") || !strings.Contains(err.Error(), "Extract") {
		t.Errorf("the refusal names neither the file nor the remedy: %v", err)
	}
}

// The gate is about the graph, not about the money: it must not block the two
// commands that build a graph from nothing, or the repository would have no
// way out of the state.
func TestPrecheckAllowsTheCommandsThatBuildAGraph(t *testing.T) {
	none := filepath.Join(t.TempDir(), "never-built")
	for _, kind := range []string{"extract", "update", "install", "hook-install", "global-list"} {
		if err := RequireGraph(&Job{Kind: kind, Out: none}); err != nil {
			t.Errorf("%s was refused on a graphless repository: %v", kind, err)
		}
	}
}

// Every graph-reading kind is covered, and all of them pass once a graph is
// actually there.
func TestPrecheckCoversEveryGraphReadingKind(t *testing.T) {
	none := filepath.Join(t.TempDir(), "never-built")
	has := withGraph(t)
	n := 0
	for kind, spec := range gfy.Known {
		if !spec.NeedsGraph {
			continue
		}
		n++
		if err := RequireGraph(&Job{Kind: kind, Out: none}); err == nil {
			t.Errorf("%s was allowed with no graph", kind)
		}
		if err := RequireGraph(&Job{Kind: kind, Out: has}); err != nil {
			t.Errorf("%s was refused with a graph present: %v", kind, err)
		}
	}
	if n == 0 {
		t.Fatal("no kind is marked NeedsGraph; the gate would be inert")
	}
}

// An unknown kind is never gated: the hook exists to give a clearer message
// than graphify's own, not to become a second allowlist that a new command has
// to be added to before it can run.
func TestPrecheckIgnoresUnknownKinds(t *testing.T) {
	if err := RequireGraph(&Job{Kind: "no-such-kind", Out: filepath.Join(t.TempDir(), "nope")}); err != nil {
		t.Errorf("an unknown kind was gated: %v", err)
	}
}

// ollamaJob is a job as SubmitCmd would have built it for the local backend:
// the argv is what RequireLocalServer reads, not the Params.
func ollamaJob(t *testing.T) *Job {
	t.Helper()
	repo := t.TempDir()
	p := gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}
	return &Job{
		Kind:  "extract",
		Repo:  repo,
		Local: true,
		Argv:  gfy.Argv("extract", p),
	}
}

// deadOllama points the backend at a port nothing is listening on, which is
// exactly the state the regression happened in: the server is configured, and
// it is not answering.
func deadOllama(t *testing.T) {
	t.Helper()
	t.Setenv(gfy.OllamaHostVar, "http://127.0.0.1:1")
	gfy.InvalidateLocalProbe()
	t.Cleanup(gfy.InvalidateLocalProbe)
}

// The refusal that this gate exists for, in the one arm that needs no systemd
// to reproduce: the server is down and the board is not going to start it, so
// running the job could only produce a log full of connection errors.
func TestRequireLocalServerRefusesADownServerTheBoardWillNotStart(t *testing.T) {
	deadOllama(t)
	err := RequireLocalServer(func() bool { return false })(ollamaJob(t))
	if err == nil {
		t.Fatal("a local job was queued against a server nothing was going to start")
	}
	for _, want := range []string{"127.0.0.1:1", "lifecycle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// And it stays out of the way of every job it is not about: a metered backend
// talks to nobody's ollama, and a down server it is not using must not stop it.
func TestRequireLocalServerIgnoresAMeteredJob(t *testing.T) {
	deadOllama(t)
	repo := t.TempDir()
	j := &Job{Kind: "extract", Repo: repo,
		Argv: gfy.Argv("extract", gfy.Params{Repo: repo, Backend: gfy.ClaudeCLIBackend})}
	if err := RequireLocalServer(func() bool { return false })(j); err != nil {
		t.Errorf("a claude-cli job was refused for the local server's sake: %v", err)
	}
	if err := RequireLocalServer(nil)(nil); err != nil {
		t.Errorf("a nil job was refused: %v", err)
	}
}

// A server that answers is a server that answers, whoever started it — the
// gate has nothing to say and must say nothing.
func TestRequireLocalServerAllowsAServerThatIsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(gfy.OllamaHostVar, srv.URL)
	gfy.InvalidateLocalProbe()
	t.Cleanup(gfy.InvalidateLocalProbe)

	if err := RequireLocalServer(func() bool { return false })(ollamaJob(t)); err != nil {
		t.Errorf("a job was refused against a server that was answering: %v", err)
	}
}

// Checks stops at the first refusal and reports it verbatim: two gates whose
// messages were concatenated would be a sentence about neither.
func TestChecksStopsAtTheFirstRefusal(t *testing.T) {
	ran := 0
	boom := errors.New("first")
	err := Checks(nil,
		func(*Job) error { ran++; return nil },
		func(*Job) error { ran++; return boom },
		func(*Job) error { ran++; return errors.New("second") },
	)(&Job{})
	if err != boom {
		t.Errorf("Checks returned %v, want the first refusal", err)
	}
	if ran != 2 {
		t.Errorf("%d checks ran; the one after the refusal should not have", ran)
	}
}
