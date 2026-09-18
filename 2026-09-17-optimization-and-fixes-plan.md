# ggraphify — optimization and fix opportunities

**Written:** 2026-09-17
**Scope:** whole repo at `main` @ `1b39655`, plus the uncommitted working tree
(the ollama-lifecycle work: `internal/gfy/ollamactl.go`, the `jobs.Lease` hook,
the settings rows, the `-auto-ollama` flag).
**Method:** `go build` / `go vet` / `go test` (plain, `-race`, `-count=3`, `-cover`),
`golangci-lint`, `gosec`, plus reading the spans each tool pointed at.

## Baseline

| Gate | Before | After (2026-09-17) |
| --- | --- | --- |
| `go build ./...` | clean | clean |
| `go vet ./...` | clean | clean |
| `go test ./...` | **FAIL** — `internal/jobs` | **PASS** |
| `go test -race -count=3 ./...` | **FAIL** — `internal/jobs` | **PASS** |
| `golangci-lint` | 64 findings | **0** |
| standalone `staticcheck` | refused the module (toolchain) | **0** |
| `gosec` | 72 | 58 — every actionable class cleared |
| coverage | `internal/ui` 3.9%, `cmd/ggraphify-job` 0% | `cmd/ggraphify-job` **62.7%**, `internal/ui` 4.2% |

