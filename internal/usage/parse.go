package usage

import (
	"strings"
)

// Call is one invocation found in a shell command line.
type Call struct {
	Tool Tool
	Verb string
}

// graphifyVerbs and graftVerbs are the subcommands each tool actually has, as
// of the releases this board is pinned to (internal/gfy holds the same pin for
// the argv it builds).
//
// A whitelist rather than "whatever token follows the binary" is load-bearing,
// and the reason is that agent transcripts are full of prose that mentions
// these tools. A heredoc writing this very file, a README being catted, a
// commit message being drafted — all of them contain lines that begin
// "graphify is", "graft genuinely", "graphify would". Counting those as
// invocations inflated every number in an early draft of this package by about
// a third. A token that is not a known subcommand and not a flag is therefore
// not a call at all.
//
// The cost of the whitelist is that a subcommand added upstream goes uncounted
// until this list is updated. That is the right way round: an undercount that
// corrects itself with a one-line change beats an overcount that cannot be
// distinguished from real use.
var graphifyVerbs = map[string]bool{
	"add": true, "affected": true, "benchmark": true, "check-update": true,
	"clone": true, "cluster-only": true, "diagnose": true, "explain": true,
	"extract": true, "god-nodes": true, "install": true, "label": true,
	"merge-driver": true, "merge-graphs": true, "path": true, "prs": true,
	"query": true, "reflect": true, "save-result": true, "tree": true,
	"uninstall": true, "update": true, "version": true, "watch": true,
}

var graftVerbs = map[string]bool{
	"ask": true, "blast": true, "brain": true, "build": true, "callers": true,
	"check": true, "grep": true, "help": true, "init": true, "map": true,
	"mcp": true, "skeleton": true, "stats": true, "telemetry": true,
	"uninstall": true, "upgrade": true, "version": true, "viz": true,
}

// graphifyGroups are the subcommands that take a subcommand of their own.
// `graphify export wiki` and `graphify export html` are different operations
// with different costs, and collapsing them to "export" would hide the one
// fact the column exists to show.
var graphifyGroups = map[string]bool{
	"export": true, "global": true, "hook": true, "provider": true,
	// The per-platform installers: `graphify claude install`, `graphify
	// cursor uninstall`, and the dozen others that follow the same shape.
	"aider": true, "antigravity": true, "claude": true, "claw": true,
	"codebuddy": true, "codex": true, "copilot": true, "cursor": true,
	"devin": true, "droid": true, "gemini": true, "hermes": true,
	"kilo": true, "kiro": true, "opencode": true, "pi": true, "trae": true,
	"trae-cn": true, "vscode": true,
}

// Commands finds every graphify and graft invocation in one shell command.
//
// It is a deliberately small shell reader, not a shell: it tracks quoting and
// command position well enough to tell `graphify query …` from `echo graphify
// query …`, and it drops heredoc bodies wholesale, which is where almost all
// of the prose in an agent's Bash calls lives.
func Commands(cmd string) []Call {
	if !strings.Contains(cmd, "graphify") && !strings.Contains(cmd, "graft") {
		return nil
	}
	var out []Call
	for _, tok := range tokenize(stripHeredocs(cmd)) {
		if !tok.start {
			continue
		}
		tool, ok := toolNamed(tok.word)
		if !ok {
			continue
		}
		if c, ok := classify(tool, tok.rest); ok {
			out = append(out, c)
		}
	}
	return out
}

// toolNamed recognises the binary, by base name, so an absolute path or a
// ./graft in a checkout counts the same as the one on PATH.
func toolNamed(word string) (Tool, bool) {
	if i := strings.LastIndexByte(word, '/'); i >= 0 {
		word = word[i+1:]
	}
	switch word {
	case "graphify":
		return Graphify, true
	case "graft":
		return Graft, true
	}
	return 0, false
}

// classify turns the words after the binary into a verb, or rejects the call.
func classify(tool Tool, rest []string) (Call, bool) {
	verbs, groups := graphifyVerbs, graphifyGroups
	if tool == Graft {
		verbs, groups = graftVerbs, nil
	}
	for _, w := range rest {
		switch {
		case w == "":
			continue
		case groups[w]:
			// A group needs its second word to mean anything. `graphify
			// export` alone is a usage message, which is still a use.
			verb := w
			if sub, ok := nextWord(rest, w); ok {
				verb += " " + sub
			}
			return Call{Tool: tool, Verb: verb}, true
		case verbs[w]:
			return Call{Tool: tool, Verb: w}, true
		case strings.HasPrefix(w, "-"):
			// A bare flag is a real invocation — `graft --version`,
			// `graphify --help` — and it is the only case where a token that
			// is not in the whitelist is accepted.
			return Call{Tool: tool, Verb: flagVerb(w)}, true
		default:
			// Anything else means this was prose, or a shell construct the
			// reader does not model. Either way it is not a call.
			return Call{}, false
		}
	}
	return Call{}, false
}

