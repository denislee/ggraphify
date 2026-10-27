package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeTranscript appends JSONL records to one session's transcript inside a
// fake account directory, in the layout Claude Code uses.
func writeTranscript(t *testing.T, acct, session string, recs ...map[string]any) string {
	t.Helper()
	dir := filepath.Join(acct, "projects", "-tmp-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func bashRec(repo, session string, at time.Time, cmd string) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": at.Format(time.RFC3339Nano),
		"cwd": repo, "gitBranch": "main", "sessionId": session,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": "Bash",
				"input": map[string]any{"command": cmd}},
		}},
	}
}

func hookRec(repo, session string, at time.Time, text string) map[string]any {
	return map[string]any{
		"type": "attachment", "timestamp": at.Format(time.RFC3339Nano),
		"cwd": repo, "sessionId": session,
		"attachment": map[string]any{"type": hookAttachment, "content": text},
	}
}

func TestUpdateCountsAndIsIncremental(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "sess-1",
		bashRec(repo, "sess-1", now.Add(-2*time.Hour), `graphify query "how does X work"`),
		bashRec(repo, "sess-1", now.Add(-time.Hour), `cd `+repo+` && graft ask "y" --source`),
		hookRec(repo, "sess-1", now.Add(-time.Hour), "[graft] SessionStart: this repo is indexed"),
		// Prose in a heredoc, which must not be counted.
		bashRec(repo, "sess-1", now, "cat > d.md <<'EOF'\ngraphify query \"not a call\"\nEOF"),
	)

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s := x.Summarize(Window{Days: 7, Now: now})
	if s.Events != 2 {
		t.Fatalf("events = %d, want 2 (%+v)", s.Events, s.ByTool)
	}
	if s.Hooks != 1 {
		t.Fatalf("hooks = %d, want 1", s.Hooks)
	}
	if s.ByTool[Graphify] != 1 || s.ByTool[Graft] != 1 {
		t.Fatalf("by tool = %v, want one call each; hooks belong in HookByTool", s.ByTool)
	}
	if s.HookByTool[Graft] != 1 || s.HookByTool[Graphify] != 0 {
		t.Fatalf("hooks by tool = %v, want the one graft injection", s.HookByTool)
	}
	if s.Sessions != 1 {
		t.Fatalf("sessions = %d, want 1", s.Sessions)
	}

	// A second update with nothing appended must open no file at all and
	// must not double count.
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, scanned := x.LastUpdate(); scanned != 0 {
		t.Fatalf("second update opened %d transcripts, want 0", scanned)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 2 {
		t.Fatalf("events after no-op update = %d, want 2", got)
	}

	// An appended line is picked up, and only it.
	writeTranscript(t, acct, "sess-1", bashRec(repo, "sess-1", now, `graphify update .`))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s = x.Summarize(Window{Days: 7, Now: now})
	if s.Events != 3 || s.ByTool[Graphify] != 2 {
		t.Fatalf("events = %d, graphify = %d, want 3 and 2", s.Events, s.ByTool[Graphify])
	}
}

// A transcript being written while the scan runs ends in a partial line. It
// must be left for the next scan rather than dropped.
func TestUpdateResumesAfterAPartialLine(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	path := writeTranscript(t, acct, "sess-1", bashRec(repo, "sess-1", now, `graft map`))
	rec, err := json.Marshal(bashRec(repo, "sess-1", now, `graphify query "x"`))
	if err != nil {
		t.Fatal(err)
	}
	half := rec[:len(rec)/2]
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(half)
	f.Close()

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 1 {
		t.Fatalf("events = %d, want 1: the partial line must not be parsed", got)
	}

	f, _ = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.Write(append(rec[len(rec)/2:], '\n'))
	f.Close()
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 2 {
		t.Fatalf("events = %d, want 2: the completed line must be picked up", got)
	}
}

