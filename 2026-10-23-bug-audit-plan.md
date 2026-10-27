# ggraphify — bug audit of existing features (2026-10-23)
<!-- plan-doc: v2 -->
**Progress:** 100% — 39/39
**Status:** 🟢 All 39 fixed in the working tree — tests green, not committed
**Last updated:** 2026-10-23 — all W1–W39 fixed, go test -race ./... 23/23 ok
**Beads:** git-lh65v

## Handoff

Next action: review the working-tree diff and commit (TECH Linear issue per the commit rule); tell users of Ollama Cloud to export OLLAMA_CLOUD_API_KEY (W9). Optional: expose autofix MeteredPerDay as a setting.
In play: /home/dns/tmp/ggraphify main@336382d — the 17 pre-existing uncommitted files plus these fixes, all uncommitted.

## Work

- [x] **W1** HIGH store.Save marshals live maps after releasing the lock — fatal concurrent map read/write — sev: high · conf: confirmed · code: committed · src: internal/store/store.go Store.Save · bd: git-lh65v.1
- [x] **W2** HIGH single-instance flock released when GC finalizes the leaked *os.File — sev: high · conf: confirmed · code: committed · src: main.go singleInstance · bd: git-lh65v.2
- [x] **W3** HIGH autofix signature flip resets attempts and skips cooldown — repeated metered runs — sev: high · conf: confirmed · code: committed · src: internal/autofix/autofix.go Engine.Plan, fleet.go reserve · bd: git-lh65v.3
- [x] **W4** HIGH shared opencode proxy: cached-root path skips the plan check — sev: high · conf: confirmed · code: uncommitted · src: internal/gfy/opencodeproxy.go StartOpenCodeProxy · bd: git-lh65v.4
- [x] **W5** HIGH ggraphify-job stdout flush treats the ring buffer as append-only — output stops and tail is lost — sev: high · conf: confirmed · code: committed · src: cmd/ggraphify-job/main.go flush · bd: git-lh65v.5
- [x] **W6** MED jobs Retry bypasses SubmitCmd and the metered confirm — sev: med · conf: confirmed · code: committed · src: internal/ui/jobsview.go retryJob · bd: git-lh65v.6
- [x] **W7** MED job Wait hangs while a grandchild holds the stdout pipe (no WaitDelay) — sev: med · conf: confirmed · code: committed · src: internal/jobs/jobs.go run · bd: git-lh65v.7
- [x] **W8** MED providers.json read-modify-write is unlocked and non-atomic — sev: med · conf: confirmed · code: committed · src: internal/gfy/opencode.go EnsureOpenCodeProvider · bd: git-lh65v.8
- [x] **W9** MED Ollama Cloud key shares OLLAMA_API_KEY and leaks to local/LAN graft targets — sev: med · conf: confirmed · code: uncommitted · src: internal/gfy/graftdeep.go, opencode.go OllamaCloudKeyVar · bd: git-lh65v.9
- [x] **W10** MED shared proxy never re-probed after the owning board exits — sev: med · conf: confirmed-by-reading · code: committed · src: internal/gfy/opencodeproxy.go StartOpenCodeProxy · bd: git-lh65v.10
- [x] **W11** MED DriftOf never walks a symlinked checkout root — zero drift forever — sev: med · conf: confirmed · code: committed · src: internal/graphstate/drift.go DriftOf · bd: git-lh65v.11
- [x] **W12** MED DriftOf skips every directory named basename(out) at any depth — sev: med · conf: confirmed · code: committed · src: internal/graphstate/drift.go DriftOf · bd: git-lh65v.12
- [x] **W13** MED graph.json over MaxGraphBytes never gets BuiltCommit — never Behind, never rebuilt — sev: med · conf: confirmed-by-reading · code: committed · src: internal/graphstate/graphstate.go readStatic, finishRead · bd: git-lh65v.13
- [x] **W14** MED discover follows symlinks but dedupes on the unresolved path — duplicate rows and jobs — sev: med · conf: confirmed · code: committed · src: internal/discover/discover.go walkRoot, isDirEntry · bd: git-lh65v.14
- [x] **W15** MED store.Open silently replaces a corrupt state.json with defaults — sev: med · conf: confirmed · code: committed · src: internal/store/store.go Open · bd: git-lh65v.15
- [x] **W16** MED opencode settings availability sweep wedges when the plan changes mid-sweep — sev: med · conf: confirmed · code: committed · src: internal/ui/opencode.go settingsOpenCode sweep · bd: git-lh65v.16
- [x] **W17** LOW-MED applyScale always writes gtk-xft-dpi, freezing desktop text scaling — sev: low-med · conf: plausible · code: uncommitted · src: internal/ui/ui.go applyScale · bd: git-lh65v.17
- [x] **W18** LOW-MED globalgraph staleness is mtime-only — byte-identical rebuild stays global-stale forever — sev: low-med · conf: plausible · code: committed · src: internal/globalgraph/globalgraph.go MemberOf · bd: git-lh65v.18
- [x] **W19** LOW-MED usage index double-counts events when a transcript shrinks/rotates — sev: low-med · conf: plausible · code: committed · src: internal/usage/index.go Update · bd: git-lh65v.19
- [x] **W20** LOW typed-count confirm gate leaves the free (--code-only) response enabled — sev: low · conf: confirmed · code: committed · src: internal/ui/actions.go confirm · bd: git-lh65v.20
- [x] **W21** LOW graft-deep sync confirm does not re-dedupe queue at response time — sev: low · conf: plausible · code: uncommitted · src: internal/ui/graft.go confirmSyncGraftDeep · bd: git-lh65v.21
- [x] **W22** LOW OLLAMA_API_KEY ignored by HasAPIKey/EffectiveBackend and the settings key row — sev: low · conf: confirmed · code: uncommitted · src: internal/gfy/env.go HasAPIKey, internal/ui/settings.go · bd: git-lh65v.22
- [x] **W23** LOW concurrent StartOpenCodeProxy can bind several proxies; port 0 accepted — sev: low · conf: confirmed · code: committed · src: internal/gfy/opencodeproxy.go StartOpenCodeProxy · bd: git-lh65v.23
- [x] **W24** LOW base-URL overlay vars offered in settings have no effect — sev: low · conf: confirmed · code: committed+uncommitted · src: internal/gfy/env.go KnownVars, opencode.go OpenCodeUpstream · bd: git-lh65v.24
- [x] **W25** LOW OpenCodeModelFor called with nil overlay — wrong preflight model and pricing — sev: low · conf: plausible · code: committed · src: internal/gfy/opencode.go · bd: git-lh65v.25
- [x] **W26** LOW stale ollama PID kept after ollama serve exits on its own — sev: low · conf: plausible · code: committed · src: internal/gfy/local.go, ollamactl.go StopOllama · bd: git-lh65v.26
- [x] **W27** LOW setPaused races a natural exit — stopped leftovers, finished row shows Paused — sev: low · conf: plausible · code: committed · src: internal/jobs/jobs.go setPaused · bd: git-lh65v.27
- [x] **W28** LOW CLI exit codes and false interrupt message — sev: low · conf: confirmed · code: committed · src: cmd/ggraphify-job/main.go, doctor.go · bd: git-lh65v.28
- [x] **W29** LOW ringbuf Write(nil) drops a pending carriage return — sev: low · conf: confirmed-by-reading · code: committed · src: internal/ringbuf/ringbuf.go Write · bd: git-lh65v.29
- [x] **W30** LOW globalgraph byPath picks a random tag when two share a source_path — sev: low · conf: plausible · code: committed · src: internal/globalgraph/globalgraph.go Load · bd: git-lh65v.30
- [x] **W31** LOW cache Prune deletes gen counters — in-flight guard defeated — sev: low · conf: plausible · code: committed · src: internal/graphstate/cache.go Prune, internal/graftstate/cache.go Prune · bd: git-lh65v.31
- [x] **W32** LOW Size column stale — dirSize cached in the never-expiring static layer — sev: low · conf: confirmed-by-reading · code: committed · src: internal/graphstate/graphstate.go dirSize, internal/graftstate/graftstate.go · bd: git-lh65v.32
- [x] **W33** LOW GRAPH_REPORT.md restamp written in place, not temp+rename — sev: low · conf: plausible · code: committed · src: internal/graphstate/restamp.go restampReport · bd: git-lh65v.33
- [x] **W34** LOW usage Index.Save uses a fixed .tmp name with no fsync — sev: low · conf: plausible · code: committed · src: internal/usage/index.go Index.Save · bd: git-lh65v.34
- [x] **W35** LOW store Flush races an in-flight timer save; saveErr never cleared — sev: low · conf: plausible · code: committed · src: internal/store/store.go Flush, schedule · bd: git-lh65v.35
- [x] **W36** LOW autofix Reset/Retry wipe running records — up to 2x Max chains — sev: low · conf: confirmed · code: committed · src: internal/autofix/autofix.go Reset, Retry · bd: git-lh65v.36
- [x] **W37** LOW EDITOR with arguments is treated as a single program name — sev: low · conf: confirmed · code: committed · src: internal/open/open.go InEditor, spawn · bd: git-lh65v.37
- [x] **W38** LOW ollama-cloud catalog offers deepseek-v4-pro which the gateway does not serve — sev: low · conf: confirmed · code: uncommitted · src: internal/gfy/opencode.go ollamaCloudCatalog · bd: git-lh65v.38
- [x] **W39** CLEANUP dead code actGlobalAdd and mergeSelected; restamp errcheck — sev: cleanup · conf: confirmed · code: committed · src: internal/ui/actions.go actGlobalAdd, internal/ui/jobsview.go mergeSelected, internal/graphstate/restamp.go · bd: git-lh65v.39

