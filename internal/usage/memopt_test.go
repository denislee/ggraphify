package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// reported is one emitR call, captured so a test can assert both which call a
// result resolved and how it was classified.
type reported struct {
	id string
	f  Fail
}

func TestMentionsPrefilter(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "bash tool_use naming graft",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cd x && graft ask foo"}}]}}`,
			want: true,
		},
		{
			name: "mcp graft tool_use",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__graft__graft_find_code","input":{}}]}}`,
			want: true,
		},
		{
			name: "mcp graphify tool_use",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__graphify__query","input":{}}]}}`,
			want: true,
		},
		{
			name: "hook attachment",
			line: `{"type":"attachment","attachment":{"type":"hook_additional_context","content":"[graft] hi"}}`,
			want: true,
		},
		{
			name: "slash command preamble",
			line: `{"type":"user","message":{"content":"<command-name>/graphify</command-name>"}}`,
			want: true,
		},
		{
			name: "escaped slash command preamble",
			line: `{"type":"user","message":{"content":"\u003ccommand-name\u003e/graphify\u003c/command-name\u003e"}}`,
			want: true,
		},
		{
			name: "tool_result merely quoting the tools",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file mentions graft and graphify"}]}}`,
			want: false,
		},
		{
			name: "tool_use with no tool name",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{}}]}}`,
			want: false,
		},
		{
			name: "empty line",
			line: ``,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mentions([]byte(tt.line)); got != tt.want {
				t.Fatalf("mentions(%s) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseLineAsksPendingFirst(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	mustJSON := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	strLine := mustJSON(resultRec("/r", "s", "toolu_X", at, true, "Exit code 1"))
	blockLine := mustJSON(map[string]any{
		"type": "user", "timestamp": at.Format(time.RFC3339Nano),
		"cwd": "/r", "sessionId": "s",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_X",
				"is_error": true, "content": []any{
					map[string]any{"type": "text", "text": "Exit code 1"},
				}},
		}},
	})
	softLine := mustJSON(resultRec("/r", "s", "toolu_X", at, false, "No graph found"))
	wantSoft := classifyFail("No graph found", false)

	tests := []struct {
		name    string
		line    []byte
		pending func(string) bool
		want    []reported
	}{
		{
			name: "nil pending asks about every result",
			line: strLine,
			want: []reported{{"toolu_X", FailOther}},
		},
		{
			name:    "pending true resolves the call",
			line:    strLine,
			pending: func(id string) bool { return id == "toolu_X" },
			want:    []reported{{"toolu_X", FailOther}},
		},
		{
			name:    "pending false skips before reading the output",
			line:    strLine,
			pending: func(string) bool { return false },
			want:    nil,
		},
		{
			name:    "block content is read",
			line:    blockLine,
			pending: func(string) bool { return true },
			want:    []reported{{"toolu_X", FailOther}},
		},
		{
			name:    "success flag with a loose phrase classifies as the reader says",
			line:    softLine,
			pending: func(string) bool { return true },
			want:    []reported{{"toolu_X", wantSoft}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []reported
			emit := func(Event) {}
			emitR := func(id string, f Fail) { got = append(got, reported{id, f}) }
			parseLine(tt.line, "acct", time.Time{}, tt.pending, emit, emitR)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("emitR calls = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestListingCacheSeesNewTranscripts(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "s1", bashRec(repo, "s1", now, `graft ask "x"`))

	projDir := filepath.Join(acct, "projects", "-tmp-repo")
	projectsDir := filepath.Join(acct, "projects")
	old := now.Add(-time.Hour)
	if err := os.Chtimes(projDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(projectsDir, old, old); err != nil {
		t.Fatal(err)
	}

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).ByTool[Graft]; got != 1 {
		t.Fatalf("after first update graft = %d, want 1", got)
	}

	// A second transcript lands in the same project directory, which moves
	// that directory's mtime — the cached listing must notice.
	writeTranscript(t, acct, "s2", bashRec(repo, "s2", now, `graft ask "y"`))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).ByTool[Graft]; got != 2 {
		t.Fatalf("after second update graft = %d, want 2", got)
	}
	if x.Scanned != 1 {
		t.Fatalf("second update scanned %d transcripts, want 1", x.Scanned)
	}
}

func TestGraftSessionDirSkippedWhenUnchanged(t *testing.T) {
	repo := t.TempDir()
	now := time.Now()
	old := now.Add(-time.Hour)

	writeGraftSession(t, repo, "a", graftSession{GraftReads: 1}, old)
	if err := os.Chtimes(GraftSessionDir(repo), old, old); err != nil {
		t.Fatal(err)
	}

	x := New()
	opts := Options{Repos: []string{repo}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(x.GraftFiles) != 1 {
		t.Fatalf("after first update graft files = %d, want 1", len(x.GraftFiles))
	}

	// Writing a second session moves the directory's mtime, so the directory
	// must be read again — and both sessions must survive.
	writeGraftSession(t, repo, "b", graftSession{GraftReads: 2}, now)
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(x.GraftFiles) != 2 {
		t.Fatalf("after second update graft files = %d, want 2 (%v)", len(x.GraftFiles), x.GraftFiles)
	}
}

func TestSaveSkipsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "s1", bashRec(repo, "s1", now, `graft ask "x"`))

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "usage.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mod := fi.ModTime()

	// Nothing changed, so the second Update must not dirty the rollup and the
	// second Save must not touch the file.
	time.Sleep(20 * time.Millisecond)
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mod) {
		t.Fatalf("unchanged rollup was rewritten: mtime %v -> %v", mod, fi.ModTime())
	}

	// Removing the file re-arms Save even though the rollup has not moved.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Save did not recreate the removed file: %v", err)
	}

	y := Load(path)
	xb, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	yb, err := json.Marshal(y)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(xb, yb) {
		t.Fatalf("reloaded rollup differs from the source:\n x=%s\n y=%s", xb, yb)
	}
}
