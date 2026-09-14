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
| Graft | the *other* index: `● 951n`, plus `~12 −3` when the tree has moved under it |
| Used | whether an agent actually *read* either index here: a week of daily calls as a sparkline, and the count |
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

## The graft column, and syncing a folder

A checkout can carry two indexes, and they answer to different tools: graphify's, under
`graphify-out/`, and [graft](https://www.npmjs.com/package/@nanonets/graft)'s wiring graph,
under `<repo>/graft`. The **Graft** column says which of the two this repository actually
has, in the same alphabet the state dot uses — `○` never built, `◍` wiring only (graft's
free default; the `--deep` concept layer has not been run), `◐` the tree has moved under
it, `●` in step, `△` a half-built directory an interrupted build left behind.

**Ctrl+G — or the folder button in the header — syncs the whole target directory**: one
`graft build` per repository in the folder the filter is on, skipping the ones already in
step. It is free (tree-sitter, no API key, no LLM request), it runs in the free lane like
any other job, and it is gated by the same typed-count confirm as every other batch —
because `graft build` writes a `graft/` directory *inside* each checkout and adds it to
that repository's `.gitignore`.

Graft freshness, like graphify drift, is **derived from files and never by running graft**:
`graft check` re-extracts every source file through tree-sitter, which is seconds per
repository and impossible on a 30-second tick over a hundred of them. The board reads the
node and edge counts out of `wiring.json`'s `meta` (a few hundred bytes of a file that is
megabytes long, since `meta` is written first), and measures drift against graft's own
`fingerprint.*.json` — one `stat` per recorded file, and a content hash only for the ones
whose `(size, mtime)` disagrees. That last rule is graft's own probe rule, and it is what
keeps a `touch`, or a branch switch that restores identical bytes, from showing up as work
to do.

That `.gitignore` entry is what keeps the two indexes out of each other's way, so the
detail pane says when it is missing. graphify's extraction is gitignore-aware, which is the
only reason it has never indexed graft's cards; a `graft/` left visible to git (graft's
`--no-gitignore`, `GRAFT_NO_GITIGNORE=1`, or a hand-edited file) is one markdown card per
source file sitting in the tree, and the next extract will read every one of them as
source — a second prose copy of the codebase in `graph.json`, counted as drift until it is,
and billed at the metered rate. It is a note on a fact and not an issue on the row: the
remedy is one line in a `.gitignore`, no graphify command can apply it, and a *Fix* button
that could never clear it would be worse than the note.

