# Keeping ggraphify aligned with the Claude Code configuration

**Date:** 2026-09-18
**Repo:** `ggraphify` (this one)
**Audience:** whoever changes this app next
**Status:** DONE — A–F implemented on 2026-09-18, plus the Claude Code hook simplification
the plan left as a follow-on. Every item carries its own *Done* note below with the evidence
it was verified against. `go build ./... && go test ./...` clean.

## Why this exists

Claude Code and ggraphify both consume graphify graphs, and on 2026-09-18 they were found
to disagree on three mechanics. One of those disagreements meant **212 fully-built graphs
had never once been read by a Claude Code session**: `~/CLAUDE.md` pointed at
`graphify-out/` in the working tree, while this app writes to `~/knowledge/<repo>-<hash>/`.

The Claude Code side has been fixed already (a rewritten `~/CLAUDE.md` rule, plus a
`UserPromptSubmit` hook at `~/.claude/helpers/graphify-hint.cjs` that resolves and prints
the real path). **That fix is a workaround: it globs `~/knowledge` by repo basename and
reads `.graphify_root` to confirm the mapping.** It works because basenames happen to be
unique across the 215 directories today. This plan is the app-side work that would make
the alignment structural instead of coincidental.

Nothing here is urgent. Nothing here is a bug in the sense of "the app does the wrong
thing" — the refusals and checks are all correct. These are *discoverability* and
*drift* gaps.

---

## A — Publish a machine-readable index of where graphs live

**The gap.** `out_base` is `~/knowledge`, and a graph lands in
`~/knowledge/<repo-basename>-<hash>/`. Nothing in the repo being graphed points back at
it, and nothing in `~/knowledge` is a lookup table. Every external consumer must glob and
then read `.graphify_root` in each candidate to disambiguate. That is what the Claude Code
hook now does, and it will break the first time two scanned repos share a basename.

**Code involved**

| What | Where |
| --- | --- |
| Out-spec resolution | `internal/discover/discover.go:129` (`OutSpec` struct) |
| Per-repo lookup | `internal/store/store.go:732` (`(*Store).OutSpec`) |
| CLI path resolution | `cmd/ggraphify-scan/main.go:221` (`resolveOut`) |
| Human-facing description | `internal/ui/diag.go:307` (`describeOut`) |

**Change.** After any build that writes a graph, update a single
`~/knowledge/index.json`:

```json
{
  "version": 1,
  "entries": {
    "/home/dns/git/acquiring": {
      "out":        "/home/dns/knowledge/acquiring-63218cff",
      "graph":      "/home/dns/knowledge/acquiring-63218cff/graph.json",
      "built_at":   "2026-09-17T13:26:00-03:00",
      "built_commit": "ea61fadd",
      "tier":       "ast",
      "nodes": 6384, "edges": 17292, "communities": 331
    }
  }
}
```

Keyed by the **absolute source path** — the same value already written to
`.graphify_root`, so it is a reshaping of data the app has, not new bookkeeping. Write it
atomically (temp + rename); several ggraphify jobs can finish at once.

**Why not the alternatives.** Writing `<repo>/graphify-out/` for real would double the
disk and re-create the split this is meant to close. A symlink per repo means 215 symlinks
that go stale whenever a hash changes. An index is one file, and a consumer resolves in
one read with no globbing and no basename assumption.

**Done when:** `jq -r '.entries["'"$PWD"'"].graph' ~/knowledge/index.json` prints the
graph path from inside any scanned repo, and the Claude Code hook can be simplified to
that one line.

**Done.** `internal/knowledge` publishes it. Entries carry `out`, `graph`, `report`,
`built_at`, `built_commit`, `head_commit`, `stale`, `state`, `tier`, the counters and the
manifest's `files`/`semantic` split. Written atomically (temp + rename) under a lock file
beside it, from three places: the GUI on every scan (only when something changed —
`internal/ui/knowledge.go`), `ggraphify-job` for its own repository after a successful build,
and `ggraphify-scan -index` for a machine that rarely opens the board.

Verified: `./ggraphify-scan -index -no-drift` → `/home/dns/knowledge/index.json: 206
repositories, written`, and from this checkout `jq -r '.entries["'"$PWD"'"].graph'` prints
`/home/dns/knowledge/ggraphify-12336d85/graph.json`. Keyed by absolute path, so two
checkouts named `api` are two entries — tested in `internal/knowledge/index_test.go`.

