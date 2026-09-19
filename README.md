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
   *metered* and is gated behind a confirm that names the backend, the model and the
   repository count; a batch of three or more additionally requires the count to be typed.
   Against a billed backend it also runs strictly one job at a time. Against a model on
   this machine it does not — there is no bill to serialize, so it gets its own lane and
   its own limit.

## What a row tells you

| Column | What it says |
| --- | --- |
| ● | none / unlabeled / stale / fresh / broken / running — by shape as well as colour |
| Repo | name, group, `⧉` for a linked worktree, `⊘` when excluded from batch actions |
| Folder | the scan root this checkout was found under, and what the folder filter narrows to |
| Branch | branch and short SHA; `≠` when the graph was built at a different commit |
| Graph | `2047n / 4835e` |
| Graft | the *other* index: `● 951n`, plus `~12 −3` when the tree has moved under it |
| Used | whether an agent actually *read* either index here: a week of daily calls as a sparkline, and the count. A red **✕** means the row was used and has no graph to have answered with |
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

A graph behind HEAD is the quieter of the two failures: it reads as perfectly healthy while
its `file:line` spans point at code the checkout has moved past. So it gets a chip of its
own — **Behind HEAD**, beside the state chips — a count in the status line, and one action
that clears the lot: **Ctrl+U**, or the `document-open-recent-symbolic` button in the header,
which queues `update` on every behind-HEAD row. That is an AST re-extraction: free, no key,
no LLM call, and deliberately *not* behind the metered confirm, because for the seventy-odd
repositories a week of committing leaves behind it is a background pass rather than a
billing decision. `ggraphify-scan -behind` is the same list headlessly.

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

| Free — fans out over four lanes | Metered — always confirmed; one at a time when billed |
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
is rather than warning about a bill that will never arrive. The confirm is the same in all
three cases; the *lane* is not. A billed backend takes the metered lane and runs one job at
a time, because that lane bounds spend. A local one takes its own lane, because there is no
spend to bound and the limit is this machine instead. See
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

## Automatic fixes — the same button, without the click

**On by default.** On every scan the board asks, for each repository it can touch, whether
`internal/heal` has a plan for it — and if it does, it runs it. Same plan, same commands,
same order, same one-chain-per-repository. The only thing removed is the click.

That is a large amount of reach to hand to a loop nobody is watching, so it is bounded by
four rules rather than by hope:

1. **It does not spend money.** The steps that need an LLM run against a model on **this
   machine**, and `autofix.Policy.Spends()` is false whenever one is in play. If no local
   server answers, the loop falls back to the *free* plan — AST rebuild, clustering, report
   — and leaves the semantic half undone rather than reaching for the billed backend. The
   one switch that changes that (**Allow metered fixes**) is off, marked, and says what it
   turns on.
2. **It gives up.** A repository whose defects survive the fix is retried a bounded number
   of times — with the cooldown growing on each failure — and then left alone, with one
   line in the log saying so. A repository whose defects *change* gets a fresh count, so a
   three-stage repair is never mistaken for a loop. Fixing one by hand clears the give-up.
3. **It respects the flags that already mean this.** A repository excluded from batch
   actions (`X`) is never touched, and neither is one with a job already running — the
   user's, or one of the loop's own.
4. **It is bounded in width.** Two repositories in flight by default; the job runner's
   lanes still apply underneath, so the loop cannot become a way to run twelve extractions
   at once.

Which model it uses is resolved per tick, not stored: when the board's backend is already
local, its own model setting is the preference; when the backend is `claude-cli` or an API,
the model setting names a model the local server has never heard of, so `gfy.AutoLocalModel`
asks for graphify's default instead and, failing that, the first **non-embedding** model the
server actually serves. An embedding-only server is a refusal rather than a bad guess — an
extraction sent to `nomic-embed-text` is a 400 per chunk of every repository.