func writeGraftSession(t *testing.T, repo, session string, s graftSession, mod time.Time) {
	t.Helper()
	dir := GraftSessionDir(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, session+".json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestGraftCountersAreReadAsDeltas(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)

	writeGraftSession(t, repo, "sess-1", graftSession{
		GraftReads: 6, SourceReads: 2, SavedTokens: 10_000,
		InputCostMicros: 1_500_000, InputTokensBilled: 900, Nudges: 1,
	}, yesterday)

	x := New()
	opts := Options{Repos: []string{repo}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s := x.Summarize(Window{Days: 7, Now: now})
	if s.GraftReads != 6 || s.SourceReads != 2 || s.SavedTokens != 10_000 {
		t.Fatalf("first read = %+v", s)
	}
	if mix, ok := s.Mix(); !ok || mix != 75 {
		t.Fatalf("mix = %d %v, want 75%%", mix, ok)
	}
	if got := s.CostUSD(); got != 1.5 {
		t.Fatalf("cost = %v, want 1.5", got)
	}

	// The counter grows; only the movement is added, and it lands on today.
	writeGraftSession(t, repo, "sess-1", graftSession{
		GraftReads: 10, SourceReads: 2, SavedTokens: 25_000,
		InputCostMicros: 2_000_000, InputTokensBilled: 1_200, Nudges: 1,
	}, now)
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s = x.Summarize(Window{Days: 7, Now: now})
	if s.GraftReads != 10 || s.SavedTokens != 25_000 {
		t.Fatalf("after growth = %+v, want the totals not double counted", s)
	}
	today := x.Summarize(Window{Days: 1, Now: now})
	if today.GraftReads != 4 || today.SavedTokens != 15_000 {
		t.Fatalf("today = %d reads / %d saved, want the delta 4 / 15000", today.GraftReads, today.SavedTokens)
	}

	// A counter that went backwards is a reset, not a negative delta.
	writeGraftSession(t, repo, "sess-1", graftSession{GraftReads: 1}, now)
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 1, Now: now}).GraftReads; got != 5 {
		t.Fatalf("after reset today = %d, want 5 (4 + the whole new value)", got)
	}
}

func TestRepoUsesAndRetention(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	nested := filepath.Join(repo, "internal", "ui")
	other := filepath.Join(dir, "other")
	now := time.Now()

	writeTranscript(t, acct, "sess-1",
		bashRec(nested, "sess-1", now, `graft callers Scan`),
		bashRec(other, "sess-2", now.AddDate(0, 0, -3), `graphify query "x"`),
		// Well outside the retention window: recorded by nothing.
		bashRec(repo, "sess-3", now.AddDate(0, 0, -200), `graphify label .`),
	)

	x := New()
	if err := x.Update(context.Background(), Options{
		Accounts: []Account{{Name: "work", Dir: acct}}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	uses := x.RepoUses([]string{repo, other}, Window{Days: 7, Now: now})
	if u := uses[repo]; u.Graft != 1 || u.Total() != 1 {
		t.Fatalf("nested cwd was not credited to its checkout: %+v", u)
	}
	if u := uses[other]; u.Graphify != 1 {
		t.Fatalf("other = %+v, want one graphify call", u)
	}
	if len(uses[repo].Series) != 7 || uses[repo].Series[6] != 1 {
		t.Fatalf("series = %v, want today's column to carry the call", uses[repo].Series)
	}
	if got := x.Summarize(Window{Days: 365, Now: now}).Events; got != 2 {
		t.Fatalf("events over a year = %d, want 2: the 200-day-old call is past retention", got)
	}

	acctCounts := x.Summarize(Window{Days: 7, Now: now}).Accounts
	if len(acctCounts) != 1 || acctCounts[0].Name != "work" || acctCounts[0].Count != 2 {
		t.Fatalf("accounts = %v, want work=2", acctCounts)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	writeTranscript(t, acct, "sess-1", bashRec(repo, "sess-1", now, `graphify query "x"`))

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	path := DefaultPath(dir)
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}

	y := Load(path)
	if got := y.Summarize(Window{Days: 7, Now: now}).Events; got != 1 {
		t.Fatalf("reloaded events = %d, want 1", got)
	}
	// The reloaded offsets mean a fresh process does not re-read the corpus.
	if err := y.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, scanned := y.LastUpdate(); scanned != 0 {
		t.Fatalf("reloaded index re-opened %d transcripts, want 0", scanned)
	}
	if got := y.Summarize(Window{Days: 7, Now: now}).Events; got != 1 {
		t.Fatalf("events after reload+update = %d, want 1", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stderr, "rollup at", path)
}

func TestRecentsExcludeHooksAndNarrowToRepo(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	other := filepath.Join(dir, "other")
	now := time.Now()
	writeTranscript(t, acct, "sess-1",
		hookRec(repo, "sess-1", now, "[graft] pointers"),
		bashRec(repo, "sess-1", now, `graft map`),
		bashRec(other, "sess-1", now, `graphify query "x"`),
	)
	x := New()
	if err := x.Update(context.Background(), Options{
		Accounts: []Account{{Name: "default", Dir: acct}}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if got := x.Recents("", 10); len(got) != 2 {
		t.Fatalf("recents = %v, want 2 (the hook excluded)", got)
	}
	got := x.Recents(repo, 10)
	if len(got) != 1 || got[0].Verb != "map" {
		t.Fatalf("recents for repo = %v, want just the graft map", got)
	}
}

// bashCallRec is bashRec with a tool_use id, so the call can be joined to the
// result that answers it.
func bashCallRec(repo, session, id string, at time.Time, cmd string) map[string]any {
	r := bashRec(repo, session, at, cmd)
	blocks := r["message"].(map[string]any)["content"].([]any)
	blocks[0].(map[string]any)["id"] = id
	return r
}

// resultRec is the user turn carrying one tool_result.
func resultRec(repo, session, id string, at time.Time, isErr bool, text string) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": at.Format(time.RFC3339Nano),
		"cwd": repo, "sessionId": session,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id,
				"is_error": isErr, "content": text},
		}},
	}
}

