# ggraphify

A native Wayland GUI for [graphify](https://github.com/Graphify-Labs/graphify) across every
local repository: one board where each checkout is a row and each graphify operation is a
supervised, streamable job against that row.

GTK4 + libadwaita, written in Go with [gotk4](https://github.com/diamondburned/gotk4).
Wayland-native — no XWayland, correct fractional scaling, and the `app_id` sway and fuzzel
key their rules off.

Design of record: [`2026-09-12-ggraphify-wayland-gui-plan.md`](2026-09-12-ggraphify-wayland-gui-plan.md).

---

## What it is for

`graphify` is excellent at one repository at a time. It has no answer at all to *"which of
my 130 checkouts have a graph, and how far behind has each one fallen?"* — and the honest
answer to that costs money to get wrong, because bringing a graph up to date is sometimes
a free AST pass and sometimes an LLM extraction billed to your own API key — or, with no
key at all, to the Claude Code CLI already installed on the machine.

ggraphify is that answer on one screen, with the cost of every action made structural
rather than remembered.

## Three rules the whole thing rests on

1. **It never re-implements graphify.** Every mutation is a subprocess — `extract`,
   `update`, `cluster-only`, `label`, `export …`, `global add`, `hook install` — whose
   exact argv is shown before it runs and whose stdout streams into the UI. No embedded
   Python, no library binding, no second implementation of extraction to keep in step.

2. **It reads state from the filesystem, not from the CLI.** Almost no graphify command
   emits JSON. But every fact a row needs is already on disk in `graphify-out/`:
   `manifest.json` (per-file mtime and AST hash), `graph.json` (nodes, links,
   `built_at_commit`), `.graphify_labels.json`, `needs_update`, `GRAPH_REPORT.md`,
   `graph.html`. Status is derived by reading those — cheap, offline, and correct even on
   a machine where graphify is not installed.

3. **Extraction costs money and minutes, so the UI makes that impossible to trigger by
   accident.** AST work is *free* and fans out across four lanes. LLM-backed work is
   *metered*, runs one at a time, and is gated behind a confirm that names the backend,
   the model and the repository count. A batch of three or more additionally requires the
   count to be typed.

## What a row tells you

| Column | What it says |
| --- | --- |
| ● | none / unlabeled / stale / fresh / broken / running — by shape as well as colour |
| Repo | name, group, `⧉` for a linked worktree, `⊘` when excluded from batch actions |
| Folder | the scan root this checkout was found under, and what the folder filter narrows to |
| Branch | branch and short SHA; `≠` when the graph was built at a different commit |
| Graph | `2047n / 4835e` |
| Comm. | community count, or "83 unnamed" when the LLM labelling pass has not run |
| Drift | `+12 ~30 −3` added / changed / removed versus `manifest.json`, or `needs-extract` |
| Built | how long ago `graph.json` was written |
| Size | the whole output directory |
| Job | the live job and its elapsed time, or how the last one ended |

**Drift is computed locally and never by running graphify.** `manifest.json` carries an
mtime per file; walking the tree and comparing answers "how far behind is this graph?" in
milliseconds, with no API cost and no subprocess. `built_at_commit` versus `HEAD` answers
the same question in commit terms — and they are genuinely different questions, since a
branch switch moves the second without touching a single file's mtime, so the board shows
both rather than folding them together.

The walk has to skip **exactly** what graphify's extraction skips, or the number is
noise. graphify is `.gitignore`-aware (that is what its `--no-gitignore` flag turns off),
it does not descend into a nested checkout, and it classifies extensionless files by their
shebang — so the drift walk reads the repository's root `.gitignore`, treats any directory
holding a `.git` as somebody else's repository, always skips `graphify-out` even when the
graphs live in a central directory, and counts an extensionless file only when the manifest
already knows it. On the reference checkout, without those four rules, a graph that had
just been extracted reported `+4758 −2` — two agent worktrees under `.claude/`, a
gitignored `graft/` cache, a leftover in-tree `graphify-out/`, and two git hooks the walk
could never see — and the row could not reach `fresh` no matter what was run against it.

## Free versus metered

The split is not a label on a button; it is a property of the command, declared in
`internal/gfy` and enforced by the job runner.

| Free — fans out over four lanes | Metered — one at a time, always confirmed |
| --- | --- |
| `update` (AST only, no API key) | `extract` (full AST + semantic LLM) |
| `cluster-only --no-label` | `label` (names communities with an LLM) |
| `export html\|wiki\|svg\|graphml\|obsidian\|callflow-html` | |
| `tree`, `check-update`, `watch`, `benchmark` | |
| `query`, `explain`, `path`, `affected`, `god-nodes`, `diagnose` | |
| `global add\|remove\|list`, `merge-graphs`, `hook install` | |

`cluster-only` is free *because* ggraphify passes `--no-label`. Without that flag it
dispatches LLM calls to name the communities and costs exactly what `label` costs — so the
board offers the two shapes as two actions and decides the cost in the argv builder, not in
a dialog.

An unknown command kind defaults to **metered**. Defaulting the other way would mean a
command a future build has never heard of could fan out over every repository without a
confirm.

"Metered" means *an LLM runs*, not *an API key is charged*: with no key set and Claude Code
installed, the metered column runs through that CLI and is billed to its plan. See
[Backends](#backends-no-api-key-required).

## Preconditions

Cost is one property of a command; **needing a graph is the other**. Most of what the board
runs — `label`, `cluster-only`, every export, the whole query console — reads
`graphify-out/graph.json` and fails without one. graphify says so itself ("no graph found
at …/graph.json — run /graphify first"), but only *after* the job has started, and for a
metered command only after a confirm dialog has already named a bill.

So `NeedsGraph` is declared next to the cost in `internal/gfy`, and enforced in one place:
`jobs.Options.Precheck`, consulted by `Runner.SubmitCmd` before anything is queued.
`jobs.RequireGraph` is the rule both front ends install — the GUI and `ggraphify-job` — so
a new button, a new keyboard shortcut, a new pane or a cron line cannot submit around it.
The check is a `stat` of the live filesystem rather than a lookup in the last board scan: a
build that finished twenty seconds ago must not leave its own repository looking
ungraphable.

Above that sit two conveniences, neither of which is the enforcement: the graph-reading
action rows are dimmed for a repository with no graph, and invoking one anyway (by
shortcut, or on a batch where only some repositories qualify) opens a dialog that names the
missing file and offers the two commands that create one — `extract`, or the free `update`.
A refusal that does not name the next step is only half an answer.

## Fix — one button to healthy

`Fix` (`h`, or the first row of the detail page's Actions) takes a repository from whatever
state it is in to a **healthy** graph and stops there. Healthy has a definition, in
`graphstate.Healthy`, and it is exactly four things:

- `graph.json` exists and parses,
- the manifest matches the working tree (no drift, no `needs_update` flag),
- the graph has communities and they have **real names**, not `community_7` placeholders,
- `GRAPH_REPORT.md` is there to read them from.

`graph.html`, the wiki, the tree and call-flow exports are deliberately **not** in that
list. Those are renderings somebody asks for; a repository without them is not unhealthy,
and including them would make one click mean "regenerate 60 MB of HTML".

The plan is computed per repository by `internal/heal` — a pure function of the graph
state, with no filesystem and no GTK, so it is testable and it is the same plan the dialog
shows you before anything runs:

| what is wrong | what Fix runs |
| --- | --- |
| no graph at all | `extract` → `cluster-only --no-label` → `label --missing-only` |
| `graph.json` missing or unreadable | the same, with `--force` (graphify's shrink guard would otherwise refuse the rebuild) |
| drift against the manifest | `update` — free, and it clusters on the way |
| graphify's `needs_update` flag | `extract` (the flag means the AST pass alone cannot reconcile it) → `cluster-only` |
| extracted but never clustered | `cluster-only --no-label` → `label --missing-only` |
| communities with placeholder names | `label --missing-only` |

Three properties make it safe to give one button that much reach:

1. **Nothing new.** Every step is a command that already has its own button, its own cost
   and its own argv builder. Fix cannot do anything the board could not be asked to do one
   click at a time.
2. **The plan is shown in full first** — the defects, the numbered steps, the reason for
   each, and the literal command line — and a metered plan gets the same backend/model
   confirm, the same destructive styling and the same typed-count gate as any other spend.
   **Free steps only** is offered next to it, and it is a genuinely different plan rather
   than the same one with steps removed: without an LLM the right first step for an
   unextracted repository is `update`, not `extract`. It says what it will leave unfixed.
3. **Sequenced, not queued.** `jobs.SubmitChain` submits each step only after the previous
   one has succeeded — because a later step's precondition is created by an earlier one
   (`cluster-only` is refused while there is no `graph.json`), and because labelling the
   communities of a graph whose rebuild just died is a bill for a wrong answer. A batch fix
   is one chain **per repository**, so a failure on one does not stop the other nine.

`ggraphify-scan -issues` prints the same verdict without a display, and `-unhealthy`
filters to the rows the button would act on.

## Keys

Press `?` in the app, or run `ggraphify -h`. The help overlay and `-h` are generated from
the same table, and a test asserts that every key it documents is one the handler actually
dispatches.

```
j k        move            h  Fix (plan, then ask)   o R V K q  detail pages
/ f Esc    filter          u  Update (free)          J          all jobs
F s        folder filter   c  Re-cluster (free)      G          global graph
g          top             E  Extract (METERED)      v Space A  batch select
r          rescan          l  Label (METERED)        X          exclude from batch
Enter      open detail     e w  export html / wiki
t          colour scheme   W  watch on/off           ,  ?       settings, help
Ctrl+F/B   page down / up  x  cancel this row's job   b  L       bottom panel, its log
```

The filter box is **fuzzy**: the letters of a term have to appear in order, not next to
each other, so `npc` finds `nova-platform-console`. Several words all have to match, each
against a single field (name, folder, path or branch) rather than across two of them. A
term of one or two letters is matched as a plain substring — two letters in order match
almost anything. **Enter** hands the keyboard to the list; **Esc** clears the box.

## What is running right now

The header bar carries a spinner as soon as a job starts: `2 running · 1 queued`. Clicking
it drops a list of exactly the work that is in flight — each job's repository, its kind,
how long it has been going, a `$` when it is metered, and a cancel button — plus a way
into the full jobs view. It hides itself when the queue is empty, so an idle board does
not carry a widget that says zero.

The same information is in the status line at the bottom, and the **jobs view** (`J`, or
the list icon in the header) is the whole picture: every job across every repository,
queued, running or finished, with its live log, its exit status and its exact command line.

## The bottom panel

Both of the surfaces above have to be gone and got: a popover to open, a dialog to
present. The panel across the bottom of the window is the half of each that is worth
having on screen all the time. It has two pages, both on by default, each switchable off
on its own in **Settings → General → Bottom panel**; `b` hides and shows the whole panel,
and the height you drag it to is remembered.

**Jobs** is the run queue as a strip: running first, then queued, then the most recently
finished, each with its repository, how long it has been going, a `$` when it is metered
and a cancel button. Clicking a row moves the board's cursor to that repository.
*Finished* filters the ended ones out; *Open jobs view* is the same `J` dialog, where each
job's log and command line live.

**Log** is ggraphify's own account of itself — scans, job transitions, failures, and
everything it toasted at you. It exists because a board launched from fuzzel has no
terminal, so before it that account went nowhere at all. GTK's own narration is kept but
hidden behind *Verbose*, since a Vulkan loader enumerating devices at INFO would otherwise
fill the buffer before the first frame.

**Copy all** is the button the page is for. It puts the whole log on the clipboard — debug
entries included — under a description of the machine it came from: both versions, GTK and
libadwaita, the session and renderer, the scan roots, where graphs are kept, which backend
a metered job would use and whether it has a credential, the board's own counters, the
Claude Code integration report, and the last fifteen jobs with their exact argv. That is
the artefact worth pasting into an issue, or handing to an agent, instead of a sentence
beginning "it did not work".

## Folders

A board over 280 checkouts is two questions, not one: *which repositories*, and then
*which of those are interesting*. The folder dropdown in the filter bar is the first cut —
one entry per scan root, each followed by the directories under it that hold checkouts of
their own, every entry carrying its repository count:

```
All folders  (282)
~/git        (140)
   ↳ .wt       (2)
~/tmp        (142)
   ↳ n8-sweep (72)
```

Add `~/git` as a root and it becomes a folder you can narrow the whole board to. The
folder list is derived from the rows themselves on every scan, not from the configured
roots, so a root that currently holds nothing does not become a filter that can only ever
show zero repositories, and a directory cloned into this morning appears without a
restart. A directory holding a single checkout is deliberately not offered: narrowing to
it would show the one row selecting it already shows.

A group is a **path**, not a name — `~/git/work` and `~/oss/work` are two folders — and
membership is "this checkout is under that directory". `F` cycles the folders, `s` narrows
to the selected row's own folder (and back off), `Esc` clears it. It composes with
everything else: filter to `~/git`, chip to **Stale**, press `A` then `u` and you have
updated every stale graph in that one tree and nothing outside it. The **Folder** column
carries the root each row was found under, and sorts by it.

The choice is persisted, and a folder that has gone away — a root removed from settings,
the last checkout in a subdirectory deleted — is dropped rather than left in force over an
empty board.

Headless, the same derivation:

```sh
ggraphify-scan -groups                        # the folders, with counts
ggraphify-scan -group ~/git/nova -state stale # the rows in one of them
```

## Where it looks, and where the knowledge lives

Both halves of "which repositories, and where are their graphs" are settings, editable in
**Settings → General** (`,`) and pinnable from the command line for one run.

- **Repository folders** — the scan roots. Type them comma-separated, pick one with
  **Add folder…**, or fall back to **Use defaults** (`~/git`, `~/tmp`,
  `~/.graphify/repos`). Each root is walked independently to the configured depth; a root
  that does not exist is skipped.
- **Hide hidden directories** — on by default. Directories whose name starts with a dot,
  and every checkout inside them, stay off the board; turn it off to board them (that is
  how `.wt-…` worktree parks show up). A root that is itself hidden, like
  `~/.graphify/repos`, is scanned either way — naming a root is asking for what is in it.
- **Graph storage** — where each repository's graphify knowledge (`graph.json`, the
  manifest, the report, the wiki) lives. Either **inside each checkout**, under a
  directory name of your choosing (`graphify-out` by default), or **in one directory
  outside the checkouts**, which gets one subdirectory per repository — named after the
  checkout plus a short digest of its absolute path, so `~/git/api` and `~/tmp/api` never
  collide on one graph. The dialog shows the resolved path for the selected row, so the
  setting is never a guess.

One resolver answers this for the whole program: the board reads state from the directory
it names and every job is launched with `GRAPHIFY_OUT` pointing at the same one, so the
board and the subprocess cannot disagree about where a graph is. A per-repository override
on the detail page still outranks the board-wide setting, and `$GRAPHIFY_OUT` /
`$GRAPHIFY_OUT_NAME` remain the fallback when nothing has been configured — so a board and
a terminal in the same environment agree by default, while something you typed here wins
over something your shell profile exported.

**Changing where graphs live does not move the graphs that already exist.** It changes
where ggraphify looks and where its jobs write; repositories whose graph is still in the
old location come back as `none` until they are extracted again, or until their files are
moved across by hand.

```sh
ggraphify -roots ~/work,~/oss            # scan these for this run
ggraphify -hidden                        # board dot-named directories too
ggraphify -out-name .graphify-out        # in-tree, under a different name
ggraphify -out-base ~/.cache/graphify    # every graph outside the checkouts
```

A setting the command line decided is shown greyed out in the dialog, naming the flag that
pinned it, rather than silently disagreeing with what the board is doing.

## Detail pages

- **Overview** — every derived fact, the per-repository overrides (backend, model,
  graph directory, extra flags, exclude-from-batch), and the action list. Each action's
  tooltip is the literal command line it will run.
- **Report** — `GRAPH_REPORT.md`, with a button to open it in `$EDITOR`.
- **Visualize** — `graph.html`, `GRAPH_TREE.html` or the call-flow HTML in an embedded
  WebKitGTK view, with a regenerate button. **WebKitGTK is bound with `dlopen` at run
  time, not linked**: on a machine without `webkitgtk-6.0` this page becomes a single
  "Open in browser" button rather than a binary that will not start.
- **Wiki** — the articles under `graphify-out/wiki/`, which is what `CLAUDE.md` already
  points agents at for broad navigation.
- **Query** — a console over the read-only traversals: `query`, `explain`, `path A B`,
  `affected`, `god-nodes`, `diagnose multigraph`, `benchmark`. `--json` responses are
  pretty-printed; everything else is shown exactly as graphify printed it. "Save result"
  feeds the answer back through `graphify save-result` into `graphify-out/memory/`.
- **Jobs** — this repository's job log and its persisted history.

## It never writes inside a repository

ggraphify's own state is a sidecar under `$XDG_DATA_HOME/ggraphify/` — settings, per-repo
overrides, window geometry, column widths, and a bounded job history. The single exception
is an explicit, confirmed `hook install` or platform install, and that write is graphify's
own, never on a batch path.

**No API key is ever written to that file.** The backend is stored by name; a key is read
from the process environment at job time and passed through. Any overlay variable whose
name looks like a credential is masked wherever the board prints it.

## Is Claude Code actually wired up?

`graphify install --platform claude` writes three things: the agent skill under
`~/.claude/skills/graphify/`, its reference pages, and the pointer line in `~/.claude/CLAUDE.md`
that makes an agent reach for the skill at all. When one of them is missing or stale
nothing errors — the agent simply never mentions the graph, which is indistinguishable
from it having nothing to say.

**Settings → Jobs → Claude Code integration** answers that question directly. *Check*
re-reads all of it and reports one row per fact: graphify itself, the Claude Code CLI and
whether a bare `claude` finds the same binary the board did, the skill, its version stamp
against the installed package, its reference pages, and the CLAUDE.md pointer. Each row is
ok, a warning or a problem, by shape as well as by colour.

*Fix* re-runs `graphify install --platform claude` as an ordinary supervised job, behind a
confirm that names every path it will write. It is insensitive when there is nothing to
fix, and stays insensitive when what is wrong is not something the installer can repair —
a missing graphify, or no Claude Code on the machine at all. `$CLAUDE_CONFIG_DIR` is
honoured, so a board checking one directory while an agent reads another cannot happen.

The same report goes into the diagnostics the log page copies.

## Backends: no API key required

`graphify`'s metered commands — `extract`, `label`, and `cluster-only` when it is naming
communities — need an LLM. They do **not** need an API key of your own: `claude-cli` shells
out to the **Claude Code CLI installed on this machine** (`claude -p`) and runs against
whatever that CLI is already logged in as, which for a Pro/Max subscriber is the
subscription rather than a metered key.

The board picks it up on its own. `graphify`'s backend auto-detection is *key-based*, so it
can never select `claude-cli` — a machine with Claude Code and no key would auto-detect
nothing and every extraction would refuse. So when the backend is left on **auto-detect**:

| environment | backend ggraphify runs | shown as |
| --- | --- | --- |
| an API key is exported | blank — graphify detects from the key | `auto-detect` |
| no key, `claude` installed | `claude-cli`, passed explicitly | `auto-detect (→ claude-cli)` |
| neither | blank, and the confirm dialog says why the run will fail | `auto-detect` |

The resolved name goes into the argv the confirm dialog prints and the job log records, so
the substitution is on screen rather than behind it. Picking `claude-cli` in **Settings ▸
LLM defaults ▸ Backend** pins it regardless of what keys are set.

Three things worth knowing about this backend:

- **Concurrency is 1.** graphify serializes it, because parallel subprocesses fight over one
  Claude Code session. A fan-out over many repositories is slower here than against an API
  key, not broken. `GRAPHIFY_CLAUDE_CLI_PARALLEL=1` in the environment overlay lifts it, at
  your own risk.
- **The model is Claude Code's default**, which is heavier than this job needs. Set
  `GRAPHIFY_CLAUDE_CLI_MODEL` (e.g. `haiku`) in the overlay for something cheaper and faster.
- **`claude` is launched by bare name**, so an install outside the session `PATH` — the usual
  shape of a `.desktop` launch — would be invisible to graphify even though the board found
  it. ggraphify prepends the resolved directory to the `PATH` each job inherits. Settings ▸
  LLM defaults shows the path it resolved; `GGRAPHIFY_CLAUDE_BIN` overrides it, and
  `GGRAPHIFY_CLAUDE_BIN=off` hides an install the board should not use.

## Building and installing

Needs Go 1.27, GTK 4 and libadwaita 1 development packages. WebKitGTK 6.0, VTE and
GtkSourceView are optional and loaded at run time if present.

```sh
make build          # ggraphify, ggraphify-scan, ggraphify-job
make test
make run

make install        # puts a launcher on PATH + a .desktop entry + the icon
```

`make install` puts `packaging/ggraphify-run` on `PATH`, **not the binary**. The launcher
rebuilds from this checkout on every launch (`flock`-guarded, mtime-gated, built to a temp
name and renamed to dodge `ETXTBSY`) and then execs the result — so fuzzel always starts
current source, and `make install` is a one-time step rather than something to repeat after
every rebuild. `GGRAPHIFY_NO_REBUILD=1` runs the last build as-is.

The launcher pins `GSK_RENDERER=vulkan`, respecting a caller override. On this hardware
(Intel Lunar Lake / Mesa 26.1) vulkan holds a 9.0 ms p95 full-window repaint against GL's
17.9–32.8 ms and 87 MB more RSS; `GSK_RENDERER=cairo` is the no-GPU fallback.

## The headless half

The interesting half of ggraphify does not import GTK, and two commands exist to prove it.

```sh
ggraphify-scan                       # the board's rows, as a table
ggraphify-scan -json -state stale    # …or as JSON, filtered
ggraphify-scan -roots ~/git -no-drift
ggraphify-scan -out-base ~/.cache/graphify       # graphs kept outside the checkouts
ggraphify-scan -issues -unhealthy                # what Fix would act on, and why

ggraphify-job -list                              # every command this build knows
ggraphify-job -kind update -repo ~/git/svc -n    # print the argv, run nothing
ggraphify-job -kind god-nodes -repo ~/git/svc    # run it, stream to stdout
ggraphify-job -kind extract -repo ~/git/svc -y   # metered: -y is required
ggraphify-job -kind update -repo ~/git/svc -out-base ~/.cache/graphify
```

Both commands take the same `-out-name` / `-out-base` pair as the GUI, and `ggraphify-job`
additionally takes `-out` to name one checkout's output directory outright. They resolve
through the same code the board does.

`ggraphify-job` applies the same cost gate as the GUI: a metered command refuses to run
without `-y`, and says so along with whether the backend it resolved has a credential at
all — see *Backends* below for what "resolved" means when no API key is set.

## Layout

```
main.go                     flags, XDG dirs, GC tuning, single-instance lock
cmd/ggraphify-scan/         headless: the board's rows as a table or JSON
cmd/ggraphify-job/          headless: run one supervised job, stream to stdout
internal/
  applog/     the application's own bounded log, and the sink the standard logger writes to
  discover/   find checkouts; branch and HEAD read straight out of .git, no subprocess;
              and OutSpec, the one resolver for where a repository's graph lives
  graphstate/ read graphify-out/: counters, labels, drift, freshness — plus its cache
  board/      join the two into the rows the GUI renders, plus the folder groups
  gfy/        the graphify CLI contract: argv builders, env composition, version probe
  jobs/       the job queue: two lanes, process groups, streaming, cancel, history
  ringbuf/    the bounded per-job log
  store/      the sidecar under $XDG_DATA_HOME/ggraphify/
  open/       xdg-open, $EDITOR in a terminal
  webview/    dlopen'd WebKitGTK 6 binding (optional)
  ui/         all GTK: board, detail, viz, query, jobs, settings, keys, help
```

Two rules hold this shape together, both inherited from `cc-docsboard` because they are
what keep a board responsive:

- **Everything in `internal/ui` runs on the GTK main thread. Nothing else does.** Workers
  talk back with `glib.IdleAdd`, one small typed message per update.
- **No package outside `internal/ui` imports GTK.** A full scan of ~300 repositories takes
  about 290 ms and happens on a goroutine; a 2.4 MB `graph.json` is parsed with a streaming
  decoder that keeps only the counters, memoised on the output directory's own identity.

## Deviations from the plan

- The shell is a `GtkPaned` rather than an `AdwNavigationSplitView`. A navigation split
  view makes the sidebar the small half, and here the board — nine sortable columns over
  hundreds of rows — is the *large* half. Everything else in the shell is libadwaita:
  `AdwApplicationWindow`, `AdwToolbarView`, `AdwToastOverlay`, `AdwBanner`,
  `AdwPreferencesDialog`, `AdwAlertDialog`, `AdwShortcutsDialog`.
- `gfy.Backends` is `gemini kimi claude claude-cli openai deepseek ollama`, taken from
  `graphify --help` at 0.9.58, rather than the list the plan guessed at.
- VTE is not bound. The job log is a `GtkTextView` over the ring buffer; graphify's output
  is line-oriented and carries no terminal control sequences worth a terminal emulator.
- M6's `graphify prs` and M7's PGO profile are not done. `make pgo` is wired and waiting
  for a real usage profile, which has to come from a board somebody is actually using.

## Version pinning

`internal/gfy` is written against **graphify 0.9.58** and says so. A board that finds a
different major/minor raises a banner rather than failing mysteriously three clicks later,
and the platform-skill skew warning graphify prints on *every* invocation is recognised,
stripped from anything about to be parsed as JSON, and offered as a one-click fix.