Everything is under **Settings → Jobs → Automatic fixes**: the master switch, the local
model switch, the metered switch, how many repositories at once, the cooldown, and how many
attempts before it gives up. Changing any of them clears the loop's memory, so a setting
changed to unblock a repository actually unblocks it. The diagnostics page (`Ctrl+D`) prints
the whole resolved policy — including which model the LLM steps would go to right now — in
one line.

The decision itself is `internal/autofix`: a pure function of the rows plus a memory of what
has been tried, with no filesystem, no subprocess and no GTK, for the same reason `heal` is.

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
t          colour scheme   W  watch on/off           U y        usage dashboard, copy it
                                                      ,  ?       settings, help
Ctrl+F/B   page down / up  x  cancel this row's job   b  L       bottom panel, its log
Ctrl+H     free fix, board  Ctrl+U  update every row behind HEAD (free)
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

### The queue survives a restart

Closing the board does not throw the queue away. What was queued, what was in flight and
what had finished are all written to the sidecar on every transition, and read back on the
next launch — a sweep over 128 checkouts is hours of work, and quitting, rebooting or
losing the session in the middle of one should not mean starting it again.

Two rules decide what comes back *running*:

- **A job that was in flight returns held, not running.** Its process group died with the
  last session and its output directory is in whatever state graphify left it, so the board
  shows the work as outstanding rather than silently repeating it. Its log says so, in the
  log itself.
- **Free work restarts itself; work that costs something waits.** AST updates, clustering
  and exports resume on their own — that is what the previous session already decided, and
  being wrong about it costs nothing. A metered job is an LLM bill and a local one is hours
  of this machine; both come back **held**, which is the same gate the confirm dialog is.

A held job sits in the queue without being dispatched, and it does not block the queue
behind it: a restored sweep of 128 free updates starts immediately while the metered ones
wait. **Start** in the jobs view runs the selected one; **Start all held** runs the lot.
The board says how many are waiting the moment it opens, and the status line keeps saying
it — `12 queued (4 held)`.

Restored jobs keep the tail of their log, so a failure you reopen the board to read still
says why it failed. A job whose checkout has since been deleted is dropped rather than
queued to fail at every launch.

## Who actually uses the indexes

Every column to the left of **Used** answers *does this repository have an index, and how
stale is it*. None of them answers the question that decides whether building one was
worth anything: **does an agent ever read it.** A fresh graph nothing has opened in three
months is a cost with no return; a repository queried forty times this week with no graph
at all is the next extraction.

That second case is the one the board used to be able to show but not to *state*. Both
halves were already on screen — the sparkline in **Used**, the dot in the state column —
but reading them together meant tracking two columns across ninety rows, and a repository
with three uses and no graph sits nowhere near the top of either. So the conjunction is
marked where the usage is: a used row with no graph (or a broken one, which has never
answered a query either) renders its count with a red **✕**, and the tooltip says how many
queries fell through to raw source.

The **Used ✕** chip in the filter bar — first after **All**, and in the `f` cycle — narrows
the board to exactly those rows. It is the one chip that is not a graph state, because no
single state can express a join between what the rollup saw and what the checkout has.
`ggraphify-scan -gap` prints the same set without a display, over the same seven-day window
(`-gap-days` changes it). An empty result is the answer you want: every repository anyone
worked in had an index to answer them with.

`U` — or the monitor icon in the header — replaces the whole window with the usage
dashboard: not a dialog over the board, a second page of it. Rows, detail pane and bottom
panel go; the header and the status line stay.

```
This repository | Everything          Watch live   7d 30d 90d   ⟳

269 uses over 30 days, every repository
239 graphify calls · 30 graft calls · 517 sessions (462 used a tool) · 41% reads · 1.5M saved

Every day    graphify  ▁▂▅█▃▁▁▂▄▇█▅▃▂▁▂▃▅█▆▃▂▁▄▇█▅▃▂▁
             graft     ·····························▇
What was run graphify: query 132 · update 59 · export wiki 6 …
             graft:    ask 8 · grep 4 · skeleton 4 · build 3 …
Where        135 ~/git/platform-nova-cli · 50 ~/tmp/ggraphify · 2 ~/git (not on the board)
Every session  last          repository           gfy graft   of all calls account
             Sep 14 09:38  ggraphify              2    18    37% of 53    default
             Sep 14 09:31  required-workflows     0     0     0% of 67    default
Latest       Sep 14 09:38  graphify  god-nodes  ggraphify  default
```

