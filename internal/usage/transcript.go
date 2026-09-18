package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileState is what Index remembers about one transcript between runs: enough
// to tell "unchanged" from "appended to" with a single stat, and to resume
// parsing where the last run stopped.
//
// Offset is always the end of the last COMPLETE line consumed. Claude Code
// appends to an open transcript while a session runs, so a scan that arrives
// mid-write sees a truncated final line; stopping short of it means the next
// scan reads that line whole rather than dropping it.
type FileState struct {
	Size   int64 `json:"size"`
	ModNS  int64 `json:"mod_ns"`
	Offset int64 `json:"offset"`
	// Tools is how many tool calls this file has contributed to its session's
	// denominator so far. It is kept per FILE rather than only in the session
	// roll so that a transcript re-read from the start — rotated, rewritten —
	// can be folded as a correction instead of counted twice. A doubled
	// denominator is not a slightly-off number; it halves every share derived
	// from it.
	Tools int `json:"tools,omitempty"`
}

// hookAttachment is the attachment type Claude Code uses for whatever a hook
// printed into the session. It is how both integrations announce themselves:
// graft's pointer block and graphify's PreToolUse reminder arrive this way.
const hookAttachment = "hook_additional_context"

// record is the narrow view this package takes of a transcript line. A
// transcript line carries the whole turn — prompts, file contents, output —
// and every field not named here is skipped by the decoder and never retained.
type record struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Cwd       string    `json:"cwd"`
	GitBranch string    `json:"gitBranch"`
	SessionID string    `json:"sessionId"`

	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`

	Attachment *struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"attachment"`
}

// block is one content block of an assistant or user message.
//
// The tool_use half carries the call; the tool_result half carries what came
// back for it, joined on ID. Content is the result's output, which is read
// only far enough to classify a failure (see failure.go) and never retained.
type block struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	ID    string `json:"id"`
	Input struct {
		Command string `json:"command"`
		Skill   string `json:"skill"`
	} `json:"input"`

	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// sessionScan is what one pass over one transcript learnt about the session
// the file belongs to, whether or not either tool was ever mentioned in it: the
// metadata to label it by, and how many tool calls it made in total.
//
// It is emitted once per pass, not once per line, and it is the half that makes
// the session list an adoption view — a sitting that used neither tool produces
// no Event at all and would otherwise be invisible.
type sessionScan struct {
	Session string
	Account string
	Repo    string
	Branch  string
	Start   time.Time
	Last    time.Time
	Tools   int
}

// scanTranscript parses one transcript from off, appending what it finds to
// events, and returns the offset to resume from next time.
//
// Lines that mention neither tool are rejected on a byte scan before the JSON
// decoder is involved. That test is what makes the corpus affordable: on a
// working machine fewer than one line in two hundred survives it, and the
// decoder — by far the expensive part — runs only on those.
//
// The session-level pass obeys the same budget. It counts tool calls with a
// byte scan, and decodes exactly two lines per pass — the first and last that
// carry a timestamp — to learn where and when the session ran. Nothing here
// widens what the decoder sees.
//
// The returned sessionScan describes THIS pass only: its Tools is what these
// lines held, not the file's total, so the caller can fold it as a delta.
func scanTranscript(ctx context.Context, path, account string, off int64, since time.Time, pend *pendingSet, emit func(Event), emitR func(string, Fail)) (int64, sessionScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return off, sessionScan{}, err
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return 0, sessionScan{}, err
		}
	}

	r := bufio.NewReaderSize(f, 256<<10)
	pos := off
	n := 0
	// The first and last timestamped lines of THIS pass. bufio.ReadBytes
	// returns a fresh slice per call, so holding on to two of them costs
	// nothing beyond the two lines themselves.
	var firstTS, lastTS []byte
	tools := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] != '\n' {
			// A partial final line: a session still being written. Leave the
			// offset before it so the next scan reads it whole.
			break
		}
		if len(line) > 0 {
			pos += int64(len(line))
			if n++; n%512 == 0 && ctx.Err() != nil {
				return pos, sessionOf(path, account, firstTS, lastTS, tools), ctx.Err()
			}
			tools += countToolUses(line)
			if bytes.Contains(line, needleTimestamp) {
				if firstTS == nil {
					firstTS = line
				}
				lastTS = line
			}
			// Two ways a line is worth decoding: it names one of the tools,
			// or it is the answer to a call that is still outstanding. The
			// second test exists because a failure rarely names the tool that
			// produced it — "Exit code 2" and a stack trace say nothing about
			// graphify — so the result of a call cannot be found by the same
			// byte scan that finds the call.
			if mentions(line) || (pend != nil && pend.match(line)) {
				parseLine(line, account, since, emit, emitR)
			}
		}
		if err != nil {
			break
		}
	}
	return pos, sessionOf(path, account, firstTS, lastTS, tools), nil
}