## Blocked / needs a human

## Context

Scope: a read-only bug audit of every existing feature of ggraphify at `main@336382d`, **including the uncommitted working-tree changes** (17 files, +418/−108 — the Ollama Cloud plan, the UI Scale setting, the graft-deep confirm, and proxy changes) and the untracked `internal/gfy/ollamacloud_test.go`. Nothing in the repo was changed.

Method:
- `go vet ./...` clean (only gotk4 cgo warnings).
- `go test -race -count=1 ./...` → all 22 packages ok.
- `golangci-lint` (errcheck, govet, staticcheck, ineffassign, unused) → 10 minor issues (2 unused funcs, 2 errcheck, 6 test-style quickfixes).
- Five parallel review passes by subsystem (jobs/cmd · graph state/scan · gfy backends+proxy · store/discover/usage/autofix · ui), each verifying its findings by code reading and, where cheap, throwaway reproductions under /tmp (not in the repo). The highest-severity items (W1, W2, W4, W6, W9, W11, W12) were re-checked against the source in the main session.

Item keys: `sev` high / med / low-med / low / cleanup · `conf` confirmed / confirmed-by-reading / plausible · `code` committed (in main@336382d) or uncommitted (only in the working tree).

## Current state

- 39 findings: 5 high, 11 med, 3 low-med, 19 low, 1 cleanup.
- Cost-safety relevant (README rule "metered — always confirmed"): W3, W6, W20, W21, plus W14 (duplicate jobs) and W16 (background paid probes).
- Crash / data-loss: W1 (fatal map race), W2 (two boards on one state.json), W8 (providers.json), W15 (state.json reset).
- Wrong status shown on the board: W11, W12, W13, W18, W32.
- In uncommitted code: W4, W9, W17, W21, W22, W38 (plus parts of W24) — fixing these before committing the current working tree is cheapest.
- Tests are green, so no existing test covers any of these; each fix needs a regression test plus a negative control.

