package gfy

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Cost says whether running a command spends money.
//
// It is the single most important property of a job in this application. AST
// work is free and can fan out over every repository at once; an LLM-backed
// extraction over 128 checkouts is a real bill, and the runner treats the two
// as separate lanes with separate concurrency limits and separate confirms.
type Cost int

const (
	Free    Cost = iota // local only: AST, clustering without labelling, exports, reads
	Metered             // dispatches LLM requests against the user's own key
)

func (c Cost) String() string {
	if c == Metered {
		return "metered"
	}
	return "free"
}

// Spec is one command ggraphify knows how to run: what it is called, what it
// costs, and how to turn parameters into an argv.
type Spec struct {
	Kind  string
	Title string
	Cost  Cost
	// Mutates says whether the command writes into graphify-out/. Read-only
	// commands (query, explain, god-nodes) can run while a build is in flight;
	// mutating ones for the same repository are serialized.
	Mutates bool
	// NeedsGraph says the command reads an existing graph.json and fails
	// without one. `graphify label` on a checkout that was never extracted
	// exits with "no graph found at …/graph.json — run /graphify first", and
	// for a metered command that error arrives *after* a confirm dialog that
	// promised a bill. The UI gates on this instead.
	NeedsGraph bool
}

// Params carry everything an argv builder may need. Zero values mean "leave
// the flag off and let graphify's own default apply" — the board never
// restates a default on the command line, because a restated default is a
// promise about a version.
type Params struct {
	Repo  string // checkout root; also the subprocess's working directory
	Out   string // graphify-out dir, for the commands that take --graph/--labels
	Graph string // explicit graph.json; defaults to Out/graph.json

	Backend string
	Model   string
	// ClaudeDir is the Claude Code configuration directory this job runs
	// against — which login pays for a claude-cli extraction, and which
	// ~/.claude* the installer writes the skill into. It is not a graphify
	// flag: it travels as CLAUDE_CONFIG_DIR in the job's environment, which
	// is where the confirm dialog shows it. Blank inherits the environment.
	ClaudeDir      string
	Deep           bool
	Force          bool
	NoCluster      bool
	NoLabel        bool
	NoViz          bool
	CodeOnly       bool
	NoGitignore    bool
	MaxWorkers     int
	TokenBudget    int
	MaxConcurrency int
	BatchSize      int
	APITimeout     int
	MissingOnly    bool

	// Query-console parameters.
	Question  string
	NodeA     string
	NodeB     string
	Budget    int
	Depth     int
	Top       int
	DFS       bool
	Contexts  []string
	Relations []string

	// Cross-repo and integration parameters.
	Tag      string
	Platform string
	Graphs   []string // merge-graphs inputs
	OutFile  string   // --out / --output for merge-graphs, tree, callflow

	// Extra is appended verbatim, after everything else. It is the per-repo
	// escape hatch in settings: a flag this build does not know about is still
	// reachable without waiting for a new release of the board.
	Extra []string
}

// graphPath is the --graph value to pass, or "" when the default is right.
func (p Params) graphPath() string {
	if p.Graph != "" {
		return p.Graph
	}
	if p.Out != "" {
		return filepath.Join(p.Out, "graph.json")
	}
	return ""
}

func (p Params) labelsPath() string {
	if p.Out == "" {
		return ""
	}
	return filepath.Join(p.Out, ".graphify_labels.json")
}

