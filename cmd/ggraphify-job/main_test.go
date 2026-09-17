package main

import (
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/jobs"
)

// TestMain keeps SIGINT trapped for the whole binary.
//
// The cancellation test signals this very process, and run() un-registers its
// own handler on the way out (which is the fix under test). Without a standing
// trap here, a signal that lands a moment late would take the default action
// and kill the test run instead of failing it.
func TestMain(m *testing.M) {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	os.Exit(m.Run())
}

// recorder is a supervisor that only remembers what was asked of it, so a test
// can assert the lifecycle was closed without a model server on the machine.
type recorder struct {
	mu       sync.Mutex
	acquired int
	stopped  int
}

func (r *recorder) Acquire() jobs.Lease {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acquired++
	return nil
}

func (r *recorder) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped++
}

func (r *recorder) shutdowns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// runWith drives run() the way a shell would, with a stub graphify in place of
// the real one, and hands back the exit code and the supervisor it used.
func runWith(t *testing.T, script string, args ...string) (int, *recorder) {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "graphify")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAPHIFY_BIN", bin)

	rec := &recorder{}
	saved := newSupervisor
	newSupervisor = func(func() bool, func(string, ...any)) supervisor { return rec }
	t.Cleanup(func() { newSupervisor = saved })

	// run() declares its flags on the default CommandLine, so each call needs a
	// fresh one or the second redefines the first and panics.
	savedArgs, savedFlags := os.Args, flag.CommandLine
	os.Args = append([]string{"ggraphify-job"}, args...)
	flag.CommandLine = flag.NewFlagSet("ggraphify-job", flag.ContinueOnError)
	t.Cleanup(func() { os.Args, flag.CommandLine = savedArgs, savedFlags })

	return run(), rec
}

// The whole point of finding 2: every terminal status leaves through the same
// return, so the deferred teardown — the ollama this process may have started —
// actually runs. os.Exit inside the event loop looked identical and ran none of
// it, which is how a headless job came to leave a server resident forever.
func TestTeardownRunsOnEveryTerminalStatus(t *testing.T) {
	repo := t.TempDir()

	for _, tc := range []struct {
		name   string
		script string
		want   int
	}{
		{"succeeded", "exit 0", 0},
		{"failed", "echo boom >&2; exit 1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, rec := runWith(t, tc.script, "-kind", "update", "-repo", repo, "-y")
			if code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
			if got := rec.shutdowns(); got != 1 {
				t.Errorf("supervisor shut down %d times, want 1 — the ollama this job started would stay resident", got)
			}
		})
	}
}

// Cancellation is the third terminal status and the one that reaches the exit
// through a signal rather than the process ending, so it gets its own run.
func TestTeardownRunsOnCancellation(t *testing.T) {
	repo := t.TempDir()

	go func() {
		// The stub sleeps; this interrupts run()'s own signal context, which is
		// exactly what Ctrl-C at the terminal does.
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()

	code, rec := runWith(t, "sleep 30", "-kind", "update", "-repo", repo, "-y")
	if code != 130 {
		t.Errorf("exit code = %d, want 130 (cancelled)", code)
	}
	if got := rec.shutdowns(); got != 1 {
		t.Errorf("supervisor shut down %d times, want 1", got)
	}
}

// -n and the unknown-kind refusal return before any supervisor exists; they are
// here because they are the paths that used to os.Exit and now must not.
func TestEarlyExitsReturnTheirCode(t *testing.T) {
	repo := t.TempDir()

	if code, _ := runWith(t, "exit 0", "-kind", "update", "-repo", repo, "-n"); code != 0 {
		t.Errorf("dry run exit code = %d, want 0", code)
	}
	if code, _ := runWith(t, "exit 0", "-kind", "no-such-kind", "-repo", repo); code != 2 {
		t.Errorf("unknown kind exit code = %d, want 2", code)
	}
}