**Every session** is one row per Claude Code session, newest first — where it ran, how
many times it reached for each tool, and what share of everything it did that was. The
sessions that used *neither* tool are in there too, dimmed: they are the denominator, and
a list of only the sessions that used something cannot answer *and how many did not*. The
second row above is the finding the section exists for — sixty-seven tool calls in a
checkout, not one of them into an index.

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
  attribute of the usage, not a filter on it. The same pass registers **every session it
  walks past**, tool or no tool, with its directory, its span and how many tool calls it
  made in total — which is the only reason the page can say how many sessions ignored both
  indexes. That census obeys the same budget as the rest: the tool calls are counted with
  a byte scan and exactly two lines per transcript are handed to the JSON decoder.
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

### Handing the window to an agent

The dashboard can show that graft's pointer was injected into six hundred sessions and
reached for in a hundred and fifty. It cannot say whether that is bad. The thing qualified
to judge it is an agent, so the copy icon in the toolbar — or `y` on the page — puts the
whole window on the clipboard as a **markdown briefing** written to be pasted into a Claude
Code session: the headline, the two hook-to-use ratios, the per-directory table with
injections beside uses, the directories that were handed an index and ran nothing, the daily
series, the per-session table, and the questions worth answering from it. It ends by naming
what each number is and is not — including that its two session counts are counted
differently, and that the per-session one *is* exhaustive, so a directory absent from that
table had no sessions rather than silent ones.

`ggraphify-scan -usage -markdown` prints the identical text, rendered by the same function
in `internal/usage`, for piping into a file or into `wl-copy`.

Both read the board the same way, which is load-bearing rather than tidy. `ggraphify-scan`
resolves **out-name and out-base from the stored settings**, exactly as the window does — a
stored preference outranks the built-in default, an explicit flag outranks both. A scan that
skipped that step looked for `graphify-out/` inside each checkout, found nothing on a board
configured with an out-base, and reported every graphed repository as having no graph at
all — turning the recommendation list into a page of metered extractions that were already
done. `resolveOut` and `(*ui.App).outLocation` are the two halves of that rule and must not
drift apart.

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

### The index: how anything else finds a graph

Graphs kept outside the checkouts are invisible from the checkout they describe — which is
how 212 fully built graphs on this machine went unread by every agent for weeks: the tools
looked in `graphify-out/` and found nothing. So the board publishes a lookup table,
`<out-base>/index.json`, keyed by each checkout's **absolute path**:

```sh
jq -r '.entries["'"$PWD"'"].graph' ~/knowledge/index.json
```

One read, no globbing, and no assumption that repository basenames are unique — `~/git/api`
and `~/tmp/api` are two entries. Each carries the output directory, `graph.json`, the
report, the build time and commit, whether the graph is behind HEAD, the node/edge/community
counters and how far the semantic tier got.

It is written from the scan the board already paid for, only when something actually
changed, atomically (temp file plus rename) and under a lock file, so several ggraphify
processes finishing at once cannot corrupt it. `ggraphify-job` updates its own entry after a
build, and `ggraphify-scan -index` writes the whole thing on a machine that rarely runs the
GUI. A board keeping graphs in-tree publishes nothing: there is no lookup to restore.

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
overrides, window geometry, column widths, and the job list — the queue and a bounded run
history, with the tail of each job's log. The single exception
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

