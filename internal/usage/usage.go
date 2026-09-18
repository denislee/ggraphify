// Package usage answers the one question neither graphify nor graft can:
// which of them the agents on this machine are actually using, on what, and
// how often.
//
// It exists because the board already knows which repositories have an index
// and how stale each one is, and that turns out to be only half the story. An
// index nothing reads is a cost with no return, and a repository queried forty
// times a week with no index at all is the one worth extracting next. Neither
// fact is visible anywhere in graphify-out/ or graft/.
//
// Like every other derivation in this application, it is read from the
// filesystem rather than from a command (internal/graphstate's rule, for the
// same reason). There are two sources, and they answer different halves:
//
//   - Claude Code's own transcripts, at <config-dir>/projects/<slug>/*.jsonl.
//     Every tool call an agent made is a line in there, with a timestamp, the
//     session's cwd, its git branch and its session id. That is where a
//     `graphify query` shows up, and a /graphify skill invocation, and the
//     PreToolUse hook firing, and an MCP call into graft's server. The board
//     reads every account on the machine, not just the selected one: which
//     login was paying is an attribute of the usage, not a filter on it.
//
//   - graft's own per-session counters, at <repo>/graft/.cache/session/
//     <session-id>.json. graft's hooks maintain these, and they carry what a
//     transcript cannot cheaply reconstruct: how many reads went to graft
//     versus straight to the source files, how many tokens that saved, and
//     what the session was billed. They are keyed by the same session id the
//     transcripts carry, which is what lets the two halves join.
//
// Both are read incrementally. The transcript corpus on a working machine is
// gigabytes across thousands of files, so a full parse on every refresh tick
// is out of the question: Index remembers a byte offset per transcript and
// parses only what was appended, and remembers the last counters seen per
// graft session file and applies only the delta.
//
// Nothing here is written back into any repository, and no transcript content
// is retained — only the classification of a tool call, its timestamp, and the
// directory it ran in. Prompts, file contents and command output are read past
// and dropped.
package usage

import (
	"sort"
	"strings"
	"time"
)

// Tool is which of the two indexes an event belongs to.
type Tool uint8

const (
	Graphify Tool = iota
	Graft
)

func (t Tool) String() string {
	if t == Graft {
		return "graft"
	}
	return "graphify"
}

// Kind is how the tool was reached. The distinction is not cosmetic: a CLI
// call and an MCP call are the same tool through different plumbing, and a
// hook injection is the integration firing whether or not the agent went on to
// run anything. A board that collapsed them could not tell "installed and
// ignored" from "not installed".
type Kind uint8

const (
	// CLI is the tool invoked as a subprocess from a Bash tool call.
	CLI Kind = iota
	// MCP is a call into the tool's MCP server (mcp__graft__graft_find_code
	// and its siblings).
	MCP
	// Skill is the packaged skill being invoked — /graphify, or the Skill
	// tool naming it.
	Skill
	// Hook is the integration injecting context into the session: graft's
	// SessionStart pointer block, graphify's PreToolUse reminder. It is the
	// cheapest event to produce and the least meaningful on its own, which is
	// why it is counted separately rather than folded into the total.
	Hook
)

func (k Kind) String() string {
	switch k {
	case MCP:
		return "mcp"
	case Skill:
		return "skill"
	case Hook:
		return "hook"
	}
	return "cli"
}

// Kinds is every kind, in the order the UI shows them.
var Kinds = []Kind{CLI, MCP, Skill, Hook}

// Event is one use of one tool by one agent session.
//
// It carries no prompt, no argument values and no output — Verb is the
// subcommand, not the query that was asked.
type Event struct {
	At      time.Time `json:"at"`
	Tool    Tool      `json:"tool"`
	Kind    Kind      `json:"kind"`
	Verb    string    `json:"verb"`
	Repo    string    `json:"repo"`   // the session's working directory
	Branch  string    `json:"branch"` // its git branch, when the record named one
	Account string    `json:"account"`
	Session string    `json:"session"`

	// ID is the harness's tool_use id, which is what lets a call be joined to
	// the result that came back for it. It is empty for the events that have
	// no result at all — a hook firing, a slash command in a user turn.
	ID string `json:"id,omitempty"`
	// Fail is why the call came back unusable, once its result has been seen.
	// A call whose result has not arrived yet, or that worked, is FailNone.
	Fail Fail `json:"fail,omitempty"`
}

// Failed reports whether this call came back unusable.
func (e Event) Failed() bool { return e.Fail != FailNone }

// Account is one Claude Code configuration directory to read: one login, and
// the transcripts written under it. It mirrors gfy.ClaudeAccount without
// depending on it, so this package stays free of the rest of the application.
type Account struct {
	Name string
	Dir  string
}

// Day is the bucket key format: a UTC-local calendar day, "2006-01-02".
// Local, because "how much did I use this yesterday" is a question about the
// day this machine was in, not about UTC.
const dayFormat = "2006-01-02"

// DayKey is the bucket a timestamp falls in.
func DayKey(t time.Time) string { return t.Local().Format(dayFormat) }

// ParseDay reads a bucket key back. The zero time is returned for a key that
// does not parse, which sorts before every real day and is therefore dropped
// by any window.
func ParseDay(s string) time.Time {
	t, err := time.ParseInLocation(dayFormat, s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// counterKey is how a (tool, kind, verb) triple is stored in a day bucket:
// one flat string, so the whole rollup is a map[string]int that marshals
// without a custom encoder. "graphify/cli/query".
func counterKey(t Tool, k Kind, verb string) string {
	if verb == "" {
		verb = "other"
	}
	return t.String() + "/" + k.String() + "/" + verb
}

// splitCounter is counterKey's inverse. An unparseable key is reported as
// graphify/cli/<whole key>, which shows up as an odd verb rather than being
// silently dropped: a rollup written by a newer version should degrade to a
// strange-looking row, not to a wrong total.
func splitCounter(s string) (Tool, Kind, string) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		return Graphify, CLI, s
	}
	t := Graphify
	if parts[0] == "graft" {
		t = Graft
	}
	k := CLI
	switch parts[1] {
	case "mcp":
		k = MCP
	case "skill":
		k = Skill
	case "hook":
		k = Hook
	}
	return t, k, parts[2]
}

// Count is one line of a breakdown: a name and how often it happened.
type Count struct {
	Name  string
	Count int
}

// sortCounts orders a breakdown by count descending, then by name, so a
// repaint of unchanged data never reorders rows.
func sortCounts(c []Count) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Count != c[j].Count {
			return c[i].Count > c[j].Count
		}
		return c[i].Name < c[j].Name
	})
}
