package gfy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// W8: providers.json is read, edited and rewritten by every submit. Writers
// for different plans racing each other — two boards, or a board and a
// ggraphify-job — must neither drop each other's entries nor leave a file
// nothing can parse.
func TestConcurrentProviderWritesKeepEveryEntry(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)

	writes := []struct{ backend, model string }{
		{OpenCodeBackend, "glm-5.3-flash"},
		{OpenCodeZenBackend, "some-zen-model"},
		{OllamaCloudBackend, "gpt-oss:20b"},
	}
	for round := 0; round < 25; round++ {
		_ = os.Remove(ProvidersPath())
		var wg sync.WaitGroup
		for _, w := range writes {
			wg.Add(1)
			go func(backend, model string) {
				defer wg.Done()
				if _, _, err := EnsureOpenCodeProvider(backend, nil, model); err != nil {
					t.Errorf("round %d: EnsureOpenCodeProvider(%s): %v", round, backend, err)
				}
			}(w.backend, w.model)
		}
		wg.Wait()
		all := readProviders(t) // fails the test on unparseable JSON
		for _, w := range writes {
			if _, ok := all[w.backend]; !ok {
				t.Fatalf("round %d: the %s entry was lost to a concurrent write: %v", round, w.backend, all)
			}
		}
	}
	// The rename leaves nothing behind but the file and its lock.
	entries, err := os.ReadDir(filepath.Dir(ProvidersPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n := e.Name(); n != "providers.json" && n != "providers.json.lock" {
			t.Errorf("stray file left beside providers.json: %s", n)
		}
	}
}

// W9: the Ollama Cloud key has a name of its own. A local deep pass sources
// GRAFT_API_KEY from OLLAMA_API_KEY, so a cloud key exported under that name
// would be sent to whatever OLLAMA_HOST or OPENAI_BASE_URL points at.
func TestTheOllamaCloudKeyNeverReachesALocalTarget(t *testing.T) {
	if OllamaCloudKeyVar == OllamaKeyVar {
		t.Fatalf("OllamaCloudKeyVar = %q, the local server's own variable", OllamaCloudKeyVar)
	}
	t.Setenv(OllamaKeyVar, "")
	t.Setenv(OllamaCloudKeyVar, "sk-paid-cloud-key")

	e := GraftDeepEnv(Env{}, GraftDeepKind, OllamaBackend)
	if got := e[GraftKeyVar]; got == "sk-paid-cloud-key" {
		t.Fatalf("the Ollama Cloud key was exported as %s to a local target", GraftKeyVar)
	}
	if got := e[GraftKeyVar]; got != GraftLocalKey {
		t.Errorf("%s = %q, want the placeholder %q", GraftKeyVar, got, GraftLocalKey)
	}
}

// W22: with only the Ollama Cloud key exported, the board must see a key,
// auto-detect must name ollama-cloud, and readiness must not say "no key".
func TestAnOllamaCloudKeyAloneIsAKey(t *testing.T) {
	noCredentials(t)
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(ClaudeCLIBinVar, "off")
	t.Setenv(OllamaCloudKeyVar, "sk-ollama-cloud")

	if !HasAPIKey() {
		t.Error("HasAPIKey() = false with " + OllamaCloudKeyVar + " exported")
	}
	if got := EffectiveBackend(""); got != OllamaCloudBackend {
		t.Errorf("EffectiveBackend(\"\") = %q, want %q", got, OllamaCloudBackend)
	}
	if ok, why := BackendReady(EffectiveBackend("")); !ok {
		t.Errorf("BackendReady(auto) = false: %s", why)
	}
	// And the local server's own variable is NOT a cloud key.
	t.Setenv(OllamaCloudKeyVar, "")
	t.Setenv(OllamaKeyVar, "sk-local")
	if got := EffectiveBackend(""); got == OllamaCloudBackend {
		t.Error("OLLAMA_API_KEY alone resolved auto-detect to ollama-cloud")
	}
}

// W24: the overlay editor offers only variables an overlay entry can move.
// The base-URL variables are read from the board's own environment by the
// process-wide proxy, so offering them per repository did nothing.
func TestKnownVarsOfferNoInertBaseURLs(t *testing.T) {
	for _, v := range KnownVars {
		switch v {
		case OpenCodeBaseURLVar, OpenCodeZenBaseURLVar, OllamaCloudBaseURLVar:
			t.Errorf("KnownVars offers %s, which no overlay entry can affect", v)
		}
	}
}