Because the account is a setting, and frequently not the default one, **every dialog and
job log that the choice bears on names the resolved directory**: the confirm before a
`claude-cli` extraction, the confirm before `install`, and the first lines of the job's own
log. It is one sentence and no behaviour change, and it exists because the alternative is
invisible — `--backend claude-cli` in an argv says nothing about which login paid, or whose
hooks, skills and settings were in force. They are not the ones a Claude Code session in a
checkout runs with.

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
| a vendor API key is exported | blank — graphify detects from the key | `auto-detect` |
| no vendor key, `OPENCODE_API_KEY` exported | `opencode-go`, passed explicitly | `auto-detect (→ opencode-go)` |
| no key, `claude` installed | `claude-cli`, passed explicitly | `auto-detect (→ claude-cli)` |
| no key, no `claude`, a local server answering | `ollama`, passed explicitly | `auto-detect (→ ollama)` |
| none of those | blank, and the confirm dialog says why the run will fail | `auto-detect` |

The resolved name goes into the argv the confirm dialog prints and the job log records, so
the substitution is on screen rather than behind it. Picking `claude-cli` in **Settings ▸
LLM defaults ▸ Backend** pins it regardless of what keys are set.

**A pin that has stopped working is said out loud, on startup.** The confirm dialog has
always refused a metered job whose backend has no credential, server or model — but that
refusal only appears at the moment somebody tries to spend money, and a board mostly driving
the free lane can sit on a dead pin for weeks with no symptom except that every graph stays
at the AST tier. So the readiness question is asked when the board opens and again whenever
the backend or the model setting moves, and a failure raises a banner that names it. The
banner's button lists what *is* ready on this machine, each with the reason it would work,
and one click applies it — it never switches by itself, because which backend runs is which
account pays. `ggraphify doctor` answers the same question headlessly.

### OpenCode Go: a $10/month subscription, with the model as a setting

`opencode-go` is the one entry in the Backend list that `graphify` does not ship. It does
not have to: `llm.py` merges every provider in `~/.graphify/providers.json` into its backend
table before detection runs, so a registered provider is a name `--backend` accepts like any
built-in. The board writes that entry — endpoint `https://opencode.ai/zen/go/v1`, `env_key`
`OPENCODE_API_KEY` — the first time a job asks for the backend.

Registering a provider rather than repointing the `openai` backend at the gateway with
`OPENAI_BASE_URL` is deliberate. The other route would mean copying your credential through
ggraphify's own job state, and would leave a job whose argv said `openai` indistinguishable
from a real OpenAI run in the log, the retry path and the cost notice. This way the key
stays where you exported it — graphify reads it itself — and the argv says `opencode-go`.

- **Export `OPENCODE_API_KEY` before launching the board.** Subscribe at
  <https://opencode.ai/docs/go>. ggraphify never stores a key, only the backend's name.
- **The model is a dropdown, in Settings ▸ OpenCode Go ▸ Model,** not the shared Model field
  under LLM defaults. It is kept apart because a model id is not portable between backends:
  `qwen2.5-coder:7b` is a real answer for ollama and a 404 for this gateway. A repository's
  own model override still wins over it.
- **A listed model is not necessarily a working model,** so one is asked for a single token
  before a run starts. `muse-spark-1.2-contributor` and `-1.3-` are in the gateway's listing
  and answer `500 Internal server error` — the plan marks them "limited regions", and a region
  your account is not in presents as that 500 rather than as an absence. Nothing readable
  tells the two apart, so ggraphify probes the chosen model (once per model per process,
  single-digit tokens), refuses the submit with the gateway's own sentence, and says the same
  thing in the settings group as you pick. A probe that cannot reach the gateway is not
  treated as a verdict.
- **Which models exist comes from the gateway, what they cost comes from models.dev.** That
  split is not pedantry: models.dev publishes ids for this provider that the gateway does not
  serve — `ox-alpha-free` is one — and choosing one fails every chunk of a run with
  `Model ox-alpha-free is not supported`, which graphify reports only as "all semantic chunks
  failed". The dropdown is built from `https://opencode.ai/zen/go/v1/models`, priced from
  models.dev, sorted cheapest first with the unpriced ones last, and this build's catalogue is
  baked in so it is populated offline. Once a live list has been fetched, the confirm dialog
  refuses a model that list does not contain.