// nextWord returns the word after the first occurrence of w, when it is a
// plain word rather than a flag.
func nextWord(rest []string, w string) (string, bool) {
	for i, s := range rest {
		if s == w && i+1 < len(rest) {
			n := rest[i+1]
			if n != "" && !strings.HasPrefix(n, "-") && isVerbish(n) {
				return n, true
			}
			return "", false
		}
	}
	return "", false
}

// flagVerb normalises a flag to the long spelling the help text uses, so
// `-h` and `--help` are one row rather than two.
func flagVerb(w string) string {
	switch w {
	case "-h", "--help":
		return "--help"
	case "-v", "--version":
		return "--version"
	}
	if i := strings.IndexByte(w, '='); i > 0 {
		w = w[:i]
	}
	return w
}

// isVerbish is the shape a subcommand has: lowercase letters, digits and
// dashes. It keeps a path or a quoted question from being read as one.
func isVerbish(s string) bool {
	if s == "" || len(s) > 24 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// token is one word of a command line and whether it sits where a command
// name goes.
type token struct {
	word  string
	rest  []string
	start bool
}

// tokenize splits a command line into words, marking the ones in command
// position and attaching the words that follow each of those.
//
// Quoting is honoured only well enough for this job: a quoted word is one
// word and is never in command position. Command substitution opens a new
// command position after $( but not after a backtick — a backtick in an
// agent's Bash call is, overwhelmingly, markdown being written into a file.
func tokenize(s string) []token {
	var (
		words  []string
		starts []bool
		cur    strings.Builder
		quoted bool
		atCmd  = true
		word   bool
	)
	flush := func() {
		if !word {
			return
		}
		words = append(words, cur.String())
		starts = append(starts, atCmd && !quoted)
		cur.Reset()
		word = false
		quoted = false
		atCmd = false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'', '"':
			q := c
			quoted = true
			word = true
			for i++; i < len(s) && s[i] != q; i++ {
				cur.WriteByte(s[i])
			}
		case '\\':
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					cur.WriteByte(s[i])
					word = true
				}
			}
		case ' ', '\t', '\r':
			flush()
		case '\n', ';', '|', '&', '(', '{':
			flush()
			atCmd = true
		case ')', '}':
			flush()
		case '$':
			if i+1 < len(s) && s[i+1] == '(' {
				flush()
				atCmd = true
				i++
				continue
			}
			cur.WriteByte(c)
			word = true
		default:
			cur.WriteByte(c)
			word = true
		}
	}
	flush()

	out := make([]token, 0, len(words))
	for i, w := range words {
		if !starts[i] {
			continue
		}
		end := len(words)
		for j := i + 1; j < len(words); j++ {
			if starts[j] {
				end = j
				break
			}
		}
		out = append(out, token{word: w, rest: words[i+1 : end], start: true})
	}
	return out
}

// stripHeredocs removes the body of every here-document in a command.
//
// An agent writes files by piping a heredoc into cat, and those files are
// frequently about graphify or graft — this package's own source among them.
// The body is data, never a command, so it is cut before anything else looks
// at the line.
func stripHeredocs(s string) string {
	if !strings.Contains(s, "<<") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	var delim string
	for _, ln := range lines {
		if delim != "" {
			if strings.TrimSpace(ln) == delim {
				delim = ""
			}
			continue
		}
		out = append(out, ln)
		if d, ok := heredocDelim(ln); ok {
			delim = d
		}
	}
	return strings.Join(out, "\n")
}

// heredocDelim reads the delimiter word out of a line that opens a heredoc,
// accepting the quoted and <<- spellings.
func heredocDelim(ln string) (string, bool) {
	i := strings.Index(ln, "<<")
	if i < 0 {
		return "", false
	}
	rest := ln[i+2:]
	if strings.HasPrefix(rest, "<") { // <<< is a here-string: one line, no body
		return "", false
	}
	rest = strings.TrimPrefix(rest, "-")
	rest = strings.TrimLeft(rest, " \t")
	rest = strings.TrimLeft(rest, "'\"")
	end := strings.IndexAny(rest, " \t'\"|;&)")
	if end >= 0 {
		rest = rest[:end]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// mcpVerb turns an MCP tool name into a verb, or reports that the name
// belongs to neither tool.
//
// Names are "mcp__<server>__<tool>"; the server is what says which tool it is,
// and the per-server prefix on the tool name ("graft_find_code") is dropped so
// the breakdown reads find_code beside the CLI's ask and grep.
func mcpVerb(name string) (Tool, string, bool) {
	if !strings.HasPrefix(name, "mcp__") {
		return 0, "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	server, tool := parts[0], parts[1]
	var t Tool
	switch {
	case strings.Contains(server, "graphify"):
		t = Graphify
	case strings.Contains(server, "graft"):
		t = Graft
	default:
		return 0, "", false
	}
	tool = strings.TrimPrefix(tool, t.String()+"_")
	if tool == "" {
		tool = "call"
	}
	return t, tool, true
}