func TestUpdateJoinsFailedCallsToTheirResults(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "sess-f",
		bashCallRec(repo, "sess-f", "toolu_1", now.Add(-3*time.Hour), `graphify query "how does X work"`),
		resultRec(repo, "sess-f", "toolu_1", now.Add(-3*time.Hour), true,
			"error: graph file not found: "+repo+"/graphify-out/graph.json"),
		bashCallRec(repo, "sess-f", "toolu_2", now.Add(-2*time.Hour), `graft grep "Foo"`),
		resultRec(repo, "sess-f", "toolu_2", now.Add(-2*time.Hour), true,
			"✗ no graph — run graft build first"),
		bashCallRec(repo, "sess-f", "toolu_3", now.Add(-time.Hour), `graphify explain "Y"`),
		resultRec(repo, "sess-f", "toolu_3", now.Add(-time.Hour), false, "Y is the thing that does Z"),
	)

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s := x.Summarize(Window{Days: 7, Now: now})

	if s.Events != 3 {
		t.Fatalf("events = %d, want 3 — a failed call is still a call", s.Events)
	}
	if s.Fails != 2 {
		t.Fatalf("fails = %d, want 2 (%+v)", s.Fails, s.FailByReason)
	}
	if n := s.FailByReason[FailNoGraph]; n != 2 {
		t.Fatalf("no-graph failures = %d, want 2 — both tools say it in their own words", n)
	}
	if s.FailByTool[Graphify] != 1 || s.FailByTool[Graft] != 1 {
		t.Fatalf("by tool = %+v, want one each", s.FailByTool)
	}
	f := s.FailSplit[repo]
	if f.Total != 2 || f.NoGraph != 2 || f.Top() != FailNoGraph {
		t.Fatalf("repo fail = %+v, want 2 failures, both for want of an index", f)
	}
	// The successful call must have resolved and left nothing behind, and the
	// failures must not be waiting for a second answer either.
	if len(x.Pending) != 0 {
		t.Fatalf("pending = %+v, want every call resolved", x.Pending)
	}
	// The activity list carries the verdict, so "Latest" can mark the calls
	// that came back with nothing.
	failed := 0
	for _, e := range x.Recents("", 10) {
		if e.Failed() {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("failed events in Recents = %d, want 2", failed)
	}
}

func TestResultInALaterScanStillLandsOnTheCall(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}

	// The call, and nothing else: the shape the live watch reads while an
	// agent is still waiting on the command.
	writeTranscript(t, acct, "sess-l",
		bashCallRec(repo, "sess-l", "toolu_9", now.Add(-time.Hour), `graphify query "x"`))
	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if s := x.Summarize(Window{Days: 7, Now: now}); s.Fails != 0 {
		t.Fatalf("fails = %d before the result arrived, want 0", s.Fails)
	}
	if len(x.Pending) != 1 {
		t.Fatalf("pending = %d, want the one call still waiting", len(x.Pending))
	}

	// The answer, appended two seconds later — a different scan, and a line
	// that does not name either tool.
	writeTranscript(t, acct, "sess-l",
		resultRec(repo, "sess-l", "toolu_9", now.Add(-time.Hour), true, "Exit code 1\nerror: graph file not found"))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s := x.Summarize(Window{Days: 7, Now: now})
	if s.Fails != 1 || s.FailByReason[FailNoGraph] != 1 {
		t.Fatalf("fails = %d (%+v), want the failure to land on the earlier call", s.Fails, s.FailByReason)
	}
	if s.Events != 1 {
		t.Fatalf("events = %d, want 1 — the call must not be counted twice", s.Events)
	}
}

func TestPendingSurvivesASaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	x := New()
	x.Pending["toolu_x"] = pendingCall{
		Event: Event{Tool: Graphify, Verb: "query", Repo: "/tmp/repo", ID: "toolu_x", At: time.Now()},
		Seen:  time.Now(),
	}
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if c, ok := got.Pending["toolu_x"]; !ok || c.Event.Verb != "query" {
		t.Fatalf("pending after reload = %+v, want the call preserved", got.Pending)
	}
}

