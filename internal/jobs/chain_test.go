package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
)

var errPrecheck = errors.New("jobs: refused by the precheck")

// drain keeps the event channel empty so the runner never blocks on a UI that
// is not there. Chain tests assert on the ChainResult, not on events.
func drain(r *Runner) chan struct{} {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-r.Events():
			case <-stop:
				return
			}
		}
	}()
	return stop
}

func waitChain(t *testing.T, ch <-chan ChainResult) ChainResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for the chain")
		return ChainResult{}
	}
}

func chainSteps(repo string, kinds ...string) []ChainStep {
	var out []ChainStep
	for _, k := range kinds {
		out = append(out, ChainStep{
			Kind: k, Repo: repo, Label: gfy.Title(k),
			Params: gfy.Params{Repo: repo, Out: filepath.Join(repo, "graphify-out")},
		})
	}
	return out
}

// The steps run in the order given, one after another — not fanned out. A fix
// plan that clustered a graph while it was still being extracted would be
// racing the very file it reads.
func TestChainRunsStepsInOrder(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order")
	fakeGraphify(t, `echo "$1" >> `+log+`; exit 0`)
	r := New(Options{})
	defer r.Close()
	stop := drain(r)
	defer close(stop)

	repo := repoDir(t)
	ch := make(chan ChainResult, 1)
	r.SubmitChain(chainSteps(repo, "update", "cluster-only", "export-html"),
		func(res ChainResult) { ch <- res })

	res := waitChain(t, ch)
	if !res.OK() {
		t.Fatalf("chain did not finish: %+v", res)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(b))
	want := []string{"update", "cluster-only", "export"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The first failure ends the chain. Naming the communities of a graph whose
// rebuild just died is a bill for a wrong answer.
func TestChainStopsAtTheFirstFailure(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order")
	fakeGraphify(t, `echo "$1" >> `+log+`; if [ "$1" = "cluster-only" ]; then exit 3; fi; exit 0`)
	r := New(Options{})
	defer r.Close()
	stop := drain(r)
	defer close(stop)

	repo := repoDir(t)
	ch := make(chan ChainResult, 1)
	r.SubmitChain(chainSteps(repo, "update", "cluster-only", "label"),
		func(res ChainResult) { ch <- res })

	res := waitChain(t, ch)
	if res.OK() {
		t.Fatal("a chain with a failing step must not report OK")
	}
	if res.Ran != 1 {
		t.Errorf("Ran = %d, want 1", res.Ran)
	}
	if res.Stopped.Kind != "cluster-only" {
		t.Errorf("Stopped = %q, want cluster-only", res.Stopped.Kind)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "label") {
		t.Fatal("the metered step ran after its precondition failed")
	}
}

// A precheck refusal is reported the same way a failure is, rather than
// silently skipping to the next step.
func TestChainReportsAPrecheckRefusal(t *testing.T) {
	fakeGraphify(t, `exit 0`)
	r := New(Options{Precheck: func(j *Job) error {
		if j.Kind == "label" {
			return errPrecheck
		}
		return nil
	}})
	defer r.Close()
	stop := drain(r)
	defer close(stop)

	repo := repoDir(t)
	ch := make(chan ChainResult, 1)
	r.SubmitChain(chainSteps(repo, "update", "label"), func(res ChainResult) { ch <- res })

	res := waitChain(t, ch)
	if res.Err == nil || res.Stopped.Kind != "label" {
		t.Fatalf("result = %+v, want a refusal on label", res)
	}
	if res.Ran != 1 {
		t.Errorf("Ran = %d, want 1", res.Ran)
	}
}

// An empty plan is not an error: "nothing to fix" has to be sayable.
func TestEmptyChainCallsBackImmediately(t *testing.T) {
	r := New(Options{})
	defer r.Close()
	ch := make(chan ChainResult, 1)
	r.SubmitChain(nil, func(res ChainResult) { ch <- res })
	if res := waitChain(t, ch); !res.OK() {
		t.Fatalf("empty chain = %+v, want OK", res)
	}
}
