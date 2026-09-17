package ui

import (
	"path/filepath"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

// The ollama lifecycle's WIRING — as opposed to the mechanism in
// gfy/ollamactl.go, which is well covered — lives in this package and had no
// tests at all. These are the decision functions: which jobs take a lease, and
// which resume has to wait for a server. Both are plain logic on an App that
// needs no display.

func TestLeaseOllamaTakesOneOnlyForALocalOllamaJob(t *testing.T) {
	a := &App{ollama: &gfy.AutoOllama{Enabled: func() bool { return true }}}

	ollamaArgv := []string{"graphify", "extract", "--backend", gfy.OllamaBackend}

	for _, tc := range []struct {
		name string
		job  *jobs.Job
		want bool // want a lease
	}{
		{"nil job", nil, false},
		{"a free job that never talks to a model", &jobs.Job{Argv: ollamaArgv}, false},
		{"a metered job, which bills an API rather than this machine",
			&jobs.Job{Local: true, Argv: []string{"graphify", "extract", "--backend", gfy.ClaudeCLIBackend}}, false},
		{"a local ollama job", &jobs.Job{Local: true, Argv: ollamaArgv}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := a.leaseOllama(tc.job)
			if (got != nil) != tc.want {
				t.Fatalf("leaseOllama = %v, want a lease: %v", got != nil, tc.want)
			}
			if got != nil {
				got.Release()
			}
		})
	}
}

// A lease is only taken while the lifecycle is on. With it off the runner must
// get nothing to hold, or the board would be counting leases on a server it
// has promised not to manage.
func TestLeaseOllamaIsInertWhenTheLifecycleIsOff(t *testing.T) {
	a := &App{ollama: &gfy.AutoOllama{Enabled: func() bool { return false }}}
	j := &jobs.Job{Local: true, Argv: []string{"graphify", "extract", "--backend", gfy.OllamaBackend}}

	l := a.leaseOllama(j)
	if l == nil {
		t.Fatal("leaseOllama returned no handle; the runner has nothing to release")
	}
	l.Release()
	if n := a.ollama.Leases(); n != 0 {
		t.Errorf("%d leases outstanding with the lifecycle off, want 0", n)
	}
}

// resumeWaitsForOllama drives the "resuming — starting ollama first" toast.
// It has to be true exactly when the board is managing the server AND the job
// is one that talks to it; a toast on any other resume is a promise about a
// ten-second wait that is not going to happen.
func TestResumeWaitsForOllamaOnlyWhenBothAreTrue(t *testing.T) {
	newApp := func(t *testing.T, autoOllama bool) (*App, *jobs.Runner) {
		t.Helper()
		st := store.Open(filepath.Join(t.TempDir(), "state.json"))
		set := st.Settings()
		set.NoAutoOllama = !autoOllama
		st.SetSettings(set)

		r := jobs.New(jobs.Options{})
		t.Cleanup(r.Close)
		return &App{opts: Options{Store: st}, runner: r}, r
	}

	// "extract", not "update": update is the AST-only pass, so its argv names
	// no backend at all and there is nothing for ArgvBackend to find.
	submit := func(t *testing.T, r *jobs.Runner, backend string) uint64 {
		t.Helper()
		repo := t.TempDir()
		j, err := r.SubmitCmd("extract", repo, "Extract",
			gfy.Params{Repo: repo, Backend: backend}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}

	t.Run("managed server, ollama job", func(t *testing.T) {
		a, r := newApp(t, true)
		if !a.resumeWaitsForOllama(submit(t, r, gfy.OllamaBackend)) {
			t.Error("want true: the board manages the server and this job uses it")
		}
	})
	t.Run("lifecycle off", func(t *testing.T) {
		a, r := newApp(t, false)
		if a.resumeWaitsForOllama(submit(t, r, gfy.OllamaBackend)) {
			t.Error("want false: the board does not manage this server, so it starts nothing")
		}
	})
	t.Run("job that does not use the local server", func(t *testing.T) {
		a, r := newApp(t, true)
		if a.resumeWaitsForOllama(submit(t, r, gfy.ClaudeCLIBackend)) {
			t.Error("want false: a metered job does not wait for ollama")
		}
	})
	t.Run("job that does not exist", func(t *testing.T) {
		a, _ := newApp(t, true)
		if a.resumeWaitsForOllama(999999) {
			t.Error("want false for an unknown id, not a lookup on a zero value")
		}
	})
}

// togglePause reads the job's state on the main thread and acts on it in a
// goroutine, so without an in-flight guard two quick clicks both read the same
// pre-click state and queue opposite transitions — and a resume can sit inside
// lease.Resume for the ten seconds a cold model server takes, which is ample
// room for the second click to arrive.
func TestTogglePauseIgnoresASecondClickWhileOneIsInFlight(t *testing.T) {
	st := store.Open(filepath.Join(t.TempDir(), "state.json"))
	r := jobs.New(jobs.Options{})
	defer r.Close()
	a := &App{opts: Options{Store: st}, runner: r, pausing: map[uint64]bool{}}

	const id = 42

	// Hold the slot, as an in-flight transition does.
	a.mu.Lock()
	a.pausing[id] = true
	a.mu.Unlock()

	a.togglePause(id)

	// The guard must still be held by the first caller: a second click that
	// cleared it, or that spawned its own goroutine, is the bug.
	a.mu.RLock()
	held := a.pausing[id]
	a.mu.RUnlock()
	if !held {
		t.Fatal("a second togglePause cleared the in-flight guard the first one holds")
	}

	// And once the first transition has finished, a click is honoured again.
	a.mu.Lock()
	delete(a.pausing, id)
	a.mu.Unlock()
	a.togglePause(id)
}