**STATUS: all 20 findings closed.** One was reclassified on contact — see
finding 12. Two questions the plan left open (12's generation-tracking
decision, 19's G301/G302 question) were answered rather than deferred.

Findings are ordered by what they cost, not by how easy they are.

---

## P0 — Broken now, deterministically

### 1. `GGRAPHIFY_OLLAMA_SLOTS` is silently discarded whenever the probe cannot reach the server

> **DONE.** `ollamaSlots` split into `statedSlots()` (env only, no I/O) and
> `derivedSlots(base)` (systemd). `ProbeLocal` calls the first unconditionally,
> the second only when `p.Reach`. `SlotsWhy` preserved.
> `TestOllamaParallelUnlockReachesTheSubprocess` passes.

`internal/gfy/local.go:944`

```go
if p.Reach && backend == OllamaBackend {
    p.Ctx, p.CtxMax, p.CtxModel = fetchOllamaContext(base)
    p.Slots, p.SlotsWhy = ollamaSlots(base)   // <- only ever called when reachable
}
```

`ollamaSlots` opens with "Stated beats derived" and honours the operator's
`GGRAPHIFY_OLLAMA_SLOTS` before anything else (`local.go:602`). That rule is
defeated by the outer gate: an unreachable server never gets as far as reading
the variable, so `Slots` stays `0`, `LocalConcurrency` returns `0`
(`local.go:247`), and `LocalEnv` declines to set `GRAPHIFY_OLLAMA_PARALLEL`
(`local.go:272`).

Consequence: graphify clamps itself back to one chunk and the
`--max-concurrency` already in the argv is discarded — the exact silent
serialization the feature exists to prevent. It bites whenever the server is
still coming up, or a probe cached a failed reach within the 5s TTL, which is
precisely the moment a job is submitted against a server the board is about to
start.

Evidence — fails 5 runs out of 5:

```
--- FAIL: TestOllamaParallelUnlockReachesTheSubprocess (0.00s)
    jobs_test.go:620: graphify would clamp itself back to one chunk: PARALLEL=
```

**Fix:** read the stated override before, and independently of, the reach
check. Split `ollamaSlots` into `statedSlots()` (env only, no I/O) and
`derivedSlots(base)` (systemd), call the first unconditionally and the second
only when `p.Reach`. Keep `SlotsWhy` so the settings page still explains where
the number came from.

### 2. `ggraphify-job` never stops the ollama it started — `defer` after `os.Exit`

> **DONE.** `main` is now `os.Exit(run())`; every terminal path returns a code
> and the defers run. New `cmd/ggraphify-job/main_test.go` asserts the teardown
> fires for all three terminal statuses (succeeded / failed / cancelled via a
> real SIGINT). Package went 0% → 62.7%.

`cmd/ggraphify-job/main.go:160-161` vs `:204`, `:206`, `:211`

```go
defer auto.Shutdown() // after Close: the job must be gone before its server is
defer r.Close()
...
switch ev.Job.Status {
case jobs.Succeeded: os.Exit(0)
case jobs.Canceled:  os.Exit(130)
default:             os.Exit(1)
}
```

`os.Exit` does not run deferred functions. Every terminal path of the headless
tool leaves through one, so `auto.Shutdown()` — and `r.Close()`, and the
signal `stop()` — never execute. The `-auto-ollama` flag's own help text
("stop it again on the way out if we were the ones who started it") describes
behaviour the binary cannot reach.

Consequence: every headless local job leaves an auto-started `ollama serve`
resident with its weights loaded, indefinitely, on a machine where nothing
tells the user where that process came from. This is the one ownership rule
`ollamactl.go` is built around, broken by the call site.

**Fix:** collapse the exits into one. Set a `code int`, `break` out of the
event loop, let the defers run, and `os.Exit(code)` as the last statement of
`main` — or factor the body into `run() int` and have `main` be
`os.Exit(run())`. Add a test that asserts the stop hook fires for each of the
three terminal statuses; `cmd/ggraphify-job` is at 0% coverage today, which is
why this got through.

---

## P1 — Wrong ordering; shows up as intermittent failures

### 3. The lease is released *after* the job is announced finished

> **DONE.** Released explicitly after `close(exited)`, before any terminal
> bookkeeping. The `defer` stays for early returns, wrapped in a `sync.Once` so
> the runner returns each lease exactly once — the counting test doubles check
> that, and the plan's assumption that `Release` is idempotent holds for the real
> lease but not for them.

`internal/jobs/jobs.go:1031` (`defer lease.Release()`) against `:1085-1095`

`run()` does, in order: `j.finish()` (closes `done`) → unlock → `r.emit(...)`
(publishes the terminal event) → `r.kick()` (dispatches the next job) →
*return* → **only now** the deferred `lease.Release()`.

So the job is observably complete — to the event channel, to `done`, and to the
scheduler — while its lease is still outstanding. `Leases()` (read by the
settings subtitle at `internal/ui/local.go:340`) over-counts by one for that
window, and the next job is dispatched and takes its own lease before the
previous one is returned.

Evidence — these pass in isolation and fail inside the full package run:

```
--- FAIL: TestLocalLeaseReleasedOnFailureAndCancellation   (plain)
    jobs_test.go:849: 1 leases out after a failed job, want 0
--- FAIL: TestPauseSuspendsTheLeaseAndResumeTakesItBack    (-race)
```

**Fix:** release explicitly, before the terminal bookkeeping, rather than by
`defer`. Keep a `defer` as the belt-and-braces path for early returns —
`Release` is already idempotent by `leaseState` (`ollamactl.go:239`), so
calling it twice is free.

### 4. Terminal job events are droppable

> **DONE.** `Output` events still drop; status changes send blocking, with a
> `quit` channel closed at the top of `Close` so a blocked send can never wedge
> shutdown behind its own `emitMu`. `TestTerminalEventSurvivesAFullEventChannel`
> — **negative control run**: fails in 20s on the old non-blocking send.

`internal/jobs/jobs.go:907-910`

```go
select {
case r.events <- ev:
default:            // dropped
}
```

The non-blocking send is right for `Output: true` events — there are thousands
and the UI only needs "there is more". It is wrong for status changes. The
channel is buffered at 256 (`:422`); a burst of output from several parallel
jobs can fill it, and the completion event that follows is dropped on the
floor.

For the GUI that means a row stuck at "running" until the next full redraw. For
`ggraphify-job` it is worse: the whole program is `for ev := range r.Events()`
waiting for `Status.Done()`. A dropped terminal event means it waits forever —
and because of finding 2 nothing closes the channel either.

**Fix:** make the two classes distinguishable and give status changes a
blocking (or retried) send, while output events keep the drop. Cheapest
version: `if ev.Output { select{...default:} } else { r.events <- ev }`, with
`closed` still checked under `emitMu`.

---

## P2 — Robustness

### 5. `togglePause` is now asynchronous with no serialization

> **DONE.** Per-job in-flight set (`App.pausing`, under the existing `App.mu`),
> which covers all three call sites — button, keyboard and row — rather than one
> widget's sensitivity. Tested.

`internal/ui/actions.go:502-517`

The state is read on the main thread (`resume := a.runner.Paused(id)`) and
acted on in a goroutine. Two quick clicks both read the same state and both
spawn; the outcome depends on which goroutine reaches `setPaused` first, and a
resume can block ~10s inside `lease.Resume()` while a second click queues
behind it.

**Fix:** disable the row's pause control until the goroutine returns
(re-enabled via `glib.IdleAdd`), or serialize per-job with a small in-flight
set keyed by job id.

### 6. The headless supervisor announces a grace period it never honours

> **DONE.** `AutoOllama.OneShot` added: the last lease arms nothing and
> announces nothing, because `Shutdown` at exit does the stopping.
> `ggraphify-job` sets it.

`cmd/ggraphify-job/main.go:146-149` sets `Enabled` and `Logf` but not `Idle`,
so `AutoOllama.idle()` falls back to `DefaultAutoOllamaIdle` (5 min,
`ollamactl.go:186`). When the job's lease comes back, `give()` logs
`no local job left; stopping ollama in 5m0s unless one arrives`
(`ollamactl.go:373`) to stderr — immediately before the process exits. It is
noise that contradicts itself, and it is also the log line that would have made
finding 2 visible.

**Fix:** pass `Idle: func() time.Duration { return 0 }` semantics explicitly —
better, give `AutoOllama` an `AtExit`/one-shot mode so the headless path skips
arming a timer at all.

### 7. Lock-ordering hazard in `AutoOllama.give`

> **DONE.** `give` and `Shutdown` read `StartedOllama()`/`OllamaPinned()` before
> taking `a.mu`, so `a.mu → ollamaStart` no longer exists anywhere. The
> invariant is stated once at the top of the file.

`internal/gfy/ollamactl.go:355` calls `StartedOllama()` and `OllamaPinned()`,
both of which take the package-global `ollamaStart` mutex, while holding
`a.mu`. `Shutdown` (`:425`) does the same. There is no cycle today — `take()`
and `stopNow()` both drop `a.mu` before touching `ollamaStart` — but the
ordering is `a.mu → ollamaStart` in two places and unlocked in two others, and
one future caller that holds `ollamaStart` across an `AutoOllama` call closes
the loop.

**Fix:** read both flags before taking `a.mu`, or document the ordering at the
top of the file as an invariant with a comment at each site.

---

## P3 — Performance

### 8. `ringbuf.Buf.Write` is O(cap) on every write once the buffer is full

> **DONE.** Trims to a low-water mark (`cap - cap/4`) instead of to exactly
> `cap`. `Len() <= cap` still holds. Measured with a new benchmark on
> line-granular writes past capacity: **1464 ns/op → 21.57 ns/op, a 68×
> reduction** (43 MB/s → 2967 MB/s).

`internal/ringbuf/ringbuf.go:46-67`

Past capacity, every single write does `copy(b.buf, b.buf[over:])` — a memmove
of up to `DefaultCap` = 256 KiB — to reclaim one line's worth of space. It is
not a ring buffer; it is a slice that is compacted on every append.

graphify runs are verbose. A 50 MB log arriving in ~32 KiB chunks is ~1,600
compactions of ~256 KiB ≈ 400 MB of pure memmove per job, multiplied by the
number of lanes. Line-granular writes make it far worse.

**Fix:** amortize. Trim to a low-water mark (drop ~25% of capacity) instead of
to exactly `cap`, which turns the cost into O(cap) per 64 KiB written rather
than per write — roughly a 1000× reduction — and costs one extra field. A true
head/tail ring is the thorough version, but the low-water trim is a ~10-line
change that keeps the existing line-boundary and `dropped` semantics intact.

### 9. A blocking network probe on the GTK main thread, in the submit path

> **DONE.** `ProbeLocal` is now stale-while-revalidate: a stale entry returns
> immediately and refreshes in the background, single-flighted. A COLD cache
> still blocks — there is no previous answer to hand back — which is once per
> backend per process rather than once per 5s TTL expiry. An epoch counter stops
> a refresh that was in flight during `InvalidateLocalProbe` from re-caching a
> pre-start/stop measurement.

`internal/ui/actions.go:123-134` → `jobs.SubmitCmd` (`jobs.go:525`) →
`gfy.LocalEnv` → `LocalConcurrency` → **`ProbeLocal`**.

`ProbeLocal` on a cache miss does an HTTP GET with a 2s timeout
(`local.go:983`), then two more HTTP calls, then up to two `systemctl`
subprocess spawns (`ollamaSlots`). All of it synchronous, all of it on the
thread that draws, reached from an ordinary click on Run.

With the server down, that is a ~2s frozen window per 5s TTL expiry. The 5s TTL
keeps a batch submit to one probe, which is why this has not been obvious.

**Fix:** keep the probe result in the `App` as state refreshed off-thread (the
settings page already polls it) and have `SubmitCmd` take the slot count as a
parameter rather than discovering it. Failing that, make `ProbeLocal` return
the stale entry immediately and refresh in the background — "down" is already
an answer the callers render rather than handle, so a slightly stale one is
consistent with the existing contract.

### 10. Every output chunk takes the global runner lock and builds a full snapshot

> **DONE.** `logWriter.Write` takes no runner lock and builds no snapshot.
> `Event` gained `ID` (populated on EVERY event) and `Gen`; output events carry
> only those two. The generation compare was kept where it already worked — per
> pane (`dock.logGen`, `jobsview.lastGen`, `queryPage.lastGen`) — and `App.logGen`
> deleted as the redundant copy. Consumers filter on `ev.ID`.

`internal/jobs/jobs.go:1178-1185`

```go
func (w logWriter) Write(p []byte) (int, error) {
    n, err := w.buf.Write(p)
    w.r.mu.Lock()
    snap := snapshot(w.job)      // copies the whole Job
    w.r.mu.Unlock()
    w.r.emit(Event{Job: snap, Output: true})
    return n, err
}
```

`r.mu` is the lock that also serializes dispatch, `Snapshot`, `Cancel`, and job
completion. Every parallel job contends on it once per pipe read, to produce a
struct copy that the UI mostly discards.

The half-built fix is already in the tree and flagged as dead: `internal/ui/ui.go:286`

```go
// logGen is the ring-buffer generation last rendered into the job log
// pane, so a tick that finds it unmoved skips the render entirely.
logGen uint64        // reported unused by staticcheck
```

`ringbuf` already maintains `gen` (`ringbuf.go:50`). Finish that: have output
events carry `{ID, gen}` only — no snapshot, no runner lock — and let the UI's
tick compare generations. Cheaper on both ends.

### 11. `Snapshot()` sorts the whole job list to answer single-ID questions

> **DONE.** `Runner.Get(id) (Snapshot, bool)` added — running map first, then
> the two short bounded slices. The four single-ID callers point at it
> (`resumeWaitsForOllama`, `watchInto`, `queryPage.tick`, and the dock's row
> subtitle). `Snapshot` stays for the whole-list views.

`internal/jobs/jobs.go:845-860` allocates queue+running+history, copies each,
and `sort.Slice`s the result. Callers that want exactly one job scan it
linearly: `resumeWaitsForOllama` (`internal/ui/actions.go:528`),
`internal/ui/dock.go:252`, `internal/ui/jobsview.go:487`.

**Fix:** add `func (r *Runner) Get(id uint64) (Snapshot, bool)` — a map lookup
plus one copy — and point the single-ID callers at it. Leave `Snapshot` for the
views that genuinely render the whole list.

---

## P4 — Hygiene

### 12. Dead code (10 symbols, staticcheck `unused`)

> **DONE — with one reclassification.** `unused` is now **0**. But
> `(*queryPage).tick` and `queryPage.lastGen` were NOT dead code: `run()` sets
> `q.jobID` and writes "running…", and `tick` is the ONLY thing that would ever
> replace it with the answer. Nothing called it, so the Query tab showed
> "running…" forever. Deleting it would have removed the pane's only path to a
> result. It is now WIRED, from `detailPane.tick` when the query page is the
> visible child.
>
> `(*usagePane).setScopeAll` was a duplicate rather than dead: the scope toggle
> handler inlined the identical five lines. The handler now calls it.
>
> The rest were genuinely unreachable and are deleted: `actCallflow`,
> `actHookStatus` (their KINDS stay reachable — `viz.go`'s table still runs
> `export-callflow`), `idleReload`, `vizPage.reload`, `verbCount`, and
> `App.logGen` per finding 10. `(*Job).held` was kept and `run()`'s inline copy
> of the same arithmetic now calls it. One symbol not in the plan's table,
> `ollamaSlots`, became dead as a result of finding 1 and went too.

| Symbol | Location | Note |
| --- | --- | --- |
| `(*Job).held` | `internal/jobs/jobs.go:162` | `run()` reimplements this inline at `:1064` — delete one or use the other |
| `(*App).actCallflow` | `internal/ui/actions.go:410` | |
| `(*App).actHookStatus` | `internal/ui/actions.go:468` | |
| `(*detailPane).idleReload` | `internal/ui/detail.go:873` | |
| `(*queryPage).tick` | `internal/ui/query.go:227` | |
| `queryPage.lastGen` | `internal/ui/query.go:51` | same abandoned generation-tracking idea as `logGen` |
| `App.logGen` | `internal/ui/ui.go:286` | **keep — see finding 10** |
| `(*usagePane).setScopeAll` | `internal/ui/usage.go:542` | |
| `(*vizPage).reload` | `internal/ui/viz.go:186` | |
| `verbCount` | `internal/usage/report.go:358` | |

Two of these (`lastGen`, `logGen`) are not junk — they are an unfinished
optimization. Decide finding 10 first, then delete what is left over.

### 13. A test that cannot fail

> **DONE.** The tautology is gone; the test now calls `Available()` once and
> asserts only what the comment claimed — that it returns.

`internal/webview/webview_test.go:51` — `if Available() != Available()`
(staticcheck `SA4000`). The intent (per the comment: answers without a display
and without panicking) is real, but the assertion as written is a tautology the
compiler could fold. Call it once into a variable and assert it does not panic,
or drop the comparison.

### 14. `nil` contexts

> **DONE.** `context.TODO()` at all of them. staticcheck, once rebuilt (finding
> 18), found **five** sites rather than three: `settings.go:572` and `ui.go:855`
> were invisible while it could not compile the module.

`SA1012` at `internal/ui/claude.go:274`, `internal/ui/graftclaude.go:77` and
`:79` — `gfy.Reprobe(nil)`, `gfy.GraftProbe(nil)`, `gfy.GraftReprobe(nil)`.
Pass `context.TODO()`, or give those helpers a no-context overload if they
genuinely have nothing to cancel.

### 15. Deprecated GTK API

> **DONE.** `AllocatedHeight` → `Height()` at both sites.

`internal/ui/diag.go:80` and `:108` use `AllocatedHeight`, deprecated in favour
of `Widget.GetHeight()`. Two-line change; do it before the next gotk4 bump
removes it.

### 16. Minor staticcheck

> **DONE.** All three: QF1006 lifted into the loop condition, QF1002 made a
> tagged switch, S1021 merged.

`QF1006` `internal/jobs/jobs_test.go:125` (lift into the loop condition),
`QF1002` `internal/ui/columns.go:473` (tagged switch), `S1021`
`internal/ui/diag.go:75` (merge declaration and assignment).

### 17. errcheck (40)

> **DONE.** Both halves of the suggestion. A new `.golangci.yml` excludes the
> mechanical receivers (in-memory buffer writes, tabwriter output, `Close` after
> a read); the handful that were NOT mechanical got an explicit `_ =` and a
> reason at the site — the store's atomic-write cleanup path and `dirSize`'s
> walk. **64 → 0.**

Overwhelmingly `fmt.Fprintf`/`w.Flush` to a `tabwriter` in
`cmd/ggraphify-scan/main.go` and `defer f.Close()` on read-only opens — not
worth handling individually. Either add `errcheck`'s exclusion list for these
receivers to `.golangci.yml`, or assign to `_` at the sites, so the 40 stop
hiding the ones that would matter.

### 18. `staticcheck` standalone cannot run against this module

> **DONE.** Rebuilt against go1.27 (`go install honnef.co/go/tools/cmd/staticcheck@latest`)
> and it now analyses the module: **0 findings**. `.golangci.yml` records that
> golangci-lint is the repository's single linter, and why.

```
package requires newer Go version go1.27 (application built with go1.26)
```

`go.mod` says `go 1.27`; the installed `staticcheck` is built against 1.26.
`golangci-lint` bundles a working copy, which is why it still reports SA/QF
codes. Rebuild `staticcheck` against the toolchain, or drop the standalone and
standardise on `golangci-lint` — having two that disagree about whether the
code compiles is worse than having one.

### 19. gosec: triaged, mostly not actionable

> **DONE. 72 → 58, and the answer to the open question is YES.**
>
> - **G115 (4) — cleared, without `#nosec`.** Added `gfy.Utoa(uint64)` and
>   pointed the four job-id sites at it. A function that cannot narrow is
>   better than four annotations explaining why a narrowing is safe.
> - **G301/G302/G306 (6) — cleared, and they mattered.** The plan asked whether
>   anything under the state directory holds a Claude account path or token. It
>   does, twice: `Settings.ClaudeAccount` names one of this machine's Claude
>   logins by its configuration directory, and `Settings.Overlay` is an
>   arbitrary user-filled environment map applied to every job — precisely
>   where a key ends up. Tightened to **0700/0600** in all four places
>   (`store.go`, `usage/index.go`, and `main.go`'s state dir and lock file).
>   Note: an existing install's directory keeps its old 0755 until recreated;
>   the FILES become 0600 on the first save, since the atomic write renames a
>   0600 temp over them.
> - **G304/G703 (38) / G204 (7) — recorded, not chased.** File inclusion on
>   paths the user picked in the file chooser, and subprocesses named
>   `graphify`, `ollama`, `systemctl`, `claude`. This is the program's purpose.
> - **G104 (13)** — the errcheck set, now deliberate `_ =` with a reason at
>   each site or excluded by receiver in `.golangci.yml` (finding 17).

72 findings. For a local desktop app that drives user-chosen paths and
user-chosen binaries, the bulk is inherent rather than a defect:

- **G304 (29) / G703 (9)** — file inclusion and path traversal on paths the
  user picked in the file chooser. This is the program's purpose.
- **G204 (7)** — subprocess with variable arguments: `graphify`, `ollama`,
  `systemctl`, `claude`. Same.
- **G115 (4)** — `uint64 → int` on job IDs (`internal/ui/dock.go:352`, `:698`,
  `:766`, `internal/ui/activity.go:208`). Unreachable: the ID is a monotonic
  counter that would need 2^63 submissions.
- **G301/G302/G306 (6)** — 0755/0644 on the state directory and its files.
  The one worth a look: whether anything under it holds a Claude account path
  or token. If it does, tighten to 0700/0600; if not, leave it.
- **G104 (17)** — the errcheck set again.

Recommendation: add `#nosec` with a reason on the G115 sites (or widen the
formatter to take `uint64`), check the G301/G302 question above, and otherwise
record the triage rather than chase the count.

### 20. Coverage is thinnest exactly where the new work landed

> **DONE for the part that was actionable.** `cmd/ggraphify-job` 0% → **62.7%**,
> which is the whole headless supervisor including finding 2. `internal/ui` 3.9%
> → 4.2%: the named lifecycle wiring (`leaseOllama`, `resumeWaitsForOllama`,
> `togglePause`) is now tested via a bare `&App{}`, as the existing UI tests do.
> The package stays low overall because the rest of it is GTK widget
> construction that needs a display — that is a structural limit, not a gap this
> pass left.

`internal/ui` 3.9%, `cmd/ggraphify-job` 0%. The ollama lifecycle added code to
both: `leaseOllama`, `resumeWaitsForOllama`, `updateLocalIdleRow`, the settings
rows, and the whole headless supervisor. Findings 2 and 5 are both in that
untested region, and finding 2 is the kind a single test would have caught.

`internal/gfy/ollamactl.go` itself is well covered (488 lines of test for 481
of code) — the gap is the wiring, not the mechanism.

---

## Suggested order

1. **Finding 2** (headless ollama leak) — largest user-visible consequence, and
   the fix is mechanical.
2. **Finding 1** (slots override) — restores the feature that the last three
   commits were built to deliver; turns the suite green.
3. **Finding 3** (lease release ordering) — removes the intermittent failures,
   so the suite becomes trustworthy again before anything else is changed.
4. **Finding 4** (droppable terminal events) — latent hang in the headless
   tool; pairs naturally with 2.
5. **Finding 8** (ringbuf compaction) — best effort-to-payoff ratio of the
   performance items, self-contained, well-covered by existing tests.
6. **Findings 10 + 12** together — decide the generation-tracking idea, then
   delete the dead symbols that decision leaves behind.
7. **Findings 9, 11, 5, 6, 7** — the remaining robustness and latency work.
8. **P4** as a single hygiene pass, with 18 (toolchain) first so the linters
   agree with each other.

Findings 1–4 are the ones that change behaviour users would notice. Everything
below them is worth doing and none of it is urgent.

---

## Outcome (2026-09-17)

All 20 worked in the suggested order. Three things the plan did not predict:

1. **Finding 12 contained a bug, not dead code.** `(*queryPage).tick` was a
   complete, correct implementation that nothing called — the Query tab could
   never display a result. Wired rather than deleted.
2. **Finding 18 was hiding findings 14 and 16.** With staticcheck unable to
   compile the module, two of the five `SA1012` sites were invisible. Fixing
   the toolchain first, as the plan suggested, is what surfaced them.
3. **Finding 19's open question had a real answer.** The state file persists a
   Claude login's directory and a user-supplied environment map, so the
   0755/0644 modes were worth tightening rather than leaving.

Two fixes have recorded negative controls: the droppable terminal event (the
new test fails in 20s against the old non-blocking send) and the ringbuf trim
(1464 ns/op against the old trim-to-cap, 21.57 ns/op after).
