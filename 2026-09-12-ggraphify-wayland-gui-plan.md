# Plan — `ggraphify`, a native Wayland GUI for graphify across every local repository

**Date:** 2026-09-12
**Repo:** `/home/dns/tmp/ggraphify` (module `github.com/dns/ggraphify`)
**Status:** BUILT — M0-M6 implemented, M7 partial. This file remains the design of
record; see §11 for what landed and [`README.md`](README.md) for how to run it.
**Reference implementation:** `/home/dns/tmp/cc-docsboard` (`docsboard`) — same author, same
desktop, same stack. Where this plan says "as in docsboard" it means a pattern that is
already load-bearing in ~38 kLOC of shipped Go and should be copied rather than reinvented.
**Upstream:** graphify — <https://github.com/Graphify-Labs/graphify>

---

## 0. Verdict up front

Build **`ggraphify`: a GTK4 + libadwaita application in Go (gotk4), Wayland-native, that
treats every local checkout as a row in one board and every graphify operation as a
supervised, streamable job against that row.**

Three decisions carry the whole design:

1. **The GUI never re-implements graphify.** graphify is a Python CLI (`graphifyy` 0.9.58,
   installed as a `uv` tool). `ggraphify` shells out to it for every *mutation*
   (`extract`, `update`, `cluster-only`, `label`, `export …`, `global add`, `hook install`)
   and streams its stdout/stderr into the UI. There is no embedded Python, no library
   binding, no re-implementation of extraction.
2. **The GUI reads state from the filesystem, not from the CLI.** Almost no graphify
   command emits JSON (only `god-nodes --json` and `diagnose multigraph --json` do). But
   every fact the board needs to render a row is already on disk in `graphify-out/`:
   `manifest.json` (per-file `mtime` + `ast_hash`), `graph.json` (nodes/links/
   `built_at_commit`), `.graphify_labels.json`, `needs_update`, `GRAPH_REPORT.md`,
   `graph.html`. Status is derived by reading those files — cheap, offline, and correct
   even when graphify is not installed.
3. **Extraction costs money and minutes; the UI must make that impossible to trigger by
   accident.** AST-only `update` is free and can fan out wide. LLM-backed `extract` is
   rate-limited, serialized by default, and gated behind an explicit confirm that names
   the backend, the model, and the repo count.

Suggested first milestone: **M0 + M1 + M2** — discovery, the board with derived status, and
`update` as the single runnable action. That is already a useful tool, and everything after
it is additive.

---

## 1. What graphify actually is — verified facts this plan rests on

Checked on this machine, 2026-09-12:

| Fact | Value |
| --- | --- |
| Binary | `/home/dns/.local/bin/graphify` → `~/.local/share/uv/tools/graphifyy/bin/graphify` |
| Kind | Python 3.13 console script, package `graphify`, distribution `graphifyy` |
| Version | `graphify 0.9.58` (`graphify version`) |
| Sibling binary | `graphify-mcp` (MCP server — out of scope, see §13) |
| Output dir | `graphify-out/` by default, overridable per-process with `$GRAPHIFY_OUT` (relative name or absolute path) |
| Global graph | `~/.graphify/global-graph.json` (does not exist yet on this machine) |
| Repos on this box | 128 `.git` dirs under `/home/dns/git`; **6** have a `graphify-out/` |

### 1.1 The command surface the GUI drives

Grouped by what the GUI will actually expose:

- **Build / refresh:** `extract <path>` (full AST + semantic LLM; flags `--backend`,
  `--model`, `--mode deep`, `--force`, `--max-workers`, `--token-budget`,
  `--max-concurrency`, `--api-timeout`, `--out`, `--no-gitignore`, `--no-cluster`,
  `--code-only`, `--postgres DSN`, `--cargo`, `--global`, `--as <tag>`),
  `update <path>` (AST-only, no API cost; `--force`, `--no-cluster`),
  `cluster-only <path>`, `label <path>` (`--missing-only`, `--backend`, `--model`,
  `--max-concurrency`, `--batch-size`), `watch <path>`, `check-update <path>`.
- **Query / inspect:** `query "<q>"` (`--dfs`, `--context`, `--budget`, `--graph`),
  `path "A" "B"`, `explain "X"`, `affected "X"` (`--relation`, `--depth`),
  `god-nodes` (`--top`, `--json`), `diagnose multigraph` (`--json`), `benchmark`.
- **Visualize / export:** `export html`, `export callflow-html`, `export obsidian`,
  `export wiki`, `export svg`, `export graphml`, `export neo4j`, `export falkordb`,
  `tree` (D3 collapsible tree HTML).
- **Cross-repo:** `merge-graphs <g1> <g2> …`, `global add <graph.json> --as <tag>`,
  `global remove <tag>`, `global list`, `global path`.
