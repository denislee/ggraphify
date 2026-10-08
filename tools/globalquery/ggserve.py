#!/usr/bin/env python3
"""ggserve — one shared in-memory copy of the graphify global graph, over local HTTP.

Loads the global node-link graph into memory once and answers queries against
that single copy on 127.0.0.1.  A background thread watches the graph file and
swaps in a freshly loaded copy when it changes; the HTTP server is up
immediately, answering /health while the first load is still running.

Every node whose source_file is a repo-relative path is rewritten to an
openable absolute path (``<root>/<source_file>``) using the per-repo
``.graphify_root`` recorded alongside the manifest, so hits can be opened
directly.  Repos with no root fall back to ``<repo>:<source_file>``.

Routes (GET only):
  /health                      -> application/json {ready, nodes, edges, ...}
  /query?q=&budget=&depth=     -> graphify query text
  /path?a=&b=                  -> shortest-path text
  /explain?node=               -> node card, or an ambiguity report
"""
from __future__ import annotations

import argparse
import ctypes
import gc
import json
import os
import sys
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import networkx as nx
from graphify.serve import (
    _find_node,
    _query_graph_text,
    _shortest_path_text,
    find_node_ambiguity,
)

# String values repeated across hundreds of thousands of records: one shared
# object each instead of one per node/edge.
_INTERN_KEYS = (
    "relation", "confidence", "context", "file_type", "repo",
    "community_name", "source_file",
)
# Attributes no ggserve output path reads (graphify's _subgraph_to_text,
# _shortest_path_text, _find_node and the scorers never touch them).
_DROP_KEYS = ("_origin", "local_id")
# graphify.export's defaults: a confidence_score equal to these carries nothing.
_CONFIDENCE_SCORE_DEFAULTS = {"EXTRACTED": 1.0, "INFERRED": 0.55, "AMBIGUOUS": 0.2}
# Trigram id-set memo kept by graphify on G; cleared once it grows past this.
SET_CACHE_MAX = 2000

DEFAULT_GRAPH = "~/.graphify/global-graph.json"
DEFAULT_MANIFEST = "~/.graphify/global-manifest.json"

USAGE_QUERY = "usage: /query?q=<question>[&budget=2000][&depth=3]"
USAGE_PATH = "usage: /path?a=<source>&b=<target>"
USAGE_EXPLAIN = "usage: /explain?node=<symbol or node id>"
NOT_READY = "global graph is still loading — retry in a few seconds"


# --------------------------------------------------------------------------- #
# loading
# --------------------------------------------------------------------------- #

def _iso(ts: float) -> str:
    return datetime.fromtimestamp(ts, timezone.utc).isoformat()


def _file_key(path) -> tuple:
    """Identity of the loaded file: (st_mtime_ns, st_size)."""
    st = os.stat(path)
    return (st.st_mtime_ns, st.st_size)


def _read_text_quiet(path) -> str:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return fh.read().strip()
    except BaseException:
        return ""


def _home_display(root: str) -> str:
    """A repo root with the user's home prefix replaced by ``~``."""
    if not root:
        return ""
    home = os.path.expanduser("~")
    if root == home:
        return "~"
    if root.startswith(home + os.sep):
        return "~" + root[len(home):]
    return root


def _slim(attrs: dict, edge: bool) -> dict:
    """Drop unread attributes and intern repeated string values, in place."""
    for k in _DROP_KEYS:
        attrs.pop(k, None)
    for k in _INTERN_KEYS:
        v = attrs.get(k)
        if type(v) is str:
            attrs[k] = sys.intern(v)
    if edge:
        if attrs.get("weight") == 1.0:
            del attrs["weight"]
        cs = attrs.get("confidence_score")
        if cs is not None and cs == _CONFIDENCE_SCORE_DEFAULTS.get(
            attrs.get("confidence", "EXTRACTED"), 1.0
        ):
            del attrs["confidence_score"]
    return attrs


