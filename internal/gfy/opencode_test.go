package gfy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// isolateHome points ProvidersPath at a scratch directory and clears every
// credential the backend resolution reads, so a test says what the machine
// running it happens to have exported.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	// Port 0 so a test never fights the board's fixed port, and never fails
	// on a machine where something else holds it.
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)
	for _, k := range []string{
		OpenCodeKeyVar, OpenCodeBaseURLVar,
		"GRAPHIFY_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "MOONSHOT_API_KEY",
		OllamaBaseURLVar, OllamaHostVar,
	} {
		t.Setenv(k, "")
	}
	return home
}

func readProviders(t *testing.T) map[string]map[string]any {
	t.Helper()
	b, err := os.ReadFile(ProvidersPath())
	if err != nil {
		t.Fatalf("reading %s: %v", ProvidersPath(), err)
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatalf("providers.json is not valid JSON: %v\n%s", err, b)
	}
	return all
}

func TestEnsureOpenCodeProviderWritesAnEntryGraphifyCanRead(t *testing.T) {
	isolateHome(t)

	path, wrote, err := EnsureOpenCodeProvider(OpenCodeBackend, "")
	if err != nil || !wrote {
		t.Fatalf("EnsureOpenCodeProvider = %q, %v, %v; want written, no error", path, wrote, err)
	}
	cfg := readProviders(t)[OpenCodeBackend]
	if cfg == nil {
		t.Fatalf("no %q entry in %s", OpenCodeBackend, path)
	}
	// The four fields llm.py indexes by name. default_model in particular is
	// read with cfg["default_model"] and would raise, not default, if absent.
	for k, want := range map[string]string{
		"default_model": DefaultOpenCodeModel,
		"env_key":       OpenCodeKeyVar,
		"model_env_key": OpenCodeModelVar,
	} {
		if got, _ := cfg[k].(string); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// The entry points at the loopback proxy, not the gateway: graphify
	// cannot send the session header the gateway rejects a request without.
	if got, _ := cfg["base_url"].(string); !strings.HasPrefix(got, "http://127.0.0.1:") ||
		!strings.HasSuffix(got, "/v1") {
		t.Errorf("base_url = %q, want the loopback proxy", got)
	}
	// A provider with no pricing is priced at 0.0/0.0 by llm.py, which would
	// report a metered run as free — the one claim this whole board exists to
	// get right.
	pricing, _ := cfg["pricing"].(map[string]any)
	if in, _ := pricing["input"].(float64); in <= 0 {
		t.Errorf("pricing.input = %v, want a non-zero rate", pricing["input"])
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	} else if runtimeIsPOSIX() && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600: the file names where the corpus and key go", fi.Mode().Perm())
	}
}

func runtimeIsPOSIX() bool { return os.PathSeparator == '/' }

func TestTheBaseURLOverrideMovesTheGatewayNotTheEntry(t *testing.T) {
	isolateHome(t)
	t.Setenv(OpenCodeBaseURLVar, "https://gateway.example.test/v1")

	if got := OpenCodeUpstream(OpenCodeBackend); got != "https://gateway.example.test/v1" {
		t.Errorf("OpenCodeUpstream(OpenCodeBackend) = %q, want the override", got)
	}
	if _, _, err := EnsureOpenCodeProvider(OpenCodeBackend, ""); err != nil {
		t.Fatalf("EnsureOpenCodeProvider: %v", err)
	}
	// Still the proxy: the override moves where the proxy forwards to, which
	// is the only place a different gateway can be honoured, because the
	// header injection has to happen either way.
	if got, _ := readProviders(t)[OpenCodeBackend]["base_url"].(string); !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("base_url = %q, want the loopback proxy", got)
	}
}

