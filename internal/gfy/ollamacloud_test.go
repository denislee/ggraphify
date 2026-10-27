package gfy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ollama Cloud is a plan of its own, not an alias for the local ollama server
// and not a second name for OpenCode: its own credential variable, its own
// gateway, its own provider entry and its own proxy path.
func TestOllamaCloudIsAPlanOfItsOwn(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)

	plan := OpenCodePlanFor(OllamaCloudBackend)
	if plan.Backend != OllamaCloudBackend {
		t.Errorf("OpenCodePlanFor(%q).Backend = %q, want %q", OllamaCloudBackend, plan.Backend, OllamaCloudBackend)
	}
	if !IsOpenCodeBackend(OllamaCloudBackend) {
		t.Error("IsOpenCodeBackend(OllamaCloudBackend) = false, want true")
	}
	if IsOpenCodeBackend("ollama") {
		t.Error(`IsOpenCodeBackend("ollama") = true, want false — that is the local server`)
	}
	if IsLocalBackend(OllamaCloudBackend) {
		t.Error("IsLocalBackend(OllamaCloudBackend) = true, want false — it is a hosted API")
	}
	if plan.KeyVar != "OLLAMA_CLOUD_API_KEY" {
		t.Errorf("plan.KeyVar = %q, want OLLAMA_CLOUD_API_KEY — not the local server's OLLAMA_API_KEY", plan.KeyVar)
	}
	if plan.SessionHeader {
		t.Error("plan.SessionHeader = true, want false — Ollama Cloud does not require it")
	}
	if got := DefaultModelFor(OllamaCloudBackend); got != DefaultOllamaCloudModel {
		t.Errorf("DefaultModelFor(OllamaCloudBackend) = %q, want %q", got, DefaultOllamaCloudModel)
	}

	catalogue := OpenCodeCatalog(OllamaCloudBackend)
	if len(catalogue) == 0 {
		t.Fatal("OpenCodeCatalog(OllamaCloudBackend) is empty, want the baked-in list")
	}
	found := false
	for _, m := range catalogue {
		if m.ID == DefaultOllamaCloudModel {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the catalogue does not contain the default model %q", DefaultOllamaCloudModel)
	}

	if got := OpenCodePlanFor("unknown").Backend; got != OpenCodeBackend {
		t.Errorf("OpenCodePlanFor(unknown).Backend = %q, want %q", got, OpenCodeBackend)
	}
}

// The entry Ollama Cloud writes must read its key from OLLAMA_CLOUD_API_KEY and point
// at its own path on the proxy, and it must not overwrite the Go plan's entry.
func TestOllamaCloudRegistersAnEntryOnItsOwnKey(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(OllamaCloudKeyVar, "sk-ollama")

	if _, _, err := EnsureOpenCodeProvider(OllamaCloudBackend, nil, "gpt-oss:20b"); err != nil {
		t.Fatalf("EnsureOpenCodeProvider(OllamaCloudBackend): %v", err)
	}

	entry := readProviders(t)[OllamaCloudBackend]
	if entry == nil {
		t.Fatalf("providers.json has no %q entry", OllamaCloudBackend)
	}
	if base, _ := entry["base_url"].(string); !strings.HasSuffix(base, "/ollama/v1") {
		t.Errorf("base_url = %q, want it to end with /ollama/v1", base)
	}
	if got, _ := entry["env_key"].(string); got != OllamaCloudKeyVar {
		t.Errorf("env_key = %q, want %q", got, OllamaCloudKeyVar)
	}
	if got, _ := entry["model_env_key"].(string); got != OllamaCloudModelVar {
		t.Errorf("model_env_key = %q, want %q", got, OllamaCloudModelVar)
	}
	if got, _ := entry["default_model"].(string); got != "gpt-oss:20b" {
		t.Errorf("default_model = %q, want gpt-oss:20b", got)
	}
	if _, ok := readProviders(t)[OpenCodeBackend]; ok {
		t.Error("registering Ollama Cloud also wrote an OpenCode Go entry")
	}
}

// Readiness for this plan is a question about OLLAMA_CLOUD_API_KEY. An OpenCode key
// in the environment is a key for a different plan and must not satisfy it.
func TestOllamaCloudReadinessNamesItsOwnKey(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(OllamaCloudKeyVar, "")
	t.Setenv(OpenCodeKeyVar, "sk-opencode")

	ok, why := OpenCodeReady(OllamaCloudBackend, "")
	if ok {
		t.Fatalf("OpenCodeReady reported ready with only an OpenCode key: %s", why)
	}
	if !strings.Contains(why, "OLLAMA_CLOUD_API_KEY") {
		t.Errorf("the refusal does not name OLLAMA_CLOUD_API_KEY: %s", why)
	}

	t.Setenv(OllamaCloudKeyVar, "sk-ollama")
	ok, why = OpenCodeReady(OllamaCloudBackend, "")
	if !ok {
		t.Fatalf("OpenCodeReady reported not ready with OLLAMA_CLOUD_API_KEY exported: %s", why)
	}
	if !strings.Contains(why, "OLLAMA_CLOUD_API_KEY") {
		t.Errorf("the readiness text does not name OLLAMA_CLOUD_API_KEY: %s", why)
	}
}

// Ollama Cloud's traffic goes through the same loopback proxy, on its own path,
// with its credential forwarded and no x-opencode-session header added.
func TestTheProxyRoutesOllamaCloudWithoutTheSessionHeader(t *testing.T) {
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)

	type seen struct {
		path    string
		session string
		auth    string
	}
	got := make(chan seen, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{
			path:    r.URL.Path,
			session: r.Header.Get("x-opencode-session"),
			auth:    r.Header.Get("Authorization"),
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer gw.Close()
	t.Setenv(OllamaCloudBaseURLVar, gw.URL+"/v1")

	base, err := StartOpenCodeProxy(OllamaCloudBackend)
	if err != nil {
		t.Fatalf("StartOpenCodeProxy(OllamaCloudBackend): %v", err)
	}
	if !strings.HasSuffix(base, "/ollama/v1") {
		t.Fatalf("base = %q, want it to end with /ollama/v1", base)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/chat/completions", // #nosec G107 -- loopback, composed here
		bytes.NewBufferString(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-ollama")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	s := <-got
	if s.path != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", s.path)
	}
	if s.session != "" {
		t.Errorf("x-opencode-session = %q, want it left off this plan's requests", s.session)
	}
	if s.auth != "Bearer sk-ollama" {
		t.Errorf("Authorization = %q, want the client's credential passed through", s.auth)
	}

	probe, err := http.Get(strings.TrimSuffix(base, "/ollama/v1") + openCodeProbePath) // #nosec G107 -- loopback, composed here
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Body.Close() }()
	body, _ := io.ReadAll(probe.Body)
	if !strings.Contains(string(body), "plans=go,zen,ollama") {
		t.Errorf("probe answered %q, want it to advertise the ollama plan too", body)
	}
}

// The probe's plan list is a comma-separated set of whole tags, so a prefix
// match would let "zen" claim "zenith" and hand a run to the wrong gateway.
func TestPlanListedMatchesWholeTags(t *testing.T) {
	tests := []struct {
		plans, tag string
		want       bool
	}{
		{"go,zen,ollama", "ollama", true},
		{"go,zen", "ollama", false},
		{"go,zenith", "zen", false},
		{"", "go", false},
	}
	for _, tt := range tests {
		if got := planListed(tt.plans, tt.tag); got != tt.want {
			t.Errorf("planListed(%q, %q) = %v, want %v", tt.plans, tt.tag, got, tt.want)
		}
	}
}
