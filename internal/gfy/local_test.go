package gfy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
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
	return ctxModelServer(t, 0, 0, models...)
}

// ctxModelServer is modelServer that also answers ollama's native /api/ps and
// /api/tags, which is where the context slot lives. ctx == 0 serves an empty
// /api/ps — a server with nothing loaded, which is the "not measured" case and
// has to stay distinguishable from a measured narrow slot.
func ctxModelServer(t *testing.T, ctx, ctxMax int, models ...string) string {
	t.Helper()
	loaded := ""
	if len(models) > 0 {
		loaded = models[0]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type info struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length,omitempty"`
			Details       struct {
				ContextLength int `json:"context_length,omitempty"`
			} `json:"details"`
		}
		writeModels := func(ms []info) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Models []info `json:"models"`
			}{ms})
		}
		switch r.URL.Path {
		case "/api/ps":
			if ctx <= 0 || loaded == "" {
				writeModels(nil)
				return
			}
			writeModels([]info{{Name: loaded, Model: loaded, ContextLength: ctx}})
			return
		case "/api/tags":
			var out []info
			for _, m := range models {
				e := info{Name: m, Model: m}
				e.Details.ContextLength = ctxMax
				out = append(out, e)
			}
			writeModels(out)
			return
		}
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

// The chunk budget is the whole fix for the failure that motivated it, so the
// arithmetic is pinned rather than left to be re-derived from the comment.
//
// ollama caps a request for more than half the slot at half the slot, so the
// room a prompt actually gets is the slot less the reply reservation less
// graphify's system prompt.
func TestContextTokenBudget(t *testing.T) {
	cases := []struct {
		ctx  int
		want int
	}{
		{0, 0},    // not measured: leave graphify's own default alone
		{-1, 0},   // nonsense in, nothing out
		{512, 0},  // a slot too small to hold one useful file
		{2048, 0}, // 1024 of room, all of it the system prompt and headroom
		// ollama's stock slot, and the floor: 4096 - 2048 reply - 1024 system
		// - 512 headroom. It works, and it is why ContextAdvice asks for more.
		{4096, 512},
		{8192, 8192 - 4096 - 1024 - 512}, // reply still capped at half the slot
		{16384, 16384 - 8192 - 1024 - 512},
		{32768, 32768 - 8192 - 1024 - 512},
	}
	for _, c := range cases {
		if got := contextTokenBudget(c.ctx); got != c.want {
			t.Errorf("contextTokenBudget(%d) = %d, want %d", c.ctx, got, c.want)
		}
	}
	// Every budget it does return has to leave room for the reply and the
	// system prompt inside the slot it was derived from — the invariant the
	// truncation warning proved was being violated.
	for ctx := 4096; ctx <= 131072; ctx += 1024 {
		b := contextTokenBudget(ctx)
		if b == 0 {
			continue
		}
		if b+extractionSystemTokens+extractionHeadroom >= ctx {
			t.Fatalf("budget %d for a %d slot leaves no room for the reply", b, ctx)
		}
	}
}

// ProbeLocal has to read the context slot off ollama's NATIVE api: /v1/models
// does not carry it, and /v1 is also the surface that drops the num_ctx
// graphify sends, which is why the slot has to be measured at all.
func TestProbeContext(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")

	t.Run("measured", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 4096, 32768, "qwen2.5-coder:7b")+"/v1")
		InvalidateLocalProbe()

		p := ProbeLocal(OllamaBackend)
		if p.Ctx != 4096 || p.CtxMax != 32768 || p.CtxModel != "qwen2.5-coder:7b" {
			t.Fatalf("probe = %d/%d on %q", p.Ctx, p.CtxMax, p.CtxModel)
		}
		if !p.ContextTooSmall() {
			t.Error("ollama's stock 4096 slot is the narrow case")
		}
		adv := p.ContextAdvice()
		if !strings.Contains(adv, OllamaContextVar) || !strings.Contains(adv, "32768") {
			t.Errorf("the advice must name the variable and the ceiling: %q", adv)
		}
		if got := LocalTokenBudget(OllamaBackend); got != contextTokenBudget(4096) {
			t.Errorf("LocalTokenBudget = %d", got)
		}
	})

	t.Run("nothing loaded is not measured", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 0, 32768, "qwen2.5-coder:7b")+"/v1")
		InvalidateLocalProbe()

		p := ProbeLocal(OllamaBackend)
		if p.Ctx != 0 {
			t.Fatalf("a server with nothing loaded cannot report a slot: %d", p.Ctx)
		}
		// Unknown must not become a guess: graphify's own default has to stand.
		if p.ContextTooSmall() || p.ContextAdvice() != "" {
			t.Error("unmeasured must not be reported as narrow")
		}
		if got := LocalTokenBudget(OllamaBackend); got != 0 {
			t.Errorf("LocalTokenBudget = %d, want 0 so graphify's default stands", got)
		}
	})

	t.Run("a wide slot says nothing", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
		InvalidateLocalProbe()

		p := ProbeLocal(OllamaBackend)
		if p.ContextTooSmall() || p.ContextAdvice() != "" {
			t.Error("a server that was started right needs no advice")
		}
		if got := LocalTokenBudget(OllamaBackend); got != 32768-8192-1024-512 {
			t.Errorf("LocalTokenBudget = %d", got)
		}
	})

	// The openai backend may be llama-server or vLLM, where ollama's native
	// paths 404. That must read as "not measured", never as an error.
	t.Run("a non-ollama local server", func(t *testing.T) {
		setEnv(t, OllamaBaseURLVar, "-")
		setEnv(t, OpenAIBaseURLVar, modelServer(t, "local-model")+"/v1")
		InvalidateLocalProbe()

		p := ProbeLocal(OpenAIBackend)
		if !p.Reach {
			t.Fatal("the server is up")
		}
		if p.Ctx != 0 || p.ContextAdvice() != "" {
			t.Errorf("non-ollama slot = %d, advice %q", p.Ctx, p.ContextAdvice())
		}
	})
}

