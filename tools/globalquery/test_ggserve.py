#!/usr/bin/env python3
"""Tests for ggserve: loading, path rewriting, and the HTTP routes."""
from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import ggserve  # noqa: E402


def _graph_json():
    return {
        "directed": False,
        "multigraph": False,
        "graph": {},
        "nodes": [
            {
                "id": "alpha:a",
                "label": "AlphaService",
                "repo": "alpha",
                "source_file": "internal/a.go",
                "source_location": "L10",
                "community": "1",
            },
            {
                "id": "alpha:b",
                "label": "alphaHelper",
                "repo": "alpha",
                "source_file": "internal/b.go",
                "source_location": "L20",
                "community": "1",
            },
            {
                "id": "zeta:z",
                "label": "ZetaClient",
                "repo": "zeta",
                "source_file": "z.go",
                "source_location": "L1",
                "community": "2",
            },
        ],
        "links": [
            {"source": "alpha:a", "target": "alpha:b", "relation": "calls"},
            {"source": "alpha:a", "target": "zeta:z", "relation": "imports"},
        ],
    }


class GgserveTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="ggserve-test-")
        self.root = Path(self.tmp)

        self.graph_path = self.root / "global-graph.json"
        self.graph_path.write_text(json.dumps(_graph_json()), encoding="utf-8")

        # manifest + .graphify_root for alpha only (zeta deliberately absent).
        # The root file lives beside the manifest's source_path (the per-repo graph).
        alpha_root = self.root / "src" / "alpha"
        alpha_root.mkdir(parents=True, exist_ok=True)

        alpha_graph = self.root / "k" / "alpha" / "graph.json"
        alpha_graph.parent.mkdir(parents=True, exist_ok=True)
        alpha_graph.write_text("{}", encoding="utf-8")
        (alpha_graph.parent / ".graphify_root").write_text(
            str(alpha_root), encoding="utf-8"
        )

        self.manifest_path = self.root / "global-manifest.json"
        self.manifest_path.write_text(
            json.dumps({"repos": {"alpha": {"source_path": str(alpha_graph)}}}),
            encoding="utf-8",
        )

        self.expected_alpha = str(alpha_root)
        home = os.path.expanduser("~")
        if self.expected_alpha == home or self.expected_alpha.startswith(home + os.sep):
            self.expected_alpha = "~" + self.expected_alpha[len(home):]

        self.state = None
        self.httpd = None
        self.thread = None
        self.base = None

    def tearDown(self):
        if self.httpd is not None:
            self.httpd.shutdown()
            self.httpd.server_close()
        if self.thread is not None:
            self.thread.join(timeout=5)
        shutil.rmtree(self.tmp, ignore_errors=True)

    # -- helpers ----------------------------------------------------------- #

    def _start(self):
        G, stats = ggserve.load(self.graph_path, self.manifest_path)
        self.state = ggserve.State(self.graph_path, self.manifest_path)
        self.state.install(G, stats, ggserve._safe_key(self.graph_path))
        self.httpd = ggserve.make_server(self.state, "127.0.0.1", 0)
        self.port = self.httpd.server_address[1]
        self.base = f"http://127.0.0.1:{self.port}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()
        return G, stats

    def _get(self, path):
        url = self.base + path
        try:
            with urllib.request.urlopen(url, timeout=10) as resp:
                return resp.status, resp.read().decode("utf-8")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8")

    # -- tests ------------------------------------------------------------- #

    def test_load_rewrites_paths(self):
        G, stats = ggserve.load(self.graph_path, self.manifest_path)

        self.assertEqual(G.nodes["alpha:a"]["source_file"],
                         f"{self.expected_alpha}/internal/a.go")
        self.assertEqual(G.nodes["alpha:b"]["source_file"],
                         f"{self.expected_alpha}/internal/b.go")
        self.assertEqual(G.nodes["zeta:z"]["source_file"], "zeta:z.go")

        self.assertEqual(stats["nodes"], 3)
        self.assertEqual(stats["edges"], 2)
        self.assertEqual(stats["repos"], 1)

    def test_http_routes(self):
        self._start()

        code, body = self._get("/health")
        self.assertEqual(code, 200)
        payload = json.loads(body)
        self.assertTrue(payload["ready"])
        self.assertEqual(payload["nodes"], 3)

        code, body = self._get("/query?q=AlphaService")
        self.assertEqual(code, 200)
        self.assertTrue(body.startswith("# global graph"), body[:80])

        code, body = self._get("/query")
        self.assertEqual(code, 400)
        self.assertTrue(body.startswith("usage:"), body)

        code, body = self._get("/explain?node=ZetaClient")
        self.assertEqual(code, 200)
        self.assertIn("Repo: zeta", body)

        code, body = self._get("/nonsense")
        self.assertEqual(code, 404)