One thing the column deliberately does **not** report: *added* files. Naming one would mean
mirroring graft's extension table, its gitignore handling and its `--only-dir` whitelist —
a moving target in another project, and getting it wrong pins a row to "stale" that no
rebuild can clear. Changed and removed are measured against what graft itself recorded, so
they are answerable.

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
| `graft build` (tree-sitter only — graft's `--deep` pass is never run) | |

`cluster-only` is free *because* ggraphify passes `--no-label`. Without that flag it
dispatches LLM calls to name the communities and costs exactly what `label` costs — so the
board offers the two shapes as two actions and decides the cost in the argv builder, not in
a dialog.

An unknown command kind defaults to **metered**. Defaulting the other way would mean a
command a future build has never heard of could fan out over every repository without a
confirm.

"Metered" means *an LLM runs*, not *an API key is charged*: with no key set and Claude Code
installed, the metered column runs through that CLI and is billed to its plan, and against a
**local model** it is billed to nobody at all — the confirm dialog says which of the three it
is rather than warning about a bill that will never arrive. See
[Backends](#backends-no-api-key-required) and [Local models](#local-models-no-key-no-bill).

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
t          colour scheme   W  watch on/off           U          usage dashboard
                                                      ,  ?       settings, help
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

## Who actually uses the indexes

Every column to the left of **Used** answers *does this repository have an index, and how
stale is it*. None of them answers the question that decides whether building one was
worth anything: **does an agent ever read it.** A fresh graph nothing has opened in three
months is a cost with no return; a repository queried forty times this week with no graph
at all is the next extraction.

`U` — or the monitor icon in the header — replaces the whole window with the usage
dashboard: not a dialog over the board, a second page of it. Rows, detail pane and bottom
panel go; the header and the status line stay.

```
This repository | Everything          Watch live   7d 30d 90d   ⟳

269 uses over 30 days, every repository
239 graphify calls · 30 graft calls · 462 sessions · 41% reads through graft · 1.5M saved

Every day    graphify  ▁▂▅█▃▁▁▂▄▇█▅▃▂▁▂▃▅█▆▃▂▁▄▇█▅▃▂▁
             graft     ·····························▇
What was run graphify: query 132 · update 59 · export wiki 6 …
             graft:    ask 8 · grep 4 · skeleton 4 · build 3 …
Where        135 ~/git/platform-nova-cli · 50 ~/tmp/ggraphify · 2 ~/git (not on the board)
Latest       Sep 14 09:38  graphify  god-nodes  ggraphify  default
```

### Worth doing next

The dashboard's first section is the only part of it that asks for an action. It is the
join nothing else in the application can make: the board knows which repositories have an
index, the rollup knows which ones agents actually work in, and **the interesting
repositories are the ones where those two answers disagree.**

```
Worth doing next
 53  ggraphify — the tree has moved under the graph (+2 ~2) — the free AST pass catches it up   graphify update .
  4  cc-docsboard — used 4 times, with a graphify graph but no graft index — graft build is free       graft build
  2  platform-console — used 2 times, and has no graphify graph at all                     graphify extract .  $
```

Ranked by usage, because usage is the whole argument: a checkout an agent ran forty
queries in with no graph is not "unconfigured" in the abstract — it is one extraction that
would have paid for itself forty times. One recommendation per repository, and it is the
first thing that is wrong in the order a person would fix them: a missing graph, then a
pending re-extraction, then drift, then a missing graft index, and cosmetics — unnamed
communities — last. **A repository nobody used produces no recommendation however unbuilt
it is, and a repository with nothing wrong produces none either**, so a well-kept machine
shows an empty section rather than a list of nits.

A working directory that is not a boarded checkout gets the one recommendation that is not
a job — *add a scan root* — except when it is merely the directory boarded checkouts live
*under*, which is `~/git` and not a missing repository.

The button runs the same job kind the board's own actions do, which means **a metered one
goes through the same confirm** naming the repository, the backend, the model and the
exact argv, with the free `--code-only` variant beside it. The page explains why the money
would be worth spending; it is not a way around the gate that asks.

**It is read from the filesystem, like everything else here** — no telemetry, nothing sent
anywhere, and no `graphify`/`graft` subprocess. Two sources, because they answer different
halves:

- **Claude Code's transcripts**, `<config-dir>/projects/<slug>/*.jsonl`. Every tool call an
  agent made is a line in there with a timestamp, the session's cwd, its branch and its
  session id — which is where a `graphify query`, a `/graphify` skill invocation, an MCP
  call into graft's server and a hook injection all come from. **Every login on the
  machine is read**, not just the one jobs run as: which account was paying is an
  attribute of the usage, not a filter on it.
- **graft's own per-session counters**, `<repo>/graft/.cache/session/<id>.json`, which
  carry what a transcript cannot cheaply reconstruct: reads that went through the index
  versus straight to the source files, the tokens that saved, and what the session was
  billed. They join to the transcripts on the session id.

Two things the numbers deliberately are not. **Hook injections are counted separately from
uses** — the integration firing is not an agent choosing to use anything, and folding
eleven thousand of them into a headline would drown the hundred and thirty-two queries
that matter. And the **billed figure is graft's own, for the whole session** that used
graft — it is not the cost of graft.

The corpus is gigabytes across thousands of files, so it is read **incrementally**: a byte
offset per transcript and the last counters seen per graft session file, in
`$XDG_DATA_HOME/ggraphify/usage.json`. The first read of a working machine is about six
seconds on its own goroutine; every one after it is a few milliseconds of stat calls.
Ninety days are retained.

**Watch live** re-reads every two seconds while the page is on screen, which is how a call
an agent makes *right now* appears in **Latest** a moment later. Off, the page refreshes
with the board's own tick.

`ggraphify-scan -usage` prints the same rollup — recommendations included — without a
display.

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

**Settings → Jobs → graft integration** is the same question for the other indexer, one
group below: is the Claude Code CLI on this machine configured to *use* graft? That wiring
is four files deep and every way it breaks is silent — the agent greps source it could have
queried, and nothing anywhere says why. The group reads what `graft init` writes at user
level, the copy that applies to every project the CLI opens:

| Row | What it reads |
| --- | --- |
| graft | the binary, resolved the way jobs resolve it, and its version |
| Hooks shim | `helpers/graft-hooks.cjs` — **the one the hook entries actually name**, which is not always the one in this account's directory |
| Shim target | the `dist/claude` the shim bakes in: gone (an nvm bump, an uninstall) means an `npm root -g` per hook; older than the installed graft means the hooks run the old code |
| Hook entries | which of `SessionStart`, `UserPromptSubmit`, `PostToolUse`, `Stop` run the shim, and which are missing |
| MCP server | `mcpServers.graft` — a warning when absent, not a failure: the hooks still fire, the agent just has to spend a Bash call |
| graphify hook-guard | the other integration's `PreToolUse` entries in the same file, and whether they are strict — see below |
| Fix scope | present only when `graft init` cannot write the account being inspected |

*Fix* runs `graft init <repo> --no-build --no-agents --yes`. The flags are the whole design:
no index is built (the board has a button for that), no other agent's files are touched, and
graft's interactive picker is skipped because a job log cannot answer one. **`graft init` has
no machine-only mode** — the writes into `~/.claude` come alongside writes into one
repository's `.claude/` and `.mcp.json` — so the button uses the row selected on the board,
and the confirm names that repository and every file in both scopes before anything runs.
It is insensitive when nothing is wrong, and stays insensitive when what is wrong is not
`graft init`'s to repair: no graft installed, or a `settings.json` that is not valid JSON
(graft refuses to touch one, so a Fix button that promised to would be lying).

It is also insensitive when the **account selected in settings is not the default one**.
`graft init` resolves its user-level targets from the home directory alone and ignores
`CLAUDE_CONFIG_DIR` — graphify's installer honours it, so the board's per-job account
selection reaches one integration and silently misses the other. With `~/.claude-work` in
force, the group inspects that directory, `graft init` would write `~/.claude`, and a Fix
button that ran it would leave the very rows that enabled it red. So the group withdraws
fixability, says which directory the command would actually write, and names the two ways
out: select the default account, or copy graft's hook entries across by hand.

The **graphify hook-guard** row is about the two integrations meeting in one session rather
than in one file. They merge cleanly — graphify owns `PreToolUse`, graft owns the other four
events — and in the default advisory mode both simply put their own hint in front of the
agent. Strict mode is different: the guard blocks a session's first raw read until
`graphify query` has run, it decides that from `graphify-out/cache/last_query_stamp`, and a
session that followed graft's own SessionStart hint and ran `graft ask` never touches that
stamp. Following one integration's advice is what trips the other's block, so the row warns
and names the way out (`GRAPHIFY_HOOK_STRICT=0`, or re-installing the guard without
`--strict`). `graft init` cannot repair it, so it is never offered as fixable.

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
| no key, no `claude`, a local server answering | `ollama`, passed explicitly | `auto-detect (→ ollama)` |
| none of those | blank, and the confirm dialog says why the run will fail | `auto-detect` |

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

## Local models: no key, no bill

The third way to run the metered commands is a model **served from this machine**. It costs
no money, sends nothing off the box, and is slower and weaker than a frontier model. That is
the whole trade, and **Settings ▸ Local model** is where it is made.

Two shapes, because they are the two that exist:

| what you run | backend | how ggraphify finds it |
| --- | --- | --- |
| `ollama serve` | `ollama` | `OLLAMA_BASE_URL`, else `OLLAMA_HOST`, else `http://localhost:11434/v1` |
| `llama-server`, vLLM, LM Studio | `openai` | `OPENAI_BASE_URL` pointed at a loopback or private address |

The second is the one worth spelling out: graphify's `openai` backend is an ordinary
OpenAI-compatible client, so repointing `OPENAI_BASE_URL` at `http://127.0.0.1:8080/v1`
makes it llama.cpp. ggraphify checks whether that URL is *actually* local — loopback,
`.local`, or an RFC1918 address — and only then treats the backend as free. A base URL
pointed at some other vendor's proxy stays billed and is described as billed.

**Readiness is probed, not assumed.** `ollama` used to be reported as "needs no credential,
therefore fine", which is true and useless: a local backend fails for its own reasons. The
confirm dialog now separates the three, and each names its fix:

- nothing answering at the endpoint → start the server. The settings group has a **Start**
  button, which takes the only route it can take unprivileged: an *enabled* per-user
  systemd unit, else a detached `ollama serve`. Where a **system-wide** `ollama.service`
  owns the machine it starts nothing and copies `sudo systemctl start ollama` instead —
  a second server against a second model store is how an already-pulled model appears to
  vanish, and `systemctl --user disable` leaves the user unit file on disk, so "the unit
  exists" is not the question. "Is it enabled" is;
- the server is up but serving no models → `ollama pull qwen2.5-coder:7b`;
- the server is up but has not got the model the board is set to → pull it, or pick from the
  list of what it does have, which the settings group offers as a combo.

### The board-wide sweep

The local backend changes what a fan-out means, so it gets a button the metered
backends deliberately do not have. **Ctrl+L**, or the `computer-symbolic` button beside the
free sweep, **forces a full LLM extraction on every repository on the board**, against the
local model.

That sweep is the single most expensive thing this application could do against an API key,
which is exactly why no button offers it there. Against a model on this machine it costs
nothing but time, so the overnight pass over the backlog of checkouts nobody was going to
pay to extract becomes the obvious thing to want. Three things keep it honest:

- **Every row, with no state filter at all.** `--force` is passed and nothing is skipped for
  already being healthy: a graph that is fresh, labelled and reported is re-extracted from
  scratch alongside the ones that were never built. This is hours of CPU spent rebuilding
  what was already correct, and it is only defensible because the local backend charges
  nothing for it. **Ctrl+H** — the free Fix sweep, which plans the minimum command each row
  actually needs — stays the right first choice; this is "redo everything, I do not care how
  long it takes".
- **Two exclusions survive**, and both are your own instruction rather than a judgement
  about state: rows flagged `⊘` to stay out of batch actions, and rows with a job already in
  flight.
- **The backend is pinned, not auto-detected.** An exported `GEMINI_API_KEY` cannot turn a
  sweep you asked to run locally into a bill — the pinned name is in the argv the confirm
  dialog prints.
- **The ordinary confirm still applies**, which at this batch size means the typed-count
  gate too. Rows marked `⊘` and rows already building are skipped, and the toast says how
  many fell into each bucket.

Readiness is checked *before* anything is queued. A hundred jobs each failing against a
server that is not running is a log nobody can read; the refusal is one sentence instead.

A **pull is not run for you.** It is gigabytes over your network, and a button that started
one with no progress and no cancel would be worse than the terminal it replaced; the board
copies the exact command instead.

Three things worth knowing:

- **Concurrency is 1.** graphify forces it for `ollama` the same way it does for
  `claude-cli`, and for the same reason. The metered lane setting does not lift it.
- **The variables are ollama's own**, not graphify's: `OLLAMA_HOST`, `OLLAMA_BASE_URL`,
  `OLLAMA_MODEL`. graphify reads them unprefixed so a machine already configured for ollama
  needs no second configuration. `GRAPHIFY_OLLAMA_MODEL` and `GRAPHIFY_OLLAMA_HOST` do not
  exist in graphify 0.9.58 — earlier builds of this board offered them in the overlay
  editor, where setting either silently did nothing.
- **`GRAPHIFY_OLLAMA_NUM_CTX` matters more than it looks.** ollama defaults `num_ctx` to
  2048 and *silently truncates* a longer prompt, which turns into empty extractions rather
  than an error. graphify derives a value; the overlay editor is where to override it.

One consequence of the system-wide unit worth knowing before you switch to it: it runs as
the `ollama` user with `OLLAMA_MODELS=/var/lib/ollama`, which is **not** the `~/.ollama` a
`ollama pull` you ran as yourself wrote into. The server comes up serving nothing, and the
board says exactly that rather than leaving you to guess. Copying
`~/.ollama/models/{blobs,manifests}` into `/var/lib/ollama/` moves the models across
without re-downloading them.

`GGRAPHIFY_OLLAMA_BIN` overrides where the executable is found, and `=off` hides an install
the board should not use — the same contract as `GGRAPHIFY_CLAUDE_BIN`.

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
ggraphify-scan -usage                            # who actually ran graphify and graft
ggraphify-scan -usage -usage-days 7 -json        # …last week, as JSON

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
  usage/      who read the indexes: Claude Code transcripts + graft's session counters,
              rolled up per day and per repository, read incrementally
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
  `graphify --help` at 0.9.58, rather than the list the plan guessed at. `ollama`, and
  `openai` when `OPENAI_BASE_URL` is loopback, are *local*: see
  [Local models](#local-models-no-key-no-bill).
- VTE is not bound. The job log is a `GtkTextView` over the ring buffer; graphify's output
  is line-oriented and carries no terminal control sequences worth a terminal emulator.
- M6's `graphify prs` and M7's PGO profile are not done. `make pgo` is wired and waiting
  for a real usage profile, which has to come from a board somebody is actually using.

## Version pinning

`internal/gfy` is written against **graphify 0.9.58** and says so. A board that finds a
different major/minor raises a banner rather than failing mysteriously three clicks later,
and the platform-skill skew warning graphify prints on *every* invocation is recognised,
stripped from anything about to be parsed as JSON, and offered as a one-click fix.
