package gfy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The base-URL resolver has to agree with graphify's _resolve_ollama_base_url
// exactly, because the confirm dialog prints the URL as the place the corpus
// is about to be sent. These cases are the shapes ollama's own client accepts.
func TestOllamaBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		base     string // OLLAMA_BASE_URL, "-" for unset
		host     string // OLLAMA_HOST, "-" for unset
		want     string
		wantAuto string // what ollamaBaseURL("") gives: "" means "not opted in"
	}{
		{"neither set", "-", "-", DefaultOllamaBaseURL, ""},
		{"explicit base url wins verbatim", "http://box:9999/v1", "other:1", "http://box:9999/v1", "http://box:9999/v1"},
		{"explicit base url beats host even when odd", "http://box/nope", "1.2.3.4", "http://box/nope", "http://box/nope"},
		{"bare host gets scheme and default port", "-", "gpubox", "http://gpubox:11434/v1", "http://gpubox:11434/v1"},
		{"host:port", "-", "gpubox:1234", "http://gpubox:1234/v1", "http://gpubox:1234/v1"},
		{"colon port", "-", ":1234", "http://127.0.0.1:1234/v1", "http://127.0.0.1:1234/v1"},
		{"bare port", "-", "1234", "http://127.0.0.1:1234/v1", "http://127.0.0.1:1234/v1"},
		{"full url", "-", "https://gpubox:443", "https://gpubox:443/v1", "https://gpubox:443/v1"},
		{"empty host is not a configuration", "-", "", DefaultOllamaBaseURL, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, OllamaBaseURLVar, c.base)
			setEnv(t, OllamaHostVar, c.host)
			if got := OllamaBaseURL(); got != c.want {
				t.Errorf("OllamaBaseURL() = %q, want %q", got, c.want)
			}
			if got := ollamaBaseURL(""); got != c.wantAuto {
				t.Errorf("ollamaBaseURL(\"\") = %q, want %q", got, c.wantAuto)
			}
		})
	}
}

// IsLocalURL is the guard that stops the board telling someone a paid call is
// free. It must be conservative: anything it cannot prove is on this machine
// or this network is remote.
func TestIsLocalURL(t *testing.T) {
	local := []string{
		"http://localhost:11434/v1", "http://127.0.0.1:8080/v1", "http://[::1]:1234/v1",
		"http://192.168.1.40:8000/v1", "http://10.0.0.5/v1", "http://172.16.3.1/v1",
		"http://gpubox.local:11434/v1", "http://host.docker.internal:11434/v1",
	}
	for _, u := range local {
		if !IsLocalURL(u) {
			t.Errorf("IsLocalURL(%q) = false, want true", u)
		}
	}
	remote := []string{
		"https://api.openai.com/v1", "https://api.deepseek.com", "http://8.8.8.8/v1",
		"https://some-proxy.example.com/v1", "", "not a url", "//no-scheme",
		// A hostname that merely *looks* local resolves to whatever DNS says,
		// so it cannot be assumed to be on this machine.
		"http://localhost.attacker.example/v1",
	}
	for _, u := range remote {
		if IsLocalURL(u) {
			t.Errorf("IsLocalURL(%q) = true, want false", u)
		}
	}
}

func TestIsLocalBackend(t *testing.T) {
	setEnv(t, OllamaBaseURLVar, "-")
	setEnv(t, OllamaHostVar, "-")

	setEnv(t, OpenAIBaseURLVar, "-")
	if !IsLocalBackend(OllamaBackend) {
		t.Error("ollama should always be a local backend")
	}
	if IsLocalBackend(OpenAIBackend) {
		t.Error("openai with no base url is the OpenAI API, not local")
	}
	for _, b := range []string{"claude", ClaudeCLIBackend, "gemini", ""} {
		if IsLocalBackend(b) {
			t.Errorf("IsLocalBackend(%q) = true", b)
		}
	}

	setEnv(t, OpenAIBaseURLVar, "http://127.0.0.1:8080/v1")
	if !IsLocalBackend(OpenAIBackend) {
		t.Error("openai pointed at loopback is local")
	}
	if got := LocalBaseURL(OpenAIBackend); got != "http://127.0.0.1:8080/v1" {
		t.Errorf("LocalBaseURL = %q", got)
	}

	// Repointed at another vendor it is still remote and still billed.
	setEnv(t, OpenAIBaseURLVar, "https://openrouter.ai/api/v1")
	if IsLocalBackend(OpenAIBackend) {
		t.Error("openai pointed at a remote proxy must not be reported as local")
	}
}