class LeanLoadTest(unittest.TestCase):
    """_load_lean / _slim / _explain / _cap_caches internals."""

    def _load(self, graph):
        tmp = tempfile.TemporaryDirectory(prefix="ggserve-lean-")
        self.addCleanup(tmp.cleanup)
        path = os.path.join(tmp.name, "graph.json")
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(graph, fh)
        return ggserve._load_lean(path)

    @staticmethod
    def _base_graph(**overrides):
        graph = {
            "directed": False,
            "multigraph": False,
            "graph": {},
            "nodes": [{"id": "a"}, {"id": "b"}],
            "links": [{"source": "a", "target": "b", "relation": "calls"}],
        }
        graph.update(overrides)
        return graph

    def test_undirected_load_stamps_src_tgt(self):
        graph = {
            "directed": False,
            "multigraph": False,
            "graph": {},
            "nodes": [{"id": "x"}, {"id": "y"}, {"id": "z"}],
            "links": [
                {"source": "x", "target": "y", "relation": "calls"},
                # file markers already present, flipped vs source/target:
                {"source": "y", "target": "z", "relation": "calls",
                 "_src": "z", "_tgt": "y"},
            ],
        }
        G = self._load(graph)

        self.assertFalse(G.is_directed())

        plain = G.get_edge_data("x", "y")
        self.assertEqual(plain["_src"], "x")
        self.assertEqual(plain["_tgt"], "y")

        # The markers already in the file win, even when flipped.
        flipped = G.get_edge_data("y", "z")
        self.assertEqual(flipped["_src"], "z")
        self.assertEqual(flipped["_tgt"], "y")

    def test_explain_arrows_follow_src(self):
        graph = {
            "directed": True,
            "multigraph": False,
            "graph": {},
            "nodes": [
                {"id": "a", "label": "Alpha", "source_file": "a.go",
                 "source_location": "L1"},
                {"id": "b", "label": "Beta", "source_file": "b.go",
                 "source_location": "L2"},
                {"id": "c", "label": "Gamma", "source_file": "c.go",
                 "source_location": "L3"},
            ],
            "links": [
                {"source": "a", "target": "b", "relation": "calls"},
                {"source": "c", "target": "a", "relation": "imports"},
            ],
        }
        G = self._load(graph)
        out = ggserve._explain(G, "Alpha")
        lines = out.splitlines()

        outgoing = next(
            i for i, ln in enumerate(lines) if ln.startswith("  -> calls Beta")
        )
        incoming = next(
            i for i, ln in enumerate(lines) if ln.startswith("  <- imports Gamma")
        )
        self.assertLess(outgoing, incoming)

    def test_set_cache_cap(self):
        G = self._load(self._base_graph())
        G.graph["_trigram_index"] = {
            "ids": [],
            "postings": {},
            "set_cache": {
                str(i): {i} for i in range(ggserve.SET_CACHE_MAX + 1)
            },
        }
        ggserve._cap_caches(G)
        self.assertEqual(G.graph["_trigram_index"]["set_cache"], {})

        G2 = self._load(self._base_graph())
        G2.graph["_trigram_index"] = {
            "ids": [],
            "postings": {},
            "set_cache": {
                str(i): {i} for i in range(ggserve.SET_CACHE_MAX)
            },
        }
        ggserve._cap_caches(G2)
        self.assertEqual(
            len(G2.graph["_trigram_index"]["set_cache"]),
            ggserve.SET_CACHE_MAX,
        )

        ggserve._cap_caches(None)  # must not raise

        G3 = self._load(self._base_graph())
        self.assertNotIn("_trigram_index", G3.graph)
        ggserve._cap_caches(G3)  # must not raise

    def test_slimming_keeps_printed_attrs(self):
        nodes = [{
            "id": "n1",
            "label": "Alpha",
            "source_file": "a.go",
            "source_location": "L1",
            "community": "1",
            "community_name": "core",
            "repo": "r1",
            "file_type": "go",
            "norm_label": "alpha",
            "_origin": "file",
            "local_id": 7,
        }]
        # Equal repo strings, built separately, must intern to one object.
        for nid in ("n2", "n3", "n4", "n5", "n6", "n7"):
            nodes.append({"id": nid, "label": nid, "repo": "".join(["re", "po"])})

        links = [
            {"source": "n1", "target": "n2", "relation": "calls",
             "confidence": "EXTRACTED", "confidence_score": 0.9,
             "weight": 2.0, "context": "ctx", "source_file": "a.go",
             "source_location": "L1", "_origin": "file"},
            {"source": "n1", "target": "n3", "relation": "calls", "weight": 1.0},
            {"source": "n1", "target": "n4", "relation": "calls", "weight": 0.5},
            {"source": "n1", "target": "n5", "relation": "calls",
             "confidence": "EXTRACTED", "confidence_score": 1.0},
            {"source": "n1", "target": "n6", "relation": "calls",
             "confidence": "INFERRED", "confidence_score": 0.55},
            {"source": "n1", "target": "n7", "relation": "calls",
             "confidence": "INFERRED", "confidence_score": 0.8},
        ]
        G = self._load({
            "directed": False, "multigraph": False, "graph": {},
            "nodes": nodes, "links": links,
        })

        nd = G.nodes["n1"]
        for key in ("label", "source_file", "source_location", "community",
                    "community_name", "repo", "file_type", "norm_label"):
            self.assertIn(key, nd)
        self.assertNotIn("_origin", nd)
        self.assertNotIn("local_id", nd)

        e1 = G.get_edge_data("n1", "n2")
        for key in ("relation", "confidence", "context", "source_file",
                    "source_location", "_src", "_tgt"):
            self.assertIn(key, e1)
        self.assertNotIn("_origin", e1)
        self.assertEqual(e1["confidence_score"], 0.9)

        self.assertNotIn("weight", G.get_edge_data("n1", "n3"))
        self.assertEqual(G.get_edge_data("n1", "n4")["weight"], 0.5)

        self.assertNotIn("confidence_score", G.get_edge_data("n1", "n5"))
        self.assertNotIn("confidence_score", G.get_edge_data("n1", "n6"))
        self.assertEqual(G.get_edge_data("n1", "n7")["confidence_score"], 0.8)

        self.assertIs(G.nodes["n2"]["repo"], G.nodes["n3"]["repo"])


if __name__ == "__main__":
    unittest.main()
