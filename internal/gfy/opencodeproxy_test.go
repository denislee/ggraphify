package gfy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/applog"
)

// The gateway answers 400 MissingSessionID to any request without
// x-opencode-session, and graphify's OpenAI client cannot send one. This is
// the test for the thing that puts it there.
func TestTheProxyAddsWhatTheGatewayDemands(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)

	var got *http.Request
	var body []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer upstream.Close()
	t.Setenv(OpenCodeBaseURLVar, upstream.URL+"/v1")

	base, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy: %v", err)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.HasSuffix(base, "/v1") {
		t.Fatalf("base = %q, want a loopback /v1 URL", base)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/chat/completions",
		bytes.NewBufferString(`{"model":"glm-5.3-flash"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-not-a-real-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(out), `"ok"`) {
		t.Fatalf("status %d body %q", resp.StatusCode, out)
	}

	if got == nil {
		t.Fatal("the upstream saw nothing")
	}
	if got.URL.Path != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want the client's path under the gateway's /v1", got.URL.Path)
	}
	if s := got.Header.Get("x-opencode-session"); !strings.HasPrefix(s, "ggraphify-") {
		t.Errorf("x-opencode-session = %q, want a stable ggraphify session id", s)
	}
	if ua := got.Header.Get("User-Agent"); !strings.HasPrefix(ua, "ggraphify/") {
		t.Errorf("User-Agent = %q, want this application's own name", ua)
	}
	// The credential is forwarded, never rewritten — the proxy exists for two
	// headers and must be transparent about everything else.
	if a := got.Header.Get("Authorization"); a != "Bearer sk-not-a-real-key" {
		t.Errorf("Authorization = %q, want it passed through untouched", a)
	}
	if string(body) != `{"model":"glm-5.3-flash"}` {
		t.Errorf("body = %q, want it passed through untouched", body)
	}

	// The session id is one per process, which is the granularity the plan
	// asks for: an extraction sweep is one conversation.
	if OpenCodeSession() != got.Header.Get("x-opencode-session") {
		t.Error("the session id moved between requests")
	}
	// Idempotent: a second submit reuses the running proxy.
	if again, err := StartOpenCodeProxy(OpenCodeBackend); err != nil || again != base {
		t.Errorf("StartOpenCodeProxy again = %q, %v; want the same proxy", again, err)
	}
}

func TestTheProxyForwardsNothingButTheGatewaysAPI(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	t.Setenv(OpenCodeBaseURLVar, upstream.URL+"/v1")

	base, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy: %v", err)
	}
	root := strings.TrimSuffix(base, "/v1")

	// A loopback proxy that forwarded anything would be an open relay for
	// whatever else runs on this machine.
	resp, err := http.Get(root + "/somewhere/else") // #nosec G107 -- loopback, composed here
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d for a path off the gateway's API, want 404", resp.StatusCode)
	}

	// And it identifies itself, so a second ggraphify can tell whether the
	// port it failed to bind is a sibling or a stranger.
	probe, err := http.Get(root + openCodeProbePath) // #nosec G107 -- loopback, composed here
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Body.Close() }()
	b, _ := io.ReadAll(probe.Body)
	if !strings.HasPrefix(string(b), openCodeProbeBody) {
		t.Errorf("probe answered %q", b)
	}
}

// One loopback port, two plans, and the path is what tells them apart. This
// is the test for the claim the provider entries make: a Zen entry points at
// /zen/v1 and its traffic reaches the Zen gateway, a Go entry points at /v1
// and reaches the Go one, and neither can be routed to the other by anything
// the client sends.
func TestTheProxyKeepsTheTwoPlansApartByPath(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)

	seen := make(chan string, 4)
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- name + " " + r.URL.Path
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
	}
	goGW, zenGW := mk("go"), mk("zen")
	defer goGW.Close()
	defer zenGW.Close()
	t.Setenv(OpenCodeBaseURLVar, goGW.URL+"/v1")
	t.Setenv(OpenCodeZenBaseURLVar, zenGW.URL+"/v1")

	goBase, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy(go): %v", err)
	}
	zenBase, err := StartOpenCodeProxy(OpenCodeZenBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy(zen): %v", err)
	}
	if !strings.HasSuffix(zenBase, "/zen/v1") {
		t.Fatalf("zen base = %q, want the plan's own path on the proxy", zenBase)
	}
	if strings.TrimSuffix(goBase, "/v1") != strings.TrimSuffix(zenBase, "/zen/v1") {
		t.Fatalf("the plans bound different proxies: %q and %q", goBase, zenBase)
	}

	for _, base := range []string{goBase, zenBase} {
		resp, err := http.Post(base+"/chat/completions", "application/json", // #nosec G107 -- loopback, composed here
			bytes.NewBufferString(`{"model":"m"}`))
		if err != nil {
			t.Fatalf("through %s: %v", base, err)
		}
		_ = resp.Body.Close()
	}

	got := []string{<-seen, <-seen}
	want := []string{"go /v1/chat/completions", "zen /v1/chat/completions"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d reached %q, want %q", i, got[i], want[i])
		}
	}

	// The probe says which plans this proxy can serve, because a board that
	// inherited an older ggraphify's proxy would otherwise write a Zen entry
	// pointing at a path that 404s every chunk.
	probe, err := http.Get(strings.TrimSuffix(goBase, "/v1") + openCodeProbePath) // #nosec G107 -- loopback, composed here
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Body.Close() }()
	b, _ := io.ReadAll(probe.Body)
	if !strings.Contains(string(b), "plans=go,zen") {
		t.Errorf("probe answered %q, want it to advertise both plans", b)
	}
}

// graphify reduces a refused chunk to "chunk failed". The proxy is the one
// place that sees the gateway's own reason, so a refusal has to reach the log
// with the model it was for, and the credential must not.
func TestTheProxyLogsWhyTheGatewayRefused(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"weekly quota exhausted"}}`)
	}))
	defer upstream.Close()
	t.Setenv(OpenCodeBaseURLVar, upstream.URL+"/v1")

	base, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy: %v", err)
	}
	applog.Default.Clear()
	req, _ := http.NewRequest(http.MethodPost, base+"/chat/completions",
		bytes.NewBufferString(`{"model":"mimo-v2.6-flash","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-secret-in-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the gateway's 429 passed through", resp.StatusCode)
	}

	// The log line is written after the body is copied, which can land a
	// moment after the client has read it.
	var log string
	for i := 0; i < 50; i++ {
		if log = applog.Default.Text(); strings.Contains(log, "HTTP 429") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{"model=mimo-v2.6-flash", "HTTP 429", "weekly quota exhausted"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "sk-secret-in-test") {
		t.Errorf("the credential reached the log:\n%s", log)
	}
}

func TestRequestModel(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"glm-5.3-flash","messages":[]}`: "glm-5.3-flash",
		`{"messages":[]}`:                         "-",
		``:                                        "-",
		`not json`:                                "-",
	} {
		if got := requestModel([]byte(body)); got != want {
			t.Errorf("requestModel(%q) = %q, want %q", body, got, want)
		}
	}
}