## Open questions

## Spec

Each block: **Defect** · **Failure** · **Fix**. `conf: confirmed` = reproduced with a throwaway test outside the repo or verified by reading every code path; `plausible` = real code path, narrow or environment-dependent trigger.

### W1 — store.Save marshals live maps after releasing the lock
- **Defect:** `snap := s.s` is a shallow copy under RLock; `json.MarshalIndent(snap)` then walks the live `Repos`/`Columns`/`Baselines` (and `Jobs`) maps unlocked while `SetOverride`, `SetColumnWidth`, `SetDriftBaseline`, `PruneDriftBaselines` mutate them in place.
- **Failure:** debounce-timer Save while the user drags a column or a job records a drift baseline → `fatal error: concurrent map read and map write` (process dies, not a recoverable panic). Reproduced under `-race` with SetColumnWidth ∥ Save.
- **Fix:** marshal while holding the RLock, or deep-copy the maps/slices inside the lock. Add a concurrent Save/Set race test.

### W2 — single-instance flock released by the GC finalizer
- **Defect:** `singleInstance` "deliberately leaks" the locked `*os.File`, but it becomes unreachable when the function returns; the `os.File` finalizer closes the fd on the next GC, releasing the `flock`.
- **Failure:** after any GC cycle (frequent with `SetMemoryLimit`), a second launch succeeds → two boards race on `state.json` and can each start a mutating job on the same repo. Reproduced: open+flock after `runtime.GC()` returned nil; control holding a reference got EWOULDBLOCK.
- **Fix:** keep the file in a package-level var (or `runtime.KeepAlive` at end of main).