// The cost paragraph is where a narrow slot has to be visible before a sweep
// starts, not after it has spent the night producing prose.
func TestLocalNoticeCarriesContext(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 4096, 32768, "qwen2.5-coder:7b")+"/v1")
	InvalidateLocalProbe()

	n := LocalNotice(OllamaBackend, "qwen2.5-coder:7b")
	if !strings.Contains(n, "--token-budget") {
		t.Errorf("the notice must say what chunks are capped at: %q", n)
	}
	if !strings.Contains(n, OllamaContextVar) {
		t.Errorf("the notice must name the fix for a narrow slot: %q", n)
	}
}

// graphify's 600-second default is shorter than one full reply from a 7B on
// CPU, and a request killed on the deadline costs the file it was extracting.
// A metered backend answers in seconds and keeps graphify's own default.
func TestLocalAPITimeout(t *testing.T) {
	if got := LocalAPITimeout(OllamaBackend, 1); got != LocalAPITimeoutSeconds {
		t.Errorf("LocalAPITimeout(ollama) = %d, want %d", got, LocalAPITimeoutSeconds)
	}
	// openai is local only once it has been repointed at a self-hosted
	// server, and the timeout has to follow that same test rather than the
	// backend's name.
	if got := LocalAPITimeout(OpenAIBackend, 1); got != 0 {
		t.Errorf("the metered openai API keeps graphify's default, got %d", got)
	}
	setEnv(t, OpenAIBaseURLVar, "http://127.0.0.1:8080/v1")
	if got := LocalAPITimeout(OpenAIBackend, 1); got != LocalAPITimeoutSeconds {
		t.Errorf("a self-hosted openai server is local, got %d", got)
	}
	if got := LocalAPITimeout("claude", 1); got != 0 {
		t.Errorf("a metered backend must keep graphify's default, got %d", got)
	}
	if LocalAPITimeoutSeconds <= 600 {
		t.Errorf("the point is to be longer than graphify's 600s, got %d", LocalAPITimeoutSeconds)
	}

	// With several jobs sharing one server, a chunk waits in the server's
	// queue behind the other jobs' chunks — and the SDK's timeout covers the
	// wait, not just the generation. A flat timeout would make the extra lanes
	// a source of killed chunks, which is the failure this timeout exists to
	// prevent, re-introduced by the setting meant to make things faster.
	if got := LocalAPITimeout(OllamaBackend, 3); got != LocalAPITimeoutSeconds*3 {
		t.Errorf("three lanes = %d, want %d", got, LocalAPITimeoutSeconds*3)
	}
	// Zero and negative are "one job", not "no timeout".
	for _, n := range []int{0, -1} {
		if got := LocalAPITimeout(OllamaBackend, n); got != LocalAPITimeoutSeconds {
			t.Errorf("LocalAPITimeout(ollama, %d) = %d, want %d", n, got, LocalAPITimeoutSeconds)
		}
	}
	// And the ceiling is the spinner's, so no setting can produce a timeout
	// that is effectively no bound at all.
	if got := LocalAPITimeout(OllamaBackend, 9999); got != LocalAPITimeoutSeconds*MaxLocalLanes {
		t.Errorf("an absurd lane count = %d, want the %d-lane ceiling", got, MaxLocalLanes)
	}
}

