# globalquery — a shared in-memory global graph

`graphify query` on the 373 MB global graph takes ~18 s per call, because every
invocation re-reads and re-warms the whole file. `ggserve` loads the graph
**once** into a long-running local process and answers queries against that one
copy over HTTP, so a query is fast after the first load. Hits are rewritten to
openable `~/git/<repo>/<file>` paths.

## Install

```bash
tools/globalquery/install.sh   # copies ggserve.py to ~/.local/share/ggraphify/globalquery/,
                               # ggq to ~/.local/bin/, enables + (re)starts the user unit
ggq health                     # "ready": true after the first load (~30 s)
```

Re-run it after changing `ggserve.py` or `ggq`.

## Usage

```bash
ggq query "how does the deploy pipeline work" --budget 3000 --depth 3
ggq path "AlphaService" "ZetaClient"
ggq explain "ZetaClient"
ggq health
```

`ggq` talks to `http://127.0.0.1:7791` (override with `GGQ_PORT`, timeout with
`GGQ_TIMEOUT`). If the service is not running it prints the `systemctl` command
to start it and exits 2.

## How it works

* `ggserve.py` loads `~/.graphify/global-graph.json` (override with `--graph`)
  and `~/.graphify/global-manifest.json` (`--manifest`).
* A background thread reloads and atomically swaps the graph when the file
  changes (`--poll`, `--settle`); the HTTP server is available immediately and
  returns 503 until the first load finishes.
* `/health`, `/query`, `/path` and `/explain` are the routes; every graph call
  is serialised with a lock because graphify builds lazy caches on the graph.
* Node `source_file` values are rewritten using each repo's `.graphify_root`
  (from the manifest) into `~/git/<repo>/...`; repos without a root fall back to
  `<repo>:<file>`.