### W3 — autofix signature flip resets attempts and skips the cooldown
- **Defect:** in `Engine.Plan`, `case rec.sig != sig` resets attempts and exits the switch without reaching the cooldown case; a healthy tick deletes the record and a new record gets no cooldown. Same pattern in `fleet.go reserve` (workspace signature includes `Summary()`).
- **Failure:** with `Metered: true`, a graph flipping no-graph ↔ broken produced 10 metered `extract` plans in 10 back-to-back ticks, each attempt=1. Realistic loops: killed extract leaving none/partial graph.json; drift → free update → unnamed → paid `label` → healthy (record deleted) → next edit → repeat (unnamed labels have no `HubRelabelCooldown` equivalent); fleet `graft build` restarts whenever child counts change. Violates the README cost-safety rule.
- **Fix:** keep `last` across signature changes and always enforce a minimum cooldown; keep healthy records until the cooldown expires; add a per-repo cap on metered runs per day.

### W4 — shared proxy cached-root path skips the plan check
- **Defect:** once `proxyState.root` is set, `StartOpenCodeProxy` returns `root + plan.ProxyPath` without checking that a *shared* (not owned) proxy serves that plan; the probe's `plans=` list is never stored.
- **Failure:** board shares an older board's port for an `opencode-go` job; a later `ollama-cloud` job gets `…/ollama/v1`, the old proxy 404s every chunk, graphify reports only "all semantic chunks failed". Reproduced: first ollama-cloud call refused, Go accepted, second ollama-cloud call accepted with err=nil. (Same latent gap existed for Zen in committed code.)
- **Fix:** store `plans` and `owned` in `proxyState`; run `planListed` on the cached path when `!owned`.

### W5 — ggraphify-job stdout flush assumes an append-only buffer
- **Defect:** `flush` prints `job.Log.String()[printed:]`, but the ring buffer evicts from the front, prepends an "… N earlier lines elided …" marker, rewrites lines on `\r`, and `retire` compacts to 32 KiB before the terminal event.
- **Failure:** any job whose log exceeds `ringbuf.DefaultCap` (256 KiB) goes silent on stdout or prints from wrong offsets; the unprinted tail is lost at retire. Reproduced: 22 000 bytes through a 4 KiB buffer printed 4 126 bytes, last line missing.
- **Fix:** an absolute write offset in ringbuf with `Since(off)`, or tee subprocess output straight to stdout in the headless command.

### W6 — jobs Retry bypasses SubmitCmd and the metered confirm
- **Defect:** `retryJob` hand-builds a `jobs.Job` and calls `runner.Submit`: no `Local`, no `GraftDeepEnv`, no `Out`/`Notes`, no precheck (`RequireGraph`/`RequireLocalServer`), no `PreflightOpenCode`/`EnsureOpenCodeProvider`. The toolbar Retry accepts any Done job, including succeeded/cancelled metered ones, with no confirm.
- **Failure:** retried ollama extraction lands in the metered lane without a `LocalLease` (AutoOllama may idle-stop the server mid-run); retried `graft-deep` lacks `GRAFT_API_KEY` and fails again; one click re-runs a paid extraction, contradicting "metered — always confirmed".
- **Fix:** route Retry through `SubmitCmd` (or a `RetryCmd` that rebuilds the same fields); gate `Cost==Metered && !Local` through `confirm()`/`meteredNotice`.

### W7 — job Wait hangs while a grandchild holds the stdout pipe
- **Defect:** `cmd.Stdout` is not an `*os.File`, so `cmd.Wait` blocks until every holder of the pipe closes it; `WaitDelay` is unset and `reap` only signals on cancel.
- **Failure:** graphify or `claude` leaves a lingering/`setsid` grandchild → job stays Running, holds its lane and `busyRepo`; `Runner.Close` blocks in `wg.Wait` forever; in ggraphify-job a second Ctrl-C is swallowed. Reproduced: `sh -c 'setsid sleep 3 & echo'` → Wait returned after 3.0 s.
- **Fix:** set `cmd.WaitDelay`; on parent exit, SIGTERM/SIGKILL the remaining process group.

