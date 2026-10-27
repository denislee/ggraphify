package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// W6: a retried job must carry what SubmitCmd would have composed for it —
// the local lane, the output directory, the working directory, the log
// preamble and the overlay — not just the argv.
func TestRetryJobFromRebuildsSubmitCmdFields(t *testing.T) {
	s := jobs.Snapshot{
		Kind: "extract", Repo: "/r", Label: "Extract · r", Cost: gfy.Metered,
		Local: true, Dir: "/r/sub", Out: "/o",
		Argv:   []string{"graphify", "extract", "--backend", gfy.ClaudeCLIBackend, "/r"},
		Status: jobs.Failed,
	}
	j := retryJobFrom(s, gfy.Env{"FOO": "bar"}, "")
	if !j.Local {
		t.Error("Local dropped: a local retry would land in the metered lane without a lease")
	}
	if j.Out != "/o" || j.Dir != "/r/sub" {
		t.Errorf("Out/Dir = %q/%q, want /o and /r/sub", j.Out, j.Dir)
	}
	if j.Env["FOO"] != "bar" || j.Env["GRAPHIFY_NO_TIPS"] != "1" {
		t.Errorf("overlay not rebuilt: %v", j.Env)
	}
	if len(j.Notes) == 0 {
		t.Error("a claude-cli retry must name its Claude Code account, as SubmitCmd does")
	}
	if j.Kind != s.Kind || j.Cost != s.Cost || !reflect.DeepEqual(j.Argv, s.Argv) {
		t.Errorf("identity changed: %+v", j)
	}
	j.Argv[0] = "mutated"
	if s.Argv[0] != "graphify" {
		t.Error("the retry's argv aliases the snapshot's")
	}

	s.Dir = ""
	if j := retryJobFrom(s, nil, ""); j.Dir != "/r" {
		t.Errorf("Dir fallback = %q, want the repo", j.Dir)
	}
}

// W6: the retry path gates a billing job behind the metered confirm and runs
// the prechecks; asserted on the source because the dialog needs a display.
func TestRetryGoesThroughTheCostGate(t *testing.T) {
	src := readSource(t, "jobsview.go")
	body := src[strings.Index(src, "func (v *jobsView) retryJob("):]
	body = body[:strings.Index(body, "\n}\n")]
	for _, want := range []string{"gfy.Metered && !j.Local", "confirmRetry", "submitRetry", "a.run(s.Kind"} {
		if !strings.Contains(body, want) {
			t.Errorf("retryJob lost %q", want)
		}
	}
	if strings.Contains(body, "runner.Submit(") {
		t.Error("retryJob submits directly, bypassing the gate")
	}
	sub := src[strings.Index(src, "func (a *App) submitRetry("):]
	sub = sub[:strings.Index(sub, "\n}\n")]
	for _, want := range []string{"PreflightOpenCode", "EnsureOpenCodeProvider", "a.precheck"} {
		if !strings.Contains(sub, want) {
			t.Errorf("submitRetry lost %q", want)
		}
	}
}

// W20: the typed-count gate holds every submitting response, the free one too.
func TestTypedGateCoversTheFreeResponse(t *testing.T) {
	if got := typedGateResponses(true); !reflect.DeepEqual(got, []string{"run", "free"}) {
		t.Errorf("typedGateResponses(true) = %q", got)
	}
	if got := typedGateResponses(false); !reflect.DeepEqual(got, []string{"run"}) {
		t.Errorf("typedGateResponses(false) = %q", got)
	}
	if !strings.Contains(readSource(t, "actions.go"), `typedGateResponses(dlg.HasResponse("free"))`) {
		t.Error("confirm no longer gates its responses through typedGateResponses")
	}
}

// W21: the sync response re-filters the queue against what is live by then.
func TestWithoutLiveDropsQueuedRows(t *testing.T) {
	rows := []board.Row{{Path: "/a"}, {Path: "/b"}, {Path: "/c"}}
	got := withoutLive(rows, map[string]bool{"/b": true})
	if len(got) != 2 || got[0].Path != "/a" || got[1].Path != "/c" {
		t.Errorf("withoutLive = %v", got)
	}
	if got := withoutLive(rows, nil); len(got) != 3 {
		t.Errorf("withoutLive(nil) dropped rows: %v", got)
	}
	src := readSource(t, "graft.go")
	if !strings.Contains(src, "withoutLive(queue, a.liveGraftDeep())") {
		t.Error("the sync response no longer re-dedupes at the click")
	}
}

// W17: scale 1.0 must leave gtk-xft-dpi to the desktop, and a return to 1.0
// after an override must reset rather than write base × 1.
func TestScaleDPI(t *testing.T) {
	if act, _ := scaleDPI(98304, 1, false); act != dpiLeave {
		t.Errorf("scale 1.0, never overridden: %v, want dpiLeave", act)
	}
	if act, _ := scaleDPI(98304, 1, true); act != dpiReset {
		t.Errorf("scale 1.0 after an override: %v, want dpiReset", act)
	}
	if act, v := scaleDPI(98304, 1.25, false); act != dpiWrite || v != 122880 {
		t.Errorf("scale 1.25: %v %d, want dpiWrite 122880", act, v)
	}
}