- **Integration:** `install --platform P` / `uninstall`, the per-platform
  `claude|codex|cursor|opencode|gemini|…` install/uninstall pairs, `hook install|uninstall|
  status` (git post-commit/post-checkout), `clone <github-url>`, `add <url>`.
- **Memory loop:** `save-result`, `reflect`, `prs`, `provider [list|show|add|remove]`.

### 1.2 On-disk state, i.e. what a row is derived from

```
<repo>/graphify-out/
  graph.json              # {directed, multigraph, graph, nodes[], links[], hyperedges[], built_at_commit}
  manifest.json           # {"<rel path>": {mtime, ast_hash, semantic_hash}}
  GRAPH_REPORT.md         # architecture report
  graph.html              # interactive viz (1.9 MB on the docsboard graph)
  GRAPH_TREE.html         # from `graphify tree`
  .graphify_labels.json   # community labels  (+ .sig)
  .graphify_root          # root marker
  needs_update            # flag file: semantic re-extraction pending
  cache/                  # incremental extraction cache
  memory/ reflections/    # Q&A feedback loop
  wiki/                   # `export wiki` output (index.md is the navigation entry)
```

Node shape (from the docsboard graph): `{label, file_type, source_file, source_location,
_origin, id, community, community_name, norm_label}`. Scale reference: 2 047 nodes /
4 835 links = 2.4 MB of JSON.

### 1.3 Environment graphify reads

`GRAPHIFY_OUT`, `GRAPHIFY_OUT_NAME`, `GRAPHIFY_FORCE`, `GRAPHIFY_MAX_WORKERS`,
`GRAPHIFY_MAX_CONTEXTS`, `GRAPHIFY_MAX_OUTPUT_TOKENS`, `GRAPHIFY_MAX_RETRIES`,
`GRAPHIFY_API_TIMEOUT`, `GRAPHIFY_API_KEY`, `GRAPHIFY_LLM_TEMPERATURE`,
`GRAPHIFY_DISABLE_THINKING`, `GRAPHIFY_NO_INCREMENTAL_CACHE`, `GRAPHIFY_NO_BACKUP`,
`GRAPHIFY_NO_TIPS`, `GRAPHIFY_DEBUG`, `GRAPHIFY_LOG`, `GRAPHIFY_QUERY_LOG`,
`GRAPHIFY_BIN`, `GRAPHIFY_PYTHON`, `GRAPHIFY_HOOK_STRICT[_TTL]`, `GRAPHIFY_CHANGED`,
`GRAPHIFY_MTIME_GRANULARITY_MS`, `GRAPHIFY_MAX_GRAPH_BYTES`, `GRAPHIFY_BUILD_PERF`,
plus per-backend `GRAPHIFY_{GEMINI,OPENAI,DEEPSEEK,BEDROCK,AZURE,CLAUDE_CLI}_MODEL`,
`GRAPHIFY_OLLAMA_*`, `GRAPHIFY_ALLOW_LOCAL_PROVIDERS`, `GRAPHIFY_GOOGLE_WORKSPACE[_TIMEOUT]`.

`ggraphify` composes these per job rather than exporting them globally, so two concurrent
jobs can target different backends. **API keys are read from the environment and passed
through; they are never stored in `ggraphify`'s own state file and never rendered.**

---

## 2. Stack — what to build it in, and why

### 2.1 The choice

| Layer | Choice | Version on this box |
| --- | --- | --- |
| Language | **Go 1.27** | `go1.27.0 linux/amd64` |
| Toolkit | **GTK 4** via `github.com/diamondburned/gotk4/pkg` | GTK 4.22.4 |
| Design system | **libadwaita 1** via `gotk4-adwaita` | libadwaita 1.9.3 |
| Embedded web view (graph.html) | **WebKitGTK 6.0**, dlopen'd at run time | 2.52.6 |
| Embedded terminal (optional, job log) | **VTE 2.91-gtk4**, dlopen'd at run time | 0.84.1 |
| Source display (GRAPH_REPORT.md, SQL/py snippets) | **GtkSourceView 5** (optional) | 5.20.0 |
| Session | **Wayland**, `XDG_SESSION_TYPE=wayland` | wayland-1 |

GTK4 is a first-class Wayland client: no XWayland, no `GDK_BACKEND` override, correct
fractional scaling, and the app-id set on the `GtkApplication` becomes the Wayland
`app_id` that sway/fuzzel key their rules off.

### 2.2 Why Go + gotk4 rather than the alternatives

- **Go + gotk4 (chosen).** The reference app on this machine is already this stack and
  the hard parts are solved there: `internal/vte` dlopen binding, GC tuning, the GTK
  main-thread discipline, the `ColumnView` + sort/filter model, PGO, the rebuild-on-launch
  launcher. Static binary, no runtime deps beyond the system GTK. Go's concurrency is
  exactly the shape of this problem: N repos × M supervised subprocesses, each streaming.