// rewriteTranscript replaces a transcript's contents wholesale, the way a
// rotation or a rewrite does, rather than appending to it.
func rewriteTranscript(t *testing.T, path string, recs ...map[string]any) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	acct := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	writeTranscript(t, acct, strings.TrimSuffix(filepath.Base(path), ".jsonl"), recs...)
}

// W19: a transcript that shrinks — rotated, or rewritten without some of its
// lines — is re-read from the start, and every event it still carries used to
// be counted a second time.
func TestUpdateDoesNotDoubleCountAShrunkTranscript(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	a := bashRec(repo, "sess-1", now.Add(-3*time.Hour), `graphify query "a"`)
	b := bashRec(repo, "sess-1", now.Add(-2*time.Hour), `graphify query "b"`)
	c := bashRec(repo, "sess-1", now.Add(-1*time.Hour), `graphify query "c"`)
	path := writeTranscript(t, acct, "sess-1", a, b, c)

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 3 {
		t.Fatalf("setup: events = %d, want 3", got)
	}

	rewriteTranscript(t, path, b, c) // shorter: the old offset is past the end
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 3 {
		t.Fatalf("events after shrink = %d, want 3 — b and c were counted again", got)
	}

	// What is genuinely new after the rewrite still counts.
	writeTranscript(t, acct, "sess-1", bashRec(repo, "sess-1", now, `graphify update .`))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Events; got != 4 {
		t.Fatalf("events after append = %d, want 4", got)
	}
}

// W19: a transcript rewritten and grown past the old offset no longer carries
// what was read up to it; resuming there starts mid-line in different content.
// It must be read as a replacement: old events not recounted, new ones counted.
func TestUpdateReadsAGrownRewriteAsAReplacement(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	a := bashRec(repo, "sess-1", now.Add(-3*time.Hour), `graphify query "a"`)
	b := bashRec(repo, "sess-1", now.Add(-2*time.Hour), `graphify query "b"`)
	path := writeTranscript(t, acct, "sess-1", a, b)

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The same two calls behind a long new first line, then two new calls:
	// bigger than before, and nothing at the old offset lines up.
	pad := map[string]any{"type": "user", "timestamp": now.Add(-4 * time.Hour).Format(time.RFC3339Nano),
		"cwd": repo, "sessionId": "sess-1", "message": map[string]any{"content": strings.Repeat("x", 300)}}
	rewriteTranscript(t, path, pad, a, b,
		bashRec(repo, "sess-1", now.Add(-time.Hour), `graphify query "c"`),
		bashRec(repo, "sess-1", now.Add(-time.Minute), `graft ask "d" --source`))
	if nfi, err := os.Stat(path); err != nil || nfi.Size() <= fi.Size() {
		t.Fatalf("setup: rewrite did not grow the file (%v)", err)
	}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	s := x.Summarize(Window{Days: 7, Now: now})
	if s.Events != 4 || s.ByTool[Graphify] != 3 || s.ByTool[Graft] != 1 {
		t.Fatalf("events = %d %v, want 4: a and b once, c and d new", s.Events, s.ByTool)
	}
}

// W34: Save goes through a unique temp file and a rename. Concurrent savers —
// the board's background save, the window-close save, ggraphify-scan — each
// get their own temp file, the published file is always one whole encode, no
// temp file is left behind, and the result is still private (0600).
func TestSaveIsAtomicUnderConcurrentSavers(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	var recs []map[string]any
	for i := 0; i < 200; i++ {
		recs = append(recs, bashRec(repo, "sess-1", now.Add(-time.Duration(i)*time.Minute), `graphify query "x"`))
	}
	writeTranscript(t, acct, "sess-1", recs...)
	path := DefaultPath(dir)

	// A stale fixed-name temp from an older build must not be touched or reused.
	legacy := path + ".tmp"
	if err := os.WriteFile(legacy, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		x := New()
		if err := x.Update(context.Background(), Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				x.mu.Lock()
				x.gen++ // force a write every time
				x.mu.Unlock()
				if err := x.Save(path); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := Load(path).Summarize(Window{Days: 7, Now: now}).Events; got != 200 {
		t.Fatalf("reloaded events = %d, want 200 — the published file was torn", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "usage.json.tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if b, err := os.ReadFile(legacy); err != nil || string(b) != "half" {
		t.Errorf("the fixed-name temp was written through: %q, %v", b, err)
	}
}