- **Choosing a model moves the price with it.** `llm.py` estimates a run's cost from the
  provider entry's one price pair, so the entry is rewritten — `base_url`, `default_model`,
  `env_key`, `model_env_key` and `pricing` are the board's fields. Any other field you add by
  hand survives, as does every other provider in the file. The default model is
  `glm-5.3-flash`: an extraction is a schema-constrained JSON job over one file at a time,
  which is the shape a small fast model does well and a frontier model only does expensively.
- **`OPENCODE_BASE_URL` overrides the gateway the proxy forwards to** — for a corporate proxy, or for
  OpenCode Zen (`https://opencode.ai/zen/v1`, pay-as-you-go over the same estate plus the
  frontier models) for somebody on that plan instead. Both plans read the same key variable,
  so the URL is what decides which one a request is billed against.
- **Jobs reach the gateway through a loopback proxy, and that is why `base_url` says
  `127.0.0.1`.** The gateway *refuses* a request with no `x-opencode-session` header — every
  model answers `400 MissingSessionID` — and graphify builds its client as
  `OpenAI(api_key, base_url, timeout, max_retries)` with no `default_headers` and no hook for
  one. So the board runs a proxy on 127.0.0.1 (port 11437, `GGRAPHIFY_OPENCODE_PORT` to move
  it), started when an `opencode-go` job is submitted and shared with any other ggraphify that
  finds the port already answering its probe. It forwards to the gateway and nowhere else,
  adding that header — one stable session id per board process, which is the granularity the
  plan describes for cache routing — and a `User-Agent: ggraphify/<version>`, which the plan
  also asks for. Everything else, the `Authorization` header included, is passed through
  untouched; the key is never read, logged or stored.
- **That proxy is GUI-bound: it lives and dies with the ggraphify window.** It is a useful
  endpoint for other tools — register `http://127.0.0.1:11437/v1` in another tool's
  `~/.graphify/providers.json` and it will work — but it is *not* a service. There is no
  daemon, no unit file and nothing that restarts it; close the board and the port stops
  answering, which an external consumer sees as its model refusing connections mid-run. The
  board will not do that silently: quitting while the proxy has forwarded a request in the
  last ten minutes asks first, naming the endpoint and the traffic. If you need a gateway
  that outlives the GUI, run the board and leave it running — a headless `--serve-proxy`
  mode does not exist yet.

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

### The server's lifecycle: started for a job, stopped when idle

**Start and stop the server automatically** is on by default. The board starts ollama when
a job that will talk to it is about to run, and stops it again once no job does — so a
machine that reaches for a local model occasionally is not holding a 7B's weights resident
between sweeps, out of the page cache the rest of the desktop wants.

It is a **lease counter**, not a per-job start: every job takes a lease just before its
process starts and gives it back when that process ends, whatever it ended as — succeeded,
failed, cancelled, killed at shutdown. The server stays up while at least one lease is out.
The hook is on the runner (`jobs.Options.LocalLease`), which is the one path every job
takes: the board's buttons, the batch sweep and the unattended fix loop all go through it,
so the fourth caller somebody adds next cannot forget the server. What it hands back is a
`jobs.Lease` — `Suspend`, `Resume`, `Release` — because a job has three ends and not one,
and the runner drives those three in exactly the places it signals the process group. The
runner is told nothing about what it is leasing.

When the last lease comes back a **grace period** starts — five minutes by default,
configurable on the row below the switch — and the server is stopped only if no new job
arrives before it runs down. The grace period is the point: a sweep is a run of short jobs
with gaps between them, and stopping too eagerly is paid twice, once for the cold start and
once for reading the weights off disk again.

**Pausing a job gives the server up too.** A paused graphify is a SIGSTOPped process that
will not send another request until somebody says so, and holding a 7B's weights resident
for it defeats the point of having paused it. So the lease is *suspended* rather than
released: the server stops if nothing else holds one, and the job's place in the counter is
taken again when it resumes.