### W8 — providers.json read-modify-write unlocked and non-atomic
- **Defect:** `EnsureOpenCodeProvider` reads, edits and `os.WriteFile`s (truncate in place), no lock, no rename.
- **Failure:** concurrent submits for different plans, or GUI + ggraphify-job together, clobber entries or interleave into broken JSON; every later submit refuses on the parse error; a graphify child starting mid-write sees a truncated file → "unknown backend". Reproduced: 3 concurrent calls → unparseable JSON in 200/200 runs.
- **Fix:** flock a sibling `.lock` around the whole RMW; write temp + fsync + `os.Rename`.

### W9 — Ollama Cloud key shares OLLAMA_API_KEY with local targets
- **Defect:** the uncommitted diff sets `OllamaCloudKeyVar = OllamaKeyVar`; the local graft-deep target (ollama, or openai pointed at a LAN server) also reads `OLLAMA_API_KEY` and exports it as `GRAFT_API_KEY` (`GraftDeepEnv`).
- **Failure:** user exports the paid Ollama Cloud key → every local deep pass sends it to whatever `OLLAMA_HOST`/`OPENAI_BASE_URL` points at (tailnet box, LM Studio).
- **Fix:** a dedicated variable (e.g. `OLLAMA_CLOUD_API_KEY`), or stop sourcing local credentials from `OllamaKeyVar` unless the overlay sets it explicitly.

### W10 — shared proxy never re-probed after its owner exits
- **Defect:** after sharing another process's proxy, `proxyState.root` is cached for the process lifetime.
- **Failure:** board B shares board A's proxy; A quits → every later B job (graphify, graft deep, providers.json `base_url`) gets connection refused until B restarts, although the port is free.
- **Fix:** when `!owned`, re-probe on each Start (2 s timeout); on failure clear the cached root and bind.

### W11 — DriftOf never walks a symlinked checkout root
- **Defect:** `filepath.WalkDir` Lstats its root, so a symlinked `repo` is visited as one non-dir entry; `discover.isDirEntry` deliberately boards symlinked checkouts and keeps the link as `Repo.Path`.
- **Failure:** `~/git/foo -> /data/foo` always reports zero drift: git rows look fresh with pending edits; NoGit rows never `NeedsRebuild`, so the sweep never rebuilds them. Reproduced: real path `{Added:[b.go] Changed:[a.go]}`, symlink `{}`.
- **Fix:** `filepath.EvalSymlinks(repo)` before walking.

### W12 — DriftOf skips every directory named basename(out)
- **Defect:** `name == outBase` matches at any depth.
- **Failure:** an in-tree out like `.kb/graph` hides every `graph/` dir (e.g. Go `internal/graph/`); an absolute override `~/graphs/acquiring` hides `cmd/acquiring/`. New files there never count as Added. Reproduced with `internal/graph/new.go`.
- **Fix:** skip only when `filepath.Join(repo, rel) == filepath.Clean(out)`; keep the `DefaultOutName` basename rule.

### W13 — oversized graph.json never gets BuiltCommit
- **Defect:** the `> MaxGraphBytes` branch of `readStatic` never fills `BuiltCommit`; `finishRead` leaves Behind/drift untouched when Err is "too large". `CommitOnly` mirrors it deliberately.
- **Failure:** a large monorepo's `Behind()` is always false → `Row.Behind`, `NeedsRebuild`, the `behind` issue and `knowledge Entry.Stale` stay false; the sweep never rebuilds it after HEAD moves.
- **Fix:** call `ReadBuiltCommit(out)` (4 KB tail read) in that branch; let Behind/drift be computed even when the body is too large.

### W14 — discover dedupes symlinked dirs on the unresolved path
- **Defect:** symlinked directories are followed but `seen` is keyed on the link path.
- **Failure:** `root/api` + `api-link` + `grp/loop -> root` → 4 rows for one checkout, each with its own `Busy` and autofix record → up to 4 concurrent graphify jobs (metered included) on one tree writing the same out dir. Ancestor links re-walk the root per depth level (bounded by Depth).
- **Fix:** key `seen` on `EvalSymlinks(dir)`; skip links resolving to an ancestor.

### W15 — store.Open silently replaces a corrupt state.json
- **Defect:** a parse error falls back to defaults; the first debounced save overwrites the file.
- **Failure:** one stray brace from a hand edit loses all overrides, baselines, the persisted job queue and settings, silently.
- **Fix:** rename to `state.json.corrupt-<ts>` before continuing; log and toast it.

