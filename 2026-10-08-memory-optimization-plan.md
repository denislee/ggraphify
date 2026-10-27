# ggraphify — memory-usage optimization plan (2026-10-08)
<!-- plan-doc: v2 -->
**Progress:** 94% — 36/38
**Status:** 🟡 All code items landed (uncommitted, tests green); ggserve live at ~1.0 GB; board runtime check + UI8 GC-limit tuning open
**Last updated:** 2026-10-08 — /next: W1–W37 done, committed as TECH-20623; W31/W38 wait on a board relaunch

## Handoff

Next action: W38 — relaunch the board (packaging/ggraphify-run rebuilds from the tree) and measure VmRSS/VmHWM after 1 h idle + one AST sweep; then W31 SetMemoryLimit tuning
In play: /home/dns/tmp/ggraphify main@336382d (5 commits, TECH-20623, not pushed); ggraphify-global-query.service reinstalled
Not in this doc: backups — ggserve/unit *.bak-20261008, /tmp/ggr-orig, /tmp/ggraphify-exec-bak, /tmp/*.orig (ui), /tmp/usage-orig, /tmp/ggmem

## Work
- [x] **W1** G1 undirected lean load, `_explain` via neighbors — ev: /query 5.61 s → 0.22 s, RSS +280 MB → +28 MB; output byte-identical (26 query/6 path/12 explain) · was: G1 · src: L105
- [x] **W2** G2 release old G before reload + malloc_trim — ev: VmHWM 4.28 GB → 1.16 GB; reload 503 ~21 s, swap peak 0 · was: G2 · src: L119
- [x] **W3** G3 intern + drop constant attrs — ev: steady RSS 2.56 GB+1.31 GB swap → 0.93–1.0 GB, swap 0 · was: G3 · src: L136
- [x] **W4** G4 cap trigram set_cache at 2000 — ev: test_set_cache_cap OK · was: G4 · src: L151
- [x] **W5** G5 audit global enrollments — ev: 135 tags, no worktrees/dupes; open-finance-payments-wt-19867 is a full clone and sole checkout → kept · was: G5 · src: L160
- [x] **W6** G6 systemd guardrails — ev: MemoryMax=4500M, MemorySwapMax=512M, MALLOC_ARENA_MAX=2 active · was: G6 · src: L168
- [x] **W7** B1 static/drift cache split — ev: warm no-drift scan 1056 MB → 7.6 MB alloc · was: B1 · src: L180
- [x] **W8** B2 zero-alloc counter + tail stamp read + locateStamp ceiling — ev: 153031 → 2 allocs/op; parity on 6 real graphs · was: B2 · src: L192
- [x] **W9** B3 stream manifest MTime only (no cache, per decision) — ev: drift.go readDriftManifest · was: B3 · src: L204
- [x] **W10** B4 capPaths copy — ev: cappaths_test; persisted baselines already capped at 2000, not lowered (would revive acknowledged drift) · was: B4 · src: L214
- [x] **W11** B5 dirSize in static layer — ev: part of W7 numbers · was: B5 · src: L222
- [x] **W12** B6 single typed labels read — ev: prune_test readLabelState parity · was: B6 · src: L227
- [x] **W13** B7 prune vanished repos — ev: graphstate/graftstate Cache.Prune via board.Scan; Store.PruneDriftBaselines(keep func) from ui refresh, drops only baselines whose checkout is gone from disk; TestPruneBaselinesDropsOnlyDeletedCheckouts + negative control · was: B7 · src: L234
- [x] **W14** B8 misc — ev: print.UnmarshalJSON; graphstate.Nodes deleted; /home/dns/tmp row kept (decision) · was: B8 · src: L239
- [x] **W15** U1 pooled reader + ReadSlice — ev: UpdateCold 2.25 GB → ~145 MB alloc · was: U1 · src: L256
- [x] **W16** U2 pending-first — ev: TestParseLineAsksPendingFirst; raw-clip sub-part declined (changes classification) · was: U2 · src: L264
- [x] **W17** U3 tighter prefilter + no RawMessage copies — ev: golden usage output byte-identical; >256 KB byte-scan declined (breaks identity) · was: U3 · src: L271
- [x] **W18** U4 dir listing cache by mtime — ev: UpdateWarm 1.37 MB → 0.51 MB, 8652 → 2501 allocs · was: U4 · src: L278
- [x] **W19** U5 prune Files/GraftFiles — ev: memopt_test · was: U5 · src: L285
- [x] **W20** U6 RepoUses allocs — ev: 3.3 MB/66k allocs → 87 KB/113 allocs · was: U6 · src: L289
- [x] **W21** U7 Save dirty flag + lazy Counts — ev: unchanged save 863 ns; bytes identical; Owners kept (derivation would change results) · was: U7 · src: L294
- [x] **W22** J1 compact retired logs, evict-before-append, prune jobByRepo — ev: compact_test cap invariant (fails on old code), retire_test · was: J1 · src: L304
- [x] **W23** J2 Runner.Live, err string once, dock entries once, persistJobs cache — ev: jobs tests 93.8% · was: J2 · src: L315
- [x] **W24** J3 applog clip/Clone, WriteString no-alloc, LastLine deleted, retire ring, SubmitChain quits, Engine.Prune — ev: applog/ringbuf/autofix tests · was: J3 · src: L323
- [x] **W25** UI1 skip Output events before IdleAdd — ev: ui.go pumpJobs · was: UI1 · src: L339
- [x] **W26** UI2 Jobs dialog open flag + log append — ev: textsync_test 11 cases · was: UI2 · src: L345
- [x] **W27** UI3 usage page signature gate — ev: usage.go usagePageSig · was: UI3 · src: L354
- [x] **W28** UI4 lazy tooltips via ConnectQueryTooltip — ev: columns.go colSpec.tip · was: UI4 · src: L361
- [x] **W29** UI5 count in place, shared copies — ev: refreshStatus/autoFixTick/globalpane reloadWith · was: UI5 · src: L368
- [x] **W30** UI6 append-only log views — ev: dock renderLog, query tick · was: UI6 · src: L376
- [~] **W31** UI8 malloc_trim + MALLOC_ARENA_MAX — ev: trim.go every 5 min, ggraphify-run sets MALLOC_ARENA_MAX=2 · left: SetMemoryLimit still 512 MiB; tighten to 128–160 MiB only after measuring GC CPU on the running board · after: W38 · was: UI8 · src: L392
- [x] **W32** UI7 detail pane page keys — ev: detail.go pageKey/statKey · was: UI7 · src: L384
- [x] **W33** UI9 rowCell path, closure captures, viz about:blank on leave — ev: columns.go/globalpane.go/viz.go · was: UI9 · src: L399
- [x] **W34** C1 proxy streams body, 413 via MaxBytesReader — ev: sniff_test 7 cases · was: C1 · src: L411
- [x] **W35** C2 cheap commit read in doctor — ev: doctor 256 GCs/0.96 s → 24 GCs/0.23 s; -usage and publishIndex keep the full scan (need real counts; scan now cheap) · was: C2 · src: L416
- [x] **W36** C3 decode one provider — ev: decodemember_test · was: C3 · src: L420
- [x] **W37** Commit the change set — ev: TECH-20623 (Done); commits e74802e 3211e98 13206a1 9da162d 336382d on main, not pushed
- [ ] **W38** Runtime verification of the board — verify: /proc/<pid>/status VmRSS/VmHWM after 1 h idle and after a 128-repo AST sweep; hover tooltips, jobs dialog, usage page live update · src: L450

## Log

### 2026-10-08 — /next — W1–W36
- Env: local only (repo /home/dns/tmp/ggraphify, user unit ggraphify-global-query.service). Risk LOW except W1–W6 install (local service; rollback = cp *.bak-20261008 + daemon-reload + restart).
- Decisions: G2 drop-old+trim · G3/G5 attrs only · B3 stream, no cache · /home/dns/tmp row kept.
- ggserve: VmRSS 2,562,696 kB + 1,311,056 kB swap → 999,792 kB, swap 252 kB; VmHWM 4,284,816 → 1,155,452 kB; /health ready 214172 nodes; installed == repo.
- Board scan: with drift 1297 MB → 384 MB (cold), 1287 MB → 246 MB (warm); no drift warm 1056 MB → 7.6 MB. ggraphify-scan -json 274 → 110 GCs. Output identical on 235 rows.
- Usage: UpdateCold 2.25 GB → ~145 MB/op; golden usage output byte-identical.
- Gate: `go test -race -cover -count=1 ./...` → all 23 packages ok; gofmt/vet clean.
- Declined sub-parts (break identity/semantics): U2 raw clip, U3 >256 KB byte-scan, U7 derive Owners, C2 -usage/publishIndex, B4 lower persisted cap.

### 2026-10-08 — migrated to plan-doc v2
Everything below this entry is the pre-v2 body, kept verbatim. The live plan is above `## Log`.

### Pre-v2 body


Audit of every place ggraphify (the GTK board, its CLIs, and the `ggserve` global-query
service it installs) spends memory. Read-only: nothing was changed. Findings carry
`file:line (symbol)` at commit `55b0693`; line numbers will drift, symbols will not.

## 1. Where the memory actually is (measured 2026-10-08 ~10:40 -03)

| Process | RSS | Swap | Peak (HWM) | Notes |
| --- | --- | --- | --- | --- |
| `ggserve.py` (ggraphify-global-query.service) | **3.12 GB** (3.32 GB after 3 queries) | **730 MB** | **4.28 GB** | systemd `MemoryHigh=6G`; anon, private-dirty |
| `ggraphify` board (pid 1065819, up 20 h) | 158–174 MB | 24 MB | 219 MB | glibc `[heap]` 67.7 MB, Go arena 46 MB, 32 threads |

The machine is under real pressure: **swap 19/19 GB used**, 7 GB available. ggserve alone is
~20× the board. **The global query service is the #1 target; the board is a churn
(allocation-rate) problem, not a retention problem.**

Inputs that drive the numbers:

- `~/.graphify/global-graph.json`: **363 MB**, 214,172 nodes / 545,873 edges / 135 repos.
- `~/knowledge`: 9.1 GB, 251 `graph.json` totalling 1.14 GB; largest is
  `tmp-f6be1731` (a graph of `/home/dns/tmp` itself) at **496 MB** + an 11.5 MB / 54k-entry
  manifest. Next: nova_mobile_app 46 MB, ro-espirro 45 MB, ios-tts 31 MB.
- `~/.claude/projects`: 2.9 GB; the usage scanner reads 1,435 top-level transcripts
  (~1.7 GB, lines up to 1.34 MB) across 4 accounts.
- `~/.local/share/ggraphify/usage.json` 1.85 MB; `state.json` 564 KB (200 jobs, 145 baselines).

Board allocation rate (measured with `ggraphify-scan -json` + `GODEBUG=gctrace=1`, 235 rows):
**~650 MB allocated per scan without drift, ~950 MB with drift**, peak live heap only 29–40 MB,
225–370 GC cycles. With a 30 s tick and 45 s TTL this is roughly **1 GB/min of garbage** at idle.

---

## 2. ggserve — global query service (`tools/globalquery/ggserve.py`)

Experiment (`/tmp/memexp.py`, graphify's own `_load_graph` vs a lean loader, on the 27.8 MB
rathena graph — 21k nodes / 54k edges, i.e. 1/10 of the global graph):

| | steady (tracemalloc) | per-query `_traversal_view` copy | trigram index |
| --- | --- | --- | --- |
| graphify `_load_graph` (today) | 64 MiB | **+26 MiB** | +8 MiB |
| lean load (intern + drop constant attrs + undirected w/ `_src/_tgt`) | **35 MiB (−45 %)** | **+0 MiB** | +8 MiB |

Scaled ×10 to the global graph: ~650 MiB → ~350 MiB steady, and ~260 MiB of per-query copy
removed. Live check: the first `/query` grew ggserve RSS by **+198 MB** that was never given
back; queries take 2.7–5.0 s, most of it the copy.

### G1 — HIGH: every `/query` deep-copies the whole graph (546k edge dicts)
- `ggserve.py:330` → graphify `serve.py:_query_graph_text` → `_traversal_view` (serve.py:1233).
  `_load_graph` forces `directed: True`, so `_traversal_view` builds a fresh undirected
  `nx.Graph` per query with `{**d, "_src": u, "_tgt": v}` for every edge. The on-disk global
  graph is `"directed": false` anyway.
- Cost: ~200–260 MB transient per query, RSS ratchets to the high-water mark (pymalloc/glibc
  don't return it), and it is most of the 3–5 s latency.
- **Fix:** load once into an undirected `nx.Graph` with `_src`/`_tgt` already stamped on every
  edge (what the graphify CLI loader does). `_traversal_view` then returns `G` unchanged
  (`if not G.is_directed(): return G`). Rewrite ggserve's `_explain` (`ggserve.py:241`) to
  walk `G.neighbors(nid)` and use `_src`/`_tgt` to pick the `->`/`<-` arrow instead of
  `successors`/`predecessors`. Check `_shortest_path_text` still renders: it already
  builds from `_src`/`_tgt`.

### G2 — HIGH: reload holds two full graphs + the raw JSON at once
- `ggserve.py:197-201` (`reloader_loop`): `load()` builds the new graph while `state.G` still
  references the old one. Inside `_load_graph` (serve.py:44) the whole JSON is
  `read_text()` (363 MB str) → `json.loads` dict tree → `node_link_graph` copy. All
  coexist at the moment of `install`. That explains the 4.28 GB HWM / 2.3 GB swap peak. The
  global graph is rewritten on every `global add` (mtime 13:30 today), so this happens often.
- **Fix (pick one or both):**
  - Stream the parse: `ijson` (or `json.JSONDecoder.raw_decode` per element) over
    `nodes`/`links`, building `G` incrementally, so the dict tree and the 363 MB string never
    exist.
  - Drop the old graph before loading (`state.G = None` under the lock, serve 503 for ~30 s),
    or do the parse and slimming in a **short-lived subprocess** that writes a compact,
    pre-slimmed JSON (or msgpack) and exits, so the parse garbage dies with the child.
  - After `install`, call `gc.collect()` and `ctypes.CDLL("libc.so.6").malloc_trim(0)` so
    freed arenas go back to the OS. Run with `PYTHONMALLOC=malloc` + `MALLOC_ARENA_MAX=2` in
    the unit if trim alone is not enough.

### G3 — HIGH: per-node/per-edge attribute bloat (no interning, constant fields)
- Every edge carries `_origin`, `confidence`, `confidence_score`, `weight`, `context`,
  `relation`, `source_file`, `source_location` as separate objects. `json.loads` interns keys
  but **not values**, so `"contains"`, `"EXTRACTED"`, `"ast"`, `"code"`, the repo tag,
  community names and every repeated `source_file` path are separate str objects per
  record, and `1.0` is a separate float per edge (×2).
- Nodes carry `local_id` (a suffix of `id`) and `_origin`, which no query path reads.
- **Fix:** in `ggserve.load()`, or better in the streaming loader from G2, `sys.intern`
  `relation`/`confidence`/`context`/`file_type`/`repo`/`community_name`/`source_file`. Drop
  `_origin`, `weight` and `confidence_score` when they equal the default, and drop `local_id`.
  Measured −45 % steady with G1's undirected load. Confirm against graphify's
  `_subgraph_to_text` which attributes are printed before dropping one (`confidence` is
  printed; keep it).
- Cheaper still: store `source_location` as an int line (`"L49"` → 49) and re-format on output.

### G4 — MED: unbounded per-generation search caches
- graphify `_get_trigram_index` (serve.py:398) keeps `set_cache` (trigram → `set` of node
  ids) that memoizes **every trigram any query ever touched**, unbounded until the next
  reload. Common trigrams map to sets of tens of thousands of ints. `_idf_cache` grows the
  same way (small).
- **Fix:** in ggserve, after each query, cap `G.graph["_trigram_index"]["set_cache"]` (clear
  it when `len > N`, e.g. 2,000), or wrap it in an LRU. This is a few lines in ggserve and
  needs no graphify change.

### G5 — MED: global graph content that shouldn't be there
- `open-finance-payments-wt-19867` is enrolled: a worktree, despite commit 987b54d
  ("one canonical checkout per origin"). Audit the manifest for worktree/duplicate tags and
  remove them. Every duplicated repo is pure node+edge weight.
- Consider whether test-file nodes and `contains` edges (a large share of edges) are needed
  for cross-repo `ggq` use. A `--slim` enrollment that drops them would shrink everything
  above proportionally.

### G6 — LOW: systemd guardrails
- `ggraphify-global-query.service` has `MemoryHigh=6G` only. Add `MemoryMax=` (e.g. 4.5G)
  and `MemorySwapMax=512M` so a reload storm can't push the desktop into swap. Add
  `Environment=MALLOC_ARENA_MAX=2`. Set these after G1–G3, which should bring steady state
  under ~1 GB.
- Note: the installed copy at `~/.local/share/ggraphify/globalquery/ggserve.py` is identical
  to the repo copy. Fixes ship via `tools/globalquery/install.sh`.

---

## 3. Board — graph/drift state (`internal/graphstate`, `internal/graftstate`)

### B1 — HIGH: every graph.json is re-tokenized about once a minute
- `internal/graphstate/cache.go:55` (`DefaultTTL = 45s`) + `:65-107` (`Cache.Read`),
  `internal/ui/ui.go:624-636` (`startTick`, 30 s). The TTL exists for drift, but expiry
  re-runs all of `graphstate.Read` (`graphstate.go:239-329`): `readGraph` over the full
  graph.json, `readLabels`, `readHubNamed`, `readAnalysisCommunities` and `dirSize`.
- ~650 MB allocated per minute for numbers that change only when graph.json changes.
- **Fix:** split the cache into a **static layer** (Nodes, Links, Hyperedges, BuiltCommit,
  Communities, Labeled, HubNamed, Has*, SizeBytes) keyed only on `outKey` with no TTL, and a
  **drift layer** that alone expires on the TTL. Do the same split in
  `internal/graftstate/cache.go:51-83` (`dirSize`, `exposedToGit` and `readFingerprint`
  re-run on every TTL).

### B2 — HIGH: `readGraph` counts elements with `json.Decoder.Token()`
- `graphstate.go:338-459` (`readGraph`, `countArray`, `skipValue`, `skipRest`). `Token()`
  boxes every scalar and allocates every string: millions of allocations to produce three
  ints. A 46 MB graph is about 2M tokens.
- **Fix:** a hand-written byte scanner that counts depth-1 elements of
  `nodes`/`links`/`edges`/`hyperedges`, tracking depth, in-string and escape state, over a
  reused 64 KB `bufio.Reader`, with zero allocations. Read `built_at_commit` (the last
  top-level key) from the last ~4 KB, falling back to the full scan.
- Same pattern in `restamp.go:121-149` (`locateStamp`), which also has **no size ceiling**,
  so the 496 MB graph would be fully tokenized on restamp. Add the ceiling and use the tail
  read.

### B3 — MED: `DriftOf` reads and unmarshals the whole manifest each TTL
- `drift.go:186-190` (`manifestEntry` carries `ASTHash`/`SemanticHash`), `:215-343`
  (`DriftOf`: `os.ReadFile` + `json.Unmarshal` into `map[string]manifestEntry`, plus a
  `seen` map). Only `MTime` is used. For the `/home/dns/tmp` row (54k entries) that is
  ~30–40 MB of garbage per pass; drift adds ~300 MB per full scan.
- **Fix:** a DriftOf-only `struct{ MTime float64 }`, streamed with `json.Decoder` (key token,
  then Decode the small struct) so the raw bytes and the map never coexist. Optionally cache
  a compact `map[string]float64` per repo keyed on the manifest stat: ~10 MB retained vs
  ~300 MB/min churn. That trade-off is a decision for you.

### B4 — MED: `capPaths` pins the full drift list
- `drift.go:380-385` (`capPaths`) returns `xs[:driftPathsMax]`, a reslice, so the cached
  `Graph.DriftFiles` keeps the entire backing array alive (20,831 removed paths for
  `/home/dns/tmp`, ~1.5–2 MB). **Fix (one line):** `return append([]string(nil), xs[:driftPathsMax]...)`.
- The UI shows only 12 paths (`ui/format.go:562`, `driftLines(g, 12)`), and persisted
  baselines reach ~100 KB per repo (`store.go:611`). Cap persisted lists too and keep
  only the counts.

### B5 — MED: `dirSize` walks ~170k files per TTL for the Size column
- `graphstate.go:690-702` (output dirs, ~122k files) and `graftstate.go:492-511` (`graft/`
  dirs, ~47k). **Fix:** move it into the static layer (B1), keyed on the dir stamp, or compute
  it lazily for the selected row only.

### B6 — LOW-MED: labels parsed twice into `map[string]any`
- `graphstate.go:467-491` (`readLabels`) and `:503-543` (`readHubNamed`) each ReadFile and
  unmarshal `.graphify_labels.json`. `readSigMap` (`:549-559`) runs for both the current sig
  and the snapshot. For `/home/dns/tmp`: 1.8 MB × 2 + 1.3 MB × 2 per TTL.
- **Fix:** one read into a typed map, sigs decoded only when both exist. Under B1 this runs
  only on change.

### B7 — LOW: caches never forget repos that vanished
- `graphstate/cache.go:25-40` and `graftstate/cache.go:21-31` (`m`/`gen`) and
  `store.Baselines` (`store.go:975-990`; two `pulumi-deploy-wt-*` entries ~100 KB each).
  Agent worktrees come and go. **Fix:** after `board.Scan`, drop keys not in the current repo set.

### B8 — LOW: misc
- `graftstate.readFingerprint` (`graftstate.go:355-383`) builds `map[string][]any`, then
  copies it to `map[string]print`. Use a custom `UnmarshalJSON` instead.
- `graphstate.Nodes` (`graphstate.go:719-759`) materializes the node array and has no
  callers. Delete it.
- `~/knowledge/tmp-f6be1731` (496 MB graph of `/home/dns/tmp`) is the outlier behind several
  findings above. Consider excluding `/home/dns/tmp` as a scan root/row. It also costs 26k
  files in `dirSize` and 54k Lstat calls per drift pass.

---

## 4. Board — usage index (`internal/usage`)

> The prefilter comment says fewer than 1 in 200 lines mention graft/graphify. A 20-transcript
> sample measured **27.9 % of lines (42 % of bytes)**, so far more goes to the JSON decoder
> than designed.

### U1 — HIGH: fresh 256 KB reader + per-line slice on every 2 s tick
- `transcript.go:124` (`bufio.NewReaderSize(f, 256<<10)` per changed transcript) and `:133`
  (`r.ReadBytes('\n')`, a new slice per line; a 1.3 MB line costs ~2×). `Update` runs
  ≥ every 2 s (`ui.go:629-634`), and every live Claude session changes every tick. A cold
  rescan churns ~1.7 GB. This is likely what sets the 219 MB peak.
- **Fix:** one pooled reader, `Reset(f)` per file; `ReadSlice('\n')` with a reusable scratch
  for `ErrBufferFull`; copy only `firstTS`/`lastTS`.

### U2 — HIGH: failed tool results unescaped before checking if anyone waits for them
- `transcript.go:289` (`parseLine` → `emitR(id, classifyFail(resultText(b.Content), …))`).
  `resultText` → `decodeString` (`failure.go:239`) unescapes MB-sized results, and then
  `emitR` discards them unless the id is pending (`index.go:247-249`).
- **Fix:** check `pend` first (pass a `pending(id) bool`), and clip the raw JSON before
  unescaping.

### U3 — HIGH (churn): prefilter too loose, and `json.RawMessage` copies per line
- `transcript.go:231` (`mentions`), `:240`, `:280`: `Message.Content` and `block.Content`
  are RawMessage, so a 1 MB line is copied twice more. A cold scan decodes ~700 MB.
- **Fix:** tighten the prefilter to call/hook-only needles (`"name":"mcp__graft`,
  `"command":"graft `, `[graft]`, `<command-name>/graf…`). Decode `Input` only for
  `tool_use`. Skip `Content` unless pending. Above a 256 KB line, byte-scan only.

### U4 — MED: `Update` copies state maps and re-lists every dir every tick
- `index.go:220-227` (copies `Files`/`GraftFiles`), `:240-241`; `transcript.go:405-431`
  (`transcripts`, 1,435 `filepath.Join` + `ReadDir` per project dir including subagent
  sidecars); `graftsession.go:85,93`. ~0.5–1 MB of garbage every 2 s when idle.
- **Fix:** cache dir listings by dir mtime; mutate `Files` in place; skip
  `scanGraftSessions` when the session dir mtime is unchanged.

### U5 — MED: `GraftFiles` (and `Files`) never pruned
- `index.go:316-319`; `prune` (`:536-583`) skips both. 1,065 + 1,435 entries that only grow.
  **Fix:** drop entries older than `Retain` or no longer present.

### U6 — LOW-MED: `RepoUses`/`RepoMatch` allocate in a triple loop
- `query.go:332-391`, `index.go:597-605` (`TrimSuffix(repo,sep)+sep` per day × cwd × repo,
  ~27k allocs per publish). `splitCounter` (`usage.go:169`) uses `SplitN`.
- **Fix:** resolve cwd → owner once (172 cwds), precompute prefixes, and use `strings.Cut`.

### U7 — LOW: `Save`/`Load` whole-buffer, idle sessions carry maps
- `index.go:130-135`, `:167`: Marshal 1.9 MB every 5 min even when nothing changed. Stream
  with an encoder and add a dirty flag.
- `index.go:496-509`: 1,318 of 2,848 `SessionRoll`s have an empty `Counts` map, and `Owners`
  duplicates `SessionRoll.Account`. Allocate maps lazily and derive `Owners`.

---

## 5. Board — jobs, logs (`internal/jobs`, `internal/ringbuf`, `internal/applog`)

### J1 — MED: 200 finished jobs keep full ring buffers that can exceed the cap
- `jobs.go:351` (`DefaultHistory = 200`), `:1460-1465` (`retire`); `ringbuf.go:23`
  (`DefaultCap = 256 KiB`), `:97/:100` (append before evict, so the backing array grows to
  ~320–512 KiB), `:137` (copy-down never shrinks).
- Worst case 50–100 MB. Today it is small (average log ≈ 0.5 KB), but one chatty sweep fills it.
- `ui.go:1081`/`session.go:126` (`jobByRepo`) also pins the last Snapshot and its `Log` per
  repo forever, even after the runner drops it, and never removes vanished repos.
- **Fix:** in `retire`, add `Buf.Compact(32<<10)` (copy the tail into an exact-size slice);
  keep full logs only for the last ~10 failures. In `Write`, evict before appending. Prune
  `jobByRepo`/`jobIDs` in `setRows`.

### J2 — MED (churn): Snapshot copies many times a second
- `jobs.go:1080-1095` (`Snapshot` copies and sorts ~200 jobs, and calls `Err.Error()` for each
  failure). Called by `dock.go:254` (`dockEntries`, twice per tick via `reloadJobs` +
  `tickJobs`), `activity.go:131` (`liveJobs`, 1 Hz), `jobsview.go:204`, and
  `session.go:47-66` (`persistJobs`, ~3× per job, plus `TailBytes(8 KB)` × 200 ≈ 1.6 MB).
- **Fix:** add `Runner.Live()` (queued and running only); compute dock entries once per tick;
  store the error string once at finish; cache finished `JobEntry` by ID in `persistJobs`.

### J3 — LOW
- `applog.go:151` (`Write`): line substrings of `string(p)` pin the whole write buffer, and the
  2,000-entry cap has no byte bound. Use `strings.Clone` and clip at 4 KB.
- `ringbuf.go:174` `WriteString` does `[]byte(s)` (allocates, despite its comment).
  `ringbuf.go:246` `LastLine` has no non-test callers.
- `jobs.go:1461` `retire` prepends, reallocating 200 slots per completion. Use a ring index.
- `jobs/chain.go:56` `SubmitChain` goroutine waits forever on a held step (bounded leak).
- `autofix.go:463`/`fleet.go:202` `Engine.seen` is never pruned for vanished paths/tags.

---

## 6. Board — GTK/UI (`internal/ui`): the 67.7 MB C heap

The glibc main arena (GTK/Pango) is larger than the Go heap. Widget and text-buffer churn on
the main thread grows it, and it never shrinks.

### UI1 — HIGH: one `glib.IdleAdd` per log write, for events that are dropped
- `ui.go:1052-1056` (`pumpJobs`) posts an IdleAdd closure for **every** event, and
  `onJobEvent` returns immediately for `ev.Output` (`ui.go:1061`). Each one is a Go closure,
  a gotk4 registry entry and a C GSource: thousands per second during a sweep.
- **Fix (one line):** `if ev.Output { continue }` in `pumpJobs` before `IdleAdd`.

### UI2 — HIGH: Jobs dialog keeps rebuilding after it is closed
- `jobsview.go:186` sets `a.jobsPage` and nothing ever clears it. `ui.go:666` (`tick`, 1 Hz)
  and `ui.go:1105/1180` (`onJobEvent`), plus `heldbanner.go:63`, keep driving
  `reload`/`tick`: rebuild 200 rows × ~8 widgets per transition, plus
  `SetText(s.Log.String())` (up to 256 KB) every second (`jobsview.go:318`, `:335-337`).
- **Fix:** on the dialog's `closed` signal, set `a.jobsPage = nil` (or set a visible flag) and
  return early when unmapped. Append only new log bytes (track the offset and
  `buf.Insert(end, delta)`), or use `Tail(400)` like `detail.go:809`.

### UI3 — HIGH: Usage page rebuilds ~100 widgets every 2 s
- `usage.go:159-161`: `publishUsage` calls `usagePane.reload()` outside any `changed` guard.
  `reload` (`usage.go:602-682`) clears and rebuilds every section. `fillRecs`/`fillFails`
  copy rows twice (`:692`, `:838`).
- **Fix:** gate on a cheap signature (event count, `LastUpdate`, scope). Keep fixed label
  slots and use `SetText`/`SetVisible`.

### UI4 — HIGH: tooltips formatted for every visible cell every second
- `columns.go:130` (`globalTooltip(*r)`, which copies a whole Row) and `:146`
  (`usageTooltip`) are inside `render`, which runs on every 1 s `repaintRows` and every job
  event. The comment at `columns.go:421` says tooltips are built on hover.
- **Fix:** cache the last tooltip on `rowCell` and set it only on change, or use
  `ConnectQueryTooltip` lazily as `detail.go:365` does.

### UI5 — MED: whole board copied every second just to count
- `ui.go:894` (`refreshStatus`, 1 Hz) → `board.Summarize(a.allRows())` copies ~300 Rows
  (~250 KB/s). The `ui.go:885` comment says it "must not allocate much". Scans add more
  copies: `autofix.go:81,84,119`, `knowledge.go:33`, `globalpane.go:502` + `:547` (the model
  is built twice, with an `os.Stat` per member), and `usage.go:71-72`.
- **Fix:** compute summary counts once in `setRows`; pass the held rows to
  `autoFixTick`/`fleetTick`/`worktreeOwners`; reuse the `reloadIfChanged` model.

### UI6 — MED: full text re-render instead of append
- `dock.go:899` (`renderLog`): `SetText(applog.Default.TextFrom(level))`, up to 2,000 entries
  per generation change. Insert only new entries.
- `query.go:245-246`: `SetText(prettyIfJSON(s.Log.String()))` per generation, where
  `prettyIfJSON` (`:266-269`) Unmarshals into `any` and MarshalIndents the whole buffer. Show
  raw text while streaming and pretty-print once at the end. `q.lastA` (`:248`) keeps
  256 KB after the job ends.

### UI7 — MED: detail pane reloads on every scan
- `ui.go:807` (`setRows` → `detail.reload()`): `loadReport` (`detail.go:616`) re-reads
  GRAPH_REPORT.md (≤2 MB) and calls SetText. `loadWiki` (`:674-721`) rebuilds the list.
  `loadOverview` (`:473-541`) rebuilds the facts grid, despite its own comment. `loadHistory`
  (`:822-847`) copies 200 history entries.
- **Fix:** key each page on (path, BuiltAt, size, mtime) and skip the reload when the key is
  unchanged.

### UI8 — MED: glibc never returns the freed arena
- After UI1–UI7, add a periodic `C.malloc_trim(0)` via cgo (every ~5 min, or after large
  rebuilds), and launch with `MALLOC_ARENA_MAX=2` (32 threads → many arenas).
- `main.go:90-91` sets `SetMemoryLimit(512 MiB)`. The live Go heap is ~35–46 MB, so a limit
  around 128–160 MiB would hold the Go side near its live size without GC thrash.
  Measure GC CPU after B1/B2 before tightening.

### UI9 — LOW
- `columns.go:324/414`: `rowCell.row *board.Row` can pin a previous scan's whole rows
  array. Store `path string` instead.
- `globalpane.go:724,768`: button closures capture full `board.Row` copies. Capture the path.
- `viz.go:167-183`: the WebKitWebView keeps the last graph.html (and a running D3 layout)
  forever. Load `about:blank` on page leave; consider the document-viewer cache model
  (`internal/webview/webview.go:120`).

---

## 7. Gfy / CLIs

- **C1 — LOW-MED:** `gfy/opencodeproxy.go:314` reads each request body whole
  (`io.ReadAll(io.LimitReader(…, 64 MB))`) and `requestModel` (`:415-423`) Unmarshals the
  full body for one field. `LimitReader` silently truncates above 64 MB, so upstream receives
  broken JSON. **Fix:** stream the body through and sniff `"model"` from a 4 KB `TeeReader`
  prefix; use `http.MaxBytesReader` for a proper 413.
- **C2 — LOW-MED:** `cmd/ggraphify-scan/main.go:111-115`: `-usage` runs a full `board.Scan`
  (~650 MB of tokenizing) just for the repo list. Use `discover.Walk`. `doctor.go:71-86` and
  `cmd/ggraphify-job/main.go:350` (`publishIndex`) only need the commit and tier, which B2's
  tail read covers.
- **C3 — LOW:** `gfy/opencode.go:565-596` (`fetchModelsDev`) decodes all providers to read
  one. It runs on demand only.

## 8. Already fine (checked)
`graftstate.readMeta` stops early; `CoverageOf` streams; discover caches heads; globalgraph is
cached on stat; the proxy response streams through 16 KB; worker fan-out is capped at 8
(peak live heap 35–40 MB); 128 MB graph and 512 MB wiring ceilings protect against the
496 MB graph; no regexp in hot paths; no `bufio.Scanner` overflow; job subprocess output is
bounded by ringbuf; all `go func`s in ui are one-shot or guarded; settings and help dialogs are
built once.

---

## 9. Execution order

| # | Item | Effort | Expected win |
| --- | --- | --- | --- |
| 1 | **G1** undirected load + `_src/_tgt`, rewrite `_explain` | S | −200–260 MB/query transient, much faster `/query` |
| 2 | **G2** drop old G / streaming or subprocess load + `malloc_trim` | M | HWM 4.3 GB → ~1.5 GB, swap relief |
| 3 | **G3** intern + drop constant attrs | S | −45 % steady (~3 GB → est. <1 GB with G1/G2) |
| 4 | **UI1** skip Output events in `pumpJobs` | XS | stops C-heap fragmentation during sweeps |
| 5 | **B4** copy in `capPaths` | XS | frees pinned drift arrays |
| 6 | **B1 + B5** static/drift cache split (graphstate + graftstate) | M | ~1 GB/min of garbage → ~MBs |
| 7 | **B2** zero-alloc counter + tail read (also `locateStamp` ceiling) | M | ~10× less CPU per derivation |
| 8 | **UI2, UI3, UI4** dialog/usage/tooltip churn | S each | main-arena growth |
| 9 | **U1–U3** transcript reader reuse, pending-first, tighter prefilter | M | lower peak (219 MB), less CPU |
| 10 | **J1 + J2** compact retired logs, `Runner.Live()` | S | caps worst case 50–100 MB |
| 11 | **B3, B6, U4–U7, UI5–UI7, C1–C2** | S–M | churn |
| 12 | **G4–G6, UI8, B7, J3, UI9** | XS–S | hygiene / guardrails |

**Verification for each item:** before and after, `GODEBUG=gctrace=1 ./ggraphify-scan -json >/dev/null`
(total allocated, GC count); board `/proc/<pid>/status` VmRSS/VmHWM after 1 h idle and after
a 128-repo AST sweep; ggserve `curl :7791/health` + VmRSS around 10 `/query` calls and one
forced reload (`touch ~/.graphify/global-graph.json`). Re-run `/tmp/memexp.py` (lean vs
baseline) on the global graph once memory headroom allows.

## 10. Decisions needed
- B3: cache a compact per-repo mtime map (~10 MB retained) vs re-stream each TTL.
- G2: subprocess-load (zero parse garbage in the server, ~2× load time) vs in-process
  streaming (needs `ijson` in the graphify uv env).
- G3/G5: whether slimmed attributes and dropping test/`contains` edges are acceptable for `ggq`.
- Whether `/home/dns/tmp` should remain a board row (it drives most of the outliers).
