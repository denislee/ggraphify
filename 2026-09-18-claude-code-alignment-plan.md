# Keeping ggraphify aligned with the Claude Code configuration

**Date:** 2026-09-18
**Repo:** `ggraphify` (this one)
**Audience:** whoever changes this app next
**Status:** proposed — nothing here is implemented yet

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

---

## Order of work

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