### W16 — opencode settings sweep wedges on a plan change
- **Defect:** both idle callbacks of the `sweep` closure return early when `plan.Backend != backend`, so `sweeping` is never reset; the goroutine has no cancel and keeps probing (paid verification requests) the old plan's gateway for up to 5 min.
- **Failure:** settings open on Zen (auto-sweep starts), switch to Ollama Cloud → the new plan's auto sweep is skipped, "Check availability" does nothing, the row says "Asking the gateway…" forever (dialog lives for the process).
- **Fix:** per-sweep `context.CancelFunc`; in `ocApply` cancel it and reset `sweeping` on plan change.

### W17 — applyScale always writes gtk-xft-dpi
- **Defect:** writes the property on every launch even at scale 1.0, marking it application-set so later XSETTINGS/portal updates no longer apply; `baseDPI` captured once.
- **Failure:** a user who never touched Scale turns on "Large text" → board ignores it until restart; a user with a scale gets it multiplied against a stale base.
- **Fix:** skip the write at 1.0 when no override was ever applied; refresh `baseDPI` on `notify::gtk-xft-dpi` changes not made by us.

### W18 — globalgraph staleness is mtime-only
- **Defect:** `MemberOf` compares graph.json mtime with `added_at`. graphify always rewrites graph.json; `global_add` skips identical `source_hash` without bumping `added_at`.
- **Failure:** after cluster-only / no-op relabel the row stays `global-stale` forever; autofix tries once then reports "gave up: survived a re-add" on a current member.
- **Fix:** when mtime says stale and `Entry.Hash` is set, hash graph.json (sha256, 16 hex) and clear Stale on match; cache per mtime.

### W19 — usage index double-counts after a transcript shrinks
- **Defect:** the shrink branch of `Update` re-reads from offset 0 but only corrects `Tools`; every Event is emitted again. A file rewritten then grown past the old offset resumes mid-line in different content.
- **Failure:** Days/Session counts and Recent inflated after rotation/rewrite.
- **Fix:** record per-file contributions to subtract, or a head hash/inode in `FileState` and treat a rewrite as replacement.

### W20 — typed-count confirm leaves "free" enabled
- **Defect:** the entry handler disables only the `"run"` response.
- **Failure:** a 200-repo metered extract confirm can be answered with a 200-repo `--code-only` extract without typing the count — the fan-out the gate exists to stop.
- **Fix:** also `SetResponseEnabled("free", …)`.

### W21 — graft-deep sync confirm doesn't re-dedupe
- **Defect:** `liveGraftDeep` dedupe runs when the dialog opens only; `submit`/`SubmitCmd` don't dedupe.
- **Failure:** while the typed-count confirm is open, another path queues `graft-deep` on the same repos → duplicates; on a metered pin each is a full per-file bill.
- **Fix:** filter `queue` against `a.liveGraftDeep()` again in the `"sync"` response.

### W22 — OLLAMA_API_KEY not counted as a key
- **Defect:** `HasAPIKey`, `EffectiveBackend`, `BackendReady("")` ignore `OLLAMA_API_KEY`; the new ollama-cloud sentence in the settings key row is nested inside `if HasAPIKey()`. doctor still says "set OPENCODE_API_KEY or OLLAMA_API_KEY".
- **Failure:** with only `OLLAMA_API_KEY` set the row says "No API key is visible — metered commands will fail" while ollama-cloud works; auto-detect finds nothing.
- **Fix:** check `HasPlanKey(OllamaCloudBackend)` outside the branch; decide whether auto-detect should consider it.

### W23 — concurrent StartOpenCodeProxy can bind several proxies
- **Defect:** the lock is dropped between the `root` check and `Listen`; port 0 is accepted (`n>=0`).
- **Failure:** with port 0, concurrent Starts each bound and served a proxy (3 in one process observed); goroutines/ports leak and providers.json points at whichever wrote last.
- **Fix:** a start mutex / `sync.Once` across check+bind; reject port 0.

### W24 — base-URL overlay vars are inert
- **Defect:** `KnownVars` offers `OLLAMA_CLOUD_BASE_URL`, `OPENCODE_ZEN_BASE_URL`, `OPENCODE_BASE_URL`, but `OpenCodeUpstream` reads only the board's process env and nothing applies the overlay.
- **Failure:** a user routes Ollama Cloud via a corporate proxy in the overlay; traffic still goes to `https://ollama.com/v1` with no warning.
- **Fix:** drop them from `KnownVars`, or pass the overlay to `OpenCodeUpstream`.

