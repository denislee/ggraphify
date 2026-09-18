package usage

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// A use that failed is the other half of the dashboard's question.
//
// The rollup could already say that an agent ran `graphify query` forty times
// in a checkout with no graph, but only by inference: the counters said the
// tool was used, the board said there was no index, and the disagreement was
// left for a person to notice. What it could not say is what actually came
// back — and what came back is the sharpest evidence there is. A call that
// returned "error: graph file not found" is not a statistic about coverage, it
// is an agent that asked this repository a question and was told there was
// nothing to answer it with.
//
// So the transcript scan now joins each call to its result. Nothing of the
// output is retained — the same rule the rest of this package follows — only
// which of a handful of reasons it failed for, which is enough to tell the one
// failure a button can fix from the ones it cannot.

// Fail is why a call came back unusable. FailNone is a call that worked, and
// is what the overwhelming majority of results classify as.
type Fail uint8

const (
	// FailNone is success, or a result this package cannot read as a failure.
	FailNone Fail = iota
	// FailNoGraph is the one that matters: the tool ran, and there was no
	// index in this repository for it to answer from. `graphify query` prints
	// "error: graph file not found"; `graft grep` prints "no graph — run
	// graft build first". Both mean the same thing, and both are fixed by
	// building the thing that is missing.
	FailNoGraph
	// FailNoTool is the binary not being on the agent's PATH at all.
	FailNoTool
	// FailDenied is the harness refusing the call — a permission classifier,
	// a hook, a denied rule. It says nothing about the index.
	FailDenied
	// FailTimeout is the call being killed for running too long. On a large
	// repository an extraction can legitimately outlive a Bash timeout, so it
	// is kept apart from the ordinary errors rather than read as breakage.
	FailTimeout
	// FailOther is an error the reader recognised as an error and nothing
	// more: a bad flag, a crash, a query that blew a budget.
	FailOther
)

func (f Fail) String() string {
	switch f {
	case FailNoGraph:
		return "no-graph"
	case FailNoTool:
		return "not-installed"
	case FailDenied:
		return "denied"
	case FailTimeout:
		return "timeout"
	case FailOther:
		return "error"
	}
	return "ok"
}

// Why is the one-line human form, in the terms the dashboard uses.
func (f Fail) Why() string {
	switch f {
	case FailNoGraph:
		return "no index here to answer from"
	case FailNoTool:
		return "the binary was not on the agent's PATH"
	case FailDenied:
		return "the harness refused the call"
	case FailTimeout:
		return "the call was killed on a timeout"
	case FailOther:
		return "the call returned an error"
	}
	return "succeeded"
}

// Fixable reports whether building an index is the answer. It is the test the
// UI uses to decide whether a failure gets a button.
func (f Fail) Fixable() bool { return f == FailNoGraph }

// Fails is every failing reason, in the order the UI shows them: the one a
// button fixes first.
var Fails = []Fail{FailNoGraph, FailOther, FailTimeout, FailNoTool, FailDenied}

// parseFail reads a reason back from its String form. An unknown spelling —
// a rollup written by a newer board — degrades to FailOther rather than being
// dropped, for the same reason splitCounter degrades rather than dropping.
func parseFail(s string) Fail {
	switch s {
	case "no-graph":
		return FailNoGraph
	case "not-installed":
		return FailNoTool
	case "denied":
		return FailDenied
	case "timeout":
		return FailTimeout
	case "ok":
		return FailNone
	}
	return FailOther
}

// failKey is how a failure is stored in a day bucket, mirroring counterKey:
// "graphify/no-graph/query".
func failKey(t Tool, f Fail, verb string) string {
	if verb == "" {
		verb = "other"
	}
	return t.String() + "/" + f.String() + "/" + verb
}

// splitFail is failKey's inverse.
func splitFail(s string) (Tool, Fail, string) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		return Graphify, FailOther, s
	}
	t := Graphify
	if parts[0] == "graft" {
		t = Graft
	}
	return t, parseFail(parts[1]), parts[2]
}

// The sentences the two tools print when the repository they were pointed at
// has no index, in two tiers.
//
// A whitelist of exact phrases rather than a search for the words "graph" and
// "not found", for the same reason parse.go whitelists subcommands: an agent's
// transcript is full of prose about missing graphs, and a reader that counted
// any of it would report a failure for every session that discussed one.
//
// The tiers exist because the harness's own error flag is not quite enough.
// `graphify query … | head` exits zero on head's behalf and still printed the
// error; `graft ask` exits zero by design when the index is empty. So the
// strict phrases — the ones a tool prints and prose does not — are believed on
// their own, and the looser ones only when the call also failed.
var strictNoGraphMarks = []string{
	"error: graph file not found",     // graphify query / explain / path
	"✗ no graph — run graft build",    // graft grep / callers / skeleton
	"✗ no graph - run graft build",    // the same line from a terminal that ate the dash
	"graft build` if graft/ is empty", // graft ask, which exits 0 with nothing to say
}

var noGraphMarks = []string{
	"graph file not found",
	"no graph — run graft build",
	"no graph - run graft build",
	"graphify-out/graph.json: no such file",
	"no such file or directory: graphify-out",
	"run `graphify extract`",
	"run graphify extract",
	"no graph found",
	"not indexed",
}

var deniedMarks = []string{
	"permission for this action was denied",
	"blocked by the",
	"operation not permitted",
}

var timeoutMarks = []string{
	"command timed out",
	"exit code 143",
	"context deadline exceeded",
	"signal: killed",
}

var noToolMarks = []string{
	"command not found",
	"executable file not found",
	"not recognized as an internal or external command",
}