def _load_lean(graph_path: str) -> nx.Graph:
    """Load a node-link graph as ONE undirected ``nx.Graph`` with ``_src``/``_tgt``.

    Equivalent to graphify's ``_load_graph`` followed by ``_traversal_view``
    (which otherwise rebuilds an undirected copy of the whole graph on every
    query): edges are added in the order ``_traversal_view`` would add them —
    grouped by source node in node order, file order within a source — so
    adjacency order, and therefore traversal and rendering, match.  Markers
    already in the file win, as in graphify's CLI loader (#2309).

    The JSON dict tree is released record by record while the graph is built.
    """
    try:
        from graphify.serve import check_graph_file_size_cap
        check_graph_file_size_cap(Path(graph_path).resolve())
    except ImportError:
        pass
    with open(graph_path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    links = data.get("links")
    if links is None:
        links = data.get("edges") or []
    nodes = data.get("nodes") or []
    if data.get("multigraph"):
        raise ValueError("ggserve: multigraph global graphs are not supported")

    G = nx.Graph()
    G.graph.update(data.get("graph") or {})
    G.graph["_logical_directed"] = bool(data.get("directed", False))
    del data

    for i, node in enumerate(nodes):
        nodes[i] = None
        nid = node.pop("id")
        G.add_node(nid, **_slim(node, edge=False))
    del nodes

    # Node position as a DiGraph built from this file would order them: listed
    # nodes first, then link endpoints seen for the first time, in file order.
    pos = {nid: i for i, nid in enumerate(G)}
    for link in links:
        for end in (link["source"], link["target"]):
            if end not in pos:
                pos[end] = len(pos)
                G.add_node(end)
    order = sorted(range(len(links)), key=lambda i: pos[links[i]["source"]])
    del pos
    for i in order:
        link = links[i]
        links[i] = None
        u = link.pop("source")
        v = link.pop("target")
        link.pop("key", None)
        link.setdefault("_src", u)
        link.setdefault("_tgt", v)
        G.add_edge(u, v, **_slim(link, edge=True))
    del order, links

    try:
        from graphify.reflect import load_learning_overlay as _llo
        G.graph["_learning_overlay"] = _llo(Path(graph_path).resolve())
    except Exception:
        G.graph["_learning_overlay"] = {}
    return G


def _trim_memory() -> None:
    """Return freed heap to the OS after a (re)load."""
    gc.collect()
    try:
        ctypes.CDLL("libc.so.6").malloc_trim(0)
    except (OSError, AttributeError):
        pass


def load(graph_path, manifest_path):
    """Load the global graph and make every hit openable.

    Returns ``(G, stats)``.  Corrupt/unreadable JSON raises; callers catch
    ``BaseException``.
    """
    started = time.monotonic()
    graph_path = str(graph_path)
    manifest_path = str(manifest_path)

    G = _load_lean(graph_path)
    graph_mtime = os.stat(graph_path).st_mtime

    repos = {}
    try:
        with open(manifest_path, "r", encoding="utf-8") as fh:
            manifest = json.load(fh)
        repos = manifest.get("repos") or {}
    except BaseException:
        repos = {}

    displays = {}
    for tag, info in repos.items():
        try:
            source_path = (info or {}).get("source_path", "")
            real = os.path.realpath(source_path)
            root_file = os.path.join(os.path.dirname(real), ".graphify_root")
            displays[tag] = _home_display(_read_text_quiet(root_file))
        except BaseException:
            displays[tag] = ""

    for _nid, data in G.nodes(data=True):
        repo = data.get("repo")
        src = data.get("source_file")
        if not repo or not src or os.path.isabs(src):
            continue
        display = displays.get(repo, "")
        data["source_file"] = sys.intern(
            display + "/" + src if display else str(repo) + ":" + src
        )

    # Warm graphify's lazy search index here, off the query lock, so the first
    # explain after a (re)load is not the one that pays for building it.
    _find_node(G, "warmup")

    stats = {
        "nodes": G.number_of_nodes(),
        "edges": G.number_of_edges(),
        "repos": len(repos),
        "load_seconds": round(time.monotonic() - started, 1),
        "graph_mtime": _iso(graph_mtime),
        "loaded_at": _iso(time.time()),
    }
    return G, stats


# --------------------------------------------------------------------------- #
# shared state + reloader
# --------------------------------------------------------------------------- #

class State:
    """The one shared graph copy plus its load metadata."""

    def __init__(self, graph_path, manifest_path):
        self.graph_path = str(graph_path)
        self.manifest_path = str(manifest_path)
        self.G = None
        self.stats = {}
        self.key = None
        self.ready = False
        # graphify builds lazy caches on G, so every graph call is serialised.
        self.query_lock = threading.Lock()

    def install(self, G, stats, key=None):
        with self.query_lock:
            self.G = G
            self.stats = stats
            self.key = key
            self.ready = True

    def release(self):
        """Drop the current graph before a reload, so two never coexist.

        Queries answer 503 until the next ``install``; /health stays up and
        reports ``ready: false``.
        """
        with self.query_lock:
            self.G = None
            self.ready = False
        _trim_memory()


def reloader_loop(state: State, poll: float, settle: float) -> None:
    """Initial load, then poll for changes and swap in a fresh copy."""
    while True:
        # Key taken BEFORE the load: a write that lands mid-load must still
        # read as a change on the next tick.
        key = _safe_key(state.graph_path)
        try:
            G, stats = load(state.graph_path, state.manifest_path)
        except BaseException as e:  # SystemExit from _load_graph must not kill us
            print(f"ggserve: initial load failed: {e}", file=sys.stderr)
            time.sleep(poll)
            continue
        state.install(G, stats, key)
        del G
        _trim_memory()
        break

    while True:
        time.sleep(poll)
        try:
            key = _file_key(state.graph_path)
        except OSError:
            continue
        if key == state.key:
            continue

        # Only reload once the file has stopped changing across the settle window.
        time.sleep(settle)
        try:
            settled = _file_key(state.graph_path)
        except OSError:
            continue
        if settled != key:
            continue

        # Release the old graph first: holding it while the new one parses is
        # what doubled peak memory.  A failed reload leaves the service not
        # ready; the key is unchanged, so the next tick retries the load.
        state.release()
        try:
            G, stats = load(state.graph_path, state.manifest_path)
        except BaseException as e:
            print(f"ggserve: reload failed: {e}", file=sys.stderr)
            _trim_memory()
            continue
        state.install(G, stats, settled)
        del G
        _trim_memory()


def _safe_key(path):
    try:
        return _file_key(path)
    except OSError:
        return None


# --------------------------------------------------------------------------- #
# HTTP
# --------------------------------------------------------------------------- #

def _one(qs, key: str) -> str:
    values = qs.get(key)
    return values[0] if values else ""


def _clamp_int(value, default: int, lo: int, hi: int) -> int:
    try:
        n = int(value)
    except (TypeError, ValueError):
        return default
    return max(lo, min(hi, n))


def _edge_line(G, edge_u, edge_v, shown_id, arrow: str) -> str:
    ed = G.get_edge_data(edge_u, edge_v)
    if G.is_multigraph() and ed:
        ed = next(iter(ed.values()))
    rel = (ed or {}).get("relation", "")
    nd = G.nodes.get(shown_id, {})
    label = nd.get("label", shown_id)
    return (
        f"  {arrow} {rel} {label} "
        f"[{nd.get('source_file', '')} {nd.get('source_location', '')}]"
    )


def _explain(G, node: str) -> str:
    ids = _find_node(G, node)
    if not ids:
        return f"No node matching '{node}' found."

    rivals = find_node_ambiguity(G, node)
    if rivals:
        out = [f"Ambiguous: '{node}' matches {len(rivals)} nodes in different files."]
        for rid in rivals:
            data = G.nodes.get(rid, {})
            out.append(f"  {data.get('source_file', '')}")
            out.append(f"    id: {rid}")
        out.append("Retry with the node id.")
        return "\n".join(out) + "\n"

    nid = ids[0]
    data = G.nodes.get(nid, {})
    out = [
        f"Node: {data.get('label', nid)}",
        f"  ID: {nid}",
        f"  Source: {data.get('source_file', '')} {data.get('source_location', '')}",
        f"  Repo: {data.get('repo', '')}",
        f"  Community: {data.get('community', '')}",
    ]

    # G is undirected; each edge's _src says which way it really points.
    # Outgoing first, then incoming, 40 lines in all.  A self-loop is both.
    outgoing, incoming = [], []
    for n in G.neighbors(nid):
        ed = G.get_edge_data(nid, n) or {}
        if n == nid:
            outgoing.append(n)
            incoming.append(n)
        elif ed.get("_src", nid) == nid:
            outgoing.append(n)
        else:
            incoming.append(n)
    shown = [(nid, n, "->") for n in outgoing] + [(n, nid, "<-") for n in incoming]
    for u, v, arrow in shown[:40]:
        out.append(_edge_line(G, u, v, v if arrow == "->" else u, arrow))

    return "\n".join(out) + "\n"


def _cap_caches(G) -> None:
    """Bound graphify's per-graph trigram id-set memo (grows with every query)."""
    if G is None:
        return
    idx = G.graph.get("_trigram_index")
    if idx is not None and len(idx.get("set_cache", ())) > SET_CACHE_MAX:
        idx["set_cache"].clear()


class Handler(BaseHTTPRequestHandler):
    server_version = "ggserve"
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # quiet
        return

    @property
    def state(self) -> State:
        return self.server.state

    def do_GET(self):
        try:
            self._dispatch()
        except Exception as e:  # noqa: BLE001 - report any failure as 500
            self._send(500, f"error: {e}")

    def _send(self, code: int, body: str, ctype: str = "text/plain; charset=utf-8"):
        data = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _dispatch(self):
        parsed = urlparse(self.path)
        route = parsed.path
        qs = parse_qs(parsed.query)
        state = self.state

        if route == "/health":
            payload = dict(state.stats)
            payload["ready"] = bool(state.ready)
            self._send(200, json.dumps(payload), "application/json")
            return

        if not state.ready:
            self._send(503, NOT_READY)
            return

        if route == "/query":
            q = _one(qs, "q")
            if not q:
                self._send(400, USAGE_QUERY)
                return
            budget = _clamp_int(_one(qs, "budget"), 2000, 200, 8000)
            depth = _clamp_int(_one(qs, "depth"), 3, 1, 4)
            with state.query_lock:
                if state.G is None:
                    self._send(503, NOT_READY)
                    return
                text = _query_graph_text(
                    state.G,
                    q,
                    depth=depth,
                    token_budget=budget,
                    graph_path=state.graph_path,
                )
                _cap_caches(state.G)
            stats = state.stats
            header = (
                f"# global graph · {stats.get('nodes')} nodes · "
                f"{stats.get('repos')} repos · loaded {stats.get('loaded_at')}\n"
            )
            self._send(200, header + text)
            return

        if route == "/path":
            a = _one(qs, "a")
            b = _one(qs, "b")
            if not a or not b:
                self._send(400, USAGE_PATH)
                return
            with state.query_lock:
                if state.G is None:
                    self._send(503, NOT_READY)
                    return
                text = _shortest_path_text(state.G, {"source": a, "target": b})
                _cap_caches(state.G)
            self._send(200, text)
            return

        if route == "/explain":
            node = _one(qs, "node")
            if not node:
                self._send(400, USAGE_EXPLAIN)
                return
            with state.query_lock:
                if state.G is None:
                    self._send(503, NOT_READY)
                    return
                text = _explain(state.G, node)
                _cap_caches(state.G)
            self._send(200, text)
            return

        self._send(404, f"not found: {route}")


class Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, addr, handler, state: State):
        self.state = state
        super().__init__(addr, handler)


