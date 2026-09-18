package gfy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	base, err := StartOpenCodeProxy()
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
	if again, err := StartOpenCodeProxy(); err != nil || again != base {
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

	base, err := StartOpenCodeProxy()
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
