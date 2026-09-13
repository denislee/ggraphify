package gfy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The argv builders are the whole contract with graphify, and their failure
// mode is silent and expensive: passing --max-concurrency where the command
// wants --max-workers produces a graphify usage error buried in a job log, or
// worse, a command that runs and does the wrong thing. Golden tests over the
// literal argv are the only thing that catches it.
func TestArgvGolden(t *testing.T) {
	t.Setenv("GRAPHIFY_BIN", "/fake/graphify")

	repo := "/home/u/git/svc"
	out := "/home/u/git/svc/graphify-out"
	graph := filepath.Join(out, "graph.json")
	labels := filepath.Join(out, ".graphify_labels.json")

	cases := []struct {
		name string
		kind string
		p    Params
		want string
	}{
		{
			name: "update is bare and free",
			kind: "update",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify update " + repo,
		},
		{
			name: "update --force",
			kind: "update",
			p:    Params{Repo: repo, Force: true},
			want: "/fake/graphify update " + repo + " --force",
		},
		{
			name: "extract restates no defaults",
			kind: "extract",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify extract " + repo,
		},
		{
			name: "extract with every knob",
			kind: "extract",
			p: Params{
				Repo: repo, Out: out, Backend: "gemini", Model: "g-2",
				Deep: true, Force: true, CodeOnly: true, NoCluster: true,
				NoGitignore: true, MaxWorkers: 8, TokenBudget: 60000,
				MaxConcurrency: 4, APITimeout: 600, Tag: "svc",
			},
			want: "/fake/graphify extract " + repo +
				" --backend gemini --model g-2 --mode deep --force --code-only" +
				" --no-cluster --no-gitignore --max-workers 8 --token-budget 60000" +
				" --max-concurrency 4 --api-timeout 600 --global --as svc",
		},
		{
			// The free/metered split rests on this flag: without --no-label,
			// cluster-only dispatches LLM calls to name the communities and
			// costs exactly what `label` costs.
			name: "cluster-only keeps placeholders",
			kind: "cluster-only",
			p:    Params{Repo: repo, NoLabel: true},
			want: "/fake/graphify cluster-only " + repo + " --no-label",
		},
		{
			name: "label missing only",
			kind: "label",
			p:    Params{Repo: repo, MissingOnly: true, Backend: "openai", MaxConcurrency: 4, BatchSize: 100},
			want: "/fake/graphify label " + repo + " --missing-only --backend openai --max-concurrency 4 --batch-size 100",
		},
		{
			name: "export html carries graph and labels",
			kind: "export-html",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify export html --graph " + graph + " --labels " + labels,
		},
		{
			name: "export graphml takes no labels",
			kind: "export-graphml",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify export graphml --graph " + graph,
		},
		{
			name: "tree takes --output and --root",
			kind: "tree",
			p:    Params{Repo: repo, Out: out, OutFile: out + "/GRAPH_TREE.html"},
			want: "/fake/graphify tree --graph " + graph + " --output " + out + "/GRAPH_TREE.html --root " + repo,
		},
		{
			name: "query quotes the question",
			kind: "query",
			p:    Params{Repo: repo, Out: out, Question: "how does auth work?", Budget: 4000, DFS: true},
			want: "/fake/graphify query 'how does auth work?' --dfs --budget 4000 --graph " + graph,
		},
		{
			name: "path takes two nodes",
			kind: "path",
			p:    Params{Repo: repo, Out: out, NodeA: "App", NodeB: "Store"},
			want: "/fake/graphify path App Store --graph " + graph,
		},
		{
			name: "affected takes relations and depth",
			kind: "affected",
			p:    Params{Repo: repo, Out: out, Question: "Store", Relations: []string{"CALLS"}, Depth: 3},
			want: "/fake/graphify affected Store --relation CALLS --depth 3 --graph " + graph,
		},
		{
			// Only --json outputs are ever parsed, so god-nodes always asks
			// for one.
			name: "god-nodes always asks for json",
			kind: "god-nodes",
			p:    Params{Repo: repo, Out: out, Top: 20},
			want: "/fake/graphify god-nodes --json --top 20 --graph " + graph,
		},
		{
			name: "diagnose always asks for json",
			kind: "diagnose",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify diagnose multigraph --json --graph " + graph,
		},
		{
			// benchmark takes the graph positionally, not as a flag.
			name: "benchmark is positional",
			kind: "benchmark",
			p:    Params{Repo: repo, Out: out},
			want: "/fake/graphify benchmark " + graph,
		},
		{
			name: "global add",
			kind: "global-add",
			p:    Params{Repo: repo, Out: out, Tag: "svc"},
			want: "/fake/graphify global add " + graph + " --as svc",
		},
		{
			name: "merge-graphs",
			kind: "merge-graphs",
			p:    Params{Graphs: []string{"/a/graph.json", "/b/graph.json"}, OutFile: "merged.json"},
			want: "/fake/graphify merge-graphs /a/graph.json /b/graph.json --out merged.json",
		},
		{
			name: "install names the platform",
			kind: "install",
			p:    Params{Platform: "claude"},
			want: "/fake/graphify install --platform claude",
		},
		{
			name: "extra flags are appended verbatim",
			kind: "update",
			p:    Params{Repo: repo, Extra: []string{"--no-cluster"}},
			want: "/fake/graphify update " + repo + " --no-cluster",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Quote(Argv(tc.kind, tc.p))
			if got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// A kind with no builder must return nil rather than a half-built command.
// The runner treats nil as a programming error; a partial argv would be a
// command that runs and does something unintended.
func TestArgvUnknownKindIsNil(t *testing.T) {
	if got := Argv("no-such-command", Params{Repo: "/x"}); got != nil {
		t.Fatalf("want nil for an unknown kind, got %v", got)
	}
}

// merge-graphs with fewer than two inputs is not a command.
func TestArgvMergeNeedsTwo(t *testing.T) {
	if got := Argv("merge-graphs", Params{Graphs: []string{"/a/graph.json"}}); got != nil {
		t.Fatalf("want nil for a one-input merge, got %v", got)
	}
}

// Every kind the UI can reach must have both a builder and a spec, or the
// board will offer a button that cannot run.
func TestEveryKnownKindBuilds(t *testing.T) {
	t.Setenv("GRAPHIFY_BIN", "/fake/graphify")
	p := Params{
		Repo: "/r", Out: "/r/graphify-out", Question: "q", NodeA: "A", NodeB: "B",
		Tag: "t", Platform: "claude", Graphs: []string{"/a.json", "/b.json"},
	}
	for kind := range Known {
		if got := Argv(kind, p); got == nil {
			t.Errorf("kind %q has a spec but no argv builder", kind)
		}
	}
}

// An unknown kind must be treated as metered. Defaulting the other way would
// mean a command this build has never heard of could fan out over every
// repository without a confirm.
func TestUnknownKindIsMetered(t *testing.T) {
	if CostOf("something-new") != Metered {
		t.Fatal("an unknown kind must default to Metered")
	}
	if CostOf("update") != Free {
		t.Fatal("update must be free")
	}
	if CostOf("extract") != Metered || CostOf("label") != Metered {
		t.Fatal("extract and label must be metered")
	}
}

func TestQuoteIsPasteSafe(t *testing.T) {
	cases := map[string]string{
		"plain":         "plain",
		"/a/b-c_d.json": "/a/b-c_d.json",
		"has space":     "'has space'",
		"it's":          `'it'\''s'`,
		"":              "''",
		"--flag=value":  "--flag=value",
		"$(rm -rf /)":   "'$(rm -rf /)'",
	}
	for in, want := range cases {
		if got := Quote([]string{in}); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// Bin honours $GRAPHIFY_BIN first, so a board can be pointed at a specific
// install without touching PATH.
func TestBinPrefersEnv(t *testing.T) {
	t.Setenv("GRAPHIFY_BIN", "/opt/graphify")
	if got := Bin(); got != "/opt/graphify" {
		t.Fatalf("Bin() = %q, want /opt/graphify", got)
	}
}

// The platform-skew warning is real on a machine that has upgraded graphify
// without re-running the installer, and it goes to stderr ahead of every
// --json payload. Both halves matter: recognising it, and stripping it.
func TestSkillSkew(t *testing.T) {
	line := "  warning: skill at /home/u/.claude/skills/graphify is from graphify 0.9.35, " +
		"package is 0.9.58. Run 'graphify install --platform claude' to update it"
	if SkillSkew(line) == "" {
		t.Fatal("the real skew warning was not recognised")
	}
	if SkillSkew("warning: something else entirely") != "" {
		t.Fatal("an unrelated warning was read as skew")
	}
	if SkillSkew(`{"a":1}`) != "" {
		t.Fatal("a JSON payload was read as skew")
	}
}

func TestTrimNoise(t *testing.T) {
	in := "  warning: skill at /x is from graphify 0.9.35, package is 0.9.58\n[\n  {\"a\": 1}\n]\n"
	got := TrimNoise(in)
	if !strings.HasPrefix(got, "[") {
		t.Fatalf("TrimNoise did not strip the warning: %q", got)
	}
	// A brace inside the payload must not be treated as the start.
	if TrimNoise(`{"a": {"b": 1}}`) != `{"a": {"b": 1}}` {
		t.Fatal("TrimNoise moved the start of a clean payload")
	}
	if TrimNoise("no json here at all\n") != "" {
		t.Fatal("TrimNoise invented a payload")
	}
}

// The environment overlay must never leak a credential into anything the UI
// renders, and must compose deterministically so a command preview is stable.
func TestEnvMasksSecretsAndComposes(t *testing.T) {
	e := Env{"GRAPHIFY_MAX_WORKERS": "8", "GRAPHIFY_API_KEY": "sk-secret", "ZZ_TOKEN": "t"}
	for _, line := range e.Display() {
		if strings.Contains(line, "sk-secret") || strings.Contains(line, "=t") {
			t.Fatalf("a secret reached Display(): %q", line)
		}
	}
	if !strings.Contains(strings.Join(e.Display(), "\n"), "GRAPHIFY_MAX_WORKERS=8") {
		t.Fatal("a non-secret was masked")
	}

	base := []string{"PATH=/usr/bin", "GRAPHIFY_MAX_WORKERS=1"}
	got := e.Compose(base)
	// The overlay replaces rather than duplicates.
	n := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "GRAPHIFY_MAX_WORKERS=") {
			n++
			if kv != "GRAPHIFY_MAX_WORKERS=8" {
				t.Errorf("overlay did not win: %q", kv)
			}
		}
	}
	if n != 1 {
		t.Fatalf("GRAPHIFY_MAX_WORKERS appears %d times, want 1", n)
	}
	// And it is sorted, so a preview does not reorder between runs.
	if a, b := e.Display(), e.Display(); strings.Join(a, "") != strings.Join(b, "") {
		t.Fatal("Display() is not deterministic")
	}
}

// JobEnv silences the CLI's interactive tips, which are noise in a job log,
// and changes nothing else.
func TestJobEnvSilencesTips(t *testing.T) {
	e := JobEnv(Env{"GRAPHIFY_DEBUG": "1"})
	if e["GRAPHIFY_NO_TIPS"] != "1" {
		t.Fatal("JobEnv did not silence tips")
	}
	if e["GRAPHIFY_DEBUG"] != "1" {
		t.Fatal("JobEnv dropped a caller's variable")
	}
	// A caller that wants tips keeps them.
	if JobEnv(Env{"GRAPHIFY_NO_TIPS": "0"})["GRAPHIFY_NO_TIPS"] != "0" {
		t.Fatal("JobEnv overrode an explicit choice")
	}
}

func TestValidVar(t *testing.T) {
	for _, ok := range []string{"GRAPHIFY_OUT", "_X", "A1"} {
		if !ValidVar(ok) {
			t.Errorf("ValidVar(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=B"} {
		if ValidVar(bad) {
			t.Errorf("ValidVar(%q) = true", bad)
		}
	}
}

func TestFirstErrorLine(t *testing.T) {
	log := "  warning: skill at /x is from graphify 0.9.35, package is 0.9.58\n" +
		"extracting…\nTraceback (most recent call last):\n" +
		`  File "/x/y.py", line 3, in <module>` + "\n" +
		"RuntimeError: no API key configured\n"
	got := FirstErrorLine(log)
	if got != "RuntimeError: no API key configured" {
		t.Fatalf("FirstErrorLine = %q", got)
	}
}

func TestVersionSupported(t *testing.T) {
	if !(Version{Found: true, Number: BuiltAgainst}).Supported() {
		t.Fatal("the pinned version must be supported")
	}
	// A patch bump is assumed compatible; a minor bump is not.
	mm := strings.SplitN(BuiltAgainst, ".", 3)
	if len(mm) == 3 {
		if !(Version{Found: true, Number: mm[0] + "." + mm[1] + ".999"}).Supported() {
			t.Error("a patch bump must be supported")
		}
	}
	if (Version{Found: true, Number: "1.0.0"}).Supported() {
		t.Error("a major bump must not be supported")
	}
	if (Version{Found: false}).Supported() {
		t.Error("a version that was not found is not supported")
	}
}

// Bin falls back to the uv tool location when nothing is on PATH, which is
// where graphify actually lives on a machine that installed it with uv.
func TestBinFallsBackToLocalBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GRAPHIFY_BIN", "")
	t.Setenv("PATH", filepath.Join(home, "empty"))

	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(local, "graphify")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Bin(); got != p {
		t.Fatalf("Bin() = %q, want %q", got, p)
	}
}

// Any command whose argv points at graph.json or the labels sidecar plainly
// cannot run without one, so it must be marked NeedsGraph — otherwise the UI
// would submit it against a never-extracted checkout and surface graphify's
// "no graph found at …/graph.json" instead of its own gate. The converse is
// not asserted: `label`, `cluster-only` and `check-update` take the repository
// path and still need a graph.
func TestNeedsGraphCoversEveryGraphReadingArgv(t *testing.T) {
	p := Params{Repo: "/repo", Out: "/out", Question: "q", NodeA: "a", NodeB: "b", Graphs: []string{"/g1", "/g2"}}
	for kind, spec := range Known {
		argv := Argv(kind, p)
		reads := false
		for _, a := range argv {
			if strings.Contains(a, "/out/graph.json") || strings.Contains(a, "/out/.graphify_labels.json") {
				reads = true
			}
		}
		if reads && !spec.NeedsGraph {
			t.Errorf("%s reads %s but is not marked NeedsGraph", kind, Quote(argv))
		}
	}
	for _, kind := range []string{"label", "cluster-only", "check-update"} {
		if !Known[kind].NeedsGraph {
			t.Errorf("%s needs an existing graph and must be marked NeedsGraph", kind)
		}
	}
	if NeedsGraph("extract") {
		t.Error("extract builds the graph from nothing; gating it would leave a graphless repo with no way forward")
	}
	if NeedsGraph("no-such-kind") {
		t.Error("an unknown kind must not be gated")
	}
}