// classifyFail reads one tool result and says why the call failed.
//
// isErr is the harness's own verdict, which is necessary but not sufficient:
// `graft ask` exits 0 when the index is empty and still answered nothing, and
// a Bash call that chained a graphify query onto three other commands can fail
// for any of them. So the text is consulted for the failures that name
// themselves, and the harness's flag is what catches the rest.
func classifyFail(text string, isErr bool) Fail {
	low := strings.ToLower(text)
	if containsAny(low, strictNoGraphMarks) {
		return FailNoGraph
	}
	if !isErr {
		return FailNone
	}
	if containsAny(low, noGraphMarks) {
		return FailNoGraph
	}
	switch {
	case containsAny(low, deniedMarks):
		return FailDenied
	case containsAny(low, timeoutMarks):
		return FailTimeout
	case containsAny(low, noToolMarks):
		return FailNoTool
	}
	return FailOther
}

func containsAny(low string, marks []string) bool {
	for _, m := range marks {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// failScan bounds how much of a result is read. A tool_result carries whole
// files and whole command outputs; every phrase this reader looks for is
// printed by the tool itself, which puts it at the top or — for a Bash call
// whose last command failed — at the very bottom. Reading both ends of a
// bounded window keeps a megabyte of output from being walked for a handful of
// substrings, and keeps the middle of it out of this process entirely.
const failScan = 4 << 10

// resultText decodes a tool_result's content to the bounded text classify
// reads. The content is either a string or the list of blocks a structured
// result is made of.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s, ok := decodeString(raw); ok {
		return clipEnds(s)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range blocks {
		if x.Text == "" {
			continue
		}
		b.WriteString(clipEnds(x.Text))
		b.WriteByte('\n')
		if b.Len() > 2*failScan {
			break
		}
	}
	return b.String()
}

// clipEnds keeps the head and the tail of a long string and drops the middle.
func clipEnds(s string) string {
	if len(s) <= 2*failScan {
		return s
	}
	return s[:failScan] + "\n…\n" + s[len(s)-failScan:]
}

// --- joining a call to its result -----------------------------------------

// pendingCall is a call whose result has not been seen yet.
//
// It has to survive between scans, not just within one: the assistant's
// tool_use line and the user's tool_result line are written seconds apart, and
// the live watch re-reads every two seconds, so a call and its answer land in
// different passes routinely. Without this the failures visible on the
// dashboard would be exactly the ones that happened while nobody was looking.
type pendingCall struct {
	Event Event     `json:"event"`
	Seen  time.Time `json:"seen"`
}

// pendingTTL is how long a call waits for its result. An agent that was
// interrupted, or a session that ended mid-call, leaves one behind forever;
// two hours is long enough for the slowest extraction anyone runs through a
// Bash call and short enough that the map cannot grow without bound.
const pendingTTL = 2 * time.Hour

// maxPending caps the map regardless of the clock. It also caps the per-line
// byte scan below, which is linear in it.
const maxPending = 256

// pendingSet is the in-flight calls, with the id list kept alongside the map
// so a transcript line can be tested against every outstanding id without
// rebuilding anything.
type pendingSet struct {
	byID  map[string]pendingCall
	order []string
	ids   [][]byte
}

func newPendingSet(prev map[string]pendingCall, since time.Time) *pendingSet {
	p := &pendingSet{byID: map[string]pendingCall{}}
	for id, c := range prev {
		if id == "" || c.Seen.Before(since) {
			continue
		}
		p.put(id, c)
	}
	return p
}

func (p *pendingSet) put(id string, c pendingCall) {
	if _, ok := p.byID[id]; !ok {
		p.order = append(p.order, id)
		p.ids = append(p.ids, []byte(id))
	}
	p.byID[id] = c
	for len(p.order) > maxPending {
		p.drop(p.order[0])
	}
}

// add records a call that has just been read out of a transcript.
func (p *pendingSet) add(e Event, now time.Time) {
	if e.ID == "" {
		return
	}
	p.put(e.ID, pendingCall{Event: e, Seen: now})
}

// take resolves a call: the result for it has arrived.
func (p *pendingSet) take(id string) (Event, bool) {
	c, ok := p.byID[id]
	if !ok {
		return Event{}, false
	}
	p.drop(id)
	return c.Event, true
}

func (p *pendingSet) drop(id string) {
	delete(p.byID, id)
	for i, s := range p.order {
		if s != id {
			continue
		}
		p.order = append(p.order[:i], p.order[i+1:]...)
		p.ids = append(p.ids[:i], p.ids[i+1:]...)
		return
	}
}

// match reports whether a raw transcript line carries the result of a call
// that is still outstanding.
//
// This is the filter that makes result lines affordable. A tool_result line is
// the biggest kind of line in the corpus — whole files, whole command outputs
// — and decoding every one of them would undo the byte-scan that makes this
// package cheap. An outstanding call's id is a twenty-odd byte string that
// appears in exactly one result line, so testing for it directly costs a
// memchr per pending call and decodes nothing that is not an answer.
func (p *pendingSet) match(line []byte) bool {
	if len(p.ids) == 0 || !bytes.Contains(line, needleToolResult) {
		return false
	}
	for _, id := range p.ids {
		if bytes.Contains(line, id) {
			return true
		}
	}
	return false
}

// snapshot is what gets persisted with the rollup.
func (p *pendingSet) snapshot() map[string]pendingCall {
	out := make(map[string]pendingCall, len(p.byID))
	for k, v := range p.byID {
		out[k] = v
	}
	return out
}

// failure is one resolved call that did not work: the call itself, and why.
type failure struct {
	Event  Event
	Reason Fail
}
