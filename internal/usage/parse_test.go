package usage

import "testing"

func TestCommandsFindsRealInvocations(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
		want []Call
	}{
		{"plain", `graphify query "how does X work"`, []Call{{Graphify, "query"}}},
		{"absolute path", `/home/dns/.local/bin/graphify update .`, []Call{{Graphify, "update"}}},
		{"after cd", `cd /tmp/repo && graft ask "where is Y" --source`, []Call{{Graft, "ask"}}},
		{"piped", `graft grep "Scan" | head -40`, []Call{{Graft, "grep"}}},
		{"two on a line", `graphify update . ; graft build`, []Call{{Graphify, "update"}, {Graft, "build"}}},
		{"group verb", `graphify export wiki --graph g.json`, []Call{{Graphify, "export wiki"}}},
		{"platform group", `graphify claude install`, []Call{{Graphify, "claude install"}}},
		{"bare flag", `graft --version`, []Call{{Graft, "--version"}}},
		{"short flag normalised", `graphify -h`, []Call{{Graphify, "--help"}}},
		{"command substitution", `echo $(graft map)`, []Call{{Graft, "map"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Commands(tc.cmd)
			if len(got) != len(tc.want) {
				t.Fatalf("Commands(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Commands(%q)[%d] = %v, want %v", tc.cmd, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The prose cases are the whole reason the verb whitelist exists: an agent's
// Bash calls are full of documentation that mentions both tools, and an early
// draft that counted "the token after the binary" over-reported by a third.
func TestCommandsIgnoresProse(t *testing.T) {
	for _, tc := range []struct{ name, cmd string }{
		{"english", "echo 'graphify is excellent at one repository at a time'"},
		{"sentence at line start", "cat > README.md <<'EOF'\ngraphify never re-implements anything\ngraft genuinely helps\nEOF"},
		{"heredoc body with a real-looking command", "cat > doc.md <<'EOF'\ngraphify query \"x\"\nEOF"},
		{"quoted mention", `git commit -m "graft build now runs on every commit"`},
		{"backticked in markdown", "printf '%s' 'run `graphify update .` first'"},
		{"unknown subcommand", `graphify frobnicate .`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Commands(tc.cmd); len(got) != 0 {
				t.Fatalf("Commands(%q) = %v, want none", tc.cmd, got)
			}
		})
	}
}

// A heredoc that is itself writing this package's source must not be read as
// usage — the file it writes is full of both names in command position.
func TestStripHeredocsHandlesVariants(t *testing.T) {
	cmd := "cat <<-EOF > a\ngraphify query \"x\"\nEOF\ngraft map\ncat <<\"END\" > b\ngraft ask \"y\"\nEND"
	got := Commands(cmd)
	if len(got) != 1 || got[0] != (Call{Graft, "map"}) {
		t.Fatalf("Commands = %v, want exactly one graft map", got)
	}
}

func TestMCPVerb(t *testing.T) {
	tool, verb, ok := mcpVerb("mcp__graft__graft_find_code")
	if !ok || tool != Graft || verb != "find_code" {
		t.Fatalf("mcpVerb = %v %q %v", tool, verb, ok)
	}
	if _, _, ok := mcpVerb("mcp__nova__nova_k8s"); ok {
		t.Fatal("mcpVerb accepted an unrelated server")
	}
	if _, _, ok := mcpVerb("Bash"); ok {
		t.Fatal("mcpVerb accepted a non-MCP tool")
	}
}

func TestClassifyFail(t *testing.T) {
	cases := []struct {
		name string
		text string
		err  bool
		want Fail
	}{
		{"graphify with no graph", "error: graph file not found: /r/graphify-out/graph.json", true, FailNoGraph},
		{"graft with no graph", "✗ no graph — run graft build first", true, FailNoGraph},
		// graft ask exits zero with nothing to say, which is still a repository
		// that could not answer.
		{"graft ask on an empty index", "no matching nodes — try different words, or `graft build` if graft/ is empty", false, FailNoGraph},
		{"denied", "Permission for this action was denied by the Claude Code auto mode classifier.", true, FailDenied},
		{"timeout", "Exit code 143\nCommand timed out after 10m 0s", true, FailTimeout},
		{"not installed", "bash: graphify: command not found", true, FailNoTool},
		{"other error", "Exit code 2\nls: cannot access 'x': No such file or directory", true, FailOther},
		{"success", "Traversal: BFS depth=2 | 72 nodes found", false, FailNone},
		// Prose about a missing graph, in output that did not fail, is not a
		// failure: only the phrases a tool prints verbatim are believed on
		// their own.
		{"prose about a missing graph", "whether the graph file not found case is handled", false, FailNone},
		// …but the tool's own line is, because a pipeline can swallow the
		// exit code that would otherwise have flagged it.
		{"piped through head", "error: graph file not found: /r/graphify-out/graph.json", false, FailNoGraph},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyFail(c.text, c.err); got != c.want {
				t.Fatalf("classifyFail(%q, %v) = %v, want %v", c.text, c.err, got, c.want)
			}
		})
	}
}