- **Rust + gtk4-rs / relm4** — equally native and arguably a nicer binding, but nothing on
  this desktop is Rust, and it buys no capability this app needs. Rejected on ecosystem
  continuity, not merit.
- **Python + PyGObject** — tempting since graphify itself is Python (could import
  `graphify.*` directly instead of shelling out). Rejected: it couples the GUI's runtime to
  graphify's, so a `uv tool upgrade` can break the GUI; it inherits the GIL for the
  scan/parse work; and it makes packaging a shipped venv. Shelling out to a *pinned CLI
  contract* is the looser and more durable coupling.
- **Tauri / Electron / any webview-shell** — rejected. Not Wayland-native in the sense
  asked for, and the one genuinely web-shaped surface (graph.html) is handled by embedding
  WebKitGTK in a GTK4 window, which is strictly less machinery.
- **Qt6/QML, Slint, iced, Fyne** — all workable; all off-theme for a libadwaita desktop and
  none has docsboard's proven patterns behind it.

### 2.3 Runtime-optional native deps (dlopen, not pkg-config)

Copy docsboard's `internal/vte` approach exactly: bind WebKitGTK and VTE through `dlopen`
at run time, not `#cgo pkg-config` at link time. A missing `webkitgtk-6.0` must degrade to
"the Visualize tab opens graph.html in `xdg-open` instead", never to "the app will not
start". Only `gtk4` is a hard link-time dependency.

---

## 3. Architecture

```
ggraphify/
  main.go                     flags, XDG dirs, GC tuning, app bootstrap
  Makefile                    build / run / install / uninstall / pgo
  packaging/
    dev.dns.ggraphify.desktop
    dev.dns.ggraphify.svg
    ggraphify-run             rebuild-from-source launcher (docsboard pattern)
  cmd/
    ggraphify-scan/           headless: print the discovered repo table as JSON
    ggraphify-job/            headless: run one job, stream to stdout (CI / debugging)
  internal/
    discover/     find checkouts; git metadata; ignore rules; cached walk
    gfy/          the graphify CLI contract: argv builders, env composition, version probe
    graphstate/   read graphify-out/: manifest, graph.json, labels, needs_update, drift
    jobs/         job queue, supervision, streaming, cancel, concurrency policy, history
    store/        sidecar state: ~/.local/share/ggraphify/state.json (+ caches)
    webview/      dlopen WebKitGTK 6 binding (optional)
    vte/          dlopen VTE binding (optional, job-log terminal)
    open/         xdg-open, $EDITOR-in-a-terminal, file manager
    ui/           all GTK: board, detail, viz, query, jobs, settings, keys, help
```

Rules, inherited from docsboard because they are what keep it responsive:

- **Everything in `internal/ui` runs on the GTK main thread. Nothing else does.** Workers
  talk back with `glib.IdleAdd`, one small typed message per update.
- **No package outside `internal/ui` imports GTK.** `discover`, `gfy`, `graphstate`,
  `jobs`, `store` are pure Go and unit-testable headless — that is what `cmd/ggraphify-scan`
  and `cmd/ggraphify-job` exist to prove.
- **`ggraphify` never writes inside a repo except through graphify itself.** Its own state
  is a sidecar under `$XDG_DATA_HOME/ggraphify/`. The single exception is an explicit,
  confirmed `hook install` / `claude install`, which is graphify's write, not ours.

---

## 4. Domain model

### 4.1 Discovery

```go
type Repo struct {
    Path      string   // absolute checkout root
    Name      string   // display name (dir base; disambiguated on collision)
    Group     string   // parent dir under the scan root, for grouping
    IsWorktree bool    // .git is a file → linked worktree (e.g. /home/dns/git/.wt-tech17302)
    Branch    string   // current branch (read from .git/HEAD, no git subprocess)
    HeadSHA   string   // resolved HEAD
    Dirty     bool     // optional, cheap-path only (see below)
    Out       string   // graphify-out dir in effect (honours per-repo GRAPHIFY_OUT override)
}
```

- **Roots are configurable, multi-valued, default `~/git`.** Walk with a depth cap
  (default 3) and hard skips for `node_modules`, `.venv`, `target`, `vendor`, `dist`,
  `.cache`, `graphify-out` itself. 128 repos at depth 2 here — the walk must stay under
  ~50 ms warm, so cache the listing keyed on each root's mtime, as `internal/scan/cache.go`
  does in docsboard.
- **Git metadata without subprocesses.** Read `.git/HEAD` and `.git/refs/…` /
  `packed-refs` directly. A `git status` per repo × 128 repos is not affordable on a
  refresh tick; dirtiness is therefore **opt-in per row** (computed when a row is selected)
  rather than on the board.
- **Linked worktrees are first-class rows,** because `.wt-tech17302` here has its own
  `graphify-out/`. A worktree row shows its parent repo in the Group column.