func TestLocalModel(t *testing.T) {
	setEnv(t, OllamaModelVar, "-")
	setEnv(t, OpenAIModelVar, "-")

	if got := LocalModel(OllamaBackend, "picked:1b"); got != "picked:1b" {
		t.Errorf("an explicit setting wins: %q", got)
	}
	if got := LocalModel(OllamaBackend, ""); got != DefaultOllamaModel {
		t.Errorf("blank falls through to graphify's default: %q", got)
	}
	setEnv(t, OllamaModelVar, "env:7b")
	if got := LocalModel(OllamaBackend, ""); got != "env:7b" {
		t.Errorf("the environment beats the default: %q", got)
	}
	if got := LocalModel(OllamaBackend, "picked:1b"); got != "picked:1b" {
		t.Errorf("but not an explicit setting: %q", got)
	}
	if got := LocalModel("claude", ""); got != "" {
		t.Errorf("a vendor backend has no local model: %q", got)
	}
}

func TestProbeHas(t *testing.T) {
	p := LocalProbe{Models: []string{"qwen2.5-coder:7b", "llama3.2:latest"}}
	for _, m := range []string{"qwen2.5-coder:7b", "llama3.2:latest", "llama3.2", ""} {
		if !p.Has(m) {
			t.Errorf("Has(%q) = false", m)
		}
	}
	for _, m := range []string{"qwen2.5-coder", "qwen2.5-coder:3b", "mistral"} {
		if p.Has(m) {
			t.Errorf("Has(%q) = true", m)
		}
	}
	if (LocalProbe{}).Has("") {
		t.Error("a server with nothing has nothing")
	}
}

// The three readiness failures a user actually hits, each against a real HTTP
// server, because the whole point of LocalReady is that it probes rather than
// assumes.
func TestLocalReady(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")

	t.Run("server down", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		addr := srv.URL
		srv.Close() // a port nothing is listening on any more
		setEnv(t, OllamaBaseURLVar, addr+"/v1")
		InvalidateLocalProbe()

		ok, why := LocalReady(OllamaBackend, "")
		if ok {
			t.Fatal("a dead server is not ready")
		}
		if !strings.Contains(why, addr) {
			t.Errorf("the reason must name the endpoint: %q", why)
		}
	})

	t.Run("up but empty", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, modelServer(t)+"/v1")
		InvalidateLocalProbe()

		ok, why := LocalReady(OllamaBackend, "")
		if ok {
			t.Fatal("a server with no models cannot run anything")
		}
		if !strings.Contains(why, "no models") {
			t.Errorf("why = %q", why)
		}
	})

	t.Run("up but missing the wanted model", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, modelServer(t, "llama3.2:3b")+"/v1")
		InvalidateLocalProbe()

		ok, why := LocalReady(OllamaBackend, "qwen2.5-coder:7b")
		if ok {
			t.Fatal("the wanted model is not there")
		}
		if !strings.Contains(why, "qwen2.5-coder:7b") || !strings.Contains(why, "llama3.2:3b") {
			t.Errorf("the reason must name both what was wanted and what is there: %q", why)
		}
	})

	t.Run("ready", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, modelServer(t, "qwen2.5-coder:7b")+"/v1")
		InvalidateLocalProbe()

		ok, why := LocalReady(OllamaBackend, "qwen2.5-coder:7b")
		if !ok {
			t.Fatalf("should be ready: %s", why)
		}
		// The one claim that makes a local backend different from every other.
		if !strings.Contains(why, "costs time, not money") {
			t.Errorf("why = %q", why)
		}
	})
}