// sessionOf assembles the pass's session record.
//
// The session id comes from the record when one could be decoded and from the
// file name otherwise — Claude Code names each transcript after the session it
// holds. The record is preferred because it is the key every Event carries, and
// the two halves only join if they agree.
func sessionOf(path, account string, firstTS, lastTS []byte, tools int) sessionScan {
	sc := sessionScan{
		Session: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Account: account,
		Tools:   tools,
	}
	if first, ok := decodeRecord(firstTS); ok {
		sc.Start = first.Timestamp
		if first.SessionID != "" {
			sc.Session = first.SessionID
		}
	}
	last, ok := decodeRecord(lastTS)
	if !ok {
		return sc
	}
	sc.Last = last.Timestamp
	if last.SessionID != "" {
		sc.Session = last.SessionID
	}
	if last.Cwd != "" {
		sc.Repo = filepath.Clean(last.Cwd)
	}
	sc.Branch = last.GitBranch
	return sc
}

func decodeRecord(line []byte) (record, bool) {
	var rec record
	if len(line) == 0 {
		return rec, false
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Timestamp.IsZero() {
		return rec, false
	}
	return rec, true
}

// countToolUses counts the tool calls on one line without decoding it.
//
// It is a byte count of the block marker, which makes it an approximation in
// exactly one direction: a marker quoted inside a tool's own output is escaped
// in the JSON and does not match, so the count can fall short of a decoded one
// and never runs ahead of it by much. That is the right trade for a denominator
// — it is read on EVERY line of a corpus measured in gigabytes, and decoding
// them all to make it exact would cost more than everything else this package
// does put together. SessionRoll.Share clamps for the residual.
func countToolUses(line []byte) int { return bytes.Count(line, needleToolUse) }

var (
	needleGraphify   = []byte("graphify")
	needleGraft      = []byte("graft")
	needleToolResult = []byte(`"tool_result"`)
	needleToolUse    = []byte(`"type":"tool_use"`)
	needleTimestamp  = []byte(`"timestamp":"`)
)

func mentions(line []byte) bool {
	return bytes.Contains(line, needleGraphify) || bytes.Contains(line, needleGraft)
}

// parseLine turns one transcript record into zero or more events, and
// resolves the results of calls this scan — or an earlier one — is still
// waiting on.
func parseLine(line []byte, account string, since time.Time, emit func(Event), emitR func(string, Fail)) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}
	if rec.Timestamp.IsZero() || rec.Timestamp.Before(since) {
		return
	}
	base := Event{
		At:      rec.Timestamp,
		Repo:    filepath.Clean(rec.Cwd),
		Branch:  rec.GitBranch,
		Account: account,
		Session: rec.SessionID,
	}
	if rec.Cwd == "" {
		base.Repo = ""
	}

	if rec.Attachment != nil && rec.Attachment.Type == hookAttachment {
		if t, verb, ok := hookEvent(rec.Attachment.Content); ok {
			e := base
			e.Tool, e.Kind, e.Verb = t, Hook, verb
			emit(e)
		}
	}
	if rec.Message == nil || len(rec.Message.Content) == 0 {
		return
	}

	// Content is either a plain string — a user turn, where the only thing
	// worth counting is a slash command — or the list of blocks an assistant
	// turn is made of.
	if s, ok := decodeString(rec.Message.Content); ok {
		if t, verb, ok := slashCommand(s); ok {
			e := base
			e.Tool, e.Kind, e.Verb = t, Skill, verb
			emit(e)
		}
		return
	}
	var blocks []block
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			// A result for a call this scan is not tracking is not an error:
			// it is every other tool the agent ran. match() already made that
			// rare, and take() is what makes it free.
			if emitR != nil && b.ToolUseID != "" {
				emitR(b.ToolUseID, classifyFail(resultText(b.Content), b.IsError))
			}
			continue
		}
		if b.Type != "tool_use" {
			continue
		}
		switch {
		case b.Name == "Bash":
			for _, c := range Commands(b.Input.Command) {
				e := base
				// One Bash call can hold two invocations and comes back with
				// exactly one result, so the id goes to the first of them. The
				// alternative — every call in the line sharing the id — would
				// report one failing command line as two failures.
				e.Tool, e.Kind, e.Verb, e.ID = c.Tool, CLI, c.Verb, b.ID
				emit(e)
				b.ID = ""
			}
		case b.Name == "Skill":
			if t, ok := toolNamed(b.Input.Skill); ok {
				e := base
				e.Tool, e.Kind, e.Verb, e.ID = t, Skill, "/"+b.Input.Skill, b.ID
				emit(e)
			}
		case strings.HasPrefix(b.Name, "mcp__"):
			if t, verb, ok := mcpVerb(b.Name); ok {
				e := base
				e.Tool, e.Kind, e.Verb, e.ID = t, MCP, verb, b.ID
				emit(e)
			}
		}
	}
}