def make_server(state: State, host: str = "127.0.0.1", port: int = 7791) -> Server:
    return Server((host, port), Handler, state)


# --------------------------------------------------------------------------- #
# entry point
# --------------------------------------------------------------------------- #

def main(argv=None) -> int:
    parser = argparse.ArgumentParser(
        description="Serve one shared in-memory copy of the graphify global graph."
    )
    parser.add_argument("--graph", default=DEFAULT_GRAPH,
                        help="global node-link graph JSON (default: %(default)s)")
    parser.add_argument("--manifest", default=DEFAULT_MANIFEST,
                        help="global manifest JSON (default: %(default)s)")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=7791)
    parser.add_argument("--poll", type=float, default=20,
                        help="seconds between graph-file checks")
    parser.add_argument("--settle", type=float, default=5,
                        help="seconds to wait for the file to stop changing")
    args = parser.parse_args(argv)

    state = State(os.path.expanduser(args.graph), os.path.expanduser(args.manifest))
    # Bind first: a port already in use should fail in milliseconds, not after
    # a multi-second load of the graph.
    try:
        httpd = make_server(state, args.host, args.port)
    except OSError as e:
        print(f"ggserve: cannot bind {args.host}:{args.port}: {e}", file=sys.stderr)
        return 2
    thread = threading.Thread(
        target=reloader_loop, args=(state, args.poll, args.settle), daemon=True
    )
    thread.start()

    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