// BackendReady must route a local backend through LocalReady rather than
// answering "needs no credential" for a server that is not running — the bug
// this whole file replaces.
func TestBackendReadyRoutesLocal(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, "http://127.0.0.1:1/v1") // nothing listens on port 1
	InvalidateLocalProbe()

	if ok, why := BackendReady(OllamaBackend); ok {
		t.Errorf("ollama with no server reported ready: %q", why)
	}

	setEnv(t, OpenAIBaseURLVar, modelServer(t, "local-model")+"/v1")
	InvalidateLocalProbe()
	if ok, why := BackendReady(OpenAIBackend); !ok {
		t.Errorf("openai at a live local server should be ready: %q", why)
	}
}

// The one variable set the board offers must be the one graphify reads. The
// list previously offered GRAPHIFY_OLLAMA_MODEL and GRAPHIFY_OLLAMA_HOST,
// which graphify 0.9.58 reads nowhere — setting either did nothing at all.
func TestKnownVarsUseOllamasOwnNames(t *testing.T) {
	has := map[string]bool{}
	for _, v := range KnownVars {
		has[v] = true
	}
	for _, want := range []string{OllamaHostVar, OllamaBaseURLVar, OllamaModelVar, OpenAIBaseURLVar} {
		if !has[want] {
			t.Errorf("KnownVars is missing %s", want)
		}
	}
	for _, dead := range []string{"GRAPHIFY_OLLAMA_MODEL", "GRAPHIFY_OLLAMA_HOST"} {
		if has[dead] {
			t.Errorf("KnownVars still offers %s, which graphify never reads", dead)
		}
	}
}

// modelServer is an OpenAI-compatible /v1/models endpoint serving exactly the
// named models. It is what ollama, llama-server, vLLM and LM Studio all look
// like to the probe.
func modelServer(t *testing.T, models ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		type entry struct {
			ID string `json:"id"`
		}
		body := struct {
			Data []entry `json:"data"`
		}{}
		for _, m := range models {
			body.Data = append(body.Data, entry{ID: m})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// setEnv sets a variable for one test, or unsets it when value is "-".
// t.Setenv already restores; the sentinel is only so a table can express
// "unset" without a second column.
func setEnv(t *testing.T, name, value string) {
	t.Helper()
	if value == "-" {
		// t.Setenv then Unsetenv keeps the restore-on-cleanup that t.Setenv
		// registers, which os.Unsetenv alone would not.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv(name, value)
}

// The "already running" short-circuit, which is also the only branch of
// StartOllama that is safe to exercise without a systemd on the test machine:
// every other one either starts a process or refuses.
func TestStartOllamaAlreadyRunning(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, modelServer(t, "qwen2.5-coder:7b")+"/v1")
	InvalidateLocalProbe()

	how, err := StartOllama()
	if err != nil {
		t.Fatalf("a server that is already answering needs no start: %v", err)
	}
	if how != "already running" {
		t.Errorf("how = %q", how)
	}
}

// unitEnabled must be strictly narrower than unitExists, because a machine
// that has moved from a per-user ollama to the distro's system one keeps the
// disabled user unit file on disk. Acting on mere existence would start a
// second server against a second model store — the failure that makes an
// already-pulled model look like it vanished.
func TestUnitEnabledIsNarrowerThanUnitExists(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("no systemctl on this machine")
	}
	// A name systemd cannot know, so both must be false and neither may panic
	// on the variadic scope.
	const bogus = "ggraphify-no-such-unit-2f9c.service"
	for _, scope := range [][]string{nil, {"--user"}} {
		if unitExists(bogus, scope...) {
			t.Errorf("unitExists(%q, %v) = true", bogus, scope)
		}
		if unitEnabled(bogus, scope...) {
			t.Errorf("unitEnabled(%q, %v) = true", bogus, scope)
		}
	}
	// And enabled can never be true where exists is false, for any unit this
	// machine actually has.
	for _, u := range []string{"ollama.service", "dbus.service", "basic.target"} {
		for _, scope := range [][]string{nil, {"--user"}} {
			if unitEnabled(u, scope...) && !unitExists(u, scope...) {
				t.Errorf("%s %v: enabled but not existing", u, scope)
			}
		}
	}
}