The hook was simplified too (`~/.claude/helpers/graphify-hint.cjs`): it reads `index.json`
first and takes the counters and build commit from it; the basename glob survives only as
the fallback for a machine whose board has not written the index yet.

---

## B — Make a stale backend pin impossible to sit on

**The gap — and what it is NOT.** Settings currently pin `backend: "ollama"` while nothing
serves `:11434` (the binary is installed; no server). The app handles this *correctly*:
`BackendReady` is consulted at `cmd/ggraphify-job/main.go:173` and the job refuses, and the
confirm dialog explains why via `internal/ui/actions.go:347`. There is no silent
degradation to fix.

The gap is that a refusal only appears **at the moment you try to run a metered job**. If
you mostly run the free lane, the pin can stay broken for weeks and the only visible
symptom is indirect: every graph stays AST-tier. That is the actual observed state —
across 212 graphs, `semantic_hash` is empty on **100%** of files, and community names are
raw symbols (`testing.T`, `.Return`, `mocks/generated.go`) rather than prose.

**Code involved**

| What | Where |
| --- | --- |
| Readiness, per backend | `internal/gfy/claudecli.go:162` (`BackendReady`) |
| Local server probe | `internal/gfy/local.go:1009` (`probeLocalNow`) |
| Job-time gate | `cmd/ggraphify-job/main.go:173` |
| Confirm-dialog copy | `internal/ui/actions.go:284` (`meteredNotice`) |
| Diagnostics copy | `internal/ui/diag.go:319` (`describeBackend`) |

**Change.** Evaluate `BackendReady(settings.Backend)` on **startup** and whenever the
setting changes, not only per job. When it is not ready, show a persistent banner naming
the failure and offering the ready alternatives the app can already detect — including
`opencode-go`, which this app *hosts itself* (see D). One click sets the setting; never
switch silently, since backend choice is a spend decision.

**Done when:** launching with a dead pin surfaces the problem before any job is queued,
and the banner's suggested backend actually runs a metered job to completion.

**Done.** `gfy.BackendVerdict` / `gfy.BackendChoices` / `gfy.ReadyBackends`
(`internal/gfy/ready.go`) answer "is this one ready, and what is ready instead". The board
asks on startup and whenever the backend, the model or the OpenCode model setting moves
(`internal/ui/backend.go`), shows a banner of its own — separate from the version banner, so
neither can hide the other — and re-asks every 30s while it is up, so a server started in
another terminal takes it down by itself. The banner's button opens a dialog listing every
ready alternative with the reason it would work; one click writes the setting and nothing
else. It never switches silently: which backend runs is which account pays.

Note on the premise: as of this implementation the `ollama` pin is **not** dead — the server
is up at `http://localhost:11434/v1` with `qwen2.5-coder:7b`, and `ggraphify doctor` reports
the backend `ok`. So the "launching with a dead pin" half is verified against the code path
rather than against a live failure.

---

## C — Close the loop on staleness

**The gap.** Measured 2026-09-18 across the 139 repos in the graft workspace: **71 stale,
68 fresh.** "Stale" = the `Built from commit:` in `GRAPH_REPORT.md` is not the repo's
current `HEAD`. A stale graph still describes real structure, but a node's `file:line` can
point at code that has moved — which is exactly the kind of quiet wrongness that erodes
trust in the graph.

The app already has every ingredient: `internal/discover/discover.go:346` (`head`) reads
the branch and sha, `internal/graphstate/drift.go:85` (`MakeBaseline`) builds a baseline,
`internal/store/store.go:695` (`SetDriftBaseline`) persists it, and
`internal/ui/ui.go:1041` (`ackDrift`) acknowledges it. What is missing is the step from
"drift is known" to "drift is resolved".

**Change.**
1. Record `built_commit` in the index from A (it is already in the report header).
2. Add a **Stale** column and filter to the repo list, computed as
   `built_commit != head(repo)`.
3. Offer "rebuild all stale" **in the free lane**. An AST refresh needs no LLM, no key and
   no spend — for the current 71 that is a background pass, not a billing decision, so it
   should not sit behind the metered confirm.

