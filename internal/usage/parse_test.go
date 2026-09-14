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