### W25 — OpenCodeModelFor called with a nil overlay
- **Defect:** preflight, providers.json and pricing call `OpenCodeModelFor(nil)`.
- **Failure:** blank model setting + `GRAPHIFY_OLLAMA_CLOUD_MODEL=glm-5.3` in the overlay → graphify runs glm-5.3 but preflight checks the default and cost estimates use its price (~9× too low). Plausible: the UI may always fill a model (`actions.go`).
- **Fix:** pass the job's overlay through.

### W26 — stale ollama PID
- **Defect:** the reaper goroutine never clears the recorded PID when `ollama serve` exits by itself.
- **Failure:** a later idle-stop sends SIGTERM to `-pid`, possibly a recycled unrelated process group.
- **Fix:** `noteOllamaStart(…, 0)` after `Wait` if the PID still matches.

### W27 — setPaused races a natural exit
- **Defect:** `Status==Running` is checked under the lock, then released before SIGSTOP; `j.paused=true` can be written onto a job already in history.
- **Failure:** surviving group members stay stopped forever; the finished row shows Paused; `handOverLane` releases a held job anyway.
- **Fix:** set `j.exited` under the lock in `run` before Wait returns; re-check it at the second lock in `setPaused`.

### W28 — CLI exit codes and false interrupt message
- **Defect:** ggraphify-job's watcher fires on the deferred `stop()` (closes `ctx.Done()`), printing "interrupt — cancelling the job" and calling Cancel on every normal exit; SIGTERM exits 130 like SIGINT (should be 143); `ggraphify doctor -h` exits 2 because `flag.ErrHelp` is treated as an error.
- **Fix:** select on a separate done channel; map SIGTERM→143; return 0 on `flag.ErrHelp`.

### W29 — ringbuf Write(nil) drops pendingCR
- **Defect/Fix:** an empty write clears `pendingCR` without erasing the line → next progress update appends instead of replacing. Return early on `len(p)==0`. (io.Copy never writes empty, so impact is low.)

### W30 — globalgraph byPath nondeterministic
- **Defect/Fix:** two tags sharing one `source_path` (re-added with `--as`) → `byPath` keeps whichever map iteration reaches last; Tag/AddedAt/Stale flicker between ticks. Pick deterministically (newest `AddedAt`).

### W31 — cache Prune resets gen counters
- **Defect:** deleting `gen[repo]` resets it to 0; Read(gen=1) → Prune → Invalidate(gen=1) → stamps match → stale result cached for a full TTL; a Read finishing after Prune re-adds the pruned key.
- **Fix:** never delete counters (or use a global epoch); bump `clears` in Prune.

### W32 — Size column stale
- **Defect/Fix:** `dirSize` lives in the never-expiring static layer keyed on top-dir mtime and a few named files; growth inside `cache/` or graft card subdirs never updates Size. Display only — move size to the TTL layer or key it on a cheap walk stamp.

### W33 — GRAPH_REPORT.md restamped in place
- **Defect/Fix:** `restampReport` uses `os.WriteFile`; a crash/full disk truncates the report agents read. Use the same temp+rename as graph.json.

### W34 — usage Index.Save fixed temp name
- **Defect/Fix:** `path + ".tmp"`, no fsync; overlapping GUI background save, window-close `saveUsage` and `ggraphify-scan` can tear it → `Load` discards → cold rescan. Use `os.CreateTemp` + Sync like `store.Save`.

### W35 — store Flush races an in-flight timer save
- **Defect/Fix:** `Flush` stops the timer but doesn't wait for a callback already running; an older snapshot can rename over the newer one after Flush returns (window close). Serialize saves with a mutex + generation. Also clear `saveErr` after a later successful save (UI keeps showing a gone error).

### W36 — autofix Reset/Retry wipe running records
- **Defect/Fix:** `Reset` runs on every autofix settings change and drops records still running; the later `Done` is a no-op and in-flight drops to 0 → next tick can queue another Max chains (up to 2×Max). Keep `running` records as `Prune` does.

### W37 — EDITOR with arguments
- **Defect/Fix:** `EDITOR="code --wait"` passed as argv0 → LookPath fails; `emacsclient -t` not detected as terminal; `nvim -u NONE` fails silently inside the terminal. Split with a shell-word splitter (no shell → no injection).

### W38 — ollama-cloud catalog offers an unserved model
- **Defect/Fix:** baked `ollamaCloudCatalog` lists `deepseek-v4-pro`, absent from live `https://ollama.com/v1/models` today; the preflight catches it but the dropdown offers it. Drop it or filter by the live list.