**Done when:** the stale count is visible without running anything, and clearing it is one
action.

**Done.** `built_commit` (and `head_commit`, and a `stale` boolean) are in the index from A.
The board already carried the per-row verdict — `Row.Behind`, the `≠` in the Branch column
and the `behind HEAD` count in the status line, from commit `c6d9a02` — so what this added is
the filter and the remedy: a **Behind HEAD** chip beside the state chips
(`internal/ui/header.go`, `internal/ui/columns.go`), `ggraphify-scan -behind`, and
`actUpdateBehind` — **Ctrl+U**, or the header button — which queues `update` on every
behind-HEAD row in the **free lane**, with no metered confirm in front of it.

No separate "Stale" column was added: the Branch cell already renders `≠` with its own CSS
class for exactly this, and a second column saying the same thing would be noise.

Current count on this machine: **77 of 206** graphs behind HEAD (`./ggraphify-scan -behind`),
not the 71 of 139 measured across the graft workspace when the plan was written.

---

## D — Make the OpenCode proxy's lifetime explicit

**The gap.** `internal/gfy/opencodeproxy.go:112` (`StartOpenCodeProxy`) binds
`127.0.0.1:11437`, and it is served **by the GUI process**. That endpoint is genuinely
useful to other tools — a fleet-wide `graft build --deep` is running against it right now
with `deepseek-v4.1-flash`, registered via `~/.graphify/providers.json`. But it vanishes
the moment ggraphify quits, and an external consumer just sees its LLM disappear
mid-run.

**Change.** Pick one:
- **Minimum:** on quit, if the proxy served a request in the last N minutes, say so and
  confirm. Cheap, and it converts a silent failure into a decision.
- **Better:** a headless `ggraphify --serve-proxy` mode so the gateway can outlive the GUI
  and be supervised.

Either way, say in the README that the proxy is GUI-bound today — external users will
otherwise assume a service.

**Done — the minimum, deliberately.** The proxy now counts what it forwards and records
whether *this* process bound the port (`gfy.OpenCodeProxyActivity`); a proxy inherited from
another ggraphify is that process's to lose, not ours. Quitting with our own proxy having
served a request in the last ten minutes opens a dialog naming the endpoint, the request
count and how long ago the last one was, with **Keep running** / **Quit anyway**
(`internal/ui/proxyquit.go`). The README now states plainly that the endpoint is GUI-bound,
that there is no daemon and no `--serve-proxy` mode, and what an external consumer sees when
the window closes.

---

## E — Surface which Claude account the CLI backend uses

**The gap.** `claude_account` is `/home/dns/.claude-nova`, **not** the default
`~/.claude`. This is deliberate and should stay. The risk is purely that it is invisible:
anyone reasoning about a `claude-cli` job will assume their own config dir, and therefore
assume their own hooks, skills, settings and login apply. They do not. A Claude Code
session in `/home/dns/git` runs with a `PreToolUse` guard and a set of hooks that
ggraphify's `claude -p` invocations never see.

**Change.** Print the resolved config dir in the confirm dialog and in the job log,
alongside the backend and model that are already shown. One line. No behavior change.

**Done.** `gfy.AccountNote(kind, backend, sel)` is the one sentence, and it is emitted for
exactly the jobs the account bears on — a `claude-cli` backend, or `install`, and nothing
else. It reaches: the metered confirm (which already had it), the *free* confirm for
`install` (`internal/ui/actions.go`), and the top of every job's log through the new
`Job.Notes` (`internal/jobs/jobs.go`), so the headless runner prints it too. It says the
directory, and says when it is not the default `~/.claude` and therefore not the hooks,
skills and login a Claude Code session in a checkout runs with.

---

## F — `ggraphify doctor`: assert the invariants

Give the alignment a single command, so neither a human nor an agent has to remember the
checks:

```
$ ggraphify doctor
  backend        opencode-go        ready (proxy 127.0.0.1:11437, deepseek-v4.1-flash)
  out_base       ~/knowledge        215 graphs, index.json fresh
  staleness      71 of 139 stale    → "rebuild stale" in the free lane
  claude account /home/dns/.claude-nova  (not the default ~/.claude)
  semantic tier  0% of files        → metered pass has never completed
```