// fakeSiblingProxy stands in for another ggraphify's proxy on a port of its
// own, advertising the given plan list, and points this process at that port.
func fakeSiblingProxy(t *testing.T, plans string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == openCodeProbePath {
			_, _ = io.WriteString(w, openCodeProbeBody+" old plans="+plans)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(OpenCodePortVar, strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	return srv
}

// W4: once a shared proxy's root is cached, a later plan still has to be one
// that proxy serves. An older sibling that only serves Go must not be handed
// an ollama-cloud run because a Go run got there first.
func TestASharedProxyIsCheckedForEveryPlan(t *testing.T) {
	resetProxy(t)
	fakeSiblingProxy(t, "go")

	if _, err := StartOpenCodeProxy(OpenCodeBackend); err != nil {
		t.Fatalf("StartOpenCodeProxy(go) on a Go-only sibling: %v", err)
	}
	if _, owned, _, _ := OpenCodeProxyActivity(); owned {
		t.Fatal("owned = true, want the sibling's proxy shared")
	}
	if base, err := StartOpenCodeProxy(OllamaCloudBackend); err == nil {
		t.Fatalf("StartOpenCodeProxy(ollama-cloud) = %q, nil; want a refusal — the sibling does not serve that path", base)
	}
	if base, err := StartOpenCodeProxy(OpenCodeZenBackend); err == nil {
		t.Fatalf("StartOpenCodeProxy(zen) = %q, nil; want a refusal", base)
	}
}

// W10: a shared proxy whose owner has quit is not cached forever — the next
// Start finds the port free and binds it here.
func TestASharedProxyIsReboundAfterItsOwnerQuits(t *testing.T) {
	resetProxy(t)
	sib := fakeSiblingProxy(t, "go,zen,ollama")

	first, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy: %v", err)
	}
	if _, owned, _, _ := OpenCodeProxyActivity(); owned {
		t.Fatal("owned = true, want the sibling's proxy shared")
	}
	sib.Close()

	again, err := StartOpenCodeProxy(OpenCodeBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy after the owner quit: %v", err)
	}
	if again != first {
		t.Errorf("base = %q, want the same port %q bound here", again, first)
	}
	if _, owned, _, _ := OpenCodeProxyActivity(); !owned {
		t.Fatal("owned = false after the owner quit, want this process to have bound the port")
	}
	probe, err := http.Get(strings.TrimSuffix(again, "/v1") + openCodeProbePath) // #nosec G107 -- loopback, composed here
	if err != nil {
		t.Fatalf("the port answers nothing after the rebind: %v", err)
	}
	_ = probe.Body.Close()
}

// W23: concurrent Starts in one process bind one proxy between them.
func TestConcurrentStartsBindOneProxy(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)

	const n = 8
	bases := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := StartOpenCodeProxy(OpenCodeBackend)
			if err != nil {
				t.Errorf("StartOpenCodeProxy: %v", err)
			}
			bases <- b
		}()
	}
	wg.Wait()
	close(bases)
	first := ""
	for b := range bases {
		if first == "" {
			first = b
		} else if b != first {
			t.Errorf("concurrent Starts bound different proxies: %q and %q", first, b)
		}
	}
}

// W23: port 0 is a test-only request; from the environment it falls back to
// the fixed default, which another ggraphify can find and share.
func TestPortZeroIsRefused(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	if got := OpenCodeProxyPort(); got != DefaultOpenCodePort {
		t.Errorf("OpenCodeProxyPort() with %s=0 = %d, want the default %d", OpenCodePortVar, got, DefaultOpenCodePort)
	}
}