// Known is every command the GUI can run, keyed by kind. The UI's action rows,
// the job runner's cost policy and the golden argv tests all read this one map,
// so a command cannot exist in one of them and not the others.
var Known = map[string]Spec{
	"extract":         {Kind: "extract", Title: "Extract (full, LLM)", Cost: Metered, Mutates: true},
	"update":          {Kind: "update", Title: "Update (AST only)", Cost: Free, Mutates: true},
	"cluster-only":    {Kind: "cluster-only", Title: "Re-cluster", Cost: Free, Mutates: true, NeedsGraph: true},
	"label":           {Kind: "label", Title: "Label communities (LLM)", Cost: Metered, Mutates: true, NeedsGraph: true},
	"check-update":    {Kind: "check-update", Title: "Check for pending re-extraction", Cost: Free, NeedsGraph: true},
	"watch":           {Kind: "watch", Title: "Watch and rebuild", Cost: Free, Mutates: true},
	"export-html":     {Kind: "export-html", Title: "Export graph.html", Cost: Free, Mutates: true, NeedsGraph: true},
	"export-wiki":     {Kind: "export-wiki", Title: "Export wiki", Cost: Free, Mutates: true, NeedsGraph: true},
	"export-svg":      {Kind: "export-svg", Title: "Export SVG", Cost: Free, Mutates: true, NeedsGraph: true},
	"export-graphml":  {Kind: "export-graphml", Title: "Export GraphML", Cost: Free, Mutates: true, NeedsGraph: true},
	"export-obsidian": {Kind: "export-obsidian", Title: "Export Obsidian vault", Cost: Free, Mutates: true, NeedsGraph: true},
	"export-callflow": {Kind: "export-callflow", Title: "Export call-flow HTML", Cost: Free, Mutates: true, NeedsGraph: true},
	"tree":            {Kind: "tree", Title: "Export collapsible tree", Cost: Free, Mutates: true, NeedsGraph: true},
	"query":           {Kind: "query", Title: "Query", Cost: Free, NeedsGraph: true},
	"explain":         {Kind: "explain", Title: "Explain", Cost: Free, NeedsGraph: true},
	"path":            {Kind: "path", Title: "Path between nodes", Cost: Free, NeedsGraph: true},
	"affected":        {Kind: "affected", Title: "Affected by", Cost: Free, NeedsGraph: true},
	"god-nodes":       {Kind: "god-nodes", Title: "God nodes", Cost: Free, NeedsGraph: true},
	"diagnose":        {Kind: "diagnose", Title: "Diagnose multigraph", Cost: Free, NeedsGraph: true},
	"benchmark":       {Kind: "benchmark", Title: "Benchmark token reduction", Cost: Free, NeedsGraph: true},
	"global-add":      {Kind: "global-add", Title: "Add to global graph", Cost: Free, NeedsGraph: true},
	"global-remove":   {Kind: "global-remove", Title: "Remove from global graph", Cost: Free},
	"global-list":     {Kind: "global-list", Title: "List global graph repos", Cost: Free},
	"merge-graphs":    {Kind: "merge-graphs", Title: "Merge graphs", Cost: Free},
	"hook-install":    {Kind: "hook-install", Title: "Install git hooks", Cost: Free, Mutates: true},
	"hook-uninstall":  {Kind: "hook-uninstall", Title: "Remove git hooks", Cost: Free, Mutates: true},
	"hook-status":     {Kind: "hook-status", Title: "Git hook status", Cost: Free},
	"install":         {Kind: "install", Title: "Install agent skill", Cost: Free, Mutates: true},
	"reflect":         {Kind: "reflect", Title: "Reflect on saved results", Cost: Free, Mutates: true, NeedsGraph: true},
	"save-result":     {Kind: "save-result", Title: "Save query result", Cost: Free, Mutates: true},

	// graft, the other indexer. `build` without --deep is tree-sitter only:
	// no key, no network, no bill — the same shape as `update` is for
	// graphify. It writes into <repo>/graft rather than into graphify-out/,
	// which is why Mutates is true and why the board confirms it.
	"graft-build": {Kind: "graft-build", Title: "Sync graft index", Cost: Free, Mutates: true},
	// `graft init` is the installer: it writes the Claude Code wiring, both
	// the user-level copy in ~/.claude and the repo-level one in the checkout
	// it is pointed at. Free, and never run without the dialog that lists
	// every file it touches.
	"graft-init": {Kind: "graft-init", Title: "Wire Claude Code for graft", Cost: Free, Mutates: true},
}