func TestEnsureOpenCodeProviderKeepsOtherProvidersAndTheUsersOwnEdits(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".graphify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "corp-gateway": {"base_url": "https://llm.corp.test/v1", "default_model": "m", "env_key": "CORP_KEY", "extra_field": 7}
}`
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, wrote, err := EnsureOpenCodeProvider(OpenCodeBackend, ""); err != nil || !wrote {
		t.Fatalf("first call: wrote=%v err=%v", wrote, err)
	}
	all := readProviders(t)
	if all["corp-gateway"] == nil {
		t.Fatal("the unrelated provider was dropped")
	}
	if got := all["corp-gateway"]["extra_field"]; got != float64(7) {
		t.Errorf("extra_field = %v, want it preserved verbatim", got)
	}

	// A second pass with nothing chosen must not reset the registered model,
	// and must not rewrite a file it has nothing to change. Every managed
	// field is moved to the registered model, not just the name: they are all
	// derived from it, so leaving one on the previous model's value is a file
	// that genuinely does need rewriting and would not test what this asserts.
	kimi, ok := OpenCodeModelByID(OpenCodeBackend, "kimi-k2.7-code")
	if !ok {
		t.Fatal("kimi-k2.7-code is not in the catalogue")
	}
	all[OpenCodeBackend]["default_model"] = kimi.ID
	all[OpenCodeBackend]["pricing"] = map[string]any{"input": kimi.Input, "output": kimi.Output}
	all[OpenCodeBackend]["max_tokens"] = OpenCodeOutputBudget(kimi)
	all[OpenCodeBackend]["note"] = "hand-added"
	edited, _ := json.Marshal(all)
	if err := os.WriteFile(ProvidersPath(), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := EnsureOpenCodeProvider(OpenCodeBackend, ""); err != nil || wrote {
		t.Fatalf("second call: wrote=%v err=%v; want no rewrite", wrote, err)
	}
	if got, _ := readProviders(t)[OpenCodeBackend]["default_model"].(string); got != "kimi-k2.7-code" {
		t.Errorf("default_model = %q, want the registered model kept", got)
	}

	// Choosing a model moves the price pair with it — the whole reason this
	// entry is rewritten rather than written once — and leaves a field the
	// board has never heard of alone.
	if _, wrote, err := EnsureOpenCodeProvider(OpenCodeBackend, "mimo-v2.5"); err != nil || !wrote {
		t.Fatalf("third call: wrote=%v err=%v; want a rewrite", wrote, err)
	}
	cfg := readProviders(t)[OpenCodeBackend]
	if got, _ := cfg["default_model"].(string); got != "mimo-v2.5" {
		t.Errorf("default_model = %q, want the chosen model", got)
	}
	want, ok := OpenCodeModelByID(OpenCodeBackend, "mimo-v2.5")
	if !ok {
		t.Fatal("mimo-v2.5 is not in the catalogue")
	}
	pricing, _ := cfg["pricing"].(map[string]any)
	if got, _ := pricing["input"].(float64); got != want.Input {
		t.Errorf("pricing.input = %v, want the chosen model's %v", pricing["input"], want.Input)
	}
	// And the reply cap moves with it, for the reason OpenCodeOutputBudget
	// documents: the entry left on the previous model's ceiling truncates
	// every chunk at a number this model does not need to stop at.
	if got, _ := cfg["max_tokens"].(float64); int(got) != OpenCodeOutputBudget(want) {
		t.Errorf("max_tokens = %v, want the chosen model's %d", cfg["max_tokens"], OpenCodeOutputBudget(want))
	}
	if got, _ := cfg["note"].(string); got != "hand-added" {
		t.Errorf("note = %q, want the hand-added field preserved", got)
	}
}

func TestEnsureOpenCodeProviderRefusesRatherThanOverwriteUnparseableJSON(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".graphify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	half := []byte(`{"corp-gateway": {"base_url": `)
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), half, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := EnsureOpenCodeProvider(OpenCodeBackend, ""); err == nil || wrote {
		t.Fatalf("wrote=%v err=%v; want a refusal", wrote, err)
	}
	b, _ := os.ReadFile(ProvidersPath())
	if string(b) != string(half) {
		t.Errorf("the file was rewritten:\n%s", b)
	}
}

func TestEnsureOpenCodeProviderIsANoOpForEveryOtherBackend(t *testing.T) {
	isolateHome(t)
	for _, b := range []string{"", "openai", ClaudeCLIBackend, OllamaBackend} {
		if path, wrote, err := EnsureOpenCodeProvider(b, ""); path != "" || wrote || err != nil {
			t.Errorf("EnsureOpenCodeProvider(%q) = %q, %v, %v; want silent no-op", b, path, wrote, err)
		}
	}
	if _, err := os.Stat(ProvidersPath()); !os.IsNotExist(err) {
		t.Errorf("providers.json was created for a backend that does not use it")
	}
}

func TestOpenCodeKeyResolvesTheBackendByNameRatherThanLeavingItToDetection(t *testing.T) {
	isolateHome(t)
	restore := probeLocal
	probeLocal = func(string) LocalProbe { return LocalProbe{} }
	t.Cleanup(func() { probeLocal = restore })

	if got := EffectiveBackend(""); got == OpenCodeBackend {
		t.Fatalf("EffectiveBackend() = %q with no key set", got)
	}
	t.Setenv(OpenCodeKeyVar, "sk-test")
	if got := EffectiveBackend(""); got != OpenCodeBackend {
		t.Errorf("EffectiveBackend() = %q, want %q", got, OpenCodeBackend)
	}
	// A vendor key graphify can detect on its own still wins: blank means
	// "let graphify decide" wherever graphify actually can.
	t.Setenv("OPENAI_API_KEY", "sk-vendor")
	if got := EffectiveBackend(""); got != "" {
		t.Errorf("EffectiveBackend() = %q with a vendor key set, want auto-detect", got)
	}
}

func TestBackendReadyOnOpenCodeAnswersAboutTheKeyAndTheProvider(t *testing.T) {
	isolateHome(t)

	ok, why := BackendReady(OpenCodeBackend)
	if ok {
		t.Errorf("ready with no key: %s", why)
	}
	if !strings.Contains(why, OpenCodeKeyVar) {
		t.Errorf("the refusal does not name %s: %s", OpenCodeKeyVar, why)
	}

	t.Setenv(OpenCodeKeyVar, "sk-test")
	ok, why = BackendReady(OpenCodeBackend)
	if !ok {
		t.Fatalf("not ready with a key set: %s", why)
	}
	if !strings.Contains(why, "will be registered") {
		t.Errorf("an unregistered provider should say so: %s", why)
	}
	if _, _, err := EnsureOpenCodeProvider(OpenCodeBackend, ""); err != nil {
		t.Fatal(err)
	}
	if _, why = BackendReady(OpenCodeBackend); !strings.Contains(why, "is registered") {
		t.Errorf("a registered provider should say so: %s", why)
	}
}

func TestOpenCodeIsOfferedAsABackendChoice(t *testing.T) {
	for _, b := range Backends {
		if b == OpenCodeBackend {
			return
		}
	}
	t.Errorf("Backends does not offer %q: %v", OpenCodeBackend, Backends)
}

// resetCatalog drops whatever a previous test fetched, so the baked-in list is
// what OpenCodeCatalog answers with again.
func resetProxy(t *testing.T) {
	t.Helper()
	reset := func() {
		proxyState.Lock()
		proxyState.root, proxyState.base, proxyState.session = "", "", ""
		proxyState.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func resetCatalog(t *testing.T) {
	t.Helper()
	clear := func() {
		catalogCache.Lock()
		catalogCache.byPlan = nil
		catalogCache.Unlock()
		openCodeVerdicts.Range(func(k, _ any) bool {
			openCodeVerdicts.Delete(k)
			return true
		})
	}
	clear()
	t.Cleanup(clear)
}

func TestTheBakedCatalogueCoversTheDefaultModel(t *testing.T) {
	resetCatalog(t)
	m, ok := OpenCodeModelByID(OpenCodeBackend, DefaultOpenCodeModel)
	if !ok {
		t.Fatalf("%q is not in the catalogue", DefaultOpenCodeModel)
	}
	if m.Input <= 0 || m.Output <= 0 || m.Context <= 0 {
		t.Errorf("%q has no usable metadata: %+v", DefaultOpenCodeModel, m)
	}
	// Cheapest first among the models whose price is published, and the
	// unpriced ones last: that order is what makes the dropdown meaningful.
	all := OpenCodeCatalog(OpenCodeBackend)
	for i := 1; i < len(all); i++ {
		if !all[i-1].Priced && all[i].Priced {
			t.Fatalf("an unpriced model sorts before a priced one: %s then %s", all[i-1].ID, all[i].ID)
		}
		if all[i].Priced && all[i-1].Priced && all[i].Input < all[i-1].Input {
			t.Fatalf("catalogue is not sorted by input price: %s ($%v) after %s ($%v)",
				all[i].ID, all[i].Input, all[i-1].ID, all[i-1].Input)
		}
	}
}

func TestRefreshOpenCodeCatalogTakesAvailabilityFromTheGateway(t *testing.T) {
	resetCatalog(t)
	// The gateway serves three models; models.dev knows two of them and one
	// the gateway does NOT serve. The last one must not reach the catalogue —
	// it is the ox-alpha-free case that failed every chunk of a real run.
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"cheap-model"},{"id":"dear-model"},{"id":"unlisted-model"}]}`))
	}))
	defer gw.Close()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"opencode-go":{"models":{
			"cheap-model":{"name":"Cheap","cost":{"input":0.11,"output":0.22},"limit":{"context":500000,"output":8192}},
			"dear-model":{"name":"Dear","cost":{"input":9,"output":30},"limit":{"context":200000,"output":8192}},
			"phantom-model":{"name":"Phantom","cost":{"input":0.01,"output":0.02},"limit":{"context":1000,"output":100}}}}}`))
	}))
	defer md.Close()
	t.Setenv(OpenCodeBaseURLVar, gw.URL+"/v1")
	oldMD := ModelsDevURL
	ModelsDevURL = md.URL
	t.Cleanup(func() { ModelsDevURL = oldMD })

	got, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeBackend)
	if err != nil {
		t.Fatalf("RefreshOpenCodeCatalog: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d models, want the gateway's three: %+v", len(got), got)
	}
	if got[0].ID != "cheap-model" || got[1].ID != "dear-model" {
		t.Errorf("priced models are not cheapest-first: %+v", got)
	}
	// Served but unknown to models.dev: kept, last, and honest about it.
	if got[2].ID != "unlisted-model" || got[2].Priced {
		t.Errorf("an unpriced served model is mishandled: %+v", got[2])
	}
	if _, ok := OpenCodeModelByID(OpenCodeBackend, "phantom-model"); ok {
		t.Error("a models.dev model the gateway does not serve reached the catalogue")
	}

	// A second call inside the TTL must not repeat either fetch.
	gw.Close()
	md.Close()
	if _, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeBackend); err != nil {
		t.Errorf("a cached refresh went back to the network: %v", err)
	}
}

func TestRefreshOpenCodeCatalogKeepsTheBakedListWhenTheGatewayCannotBeReached(t *testing.T) {
	resetCatalog(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gw.Close()
	t.Setenv(OpenCodeBaseURLVar, gw.URL+"/v1")

	got, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeBackend)
	if err == nil {
		t.Fatal("want the fetch error reported")
	}
	if len(got) != len(openCodeCatalog) {
		t.Errorf("got %d models, want the baked-in %d", len(got), len(openCodeCatalog))
	}
	if _, ok := OpenCodeModelByID(OpenCodeBackend, DefaultOpenCodeModel); !ok {
		t.Error("a failed refresh emptied the catalogue")
	}
}

func TestOpenCodeReadyRefusesAModelTheGatewayDoesNotServe(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(OpenCodeKeyVar, "sk-test")
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"served-model"}]}`))
	}))
	defer gw.Close()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"opencode-go":{"models":{}}}`))
	}))
	defer md.Close()
	t.Setenv(OpenCodeBaseURLVar, gw.URL+"/v1")
	oldMD := ModelsDevURL
	ModelsDevURL = md.URL
	t.Cleanup(func() { ModelsDevURL = oldMD })

	// Before a live list exists, nothing here can say a model is wrong.
	if ok, why := OpenCodeReady(OpenCodeBackend, "ox-alpha-free"); !ok {
		t.Fatalf("refused against the baked catalogue: %s", why)
	}
	if _, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeBackend); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	ok, why := OpenCodeReady(OpenCodeBackend, "ox-alpha-free")
	if ok {
		t.Fatal("a model the gateway does not serve was reported ready")
	}
	if !strings.Contains(why, "ox-alpha-free") || !strings.Contains(why, "not supported") {
		t.Errorf("the refusal does not explain itself: %s", why)
	}
	if ok, why := OpenCodeReady(OpenCodeBackend, "served-model"); !ok {
		t.Errorf("a served model was refused: %s", why)
	}
}

func TestOpenCodeModelForSaysWhereTheAnswerCameFrom(t *testing.T) {
	resetCatalog(t)

	m, origin := OpenCodeModelFor(OpenCodeBackend, nil, "")
	if m.ID != DefaultOpenCodeModel || !strings.Contains(origin, "default") {
		t.Errorf("blank = %q from %q, want the board's default", m.ID, origin)
	}

	m, origin = OpenCodeModelFor(OpenCodeBackend, Env{OpenCodeModelVar: "kimi-k2.6"}, "")
	if m.ID != "kimi-k2.6" || !strings.Contains(origin, OpenCodeModelVar) {
		t.Errorf("overlay = %q from %q, want the overlay's model", m.ID, origin)
	}

	// The flag wins over the overlay, because `--model` wins inside graphify.
	m, _ = OpenCodeModelFor(OpenCodeBackend, Env{OpenCodeModelVar: "kimi-k2.6"}, "mimo-v2.5")
	if m.ID != "mimo-v2.5" {
		t.Errorf("model setting = %q, want it to win over the overlay", m.ID)
	}

	// An id this build does not know is passed through, not refused: the
	// gateway is the only thing that can say whether it exists.
	m, origin = OpenCodeModelFor(OpenCodeBackend, nil, "some-model-shipped-tomorrow")
	if m.ID != "some-model-shipped-tomorrow" || !strings.Contains(origin, "catalogue") {
		t.Errorf("unknown = %q from %q, want it passed through with a caveat", m.ID, origin)
	}
}

func TestOpenCodeOutputBudgetAsksForWhatTheModelWillActuallyGenerate(t *testing.T) {
	// The case that motivated the whole function: a chunk of 60_000 tokens of
	// corpus answers at roughly six tenths of what it read, so a cap of
	// 16_384 truncates it. This model allows 384_000 and the entry must say so.
	flash, ok := OpenCodeModelByID(OpenCodeBackend, "deepseek-v4.1-flash")
	if !ok {
		t.Fatal("deepseek-v4.1-flash is not in the catalogue")
	}
	if got := OpenCodeOutputBudget(flash); got != flash.MaxOutput {
		t.Errorf("OpenCodeOutputBudget(%s) = %d, want the model's own %d",
			flash.ID, got, flash.MaxOutput)
	}
	if OpenCodeOutputBudget(flash) <= openCodeMaxTokens {
		t.Errorf("a 384_000-token model still capped at the %d-token fallback", openCodeMaxTokens)
	}

	// A model whose MaxOutput is its entire window cannot be asked for all of
	// it: the corpus has to fit in the same window. The reply is bounded by
	// what is left after the prompt, not by the ceiling the model advertises.
	whole := OpenCodeModel{ID: "w", Context: 500_000, MaxOutput: 500_000, Priced: true}
	if got := OpenCodeOutputBudget(whole); got != 500_000-openCodeChunkHeadroom {
		t.Errorf("OpenCodeOutputBudget(whole-window) = %d, want the window less the prompt (%d)",
			got, 500_000-openCodeChunkHeadroom)
	}

	// A model models.dev has no entry for publishes neither number, and there
	// the old constant is the only figure known to work.
	if got := OpenCodeOutputBudget(OpenCodeModel{ID: "unpriced"}); got != openCodeMaxTokens {
		t.Errorf("OpenCodeOutputBudget(unpriced) = %d, want the %d-token fallback",
			got, openCodeMaxTokens)
	}
	// And a model with a genuinely small ceiling is never talked up past it.
	small := OpenCodeModel{ID: "s", Context: 202_752, MaxOutput: 32_768, Priced: true}
	if got := OpenCodeOutputBudget(small); got != 32_768 {
		t.Errorf("OpenCodeOutputBudget(small) = %d, want the model's own 32768", got)
	}
}

func TestOpenCodeLabelNamesThePriceAndTheWindow(t *testing.T) {
	m := OpenCodeModel{ID: "x", Name: "X", Input: 0.15, Output: 0.5, Context: 1000000, Priced: true}
	if got := m.Label(); !strings.Contains(got, "$0.15/$0.5 per 1M") || !strings.Contains(got, "1000K context") {
		t.Errorf("Label() = %q", got)
	}
	if got := (OpenCodeModel{ID: "f", Priced: true}).Label(); !strings.Contains(got, "free") {
		t.Errorf("a zero-priced model should read as free: %q", got)
	}
	// A price nobody published is not a price of zero — writing it into the
	// provider entry is how a metered run gets reported as free.
	if got := (OpenCodeModel{ID: "u"}).Label(); !strings.Contains(got, "not published") {
		t.Errorf("an unpriced model should say so: %q", got)
	}
}

// resetVerdicts drops the per-model probe cache between tests.
func resetVerdicts(t *testing.T) {
	t.Helper()
	clear := func() {
		openCodeVerdicts.Range(func(k, _ any) bool {
			openCodeVerdicts.Delete(k)
			return true
		})
	}
	clear()
	t.Cleanup(clear)
}

func TestPreflightRefusesAModelTheGatewayWillNotAnswer(t *testing.T) {
	isolateHome(t)
	resetVerdicts(t)
	t.Setenv(OpenCodeKeyVar, "sk-test")

	var asked int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		// The probe must look like the real traffic, or it proves nothing.
		if r.Header.Get("x-opencode-session") == "" {
			t.Error("the probe went out without the session header the gateway requires")
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model == "sulky-model" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Internal server error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer gw.Close()
	t.Setenv(OpenCodeBaseURLVar, gw.URL+"/v1")

	err := PreflightOpenCode(OpenCodeBackend, "sulky-model")
	if err == nil {
		t.Fatal("a model answering 500 was allowed to start a run")
	}
	// The gateway's own words, not graphify's "all semantic chunks failed".
	if !strings.Contains(err.Error(), "Internal server error") || !strings.Contains(err.Error(), "sulky-model") {
		t.Errorf("the refusal does not carry the cause: %v", err)
	}
	if err := PreflightOpenCode(OpenCodeBackend, "willing-model"); err != nil {
		t.Errorf("a working model was refused: %v", err)
	}

	// Cached: a sweep of many repositories pays for one probe per model.
	before := asked
	_ = PreflightOpenCode(OpenCodeBackend, "sulky-model")
	_ = PreflightOpenCode(OpenCodeBackend, "willing-model")
	if asked != before {
		t.Errorf("the gateway was asked again: %d probes, want %d", asked, before)
	}

	// And it reaches the readiness line the confirm dialog prints.
	if ok, why := OpenCodeReady(OpenCodeBackend, "sulky-model"); ok || !strings.Contains(why, "Internal server error") {
		t.Errorf("OpenCodeReady = %v, %q; want the cached refusal", ok, why)
	}

	// Every other backend goes nowhere near this.
	if err := PreflightOpenCode(OllamaBackend, "sulky-model"); err != nil {
		t.Errorf("preflight touched another backend: %v", err)
	}
}

func TestAnUnreachableGatewayIsNotAVerdictAboutTheModel(t *testing.T) {
	isolateHome(t)
	resetVerdicts(t)
	t.Setenv(OpenCodeKeyVar, "sk-test")
	// A port nothing is listening on: the probe cannot complete, which says
	// something about this machine and nothing about the model.
	t.Setenv(OpenCodeBaseURLVar, "http://127.0.0.1:1/v1")

	if err := PreflightOpenCode(OpenCodeBackend, "some-model"); err != nil {
		t.Errorf("an unreachable gateway refused the run: %v", err)
	}
	if known, _, _ := OpenCodeVerdict(OpenCodeBackend, "some-model"); known {
		t.Error("a network failure was cached as a verdict about the model")
	}
}

// Zen's catalogue comes from Zen's gateway and Zen's models.dev provider, and
// a plan with free models defaults to one of them. This is the whole point of
// the plan existing: an extraction that costs nothing is the one worth
// falling back to when nobody has chosen.
func TestTheZenPlanReadsItsOwnCatalogueAndDefaultsToAFreeModel(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"paid-model"},{"id":"free-model"}]}`))
	}))
	defer gw.Close()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"opencode-go":{"models":{"paid-model":{"name":"Not This One","cost":{"input":99,"output":99},"limit":{"context":1000,"output":100}}}},
			"opencode":{"models":{
				"paid-model":{"name":"Paid","cost":{"input":1,"output":3},"limit":{"context":200000,"output":8192}},
				"free-model":{"name":"Free","cost":{"input":0,"output":0},"limit":{"context":250000,"output":16384}}}}}`))
	}))
	defer md.Close()
	t.Setenv(OpenCodeZenBaseURLVar, gw.URL+"/v1")
	oldMD := ModelsDevURL
	ModelsDevURL = md.URL
	t.Cleanup(func() { ModelsDevURL = oldMD })

	// Before the gateway has answered, Zen claims nothing: no baked-in list,
	// so no model ids this build invented.
	if got := OpenCodeCatalog(OpenCodeZenBackend); len(got) != 0 {
		t.Fatalf("Zen started with %d baked-in models, want none: %+v", len(got), got)
	}
	if got := DefaultModelFor(OpenCodeZenBackend); got != "" {
		t.Errorf("DefaultModelFor(zen) = %q before any catalogue, want no claim", got)
	}

	got, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeZenBackend)
	if err != nil {
		t.Fatalf("RefreshOpenCodeCatalog(zen): %v", err)
	}
	if len(got) != 2 || got[0].ID != "free-model" {
		t.Fatalf("Zen catalogue = %+v, want the free model first", got)
	}
	if !got[0].IsFree() || !strings.Contains(got[0].Label(), "free") {
		t.Errorf("the free model is not labelled free: %q", got[0].Label())
	}
	// Zen's prices, not Go's: the same id is published under both providers
	// and reading the wrong one puts a number on screen nobody is charged.
	if got[1].Input != 1 {
		t.Errorf("paid-model priced at %v, want Zen's own price", got[1].Input)
	}
	if want := "free-model"; DefaultModelFor(OpenCodeZenBackend) != want {
		t.Errorf("DefaultModelFor(zen) = %q, want %q", DefaultModelFor(OpenCodeZenBackend), want)
	}
	// And the Go plan is untouched by any of it — separate catalogue, separate
	// default, separate bill.
	if _, ok := OpenCodeModelByID(OpenCodeBackend, "free-model"); ok {
		t.Error("a Zen model leaked into the Go catalogue")
	}
	if DefaultModelFor(OpenCodeBackend) != DefaultOpenCodeModel {
		t.Error("the Go plan's default moved when Zen refreshed")
	}
}

// The two plans are two provider entries, side by side, each pointing at its
// own path on the one proxy. An entry that carried the other plan's endpoint
// or the other plan's model_env_key would bill a run to the wrong plan.
func TestEachPlanRegistersItsOwnProviderEntry(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(OpenCodePortVar, "0")
	resetProxy(t)

	if _, _, err := EnsureOpenCodeProvider(OpenCodeBackend, "glm-5.3-flash"); err != nil {
		t.Fatalf("EnsureOpenCodeProvider(go): %v", err)
	}
	if _, _, err := EnsureOpenCodeProvider(OpenCodeZenBackend, "some-zen-model"); err != nil {
		t.Fatalf("EnsureOpenCodeProvider(zen): %v", err)
	}

	all := readProviders(t)
	goEntry, zenEntry := all[OpenCodeBackend], all[OpenCodeZenBackend]
	if goEntry == nil || zenEntry == nil {
		t.Fatalf("want both entries, got %v", all)
	}
	if got, _ := goEntry["base_url"].(string); !strings.HasSuffix(got, "/v1") || strings.Contains(got, "/zen/") {
		t.Errorf("go base_url = %q, want the proxy's Go path", got)
	}
	if got, _ := zenEntry["base_url"].(string); !strings.HasSuffix(got, "/zen/v1") {
		t.Errorf("zen base_url = %q, want the proxy's Zen path", got)
	}
	if got, _ := zenEntry["model_env_key"].(string); got != OpenCodeZenModelVar {
		t.Errorf("zen model_env_key = %q, want %q", got, OpenCodeZenModelVar)
	}
	if got, _ := goEntry["default_model"].(string); got != "glm-5.3-flash" {
		t.Errorf("go default_model = %q, want the Go model", got)
	}
	if got, _ := zenEntry["default_model"].(string); got != "some-zen-model" {
		t.Errorf("zen default_model = %q, want the Zen model", got)
	}
	// One key, both plans: the credential is the same variable, and it is the
	// endpoint that decides the bill.
	if got, _ := zenEntry["env_key"].(string); got != OpenCodeKeyVar {
		t.Errorf("zen env_key = %q, want %q", got, OpenCodeKeyVar)
	}
}

// The settings dropdown fades a model the gateway refuses, and that fade is
// only as honest as this sweep: it must ask about every model it has not
// asked about, cache what it was told, and record nothing at all when the
// gateway could not be reached.
func TestVerifyOpenCodeCatalogSeparatesARefusalFromAnUnreachableGateway(t *testing.T) {
	resetCatalog(t)
	t.Setenv(OpenCodeKeyVar, "sk-test")

	var asked int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "regional-model") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"Internal server error"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer gw.Close()
	t.Setenv(OpenCodeBaseURLVar, gw.URL+"/v1")

	models := []OpenCodeModel{{ID: "good-model"}, {ID: "regional-model"}}
	seen := map[string]bool{}
	details := map[string]string{}
	VerifyOpenCodeCatalog(context.Background(), OpenCodeBackend, models,
		func(id string, ok bool, detail string) {
			seen[id] = ok
			details[id] = detail
		})

	if len(seen) != 2 {
		t.Fatalf("reported on %d models, want both: %v", len(seen), seen)
	}
	if !seen["good-model"] {
		t.Error("a model that answered was reported as refused")
	}
	if seen["regional-model"] {
		t.Error("a model the gateway refused was reported as available")
	}
	// The gateway's own words, not a generic failure — that is the whole
	// reason the probe exists.
	if !strings.Contains(details["regional-model"], "Internal server error") {
		t.Errorf("refusal detail = %q, want the gateway's message", details["regional-model"])
	}
	if known, ok, _ := OpenCodeVerdict(OpenCodeBackend, "regional-model"); !known || ok {
		t.Error("the refusal was not cached")
	}
	// A verdict for one plan says nothing about the other.
	if known, _, _ := OpenCodeVerdict(OpenCodeZenBackend, "regional-model"); known {
		t.Error("a Go verdict was read as a Zen verdict")
	}

	// A second sweep answers from cache: forty models are not re-probed every
	// time the settings dialog opens.
	before := atomic.LoadInt32(&asked)
	VerifyOpenCodeCatalog(context.Background(), OpenCodeBackend, models, func(string, bool, string) {})
	if atomic.LoadInt32(&asked) != before {
		t.Errorf("the second sweep made %d more requests, want none", atomic.LoadInt32(&asked)-before)
	}

	// An unreachable gateway is not a verdict about anything. Nothing is
	// cached, so the model is asked about again rather than shown as refused.
	resetCatalog(t)
	t.Setenv(OpenCodeBaseURLVar, "http://127.0.0.1:1/v1")
	VerifyOpenCodeCatalog(context.Background(), OpenCodeBackend, []OpenCodeModel{{ID: "good-model"}},
		func(id string, ok bool, detail string) {
			if !ok {
				t.Errorf("%s reported as refused after a connection failure: %q", id, detail)
			}
		})
	if known, _, _ := OpenCodeVerdict(OpenCodeBackend, "good-model"); known {
		t.Error("a connection failure was cached as a verdict about the model")
	}
}

// Among free models the widest window wins, for the reason
// OpenCodeOutputBudget documents: a narrow window is what makes a chunk split,
// retry and lose files, and price cannot break the tie when every candidate
// is free.
func TestTheZenDefaultPrefersTheWidestFreeWindow(t *testing.T) {
	resetCatalog(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"aaa-narrow-free"},{"id":"zzz-wide-free"},{"id":"cheap-paid"}]}`))
	}))
	defer gw.Close()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"opencode":{"models":{
			"aaa-narrow-free":{"name":"Narrow","cost":{"input":0,"output":0},"limit":{"context":32768,"output":4096}},
			"zzz-wide-free":{"name":"Wide","cost":{"input":0,"output":0},"limit":{"context":1000000,"output":131072}},
			"cheap-paid":{"name":"Cheap","cost":{"input":0.1,"output":0.2},"limit":{"context":1000000,"output":131072}}}}}`))
	}))
	defer md.Close()
	t.Setenv(OpenCodeZenBaseURLVar, gw.URL+"/v1")
	oldMD := ModelsDevURL
	ModelsDevURL = md.URL
	t.Cleanup(func() { ModelsDevURL = oldMD })

	if _, err := RefreshOpenCodeCatalog(context.Background(), OpenCodeZenBackend); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := DefaultModelFor(OpenCodeZenBackend); got != "zzz-wide-free" {
		t.Errorf("DefaultModelFor(zen) = %q, want the widest free window", got)
	}
	m, origin := OpenCodeModelFor(OpenCodeZenBackend, nil, "")
	if m.ID != "zzz-wide-free" || !strings.Contains(origin, "free") {
		t.Errorf("OpenCodeModelFor(zen) = %q from %q, want the free default named as free", m.ID, origin)
	}
}