### W39 — cleanup
- `actGlobalAdd` (replaced by `actGlobalToggle`, key `m`) and `jobsView.mergeSelected` (replaced by the Global screen merge button) are unused — no feature lost; delete.
- `restamp.go`: unchecked `tmp.Close`/`os.Remove` (errcheck).
- Tooling: `~/go/bin/staticcheck` is too old for the toolchain (export data v5); use `golangci-lint` or rebuild staticcheck.

## Log

### 2026-10-23 — all 39 fixed (working tree, uncommitted)
- Gate: `go test -race -cover -count=1 ./...` → 23/23 ok; `go build ./...` + gofmt clean; golangci-lint errcheck/govet/unused/ineffassign 0, staticcheck 7 style QF only.
- Every fix has a regression test; each negative control (bug re-inserted by edit, fix copied back, cmp-verified) failed its test.
- W1 Store.save marshals under RLock — TestSaveIsSafeAgainstConcurrentSetters (ctl: fatal concurrent map write). W2 package var instanceLock — TestInstanceLockSurvivesGC (ctl in /tmp module). W15 Open renames to state.json.corrupt-<ts>, Store.Corrupt() + ui announceCorruptState toast, noSave if rename fails — TestCorruptFileIsMovedAsideNotOverwritten. W35 saveMu serializes saves, saveErr cleared on success — TestFlushIsNotOverwrittenByAnInFlightSave, TestSaveErrorClearsAfterASuccessfulSave.
- W3 cooldown survives signature change, healthy records kept until cooldown, Policy.MeteredPerDay (default 3 / rolling 24h, Engine.billed) incl. fleet reserve — internal/autofix/costsafety_test.go (flip repro 10→1 billed). W36 Reset/Retry forgive running records — TestResetKeepsARunningChainsSlot, TestRetryKeepsARunningChainsSlot. Behaviour: multi-stage repair waits one cooldown between stages.
- W4/W10/W23 StartOpenCodeProxy: proxyStart mutex, plans+owned stored, shared proxy re-probed + sharedProxyServes per Start, port 0 refused — opencodeproxy_test.go. W8 lockProviders flock + writeFileAtomic. W9 OllamaCloudKeyVar = OLLAMA_CLOUD_API_KEY (breaking: users must re-export). W22 HasAPIKey/EffectiveBackend/settings row count it. W24 inert base-URL vars dropped from KnownVars. W25 Preflight/EnsureOpenCodeProvider take overlay; OpenCodeReadyWith. W26 reapOllama/forgetOllamaPID. W38 deepseek-v4-pro dropped. Tests: internal/gfy/auditfix_test.go.
- W5 ringbuf.SetTee + jobs.Options.Tee, ggraphify-job tees to stdout — TestStdoutCarriesALogPastTheBufferCap. W7 cmd.WaitDelay + reapGroup — straggler_test.go. W27 Job.exited re-checked in setPaused — TestPauseRacingANaturalExitIsNotRecorded. W28 done channel, SIGTERM→143, doctor -h → 0 — TestSIGTERMExitsWith143, TestDoctorHelpExitsZero. W29 early return — TestEmptyWriteKeepsPendingCR.
- W11/W12 DriftOf EvalSymlinks + exact out-dir skip. W13 oversizedBuiltCommit. W14 seen keyed on resolved dir. W18 MemberOf hash fallback (sha256/16hex, verified vs live manifest). W19 FileState Tail/Through watermark. W30 newerEntry. W31 Prune bumps clears. W32 SizeTTL 5 min. W33/W34 temp+fsync+rename. W39 restamp errcheck.
- W6 Retry rebuilt via retryJobFrom + preflight/precheck + confirmRetry for metered. W16 sweepStop/sweepGen. W17 scaleDPI + notify refresh (GTK wiring verified by build/vet only, as is W16/W20 wiring). W20 typedGateResponses incl. free. W21 withoutLive at sync. W37 open.SplitWords + editorArgv. W39 actGlobalAdd/mergeSelected deleted. Tests: internal/ui/bugaudit_test.go, internal/open/open_test.go.

### 2026-10-23 — bug audit
- vet clean; `go test -race ./...` 22/22 ok; golangci-lint 10 minor.
- 5 subsystem reviews → 39 findings recorded as W1–W39 with details under ## Spec.
- Throwaway repros lived in /tmp only. One reviewer briefly placed `internal/store/zz_audit_race_test.go` in the repo and deleted it at once; `git status` re-checked afterwards: only the pre-existing modified/untracked files remain.
