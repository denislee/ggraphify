package main

import (
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	signal.Notify(make(chan os.Signal, 1), os.Interrupt, syscall.SIGTERM)
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

// capture runs f with os.Stdout and os.Stderr pointed at files, and returns
// what each received.
func capture(t *testing.T, f func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	o, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = o, e
	func() {
		defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
		f()
		// Anything run() left running on its way out — the interrupt watcher
		// is the one that matters — gets a moment to write before the files
		// are swapped back.
		time.Sleep(100 * time.Millisecond)
	}()
	o.Close()
	e.Close()
	ob, _ := os.ReadFile(o.Name())
	eb, _ := os.ReadFile(e.Name())
	return string(ob), string(eb)
}

// W5: a log well past the ring buffer's cap reaches stdout whole — every
// line, in order, through to the last one — rather than going silent once the
// buffer starts evicting, or losing its tail when the job retires.
func TestStdoutCarriesALogPastTheBufferCap(t *testing.T) {
	repo := t.TempDir()
	const n = 40000 // ~470 KiB of output, well past ringbuf.DefaultCap
	var code int
	out, _ := capture(t, func() {
		code, _ = runWith(t, `i=1; while [ $i -le `+strconv.Itoa(n)+` ]; do echo "output line $i"; i=$((i+1)); done`,
			"-kind", "update", "-repo", repo, "-y")
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out, "$ ") {
		t.Errorf("stdout does not open with the command header: %.80q", out)
	}
	if strings.Contains(out, "elided") {
		t.Error("stdout carries the ring buffer's elision note; it should carry the stream")
	}
	next := 1
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "output line "); ok {
			if rest != strconv.Itoa(next) {
				t.Fatalf("stdout skipped from line %d to %q", next, rest)
			}
			next++
		}
	}
	if next != n+1 {
		t.Fatalf("stdout ended at line %d of %d", next-1, n)
	}
}

// W28: a job that simply finishes must not be reported as interrupted.
func TestNormalExitIsNotReportedAsAnInterrupt(t *testing.T) {
	repo := t.TempDir()
	var code int
	_, errOut := capture(t, func() {
		code, _ = runWith(t, "echo done; exit 0", "-kind", "update", "-repo", repo, "-y")
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// The false report came from the watcher waking on the deferred stop(),
	// asynchronously; capture waits for it before handing stderr back.
	if strings.Contains(errOut, "cancelling") {
		t.Errorf("a normal exit printed an interrupt:\n%s", errOut)
	}
}

// W28: SIGTERM is reported as 143 (128+15), not as SIGINT's 130.
func TestSIGTERMExitsWith143(t *testing.T) {
	repo := t.TempDir()
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	var code int
	_, errOut := capture(t, func() {
		code, _ = runWith(t, "sleep 30", "-kind", "update", "-repo", repo, "-y")
	})
	if code != 143 {
		t.Errorf("exit code = %d, want 143 (terminated)", code)
	}
	if !strings.Contains(errOut, "terminated — cancelling the job") {
		t.Errorf("stderr does not name the signal:\n%s", errOut)
	}
}