Two details carry that:

- **the stop is immediate, not after the grace period.** The grace period exists because
  the lull between two jobs in a sweep is shorter than a cold start is expensive. A pause
  is somebody asking for their machine back, and answering "in five minutes" would be
  answering a different question;
- **the resume brings the server back BEFORE the process is woken**, and blocks while it
  does. A SIGCONT sent first would wake graphify into a dead port, and it would find that
  out as a connection error rather than as a wait. `Resume` therefore blocks for up to a
  cold start, so the board calls it off the thread that draws and toasts *resuming —
  starting ollama first*, because the row cannot show the change until the wait is over.

One consequence worth knowing: a request that was *in flight* when the job was paused does
not survive the server going away, so a short pause-and-resume can cost the chunk that was
mid-flight where previously it would have continued. A pause longer than graphify's own API
timeout already lost that request either way, which is most of them.

Three rules bound what it will do, and none of them has a setting that relaxes it:

- **it only ever stops a server it started itself.** An ollama that was already answering
  when the first job asked for one belongs to whoever started it — they may have a chat
  open against it or a second tool pointed at it — and there is no reading of "manage the
  server automatically" that means "kill things other people started". The evidence is
  recorded at the moment of the start, not inferred afterwards;
- **a server started by hand is exempt** — from the idle stop, from the pause stop and from
  the stop at exit. The **Start** button pins the server: the click asked for a running
  ollama, not for one that vanishes when a timer nobody saw runs down, and pausing a job is
  not a retraction of it. Manual start, manual stop;
- **only ollama.** `j.Local` is true for any local backend, which includes the `openai`
  backend repointed at a loopback llama-server, vLLM or LM Studio. Those the board did not
  start, cannot start and has no business stopping, so the job's *argv* is asked which
  backend it was built for and only `ollama` is managed. The argv rather than the current
  setting, because a queued job restored from the previous session must be leased against
  the server **it** will talk to.

The stop mirrors the start, route for route: `systemctl --user stop ollama` for a per-user
unit, and for a detached `ollama serve` a SIGTERM to the process **group** — `ollama serve`
spawns a runner subprocess per loaded model, and signalling the pid alone would leave a
runner holding the weights, and the GPU, after the server it belonged to had gone. A
system-wide unit is never stopped, for the same reason it is never started. An auto-started
server is also taken down when the board quits, after the runner has closed so the last job
is already gone — a server the board started silently outliving the board is the one
outcome nobody could have intended, since nothing on the machine would say where the
resident weights came from.

`ggraphify-job` gets the same behaviour for the single job it runs, under `-auto-ollama`
(default true). It reads no state file, so a headless run behaves identically whoever's
board last touched which switch.

### The board-wide sweep

The local backend changes what a fan-out means, so it gets a button the metered
backends deliberately do not have. **Ctrl+L**, or the `computer-symbolic` button beside the
free sweep, **forces a full LLM extraction on every repository currently listed**, against
the local model.

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
- **The listing is the scope.** It sweeps `visibleRows` — what the folder filter, the state
  filter and the search box currently admit — not every checkout on the machine. That is
  what makes something this heavy usable: narrow the board to one folder and the sweep is
  that folder, with the count in the confirm to prove it. With no filter set the listing is
  the whole board, so "everything" is one `Esc` away.

Once it is running, **Stop all** in the jobs view (`J`) cancels the entire queue in one
click. It carries the count of what it would stop, and it is the one destructive control
here with no confirm in front of it — stopping is the recoverable direction, and every job
it kills can simply be run again.
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

Five things worth knowing:

- **Local LLM work has its own lane.** The metered lane bounds *spend* — one request at a
  time, so a fan-out over 128 checkouts cannot become a fan-out bill — and one is the only
  defensible default for it. Against a model on this machine there is no bill to
  parallelize, and the limit is cores and memory instead, so those jobs count against a
  separate **Local model lanes** limit (Settings ▸ Jobs ▸ Concurrency, default 2, up to 8).
  Raising it never lifts the metered lane, and the jobs strip says which of the three a
  queued job is waiting on.

  Two is the default because a graphify run alternates between a `cpu_count`-wide AST pass
  that touches the model server not at all and a semantic pass that is nothing but requests
  to it. One job at a time leaves the model idle for every AST phase in a sweep; a second
  job fills exactly those gaps.

  The request timeout scales with the lane count. With several jobs sharing the server they
  share its slots too, and a chunk can sit in the server's queue behind other jobs' work
  before it is dispatched — a wait the OpenAI SDK's timeout covers as well as the
  generation. A flat 1800 seconds would make the extra lanes a source of killed chunks,
  dropped files and a run failed on the shrink guard: the exact failure the raised timeout
  exists to prevent, re-introduced by the setting meant to make things faster.

- **Concurrency within one job is the server's slot count, not 1 and not the metered lane.** graphify
  clamps `max_concurrency` back to 1 for `ollama` unless `GRAPHIFY_OLLAMA_PARALLEL=1` is
  exported, so a `--max-concurrency` flag on its own is silently discarded — the run looks
  parallel in the argv and in the log, and is not. The board sets the flag and the variable
  together, or neither, sized to what the server actually serves.

  How many that is, is not on the wire: `/api/ps` reports the per-slot context and nothing
  about the slot count. So it is read off the manager of the process that is serving —
  `systemctl show ollama.service --property=Environment`, the per-user unit first and the
  system one second, because a unit's `Environment=` is what the running server was actually
  given and a variable in your shell is only what a server started *from here* would get.
  For a server on another host, where none of that is on this disk, set
  `GGRAPHIFY_OLLAMA_SLOTS` — nothing here could discover it.

  A server that serves one at a time stays clamped. Unlocking it there would hand graphify's
  default of four chunks to a server that can hold one, which is the VRAM-pressure failure
  the clamp was written for.

- **The slot count and the context length are one setting.** ollama **divides**
  `OLLAMA_CONTEXT_LENGTH` across `OLLAMA_NUM_PARALLEL` slots: two slots and a 32768 context
  is two 16384 slots, not two 32768 ones. Raising the slot count alone halves every chunk
  the board is then allowed to send, while appearing to double throughput. The board's
  advice row names both, and a server it starts itself gets both — plus
  `OLLAMA_FLASH_ATTENTION=1` and `OLLAMA_KV_CACHE_TYPE=q8_0`, which roughly halve what a
  slot's K/V cache costs and are what make the second slot affordable, `OLLAMA_KEEP_ALIVE=30m`
  to span the gap between one repository's graphify process and the next one's, and
  `OLLAMA_MAX_LOADED_MODELS=1` so a second model cannot evict the first mid-sweep. Every one
  of them is a default: a value already in the environment is one you chose, and wins.

  The slot count it derives is `NumCPU/4`, capped at 4 and further capped by half of
  `MemAvailable` at roughly a gigabyte a slot. Deliberately modest — using the whole machine
  means every part of it doing useful work at once, not the model oversubscribed while the
  `cpu_count`-wide AST phase waits behind it.
- **The variables are ollama's own**, not graphify's: `OLLAMA_HOST`, `OLLAMA_BASE_URL`,
  `OLLAMA_MODEL`. graphify reads them unprefixed so a machine already configured for ollama
  needs no second configuration. `GRAPHIFY_OLLAMA_MODEL` and `GRAPHIFY_OLLAMA_HOST` do not
  exist in graphify 0.9.58 — earlier builds of this board offered them in the overlay
  editor, where setting either silently did nothing.