// The advice for a narrow slot has to describe the failure that actually
// happens — a single file bigger than the chunk cap cannot be split, so it is
// sent whole, truncated from the front, and lost from the graph. Calling that
// merely "slower" is what let a run be started and spend hours dropping files.
func TestContextAdviceNamesTheUnsplittableFile(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 4096, 32768, "qwen2.5-coder:7b")+"/v1")
	InvalidateLocalProbe()

	adv := ProbeLocal(OllamaBackend).ContextAdvice()
	for _, want := range []string{"cannot be split", "truncated", "KB"} {
		if !strings.Contains(adv, want) {
			t.Errorf("advice must mention %q: %q", want, adv)
		}
	}
	// 512 tokens of corpus at four characters a token is the 2 KB the text
	// quotes; a change to the arithmetic must move the number with it.
	if !strings.Contains(adv, "2 KB") {
		t.Errorf("a 4096 slot caps chunks at 2 KB of file: %q", adv)
	}
}

// Both entry points that submit a job — the board and ggraphify-job — have to
// size it the same way; the sizing living in the GUI was how the headless
// runner came to send graphify's 60_000-token default at a 4096-token slot.
func TestApplyLocalSizing(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
	InvalidateLocalProbe()

	// Pinned, so the suite reads a stated slot count rather than whatever
	// ollama happens to be configured with on the machine running the tests.
	setEnv(t, OllamaSlotsVar, "1")

	p := Params{Backend: OllamaBackend}
	ApplyLocalSizing(&p)
	if p.TokenBudget != contextTokenBudget(32768) || p.APITimeout != LocalAPITimeoutSeconds {
		t.Errorf("local job sized as budget=%d timeout=%d", p.TokenBudget, p.APITimeout)
	}
	// One slot means say nothing: graphify clamps ollama to one chunk itself,
	// so an explicit --max-concurrency 1 would be noise in the argv.
	if p.MaxConcurrency != 0 {
		t.Errorf("a one-slot server got --max-concurrency %d", p.MaxConcurrency)
	}

	// A metered backend keeps graphify's own defaults: zero on both fields is
	// what makes Argv omit the flags entirely.
	m := Params{Backend: "claude"}
	ApplyLocalSizing(&m)
	if m.TokenBudget != 0 || m.APITimeout != 0 {
		t.Errorf("metered job was sized: budget=%d timeout=%d", m.TokenBudget, m.APITimeout)
	}

	// A nil params is a caller bug, not a panic.
	ApplyLocalSizing(nil)
}

// The pair that has to travel together. graphify clamps max_concurrency back
// to 1 for ollama unless GRAPHIFY_OLLAMA_PARALLEL is exactly "1" — so a board
// that passed only the flag would produce an argv that looks parallel, a log
// that looks parallel, and a run that is not. Asserting them separately would
// let exactly that regression through.
func TestLocalSizingDrivesEverySlot(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
	setEnv(t, OllamaSlotsVar, "3")
	InvalidateLocalProbe()

	p := Params{Backend: OllamaBackend}
	ApplyLocalSizing(&p)
	if p.MaxConcurrency != 3 {
		t.Errorf("a three-slot server got --max-concurrency %d, want 3", p.MaxConcurrency)
	}
	argv := Argv("extract", p)
	if !strings.Contains(Quote(argv), "--max-concurrency 3") {
		t.Errorf("the flag never reached the argv: %s", Quote(argv))
	}

	e := LocalEnv(JobEnv(nil), OllamaBackend)
	if e[GraphifyOllamaParallelVar] != "1" {
		t.Errorf("%s = %q, so graphify would clamp the flag above back to one chunk",
			GraphifyOllamaParallelVar, e[GraphifyOllamaParallelVar])
	}
}

// The clamp is graphify's protection against queueing four 60k-token requests
// at a server that holds one, and unlocking it for a one-slot server would
// hand graphify's default of four straight back to that failure.
func TestLocalEnvLeavesAOneSlotServerClamped(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
	setEnv(t, OllamaSlotsVar, "1")
	InvalidateLocalProbe()

	if e := LocalEnv(JobEnv(nil), OllamaBackend); e[GraphifyOllamaParallelVar] != "" {
		t.Errorf("a one-slot server was unclamped: %s=%q",
			GraphifyOllamaParallelVar, e[GraphifyOllamaParallelVar])
	}
	// A metered backend is never touched at all.
	if e := LocalEnv(JobEnv(nil), ClaudeCLIBackend); e[GraphifyOllamaParallelVar] != "" {
		t.Errorf("claude-cli got an ollama variable: %v", e)
	}
}