// decodeString reports whether a content field is the plain-string form.
func decodeString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// slashCommand recognises a user turn that invoked one of the two skills.
// Claude Code records it as a small XML-ish preamble ahead of the prompt.
func slashCommand(s string) (Tool, string, bool) {
	const open, close = "<command-name>", "</command-name>"
	i := strings.Index(s, open)
	if i < 0 {
		return 0, "", false
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return 0, "", false
	}
	name := strings.TrimPrefix(strings.TrimSpace(rest[:j]), "/")
	t, ok := toolNamed(name)
	if !ok {
		return 0, "", false
	}
	return t, "/" + name, true
}

// hookEvent classifies a hook's injected context.
//
// graft prefixes everything it injects with "[graft]", which makes it exact.
// graphify's PreToolUse reminder has no such marker, so the fallback is the
// name — inside a hook attachment, the word is not prose.
func hookEvent(raw json.RawMessage) (Tool, string, bool) {
	s, ok := decodeString(raw)
	if !ok {
		// Some records carry the hook's output as a list of strings.
		var parts []string
		if err := json.Unmarshal(raw, &parts); err != nil {
			return 0, "", false
		}
		s = strings.Join(parts, "\n")
	}
	var t Tool
	switch {
	case strings.Contains(s, "[graft]"):
		t = Graft
	case strings.Contains(s, "graphify"):
		t = Graphify
	default:
		return 0, "", false
	}
	return t, hookVerb(s), true
}

// hookVerb names which hook fired, when the text says so. The board shows it
// because "the session-start pointer fired" and "the pre-tool reminder fired
// on every Bash call" are very different amounts of nagging.
func hookVerb(s string) string {
	switch {
	case strings.Contains(s, "PreToolUse"):
		return "pre-tool"
	case strings.Contains(s, "UserPromptSubmit"):
		return "prompt"
	case strings.Contains(s, "SessionStart"):
		return "session-start"
	}
	return "context"
}

// transcripts lists the transcript files under one account directory.
//
// The layout is projects/<slugified-cwd>/<session-id>.jsonl, with sidecar
// directories of the same name beside the files; only the .jsonl files are
// returned, and the slug is not decoded — the cwd inside each record is the
// authoritative one, and the slug is lossy about separators.
func transcripts(dir string) ([]string, error) {
	root := filepath.Join(dir, "projects")
	projects, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, p.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			out = append(out, filepath.Join(root, p.Name(), f.Name()))
		}
	}
	return out, nil
}