- **The context slot is set on the SERVER, and `GRAPHIFY_OLLAMA_NUM_CTX` does not reach
  it.** graphify derives a `num_ctx` per request and sends it in `extra_body.options` — but
  that is ollama's *OpenAI-compatible* endpoint, which does not read `options`. The server
  keeps whatever slot it was started with: 4096 tokens unless `OLLAMA_CONTEXT_LENGTH` said
  otherwise, however large the model's own trained context is. llama.cpp then truncates an
  oversized prompt **from the front** with `n_keep=4`:

  ```
  msg="truncating input prompt" limit=2050 prompt=7394 keep=4 new=2050
  ```

  The four tokens kept are the head of graphify's system prompt. Everything that asked for
  JSON, and the schema to answer with, is gone before the model sees anything — so it
  describes the file in prose instead. graphify reads that as invalid JSON, calls the chunk
  hollow, retries into the same truncation twice more, gives up, and finishes with zero
  semantic nodes; its shrink guard then refuses to overwrite the older, larger graph and the
  run exits non-zero. Hours of a CPU-bound model, and nothing written.

  So the board does the only thing a client can: it measures the slot (ollama's native
  `/api/ps`, which `/v1/models` has no equivalent of) and **sizes every chunk to fit it**,
  passing `--token-budget` on the command line where the confirm dialog shows it. A slot it
  could not measure — a server with nothing loaded, or one that is not ollama — leaves
  graphify's own default alone rather than guessing. A server the board starts itself gets
  `OLLAMA_CONTEXT_LENGTH=32768`; a stock 4096 one still runs, and the settings row says so
  and names the fix.

  The cap is not a guarantee, and the settings row says that too. It bounds a chunk the
  packer *builds*, but a **single file larger than the cap cannot be split any further** —
  graphify sends it whole, and on a 4096-token slot that is most files over about 2 KB. The
  prompt is truncated from the front exactly as above, the chunk comes back as prose or
  runs past its deadline, and those files are simply absent from the graph:

  ```
  [graphify] single-file chunk .../SKILL.md timed out and cannot be split further
  [graphify] WARNING: 1/3 dispatched file(s) produced no nodes and are absent from the graph
  ```

  So a narrow slot is something to widen before a sweep, not a slower road to the same
  graph. What the board can do from the client side it does: alongside the budget it passes
  `--api-timeout 1800`, because graphify allows a request 600 seconds and a CPU-bound 7B at
  a few tokens a second needs longer than that for one full reply — a request killed on the
  deadline costs the file it was extracting, and enough of those fail the run on the shrink
  guard.

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
ggraphify-scan -gap                              # used, and no graph to have answered with
ggraphify-scan -usage                            # who actually ran graphify and graft
ggraphify-scan -usage -usage-days 7 -json        # …last week, as JSON
ggraphify-scan -usage -markdown                  # …as the briefing to paste into an agent
ggraphify-scan -behind                           # graphs built before the current HEAD
ggraphify-scan -index                            # republish <out-base>/index.json, then exit

ggraphify-job -list                              # every command this build knows
ggraphify-job -kind update -repo ~/git/svc -n    # print the argv, run nothing
ggraphify-job -kind god-nodes -repo ~/git/svc    # run it, stream to stdout
ggraphify-job -kind extract -repo ~/git/svc -y   # metered: -y is required
ggraphify-job -kind update -repo ~/git/svc -out-base ~/.cache/graphify
```

Both commands take the same `-out-name` / `-out-base` pair as the GUI, and `ggraphify-job`
additionally takes `-out` to name one checkout's output directory outright. They resolve
through the same code the board does.

There is also one subcommand of the GUI binary, which needs no display:

```sh
ggraphify doctor            # assert the invariants; exit 1 when one is broken
ggraphify doctor -json      # …as JSON, for a cron or a pre-flight
ggraphify doctor -strict    # …treating warnings as failures too
```

It answers, against live state: is graphify installed and is it the release the command
builders target; could the configured backend actually run a metered job right now, and
what is ready instead if not; is every graph discoverable from the checkout it describes;
how many graphs were built before the current HEAD; which Claude Code account jobs run as
(it is a setting, and frequently not `~/.claude`); and how far the semantic tier has got
across the estate — the one measure that distinguishes "a healthy AST graph" from "a graph
no metered pass has ever completed on".

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