// Graft reports whether a kind runs the graft CLI rather than graphify. The
// two binaries are resolved differently and a job's Out means nothing to the
// graft ones, so the board has to be able to tell them apart.
func Graft(kind string) bool { return strings.HasPrefix(kind, "graft-") }

// Argv builds the command line for a kind. The returned slice starts with the
// graphify binary, so it is exec-ready and also copy-pasteable verbatim — the
// two things the UI needs from it.
//
// An unknown kind returns nil. Callers treat that as a programming error, not
// as a user-facing condition: every kind the UI can reach is in Known.
func Argv(kind string, p Params) []string {
	bin := Bin()
	if Graft(kind) {
		bin = GraftBin()
	}
	a := []string{bin}
	add := func(v ...string) { a = append(a, v...) }
	// flag appends `--name value` only when value is non-empty. Restating a
	// default is what makes a GUI break on a release that moves one.
	flag := func(name, value string) {
		if value != "" {
			add(name, value)
		}
	}
	num := func(name string, v int) {
		if v > 0 {
			add(name, strconv.Itoa(v))
		}
	}
	graph := func() { flag("--graph", p.graphPath()) }
	labels := func() { flag("--labels", p.labelsPath()) }

	switch kind {
	case "extract":
		add("extract", p.Repo)
		flag("--backend", p.Backend)
		flag("--model", p.Model)
		if p.Deep {
			add("--mode", "deep")
		}
		if p.Force {
			add("--force")
		}
		if p.CodeOnly {
			add("--code-only")
		}
		if p.NoCluster {
			add("--no-cluster")
		}
		if p.NoGitignore {
			add("--no-gitignore")
		}
		num("--max-workers", p.MaxWorkers)
		num("--token-budget", p.TokenBudget)
		num("--max-concurrency", p.MaxConcurrency)
		num("--api-timeout", p.APITimeout)
		if p.Tag != "" {
			add("--global", "--as", p.Tag)
		}

	case "update":
		add("update", p.Repo)
		if p.Force {
			add("--force")
		}
		if p.NoCluster {
			add("--no-cluster")
		}

	case "cluster-only":
		add("cluster-only", p.Repo)
		if p.NoViz {
			add("--no-viz")
		}
		// --no-label is what keeps cluster-only free. Without it the command
		// dispatches LLM calls to name the communities, which is the same
		// spend as `label` — so the UI offers the two shapes as two actions
		// and the cost of each is decided here, not in a dialog.
		if p.NoLabel {
			add("--no-label")
		}
		flag("--backend", p.Backend)
		flag("--model", p.Model)
		num("--max-concurrency", p.MaxConcurrency)
		num("--batch-size", p.BatchSize)

	case "label":
		add("label", p.Repo)
		if p.MissingOnly {
			add("--missing-only")
		}
		flag("--backend", p.Backend)
		flag("--model", p.Model)
		num("--max-concurrency", p.MaxConcurrency)
		num("--batch-size", p.BatchSize)

	case "check-update":
		add("check-update", p.Repo)

	case "watch":
		add("watch", p.Repo)

	case "export-html":
		add("export", "html")
		graph()
		labels()
		if p.NoViz {
			add("--no-viz")
		}
	case "export-wiki":
		add("export", "wiki")
		graph()
		labels()
	case "export-svg":
		add("export", "svg")
		graph()
		labels()
	case "export-graphml":
		add("export", "graphml")
		graph()
	case "export-obsidian":
		add("export", "obsidian")
		graph()
		labels()
	case "export-callflow":
		add("export", "callflow-html")
		graph()
		labels()
		flag("--output", p.OutFile)

	case "tree":
		add("tree")
		graph()
		flag("--output", p.OutFile)
		flag("--root", p.Repo)

	case "query":
		add("query", p.Question)
		if p.DFS {
			add("--dfs")
		}
		for _, c := range p.Contexts {
			flag("--context", c)
		}
		num("--budget", p.Budget)
		graph()

	case "explain":
		add("explain", p.Question)
		graph()

	case "path":
		add("path", p.NodeA, p.NodeB)
		graph()

	case "affected":
		add("affected", p.Question)
		for _, r := range p.Relations {
			flag("--relation", r)
		}
		num("--depth", p.Depth)
		graph()

	case "god-nodes":
		add("god-nodes", "--json")
		num("--top", p.Top)
		graph()

	case "diagnose":
		add("diagnose", "multigraph", "--json")
		graph()

	case "benchmark":
		add("benchmark")
		if g := p.graphPath(); g != "" {
			add(g) // benchmark takes the graph positionally, not as a flag
		}

	case "global-add":
		g := p.graphPath()
		if g == "" {
			return nil
		}
		add("global", "add", g)
		flag("--as", p.Tag)
	case "global-remove":
		add("global", "remove", p.Tag)
	case "global-list":
		add("global", "list")

	case "merge-graphs":
		if len(p.Graphs) < 2 {
			return nil
		}
		add("merge-graphs")
		add(p.Graphs...)
		flag("--out", p.OutFile)

	case "hook-install":
		add("hook", "install")
	case "hook-uninstall":
		add("hook", "uninstall")
	case "hook-status":
		add("hook", "status")

	case "install":
		add("install")
		flag("--platform", p.Platform)

	case "reflect":
		add("reflect")
		graph()

	case "save-result":
		add("save-result", "--question", p.Question, "--answer", p.NodeA)

	case "graft-init":
		add("init", p.Repo)
		// --no-build because the board has its own sync button for that and a
		// dialog about wiring should not silently start an index of a
		// monorepo; --no-agents because ggraphify speaks for Claude Code and
		// has no business writing Cursor's or Copilot's files; --yes because
		// graft's interactive picker cannot be answered from a job log.
		//
		// Note that the CLAUDE_CONFIG_DIR every job carries does NOT steer
		// this one: graft's installer resolves its user-level targets from the
		// home directory and reads the variable only inside the hooks it
		// writes. The settings group says so rather than the argv pretending
		// otherwise — see GraftInitDir and GraftSetup.InitWrites.
		add("--no-build", "--no-agents", "--yes")

	case "graft-build":
		// The repository positionally, and nothing else: every flag graft
		// build takes either costs money (--deep), or pins a choice graft
		// persists in its own fingerprint and should keep making for itself.
		add("build", p.Repo)

	default:
		return nil
	}

	return append(a, p.Extra...)
}