// W25: with no model chosen, the overlay's model_env_key is the model graphify
// runs, so it is the one providers.json must price and readiness must name.
func TestTheOverlayModelIsTheOneRegistered(t *testing.T) {
	isolateHome(t)
	resetCatalog(t)
	t.Setenv(OllamaCloudKeyVar, "sk-ollama-cloud")
	overlay := Env{OllamaCloudModelVar: "glm-5.3"}

	// A prior entry on another model must not outrank the overlay either.
	if _, _, err := EnsureOpenCodeProvider(OllamaCloudBackend, nil, "gpt-oss:20b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureOpenCodeProvider(OllamaCloudBackend, overlay, ""); err != nil {
		t.Fatal(err)
	}
	entry := readProviders(t)[OllamaCloudBackend]
	if got, _ := entry["default_model"].(string); got != "glm-5.3" {
		t.Errorf("default_model = %q, want the overlay's glm-5.3", got)
	}
	want, _ := OpenCodeModelByID(OllamaCloudBackend, "glm-5.3")
	pricing, _ := entry["pricing"].(map[string]any)
	if in, _ := pricing["input"].(float64); in != want.Input {
		t.Errorf("pricing.input = %v, want glm-5.3's %v", in, want.Input)
	}

	if ok, why := OpenCodeReadyWith(OllamaCloudBackend, overlay, ""); !ok || !strings.Contains(why, "glm-5.3 ") && !strings.Contains(why, "GLM-5.3 ") {
		t.Errorf("OpenCodeReadyWith = %v, %q; want it to name the overlay's model", ok, why)
	}
}

// W25: the preflight asks the gateway about the model the overlay selects,
// not the plan's default.
func TestPreflightAsksAboutTheOverlayModel(t *testing.T) {
	isolateHome(t)
	resetVerdicts(t)
	t.Setenv(OllamaCloudKeyVar, "sk-ollama-cloud")
	// A model verdict already cached as refused: only a preflight that resolves
	// the overlay's model sees it.
	openCodeVerdicts.Store(verdictKey(OllamaCloudBackend, "glm-5.3"),
		openCodeVerdict{ok: false, detail: "glm-5.3 refused this account", at: time.Now()})
	t.Setenv(OllamaCloudBaseURLVar, "http://127.0.0.1:1/v1") // never reached for a cached verdict

	err := PreflightOpenCode(OllamaCloudBackend, Env{OllamaCloudModelVar: "glm-5.3"}, "")
	if err == nil || !strings.Contains(err.Error(), "glm-5.3") {
		t.Errorf("PreflightOpenCode with the overlay = %v; want the overlay model's refusal", err)
	}
}

// W26: a detached ollama that exits on its own must not leave its pid
// recorded, or a later idle stop signals a recycled process group.
func TestAnOllamaThatExitsOnItsOwnIsForgotten(t *testing.T) {
	resetOllamaStart(t)
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start a child process here: ", err)
	}
	noteOllamaStart(startedDetached, cmd.Process.Pid)
	reapOllama(cmd)
	if StartedOllama() {
		t.Fatal("StartedOllama() = true after the server exited by itself")
	}
	if _, err := StopOllama(); !NotOurs(err) {
		t.Errorf("StopOllama() after the exit = %v, want errNotOurs", err)
	}

	// A newer start under a different pid is not the reaped one's to clear.
	noteOllamaStart(startedDetached, 999999)
	forgetOllamaPID(123)
	if !StartedOllama() {
		t.Error("forgetOllamaPID cleared a start that belonged to another pid")
	}
}

// W38: the baked Ollama Cloud catalogue offers only models the gateway serves.
func TestOllamaCloudCatalogOmitsUnservedModels(t *testing.T) {
	resetCatalog(t)
	for _, m := range bakedCatalog(OllamaCloudBackend) {
		if m.ID == "deepseek-v4-pro" {
			t.Errorf("the baked Ollama Cloud catalogue offers %s, which the gateway does not serve", m.ID)
		}
	}
}