// Somebody who wrote the variable themselves meant it — including "0", which
// is how a user pins a shared server back to serial without editing settings.
func TestLocalEnvYieldsToTheUsersOwnOverlay(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
	setEnv(t, OllamaSlotsVar, "4")
	InvalidateLocalProbe()

	e := LocalEnv(Env{GraphifyOllamaParallelVar: "0"}, OllamaBackend)
	if e[GraphifyOllamaParallelVar] != "0" {
		t.Errorf("the overlay was overwritten: %q", e[GraphifyOllamaParallelVar])
	}
}

// ollama DIVIDES the configured context across its slots, so the two numbers
// are one decision. A sizing that raised the slot count and left the context
// alone would halve every chunk while appearing to double throughput — the
// single most expensive mistake available in this file.
func TestSizeOllamaKeepsTheSlotWidthWhenItAddsSlots(t *testing.T) {
	s := SizeOllama()
	if s.Slots < 1 || s.Slots > MaxOllamaSlots {
		t.Fatalf("Slots = %d, outside [1,%d]", s.Slots, MaxOllamaSlots)
	}
	if s.PerSlot != WantOllamaContext {
		t.Errorf("PerSlot = %d, want %d", s.PerSlot, WantOllamaContext)
	}
	if s.Total != s.Slots*s.PerSlot {
		t.Errorf("Total = %d, want %d x %d — ollama divides it across the slots",
			s.Total, s.Slots, s.PerSlot)
	}
	if s.Why == "" {
		t.Error("a derived number with no derivation is a magic number")
	}
}

// Every variable the started server is given is a default, never an override:
// a value already in the environment is one somebody chose.
func TestOllamaServerEnvYieldsToWhatIsAlreadySet(t *testing.T) {
	for _, v := range []string{
		OllamaParallelVar, OllamaContextVar, OllamaFlashAttnVar,
		OllamaKVCacheVar, OllamaMaxLoadedVar, OllamaKeepAliveVar,
	} {
		setEnv(t, v, "-")
	}
	got := ollamaServerEnv(OllamaSizing{Slots: 2, PerSlot: 32768, Total: 65536})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		OllamaParallelVar + "=2",
		OllamaContextVar + "=65536",
		OllamaFlashAttnVar + "=1",
		OllamaKVCacheVar + "=q8_0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s from %q", want, joined)
		}
	}

	setEnv(t, OllamaParallelVar, "8")
	setEnv(t, OllamaContextVar, "131072")
	joined = strings.Join(ollamaServerEnv(OllamaSizing{Slots: 2, PerSlot: 32768, Total: 65536}), " ")
	if strings.Contains(joined, OllamaParallelVar) || strings.Contains(joined, OllamaContextVar) {
		t.Errorf("the board overrode a value the user set: %q", joined)
	}
}

// The advice is an offer, not a warning: it appears only when the machine can
// actually do better than the server is configured for, and names both halves
// of the change because doing half of it makes things worse.
func TestThroughputAdvice(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")

	setEnv(t, OllamaSlotsVar, "1")
	InvalidateLocalProbe()
	adv := ProbeLocal(OllamaBackend).ThroughputAdvice()
	if want := SizeOllama(); want.Slots > 1 {
		for _, s := range []string{OllamaParallelVar, OllamaContextVar} {
			if !strings.Contains(adv, s) {
				t.Errorf("advice must name %s, or following it halves every chunk: %q", s, adv)
			}
		}
	} else if adv != "" {
		t.Errorf("a one-slot machine was told to add slots: %q", adv)
	}

	// A server already at or past what this machine can feed has nothing to
	// be advised about.
	setEnv(t, OllamaSlotsVar, strconv.Itoa(MaxOllamaSlots+1))
	InvalidateLocalProbe()
	if adv := ProbeLocal(OllamaBackend).ThroughputAdvice(); adv != "" {
		t.Errorf("a server past the cap was still advised: %q", adv)
	}
}

// The notice is the paragraph a user reads before starting hours of work, so
// the concurrency it states has to be the concurrency that will run.
func TestLocalNoticeStatesTheRealConcurrency(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, ctxModelServer(t, 32768, 32768, "qwen2.5-coder:7b")+"/v1")
	setEnv(t, OllamaSlotsVar, "2")
	InvalidateLocalProbe()

	n := LocalNotice(OllamaBackend, "qwen2.5-coder:7b")
	if !strings.Contains(n, "2 chunks at once") {
		t.Errorf("notice does not state the real concurrency: %q", n)
	}
	if strings.Contains(n, "forces one request at a time") {
		t.Errorf("notice still claims the serialization that was just removed: %q", n)
	}
}