// Quote renders an argv the way a shell would accept it back. This is what the
// confirm dialogs and the "copy command line" action show, so it has to be
// genuinely paste-safe rather than merely readable.
func Quote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, s := range argv {
		parts = append(parts, quoteOne(s))
	}
	return strings.Join(parts, " ")
}

func quoteOne(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("-_./:=@+,", c) >= 0 {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CostOf is the cost of a kind, defaulting to Metered for anything unknown.
// Defaulting the other way would mean a command this build has never heard of
// could fan out over every repository without a confirm.
func CostOf(kind string) Cost {
	if s, ok := Known[kind]; ok {
		return s.Cost
	}
	return Metered
}

// rescansTree are the commands that walk the checkout and reconcile graphify's
// manifest against it. They are the three that read source files; everything
// else in Known reads or rewrites an existing graph.
var rescansTree = map[string]bool{"extract": true, "update": true, "watch": true}

// Rescans reports whether a command re-reads the repository's source tree.
//
// It is what tells the board that a successful run has had the final word on
// which files graphify will and will not graph — the premise behind
// graphstate.Baseline. A command that only re-clusters or exports has looked at
// no source file and cannot settle that question.
func Rescans(kind string) bool { return rescansTree[kind] }

// Title is a kind's human name, or the kind itself when it has none.
func Title(kind string) string {
	if s, ok := Known[kind]; ok {
		return s.Title
	}
	return kind
}

// NeedsGraph reports whether a kind reads an existing graph.json. Unknown
// kinds report false: the gate exists to give a clearer message than
// graphify's own error, never to block a command this build has not heard of.
func NeedsGraph(kind string) bool {
	s, ok := Known[kind]
	return ok && s.NeedsGraph
}
