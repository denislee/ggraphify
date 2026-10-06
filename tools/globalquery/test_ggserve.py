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


if __name__ == "__main__":
    unittest.main()