Exit non-zero when an invariant is broken so it can gate a cron or a pre-flight.

**Done.** `ggraphify doctor` — a subcommand of the GUI binary, not a fourth executable,
because what it asserts is this application's own configuration and resolving that twice is
the drift it exists to catch. `internal/doctor` holds the checks (no GTK), `doctor.go` the
command; `-json` and `-strict` are there for a cron. Exit 1 on a broken invariant, 0
otherwise; `-strict` fails on warnings too.

Live output today:

```
  graphify        0.9.58 at ~/.local/bin/graphify
  backend         ollama
                  Local model server at http://localhost:11434/v1 — running qwen2.5-coder:7b…
  out_base        ~/knowledge
                  206 graph(s), 206 in ~/knowledge/index.json, written 13m ago; look one up
                  with: jq -r '.entries["'"$PWD"'"].graph' ~/knowledge/index.json
  staleness       77 of 206 graphs behind HEAD   [warn]
  claude account  nova (/home/dns/.claude-nova)
  semantic tier   13% of 78875 files carry a semantic hash   [warn]
                  158 of 206 graphs are AST-only
```

**The plan's "0% of files" is out of date.** Measured across all 206 graphs by
`graphstate.CoverageOf`: 13% of 78 875 manifest entries carry a `semantic_hash`, 18 graphs
are fully semantic and 30 partial; 158 are AST-only. The metered pass *has* completed on
some of this estate.

---

## Order of work

All six shipped in the order below, plus tests: `internal/knowledge/index_test.go`,
`internal/doctor/doctor_test.go`, `internal/graphstate/tier_test.go`, and cases added to
`internal/gfy/claudeaccount_test.go`, `internal/jobs/jobs_test.go`, `internal/ui/keys_test.go`.

| # | Item | Size | Why this order |
| --- | --- | --- | --- |
| 1 | **A** — `index.json` | S | Unblocks every external consumer; lets the Claude Code hook stop globbing |
| 2 | **B** — startup backend check | S | The reason the semantic tier has never run once |
| 3 | **C** — stale column + free rebuild | M | Depends on A for `built_commit`; clears 71 repos |
| 4 | **F** — `doctor` | S | Cheap once A–C exist; makes regressions visible |
| 5 | **E** — show the account | XS | One line, no behavior change |
| 6 | **D** — proxy lifetime | M | Only matters while external tools depend on the gateway |

## Verifying the current state

```bash
# where graphs actually are, and the mapping back to source
ls -d ~/knowledge/*/ | wc -l
cat ~/knowledge/acquiring-*/.graphify_root

# staleness across the graft workspace
cd /home/dns/git
for r in $(jq -r '.children[]' graft/workspace.json); do
  d=$(ls -d ~/knowledge/${r}-*/ 2>/dev/null | head -1); [ -z "$d" ] && continue
  c=$(head -c 2048 "$d/GRAPH_REPORT.md" | sed -n 's/^- Built from commit: `\([0-9a-f]*\)`.*/\1/p')
  h=$(git -C "$r" rev-parse --short=${#c} HEAD 2>/dev/null)
  [ "$h" = "$c" ] || echo "STALE $r"
done

# semantic tier coverage (expect 0% until B is done)
python3 -c "
import json,glob
m=json.load(open(glob.glob('$HOME/knowledge/acquiring-*/manifest.json')[0]))
print(sum(1 for v in m.values() if v.get('semantic_hash')), 'of', len(m), 'files have semantic_hash')"

# the settings this plan refers to
jq -c '.settings | {backend, opencode_model, out_base, claude_account}' \
  ~/.local/share/ggraphify/state.json
```

## Notes for whoever picks this up

- **Do not edit `~/.local/share/ggraphify/state.json` while the app runs.** It holds
  settings in memory and persists on quit, so a file edit is silently reverted. Change
  settings through the GUI.
- The Claude Code side of this alignment lives in `~/CLAUDE.md` (the `## graphify` block)
  and `~/.claude/helpers/graphify-hint.cjs`. If A lands, simplify the hook to read
  `index.json` and delete the glob.
- Nothing in this plan requires changing `out_base`. `~/knowledge` is a reasonable place
  for graphs; it just needs to be discoverable from the outside.