### 4.2 Derived graph state — the heart of the board

```go
type State int
const (
    StateNone    State = iota // no graphify-out/ — never initialized
    StateRaw                  // graph.json exists, no labels (--no-cluster / cluster pending)
    StateStale                // manifest drift, or needs_update flag present
    StateFresh                // manifest matches the tree; labels present
    StateBroken               // graphify-out/ exists but graph.json is missing/corrupt
    StateRunning              // a job for this repo is in flight
)

type GraphState struct {
    State        State
    Nodes, Links int
    Communities  int
    Labeled      bool      // .graphify_labels.json present and non-placeholder
    BuiltAt      time.Time // graph.json mtime
    BuiltCommit  string    // graph.json "built_at_commit"
    DriftAdded   int       // files in tree, absent from manifest
    DriftChanged int       // files whose mtime is newer than the manifest entry
    DriftRemoved int       // manifest entries with no file
    NeedsUpdate  bool      // graphify-out/needs_update exists
    SizeBytes    int64
    HasHTML      bool
    HasWiki      bool
    HasReport    bool
}
```

**Drift is computed locally, never by running graphify.** `manifest.json` carries
`{mtime, ast_hash, semantic_hash}` per file; walking the repo and comparing mtimes answers
"how far has this graph fallen behind?" in milliseconds, with no API cost and no
subprocess. `BuiltCommit` vs current `HEAD` gives the same answer in commit terms and is
the one to show when they disagree. This is the single most valuable thing the GUI adds
over the CLI: **128 repos, each with an honest freshness number, on one screen.**

**graph.json is never parsed on the main thread.** 2.4 MB for a mid-size repo; parse in a
worker with a streaming `json.Decoder` that keeps only the counters and community set, and
memoise the result in the sidecar keyed on `(path, size, mtime)` — the docsboard scan-cache
pattern. A full node/link load happens only when the detail pane actually needs it.

---

## 5. The job runner (`internal/jobs`)

Every mutation is a `Job`:

```go
type Job struct {
    ID       string
    Repo     string      // repo path
    Kind     Kind        // Extract|Update|Cluster|Label|ExportHTML|ExportWiki|Tree|
                         // GlobalAdd|HookInstall|PlatformInstall|Watch|CheckUpdate|Merge
    Argv     []string    // exactly what will be exec'd, shown in the UI before it runs
    Env      []string    // composed GRAPHIFY_* overlay on os.Environ()
    Cost     Cost        // Free (AST/local) | Metered (LLM-backed)
    Started, Ended time.Time
    Exit     int
    Log      *ringbuf.Buf // bounded, ~256 KB, the last N lines are what the UI renders
}
```

Design points:

- **Two lanes with separate concurrency limits.** `Free` (update, cluster-only, exports,
  tree, check-update, hook/platform install) defaults to `min(4, NumCPU/2)`. `Metered`
  (extract, label) defaults to **1** and is never raised implicitly. Both are settings.
- **Process group per job.** `Setpgid`, so cancel is `SIGTERM` to the group then `SIGKILL`
  after a grace period — a Python parent that has spawned AST worker subprocesses must not
  leave orphans. Same discipline as `internal/sessions/kill.go` in docsboard.
- **Streaming, bounded, non-blocking.** Read stdout+stderr through a pipe into a ring
  buffer on a worker goroutine; the UI renders from the ring on a tick. A repo that emits
  100 MB of debug output must not grow the heap or stall the frame.
- **The argv is visible before it runs.** Every confirm dialog shows the literal command
  line. This is both the trust story and the debugging story: copy it, paste it in a
  terminal, get the identical result.
- **Metered jobs need an explicit confirm naming backend, model and repo count.** A
  fan-out `extract` over 128 repos is a real amount of money; the dialog says so, and
  batch metered runs additionally require typing the repo count. Free jobs run on one key.
- **Job history is persisted** (last ~200 jobs: kind, repo, argv, exit, duration, first
  error line), so "what did this do last Tuesday, and did it fail?" is answerable.
- **Failure is a first-class row state,** not a toast that disappears. A failed job pins an
  error badge on the repo row until acknowledged or superseded.

### 5.1 The graphify contract (`internal/gfy`)

One package owns every assumption about the CLI:

- Resolve the binary: `$GRAPHIFY_BIN`, then `PATH`, then `~/.local/bin/graphify`.
- Probe `graphify version` once at start, cache it, and **pin the supported range**. The
  argv builders are versioned; an unknown major/minor shows a banner ("built against
  0.9.58, found 0.10.x — commands may have moved") rather than failing mysteriously.
- Detect the platform-skew warning graphify prints on every invocation
  (`warning: skill at … is from graphify 0.9.35, package is 0.9.58`) and surface it as a
  one-click **"Update agent skills"** action running `graphify install --platform claude`.
  That warning is real on this machine right now.
- Strip that warning from parsed output — it goes to stderr ahead of real output and would
  otherwise corrupt naive parsing of `god-nodes --json`.
- Never assume JSON. Only `god-nodes --json` and `diagnose multigraph --json` are parsed as
  JSON; everything else is treated as opaque log text for display.

---

## 6. UI surfaces

libadwaita shell: `AdwApplicationWindow` + `AdwNavigationSplitView`, `AdwToastOverlay`,
`AdwPreferencesDialog` for settings, `AdwAlertDialog` for confirms. App-id
`dev.dns.ggraphify`; the window carries it as the Wayland `app_id`.

### 6.1 Board (primary view)

A `GtkColumnView` over all discovered repos, sortable and filterable on every column:

| Column | Content |
| --- | --- |
| ● | state dot: none / raw / stale / fresh / broken / running (+ error badge) |
| Repo | name, with group as dim subtitle; worktrees marked |
| Branch | branch @ short SHA; red when `built_at_commit` ≠ HEAD |
| Graph | `2047n / 4835e`, dimmed when absent |
| Comm. | community count; "unlabeled" badge when `.graphify_labels.json` is missing |
| Drift | `+12 ~30 −3` (added/changed/removed vs manifest), or `needs_update` |
| Built | relative age of graph.json |
| Size | graphify-out total bytes |
| Job | live job kind + elapsed + a spinner, or last exit status |

Header bar: search entry (fuzzy over name/group/path), a state filter chip row
(`all / none / stale / fresh / broken / running`), a root selector, and a **Select** mode
for batch actions. Status bar: `N repos · M graphed · K stale · J running`, plus the
resolved graphify version.

### 6.2 Detail pane (selected repo)

`AdwViewStack` with these pages:

1. **Overview** — full `GraphState`, the exact `graphify-out` path, per-repo overrides
   (backend, model, `GRAPHIFY_OUT`, extra flags, exclude-from-batch), and the action row:
   *Init / Update / Re-cluster / Label / Export / Visualize / Add to global / Hooks*.
   Each button's tooltip is the argv it will run.
2. **Report** — `GRAPH_REPORT.md` rendered (GtkSourceView or a minimal markdown → Pango
   pass), with a link to open it in `$EDITOR`.
3. **Visualize** — the embedded WebKitGTK view over `graphify-out/graph.html`, with a
   switcher for `GRAPH_TREE.html` (from `graphify tree`) and the callflow HTML. Buttons:
   regenerate (`export html` / `tree` / `export callflow-html`), open externally
   (`xdg-open`), copy path. **When WebKitGTK is unavailable, this page becomes a single
   "Open in browser" button** — no crash, no empty pane.
4. **Wiki** — when `graphify-out/wiki/index.md` exists, a navigable article list. This is
   what CLAUDE.md already points agents at for broad navigation.
5. **Query** — a console over the read-only commands: `query`, `explain`, `path A B`,
   `affected X`, `god-nodes`, `diagnose multigraph`, `benchmark`. Input row, budget/depth
   controls, monospace result view, history, copy-to-clipboard, and a "save this result"
   action wired to `graphify save-result` (the memory/feedback loop). `god-nodes --json`
   renders as a sortable table rather than text.
6. **Jobs** — this repo's job history, each expandable to its full log.

### 6.3 Global surfaces

- **Jobs view** (all repos): the run queue, running jobs with live logs, history, cancel,
  retry, and "copy command line".
- **Global graph view**: `graphify global list` / `global path`, add-selected-repos
  (`global add <graph.json> --as <tag>`), remove a tag, and `merge-graphs` over a chosen
  subset into a named output. This is the cross-repo answer the CLI makes tedious.
- **Integrations view**: per-platform install state (claude / codex / cursor / opencode /
  gemini / …) with the skill-version skew flag from §5.1, and git-hook status per repo
  (`hook status`) with install/uninstall.
- **Settings** (`AdwPreferencesDialog`): scan roots and depth, refresh interval, default
  backend + model, concurrency per lane (free/metered), the `GRAPHIFY_*` overlay,
  confirm-before-metered threshold, colour scheme (system/light/dark), terminal + editor
  for external opens, log ring size. Persisted in the sidecar; a value supplied on the
  command line is shown pinned and read-only, exactly as docsboard's settings screen does.

### 6.4 Keys

Vim-ish and single-key, matching docsboard's feel: `j/k` move, `/` search, `g/G` ends,
`Enter` open detail, `u` update, `E` extract (confirm), `c` cluster, `l` label,
`v` visualize, `w` wiki, `q` query console, `Space` select for batch, `x` cancel job,
`r` rescan, `J` jobs view, `,` settings, `?` help overlay (`AdwShortcutsDialog`), `Esc`
back/close. Every binding is listed in the help overlay and in the README.

---

## 7. Refresh and watching

- **Periodic rescan** (default 30 s, configurable): the cached discovery walk + a
  `graphify-out` stat per repo. Only rows whose `(size, mtime)` tuple moved are re-derived.
- **inotify on `graphify-out/`** for repos currently visible or running: `graph.json`,
  `manifest.json`, `needs_update` — so a CLI run in a terminal, or a git hook firing after
  a commit, updates the board within a frame or two. Debounced; a narrow kick must not
  trigger a full walk (this is docsboard's S1–S3 lesson: the narrow pass has to *stay*
  narrow).
- **`graphify watch <path>` as a managed job**: start/stop per repo from the UI, with the
  watcher's output in the job log. A watched repo shows a distinct badge. Watchers are
  children of the GUI process and do not outlive it.
- **Optional `check-update` sweep** on a timer, mirroring the cron-safe CLI command, to
  flag repos with a pending semantic re-extraction.

---

## 8. Persistence

`$XDG_DATA_HOME/ggraphify/` (i.e. `~/.local/share/ggraphify/`):

- `state.json` — settings, per-repo overrides, window geometry, column widths/order/sort,
  last selection, filter chips. Atomic write (temp + `os.Replace`), debounced, versioned
  with a `version` field and forward-compatible defaults for keys added later (docsboard's
  `store` tests exist precisely to pin that upgrade behaviour).
- `repos-cache.json` — the discovery walk, keyed on root mtimes.
- `graph-cache.json` — derived `GraphState` per repo keyed on `(graph.json size, mtime)`.
- `jobs.json` — bounded job history.

Nothing here is secret. **API keys are never written to any of these files** — the backend
setting stores a *variable name* or backend id, never a value.

---

## 9. Packaging and build

Straight port of docsboard's packaging, which is already tuned for this desktop:

- `Makefile` targets: `build`, `run`, `install`, `uninstall`, `hooks`, `pgo`, `clean`.
- `make install` puts **`packaging/ggraphify-run` on `PATH`, not the binary**: the launcher
  rebuilds from the checkout (`flock`-guarded, mtime-gated staleness check, build to a temp
  name then rename to dodge `ETXTBSY`) and then execs the result — so fuzzel always starts
  current source and `make install` is a one-time step.
- `dev.dns.ggraphify.desktop` with `StartupWMClass=dev.dns.ggraphify` matching the Wayland
  app_id, `Categories=Development;`, an SVG icon in the hicolor theme.
- Pin `GSK_RENDERER=vulkan` in the launcher, respecting a caller override. docsboard's
  measured A/B on this exact hardware: vulkan p95 full-window repaint 9.0 ms vs GL
  17.9–32.8 ms and +87 MB RSS; cairo is the no-GPU fallback. No reason to re-measure.
- `debug.SetGCPercent(100)` and a soft memory limit, as measured there.
- `default.pgo` via `make pgo` once there is a real usage profile.
- Failure notifications from the launcher via `notify-send`, since a `.desktop` launch has
  no terminal to print to.

---

## 10. Testing

- **Headless-first.** `discover`, `gfy`, `graphstate`, `jobs`, `store` have no GTK import
  and are tested with `t.TempDir()` fixtures: synthetic repos, hand-written
  `manifest.json`/`graph.json`, a fake `graphify` script on `PATH` that echoes its argv and
  exits with a chosen code. That fake is the whole contract test for §5.1.
- **Golden argv tests** for every command builder — the flag surface in §1.1 is large and
  the failure mode ("we passed `--max-concurrency` where the command wants
  `--max-workers`") is silent and expensive.
- **Drift tests** over a fixture tree: added/changed/removed files produce the expected
  counts, and a `needs_update` flag wins over a clean manifest.
- **Big-graph benchmark**: parse a 2.4 MB `graph.json` and assert the derive stays under a
  budget and does not allocate proportionally to node count for the counters-only path.
- **Cancellation test**: a fake graphify that spawns a child and ignores `SIGTERM` must
  still be reaped, with no orphan left behind.
- **UI smoke** only where it is cheap — key routing, sort chains, filter predicates — as
  docsboard does with its `chord_test.go` / `sortchain_test.go`.

---

## 11. Milestones

| # | Deliverable | Contents |
| --- | --- | --- |
| **M0** | Skeleton that runs | `go.mod`, GTK4+Adw window, app-id, Makefile, packaging, `--version`, dark/light, empty board. Prove the launcher + fuzzel path end to end. |
| **M1** | Discovery + derived state | `internal/discover`, `internal/graphstate`, `cmd/ggraphify-scan` printing the table as JSON. No GUI actions yet. Tests for drift. |
| **M2** | The board + first action | ColumnView with all columns, search/filter/sort, periodic rescan, the job runner, and exactly one action: **`update`** (free, AST-only). Job log pane. This is the first genuinely useful build. |
| **M3** | The full action set | `extract` (with the metered confirm), `cluster-only`, `label`, `check-update`, per-repo overrides, batch/select mode, job history, failure badges. |
| **M4** | Visualize | WebKitGTK dlopen binding, the Visualize page over `graph.html` / `GRAPH_TREE.html` / callflow, regenerate actions, external-open fallback. Report + Wiki pages. |
| **M5** | Query console | `query` / `explain` / `path` / `affected` / `god-nodes` / `diagnose`, result history, `save-result` and `reflect`. |
| **M6** | Cross-repo + integrations | Global graph view (`global add/remove/list`, `merge-graphs`), integrations view (platform installs, skill-version skew, git hooks), `watch` as a managed job. |
| **M7** | Polish | Settings dialog complete, help overlay, README, PGO profile, icon, notifications. |

M0–M2 is the honest MVP. Everything from M3 on is additive and independently shippable.

### 11.1 What actually landed (2026-09-12)

| # | State | Notes |
| --- | --- | --- |
| **M0** | **done** | `go.mod`, GTK4+Adw shell, app-id `dev.dns.ggraphify`, Makefile, `packaging/` (launcher + `.desktop` + SVG icon), `--version`, dark/light/system. |
| **M1** | **done** | `internal/discover`, `internal/graphstate` (+ its `(size, mtime)` cache), `internal/board`, `cmd/ggraphify-scan` with table and `-json` output. Drift, worktree, packed-refs and phantom-drift tests. |
| **M2** | **done** | Nine-column `ColumnView` with search, state chips, per-column sort and persisted widths; periodic rescan; `internal/jobs` with two lanes, process groups and bounded streaming; `update` wired. |
| **M3** | **done** | `extract` / `label` behind the metered confirm, `cluster-only --no-label`, `check-update`, per-repo overrides, select mode with a typed-count gate, persisted job history, failure pinned on the row. |
| **M4** | **done** | `internal/webview` — WebKitGTK 6 via `dlopen`, with the "Open in browser" degradation. Visualize (graph / tree / callflow), Report and Wiki pages. |
| **M5** | **done** | Query console over `query`/`explain`/`path`/`affected`/`god-nodes`/`diagnose`/`benchmark`, JSON pretty-printing, `save-result`. |
| **M6** | **done** | Global-graph view (`global list/add/remove`, `merge-graphs`), `watch` as a managed job, `hook install/status`, platform install for the skill-skew fix. |
| **M7** | **partial** | Settings dialog, help overlay (`AdwShortcutsDialog`), README and icon are done. **Not done:** `default.pgo` (needs a real usage profile), `graphify prs`, desktop notifications from the app itself (the launcher already uses `notify-send`). |

### 11.2 Preconditions (2026-09-12, post-M7)

`label` on a never-extracted checkout reached graphify and came back with "no graph found
at …/graph.json — run /graphify first" — after the metered confirm had already named a
bill. The plan tracked cost as a command's only property; needing an existing graph is the
second one, and it was nowhere.

Fixed structurally rather than at the button: `gfy.Spec.NeedsGraph` declares it next to
`Cost`, and `jobs.Options.Precheck` — consulted by `Runner.SubmitCmd`, installed as
`jobs.RequireGraph` by both the GUI and `ggraphify-job` — enforces it on the one path every
job takes, against a `stat` of the live filesystem rather than the last board scan. The
dimmed action rows and the "no graph yet" dialog offering `extract` / `update` sit above
that as affordances, not as the enforcement. Tests: the `gfy` invariant that any argv
naming `graph.json` is marked `NeedsGraph`, `jobs` coverage of every gated kind both ways,
and `graphstate.HasGraph` on the empty-file and directory cases.

Three decisions differ from what is written above; each is recorded in README.md under
"Deviations from the plan":

1. The shell is a `GtkPaned`, not an `AdwNavigationSplitView` (§6) — a navigation split
   view makes the sidebar the small half, and here the board is the large half.
2. `gfy.Backends` is `gemini kimi claude claude-cli openai deepseek ollama`, verbatim from
   `graphify --help` at 0.9.58, not the `claude-cli/bedrock/azure` list guessed at in §1.3.
3. VTE is not bound (§2.3). graphify's output is line-oriented and carries no terminal
   control sequences, so the job log is a `GtkTextView` over the ring buffer.

Open question 6 (§12) is resolved as proposed: all three roots, `~/git` first, editable in
settings. Open question 7 is resolved as proposed: the only write inside a repository is a
confirmed `hook install` / platform install, never on a batch path.

### 11.3 Bottom panel and the Claude Code check (2026-09-12, post-M7)

Two surfaces the plan did not have, both added because the board was silent in a way that
only shows up once it is being used every day.

**The bottom panel (§6, new).** The queue was reachable only through a header popover and
the `J` dialog, and ggraphify's own log was reachable nowhere at all — a board launched
from fuzzel has no terminal, so `log.Printf` went to a stream nobody sees. The panel is a
`GtkPaned` below the board/detail split with a two-page `GtkStack`:

- **Jobs** — running, then queued, then recently finished; rebuilt only when the set moves
  (`dockKey`), with elapsed times repainted in place on the one-second tick.
- **Log** — `internal/applog`, a bounded, level-tagged, mutex-guarded ring of entries that
  is also the `io.Writer` `log.SetOutput` is pointed at in `main.go`. GTK's structured
  glib lines are classified on the way in: `INFO`/`DEBUG` become debug (hidden unless
  *Verbose*), `WARNING`/`CRITICAL` keep their level, and the `priority=…
  syslog_identifier=…` tail is trimmed. **Copy all** emits a markdown diagnostics report —
  both versions, GTK/libadwaita, session and renderer, roots, graph storage, resolved
  backend and whether it has a credential, board counters, the Claude Code report, the
  last fifteen jobs with argv, then the whole log including debug.

Both pages are on by default and each is switchable off on its own; `HideJobsBar` /
`HideLogBar` / `HideBottom` are stored as *hide* flags so a state file written before the
panel existed reads back as "shown". `b` toggles the panel, `L` opens it on the log, and
the panel never takes more than half the paned's height — a tiling compositor can hand
this window a very short cell, and a remembered height from a tall one would otherwise
leave the board with two rows.

**The Claude Code check (§6.3, new).** `graphify install --platform claude` writes the
skill, its references and the pointer line in `~/.claude/CLAUDE.md`; when any of them is
missing or stale nothing errors, the agent simply never mentions the graph.
`gfy.InspectClaude` reads all of it — honouring `$CLAUDE_CONFIG_DIR`, comparing
`.graphify_version` against the installed package — and returns one `Check` per fact with
an ok/warning/problem state and whether re-running the installer would repair it.
Settings → Jobs shows a row each, plus *Check* and a *Fix* that runs the installer as an
ordinary confirmed job; the banner's "Update agent skills" now goes through the same
`installClaudeSkill`, and a successful install re-probes the version and re-runs the check.

---

## 12. Risks and open questions

1. **CLI drift.** graphify moves fast (0.9.58 today; flags and subcommands have clearly
   churned). Mitigation: the version probe + pinned argv builders + golden tests in §5.1,
   and treating unparsed output as opaque text. **Do not** build features that depend on
   scraping human-readable output — only `--json` outputs and on-disk files are contracts.
2. **Cost of a fan-out `extract`.** 128 repos × an LLM-backed extraction is the one way
   this GUI could do real damage. Mitigation: metered lane at concurrency 1, explicit
   typed confirm for batches, `--code-only` offered as the free alternative in the same
   dialog, and a visible running-cost hint (chunks dispatched) in the job log.
3. **`graph.json` size on large monorepos.** 2.4 MB here; a monorepo could be 50 MB+.
   Mitigation: counters-only streaming parse, the `(size, mtime)` cache, and a hard
   `GRAPHIFY_MAX_GRAPH_BYTES`-style ceiling above which the row shows size only and the
   detail pane offers "open externally" rather than loading.
4. **WebKitGTK memory.** A 1.9 MB graph.html with a force-directed D3 layout in an embedded
   webview is not free. Mitigation: one webview instance reused across repos, destroyed
   when the Visualize page is hidden, and the external-browser fallback always one click
   away.
5. **Worktrees sharing an output dir.** `GRAPHIFY_OUT` supports symlinked/shared outputs.
   Mitigation: resolve the real path of `graphify-out` per repo and de-duplicate rows that
   resolve to the same output, showing the sharing explicitly rather than double-counting.
6. **Open question — scan roots.** Default `~/git` only, or also `~/tmp` (where this repo
   and cc-docsboard live) and `~/.graphify/repos/` (where `graphify clone` puts things)?
   Proposed default: all three, each toggleable, `~/git` on by default.
7. **Open question — does `ggraphify` ever write to a repo?** Proposed: only via graphify's
   own `hook install` / platform installs, always confirmed, never on a batch path.

---

## 13. Non-goals

- **Not a graph editor.** Nodes and communities are read-only; graphify owns the graph.
- **Not an MCP client.** `graphify-mcp` exists and agents use it; the GUI is for a human at
  a desktop and does not proxy MCP.
- **Not a git client.** Branch and HEAD are shown because freshness needs them. No commits,
  no pulls, no merges. (`graphify prs` may be surfaced read-only in M6 at most.)
- **Not an LLM chat UI.** The Query console runs graphify's deterministic traversals, not a
  conversation.
- **No telemetry, no network of its own.** Every byte over the wire is graphify's, using
  the user's own keys.
- **No Artifact/hosted link.** Visual output is local HTML rendered in-process or via
  `xdg-open`.
