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
	"sync"
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
		Content content `json:"content"`
	} `json:"message"`

	Attachment *struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"attachment"`
}

// content is a message's content, decoded in place while the record is.
//
// It used to be a json.RawMessage decoded a second time, which copied the
// whole of it — on a tool_result line, megabytes — before a single block was
// looked at. Decoding it inside the record's own pass skips that copy, and the
// blocks it decodes into leave the result output out entirely (see block).
//
// The outcome is the one the two-step decode had: a string, a list of blocks,
// or neither — and neither is never an error for the record around it.
type content struct {
	set    bool
	str    string
	isStr  bool
	blocks []block
	ok     bool
}

func (c *content) UnmarshalJSON(b []byte) error {
	*c = content{set: len(b) > 0}
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &c.str); err == nil {
			c.isStr, c.ok = true, true
		}
		return nil
	}
	if err := json.Unmarshal(b, &c.blocks); err != nil {
		c.blocks = nil
		return nil
	}
	c.ok = true
	return nil
}

// block is one content block of an assistant or user message.
//
// The tool_use half carries the call; the tool_result half carries what came
// back for it, joined on ID. The result's output is deliberately not a field:
// it is read only for a result some call is still waiting on — see
// resultContent — and never retained.
type block struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	ID    string `json:"id"`
	Input struct {
		Command string `json:"command"`
		Skill   string `json:"skill"`
	} `json:"input"`

	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

// resultContent re-reads a line for the output of its i-th content block. It
// runs only for a tool_result that resolves an outstanding call, which is the
// one case the output is needed, so the copy it makes is paid once per answer
// instead of once per result line.
func resultContent(line []byte, i int) json.RawMessage {
	var rec struct {
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Message == nil {
		return nil
	}
	var blocks []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil || i >= len(blocks) {
		return nil
	}
	return blocks[i].Content
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

	// One pooled reader per pass, reset onto this file: the board re-reads
	// every live transcript every two seconds, and a fresh 256 KB buffer plus
	// a fresh slice per line was most of what this package allocated.
	lr := getLineReader(f)
	defer putLineReader(lr)
	var pending func(string) bool
	if pend != nil {
		pending = pend.has
	}
	pos := off
	n := 0
	// The first and last timestamped lines of THIS pass. The line returned by
	// the reader is only valid until the next read, so both are copied — into
	// buffers the pooled reader keeps, which costs a memmove and no garbage.
	haveFirst := false
	tools := 0
	for {
		line, err := lr.next()
		if len(line) > 0 && line[len(line)-1] != '\n' {
			// A partial final line: a session still being written. Leave the
			// offset before it so the next scan reads it whole.
			break
		}
		if len(line) > 0 {
			pos += int64(len(line))
			if n++; n%512 == 0 && ctx.Err() != nil {
				return pos, sessionOf(path, account, lr.first, lr.last, tools), ctx.Err()
			}
			tools += countToolUses(line)
			if bytes.Contains(line, needleTimestamp) {
				if !haveFirst {
					lr.first = append(lr.first[:0], line...)
					haveFirst = true
				}
				lr.last = append(lr.last[:0], line...)
			}
			// Two ways a line is worth decoding: it names one of the tools,
			// or it is the answer to a call that is still outstanding. The
			// second test exists because a failure rarely names the tool that
			// produced it — "Exit code 2" and a stack trace say nothing about
			// graphify — so the result of a call cannot be found by the same
			// byte scan that finds the call.
			if mentions(line) || (pend != nil && pend.match(line)) {
				parseLine(line, account, since, pending, emit, emitR)
			}
		}
		if err != nil {
			break
		}
	}
	return pos, sessionOf(path, account, lr.first, lr.last, tools), nil
}

// lineReader is a pooled bufio.Reader plus the scratch a long line needs.
//
// next hands out ReadSlice's view into the reader's buffer when the line fits,
// and only a line longer than the buffer is assembled — into scratch, which is
// reused for the next one. Either way the slice is valid until the next call,
// which is all parseLine needs: the decoder copies what it keeps.
type lineReader struct {
	r       *bufio.Reader
	scratch []byte
	first   []byte
	last    []byte
}

const (
	lineReaderSize = 256 << 10
	// maxPooledLine is the largest scratch a pooled reader keeps. A transcript
	// line can run to tens of megabytes; holding that much in a pool between
	// ticks would trade a transient spike for a permanent one.
	maxPooledLine = 4 << 20
)

var lineReaders = sync.Pool{New: func() any {
	return &lineReader{r: bufio.NewReaderSize(nil, lineReaderSize)}
}}

func getLineReader(rd io.Reader) *lineReader {
	lr := lineReaders.Get().(*lineReader)
	lr.r.Reset(rd)
	lr.first, lr.last = lr.first[:0], lr.last[:0]
	return lr
}

func putLineReader(lr *lineReader) {
	lr.r.Reset(nil)
	if cap(lr.scratch) > maxPooledLine {
		lr.scratch = nil
	}
	if cap(lr.first) > maxPooledLine {
		lr.first = nil
	}
	if cap(lr.last) > maxPooledLine {
		lr.last = nil
	}
	lineReaders.Put(lr)
}

// next returns the next line including its '\n', with ReadBytes' contract:
// err is non-nil exactly when the line does not end in the delimiter.
func (lr *lineReader) next() ([]byte, error) {
	line, err := lr.r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	lr.scratch = append(lr.scratch[:0], line...)
	for {
		line, err = lr.r.ReadSlice('\n')
		lr.scratch = append(lr.scratch, line...)
		if err != bufio.ErrBufferFull {
			return lr.scratch, err
		}
	}
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

// stamp is the part of a record sessionOf reads. message and attachment are
// still declared, with the same shapes as in record, so a line whose message
// is not an object is rejected exactly as decoding a whole record would — but
// their content is skipped, not copied: the line that carries the timestamp is
// usually the biggest one in the pass.
type stamp struct {
	Timestamp time.Time `json:"timestamp"`
	Cwd       string    `json:"cwd"`
	GitBranch string    `json:"gitBranch"`
	SessionID string    `json:"sessionId"`

	Message    *struct{} `json:"message"`
	Attachment *struct {
		Type string `json:"type"`
	} `json:"attachment"`
}

func decodeRecord(line []byte) (stamp, bool) {
	var rec stamp
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

	// The three places an Event can come from, as bytes: a tool call, a hook's
	// injected context, a slash command's preamble. "command-name" is matched
	// without its angle brackets so an encoder that escapes them still passes.
	needleCall    = []byte(`"tool_use"`)
	needleHook    = []byte(hookAttachment)
	needleCommand = []byte("command-name")
)

// mentions is the prefilter: could parseLine emit an Event for this line?
//
// Naming a tool is necessary but, on its own, loose — measured on a real
// corpus it let through 28 % of lines and 42 % of bytes, almost all of them
// tool_result lines whose output merely quotes the word (a file read, a grep).
// parseLine can only emit from a tool_use block, a hook attachment or a slash
// command, so a line carrying none of those markers is rejected here too. A
// result that answers an outstanding call is not this filter's job: it comes
// through pendingSet.match.
func mentions(line []byte) bool {
	if !bytes.Contains(line, needleGraphify) && !bytes.Contains(line, needleGraft) {
		return false
	}
	return bytes.Contains(line, needleCall) || bytes.Contains(line, needleHook) || bytes.Contains(line, needleCommand)
}

// parseLine turns one transcript record into zero or more events, and
// resolves the results of calls this scan — or an earlier one — is still
// waiting on.
//
// pending, when set, says whether a tool_use id is still waiting on its
// result; a result for any other id is skipped before its output is read.
// Nil means every result goes to emitR.
func parseLine(line []byte, account string, since time.Time, pending func(string) bool, emit func(Event), emitR func(string, Fail)) {
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
	if rec.Message == nil || !rec.Message.Content.set {
		return
	}

	// Content is either a plain string — a user turn, where the only thing
	// worth counting is a slash command — or the list of blocks an assistant
	// turn is made of.
	c := &rec.Message.Content
	if c.isStr {
		if t, verb, ok := slashCommand(c.str); ok {
			e := base
			e.Tool, e.Kind, e.Verb = t, Skill, verb
			emit(e)
		}
		return
	}
	if !c.ok {
		return
	}
	for i, b := range c.blocks {
		if b.Type == "tool_result" {
			// A result for a call this scan is not tracking is not an error:
			// it is every other tool the agent ran. match() already made that
			// rare, and take() is what makes it free. Asking pending first is
			// what keeps it free: classifying means unescaping the output, and
			// for a result nobody is waiting on that work was thrown away.
			if emitR != nil && b.ToolUseID != "" {
				if pending != nil && !pending(b.ToolUseID) {
					continue
				}
				emitR(b.ToolUseID, classifyFail(resultText(resultContent(line, i)), b.IsError))
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
