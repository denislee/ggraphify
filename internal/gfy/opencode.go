package gfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenCode Go is a $10/month subscription to a curated set of open coding
// models, served through an OpenAI-compatible gateway at
// https://opencode.ai/zen/go/v1 with one key. It is not OpenCode Zen: Zen is
// the pay-as-you-go gateway over the same estate plus the frontier models,
// on a different path. Both read the same OPENCODE_API_KEY, so the endpoint
// is what decides which subscription a request is billed against — which is
// why OPENCODE_BASE_URL exists below, and why the board says the URL out loud
// wherever it says the backend name.
//
// graphify 0.9.58 ships no backend by either name, and does not need to:
// llm.py merges every provider in ~/.graphify/providers.json into its backend
// table before detection runs (_load_custom_providers), so a provider
// registered there is a name `--backend` accepts like any built-in.
//
// That is why this file writes a file instead of composing an overlay. The
// alternative — running graphify's `openai` backend with OPENAI_BASE_URL
// repointed and OPENAI_API_KEY set from OPENCODE_API_KEY — would mean copying
// the user's credential through this process's job state, and would leave a
// job whose argv said "openai" indistinguishable from a real OpenAI run in the
// log, the retry path and the cost notice. Registering the provider keeps the
// key where it already is (the inherited environment, read by graphify
// itself) and puts the true name in the argv.
//
// One thing the plan requires that graphify cannot do: every request must
// carry an x-opencode-session header, and llm.py's client has no hook for
// one. opencodeproxy.go is the answer — a loopback proxy that adds it — and
// it is why the entry this file writes points at 127.0.0.1 rather than at the
// gateway. OpenCodeCaveat says so on screen.
const (
	// OpenCodeBackend is the provider name this board registers and passes to
	// `--backend`. It is deliberately not one of graphify's built-ins, which
	// _load_custom_providers refuses to shadow, and it matches the provider id
	// models.dev publishes for this subscription.
	OpenCodeBackend = "opencode-go"

	// OpenCodeKeyVar is the credential graphify reads for this provider — it
	// is the `env_key` of the registered entry, so nothing in ggraphify ever
	// holds the value. The name is OpenCode's own, shared with Zen.
	OpenCodeKeyVar = "OPENCODE_API_KEY"

	// OpenCodeBaseURLVar overrides the endpoint written into the provider
	// entry — a corporate proxy, or OpenCode Zen's own
	// https://opencode.ai/zen/v1 for somebody who is on that plan instead. It
	// is read by this board at registration time, not by graphify: a custom
	// provider's base_url is a literal in the JSON file.
	OpenCodeBaseURLVar = "OPENCODE_BASE_URL"

	// OpenCodeModelVar is the registered entry's `model_env_key`, so it
	// overrides the entry's default the same way GRAPHIFY_OPENAI_MODEL does
	// for the openai backend. The model chosen in settings still wins: it goes
	// in as `--model`, which llm.py prefers over any env default.
	OpenCodeModelVar = "GRAPHIFY_OPENCODE_MODEL"

	// DefaultOpenCodeBaseURL is OpenCode Go's OpenAI-compatible endpoint. Note
	// the `/go/` segment: dropping it is Zen, which is a different plan and a
	// different bill.
	DefaultOpenCodeBaseURL = "https://opencode.ai/zen/go/v1"

	// DefaultOpenCodeModel is what a run with no model chosen asks for. An
	// extraction is a schema-constrained JSON job over one file at a time —
	// the shape a small fast model does well and a frontier model only does
	// expensively — and GLM-5.3-Flash is the cheapest model on the plan that
	// is tested as a coding model rather than as a preview. Every model in
	// OpenCodeCatalog is one dropdown away.
	DefaultOpenCodeModel = "glm-5.3-flash"

	// openCodeMaxTokens matches what every OpenAI-compatible backend in
	// llm.py's own table asks for. Every model on this plan allows far more,
	// so it is the floor and the fallback rather than the answer —
	// OpenCodeOutputBudget explains why asking for it verbatim cost whole
	// files.
	openCodeMaxTokens = 16384

	// openCodeChunkHeadroom is what the prompt side of one extraction request
	// occupies: graphify packs a chunk up to --token-budget, 60_000 tokens of
	// corpus by default, and prepends the schema-bearing system prompt. The
	// reply has to fit in whatever the window has left after that, which is
	// the bound that bites on a model whose MaxOutput is its whole context.
	openCodeChunkHeadroom = 80_000

	// openCodeCatalogTTL is how long a fetched catalogue is trusted. Models
	// come and go on this plan monthly, not hourly.
	openCodeCatalogTTL = time.Hour
)

// ModelsDevURL is the catalogue OpenCodeCatalog is refreshed from — the same
// source the OpenCode clients read, which is why the ids there match the ones
// `/models` on the gateway returns. A variable rather than a constant so a
// test can answer it locally; nothing in the application writes it.
var ModelsDevURL = "https://models.dev/api.json"

// OpenCodeCaveat explains the loopback address in the provider entry, which
// is otherwise the most alarming thing in this backend's configuration.
const OpenCodeCaveat = "The gateway REFUSES a request with no x-opencode-session header — every " +
	"model answers 400 MissingSessionID — and graphify builds its OpenAI client with no way to " +
	"add one. So jobs go through a proxy this board runs on 127.0.0.1, which forwards to the " +
	"gateway adding that header and a User-Agent naming ggraphify, and changes nothing else. " +
	"That is why the provider entry's base_url is a loopback address. Your key is passed " +
	"through untouched and is never read or stored; the proxy accepts connections from this " +
	"machine only, and forwards to the gateway and nowhere else."

// An OpenCodeModel is one model the plan serves, with the two facts that
// decide whether it is the right one for an extraction: what it costs and how
// much of a file it can see at once.
type OpenCodeModel struct {
	ID     string
	Name   string
	Input  float64 // USD per 1M input tokens
	Output float64 // USD per 1M output tokens
	// Context is the model's window, and MaxOutput the most it will generate
	// in one reply. Both come from models.dev.
	Context   int
	MaxOutput int
	// Priced records whether those numbers are published at all. The gateway
	// serves a few models models.dev has no entry for, and a zero price that
	// means "not published" must not be shown — or written into the provider
	// entry's pricing — as if it meant "free".
	Priced bool
}

// Label is the model as the settings dropdown and the confirm dialog name it:
// the id first, because that is what goes on the command line.
func (m OpenCodeModel) Label() string {
	s := m.ID
	if m.Name != "" && m.Name != m.ID {
		s += " — " + m.Name
	}
	switch {
	case !m.Priced:
		s += " · price not published"
	case m.Input == 0 && m.Output == 0:
		s += " · free"
	default:
		s += " · $" + trimFloat(m.Input) + "/$" + trimFloat(m.Output) + " per 1M"
	}
	if m.Context > 0 {
		s += " · " + Itoa(m.Context/1000) + "K context"
	}
	return s
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// OpenCodeOutputBudget is the reply cap the provider entry declares for a
// model — graphify's `max_tokens` — and it has to be the model's own limit
// rather than a constant, because a constant cost this board whole files.
//
// graphify packs a chunk up to --token-budget, 60_000 tokens of corpus by
// default, and asks for one JSON document describing every file in it. An
// extraction answer runs at roughly six tenths of the corpus it read — the
// runs on this machine came back 123_662 in / 73_764 out on one repository and
// 192_026 / 146_386 on another — so a full chunk wants well over 40_000 tokens
// of reply. A 16_384-token cap truncates that answer mid-document, and
// graphify then does one of two things with the fragment. It notices the
// truncation, halves the chunk and pays for the corpus a second time:
//
//	[graphify] chunk of 56 truncated at depth 0, splitting into halves of 28 and 28
//
// or it cannot parse the fragment at all, calls the response hollow, retries
// the same chunk into the same cap, and finishes with files that produced
// nothing:
//
//	[graphify] opencode-go returned a hollow response (content=no nodes/edges, output_tokens=29238)
//	[graphify] WARNING: 4/174 dispatched file(s) produced no nodes and are absent from the graph
//
// which arms the shrink guard and can refuse the write outright. Every model
// on this plan allows far more than 16_384 — the flash models allow 384_000 —
// and a cap costs nothing until it is used, because the bill is per token
// actually generated. Raising it is free; leaving it low is what was expensive.
//
// Two bounds, and the smaller wins. MaxOutput is what the model will honour.
// The window less openCodeChunkHeadroom is what is left for a reply once the
// prompt is in it, which is the bound that matters for a model whose MaxOutput
// is its entire context — asking grok-4.5 for 500_000 tokens of reply inside a
// 500_000-token window leaves nowhere to put the corpus. Both are 0 for a
// model models.dev has no entry for, and there the old constant stands: it is
// the one number already known to work.
func OpenCodeOutputBudget(m OpenCodeModel) int {
	budget := m.MaxOutput
	if m.Context > 0 {
		if room := m.Context - openCodeChunkHeadroom; budget <= 0 || room < budget {
			budget = room
		}
	}
	if budget < openCodeMaxTokens {
		return openCodeMaxTokens
	}
	return budget
}

// openCodeCatalog is the plan's model list as of the date below, baked in so
// the dropdown is populated before any network call and still populated on a
// machine that cannot reach the network at all.
//
// Two sources, and the split matters. WHICH models exist comes from the
// gateway's own /models: models.dev publishes ids for this provider that the
// gateway does not serve — ox-alpha-free is one, and choosing it fails every
// chunk of a run with "Model ox-alpha-free is not supported". What each one
// COSTS comes from models.dev, which is the only published source for it; the
// handful the gateway serves and models.dev has never heard of are listed
// last, unpriced and labelled as such.
//
// Sorted by input price, cheapest first: on a subscription with a per-model
// monthly allowance, the cheap end is where an extraction sweep belongs.
//
// Generated on 2026-09-18 from https://opencode.ai/zen/go/v1/models joined
// with models.dev's opencode-go provider.
var openCodeCatalog = []OpenCodeModel{
	{ID: "muse-spark-1.2-contributor", Name: "Muse Spark 1.2 Contributor", Input: 0.1, Output: 0.2, Context: 1048576, MaxOutput: 131072, Priced: true},
	{ID: "muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", Input: 0.1, Output: 0.2, Context: 1048576, MaxOutput: 131072, Priced: true},
	{ID: "hy3", Name: "Hy3", Input: 0.14, Output: 0.58, Context: 256000, MaxOutput: 128000, Priced: true},
	{ID: "mimo-v2.5", Name: "MiMo V2.5", Input: 0.14, Output: 0.28, Context: 1000000, MaxOutput: 128000, Priced: true},
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Input: 0.15, Output: 0.6, Context: 1000000, MaxOutput: 384000, Priced: true},
	{ID: "deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision Exp", Input: 0.15, Output: 0.6, Context: 1000000, MaxOutput: 384000, Priced: true},
	{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Input: 0.15, Output: 0.6, Context: 1000000, MaxOutput: 384000, Priced: true},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", Input: 0.15, Output: 0.5, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "qwen3.8-flash", Name: "Qwen3.8 Flash", Input: 0.15, Output: 0.47, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", Input: 0.2, Output: 1.2, Context: 1050000, MaxOutput: 128000, Priced: true},
	{ID: "omen-alpha", Name: "Omen Alpha", Input: 0.2, Output: 0.66, Context: 500000, MaxOutput: 128000, Priced: true},
	{ID: "qwen3.5-plus", Name: "Qwen3.5 Plus", Input: 0.2, Output: 1.2, Context: 262144, MaxOutput: 65536, Priced: true},
	{ID: "longcat-2.0", Name: "LongCat-2.0", Input: 0.3, Output: 1.2, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "minimax-m2.5", Name: "MiniMax-M2.5", Input: 0.3, Output: 1.2, Context: 204800, MaxOutput: 65536, Priced: true},
	{ID: "minimax-m2.7", Name: "MiniMax-M2.7", Input: 0.3, Output: 1.2, Context: 204800, MaxOutput: 131072, Priced: true},
	{ID: "minimax-m3", Name: "MiniMax-M3", Input: 0.3, Output: 1.2, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "mimo-v2-omni", Name: "MiMo V2 Omni", Input: 0.4, Output: 2, Context: 262144, MaxOutput: 128000, Priced: true},
	{ID: "qwen3.7-plus", Name: "Qwen3.7 Plus", Input: 0.4, Output: 1.6, Context: 1000000, MaxOutput: 65536, Priced: true},
	{ID: "mimo-v2.5-pro", Name: "MiMo V2.5 Pro", Input: 0.435, Output: 0.87, Context: 1048576, MaxOutput: 128000, Priced: true},
	{ID: "qwen3.6-plus", Name: "Qwen3.6 Plus", Input: 0.5, Output: 3, Context: 1000000, MaxOutput: 65536, Priced: true},
	{ID: "kimi-k2.5", Name: "Kimi K2.5", Input: 0.6, Output: 3, Context: 262144, MaxOutput: 65536, Priced: true},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro (New)", Input: 0.66, Output: 1.98, Context: 1000000, MaxOutput: 384000, Priced: true},
	{ID: "hy4-preview", Name: "Hy4 preview", Input: 0.834, Output: 2.501, Context: 1024000, MaxOutput: 64000, Priced: true},
	{ID: "kimi-k2.6", Name: "Kimi K2.6", Input: 0.95, Output: 4, Context: 262144, MaxOutput: 65536, Priced: true},
	{ID: "kimi-k2.7-code", Name: "Kimi K2.7 Code", Input: 0.95, Output: 4, Context: 262144, MaxOutput: 262144, Priced: true},
	{ID: "glm-5", Name: "GLM-5", Input: 1, Output: 3.2, Context: 202752, MaxOutput: 32768, Priced: true},
	{ID: "mimo-v2-pro", Name: "MiMo V2 Pro", Input: 1, Output: 3, Context: 1048576, MaxOutput: 128000, Priced: true},
	{ID: "glm-5.1", Name: "GLM-5.1", Input: 1.4, Output: 4.4, Context: 202752, MaxOutput: 32768, Priced: true},
	{ID: "glm-5.2", Name: "GLM-5.2", Input: 1.4, Output: 4.4, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "glm-5.3", Name: "GLM-5.3", Input: 1.4, Output: 4.4, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "grok-4.5", Name: "Grok 4.5", Input: 2, Output: 6, Context: 500000, MaxOutput: 500000, Priced: true},
	{ID: "grok-4.6", Name: "Grok 4.6", Input: 2, Output: 6, Context: 500000, MaxOutput: 500000, Priced: true},
	{ID: "qwen3.8-max", Name: "Qwen3.8 Max", Input: 2, Output: 6, Context: 1000000, MaxOutput: 131072, Priced: true},
	{ID: "qwen3.7-max", Name: "Qwen3.7 Max", Input: 2.5, Output: 7.5, Context: 1000000, MaxOutput: 65536, Priced: true},
	{ID: "kimi-k3", Name: "Kimi K3", Input: 3, Output: 15, Context: 1048576, MaxOutput: 131072, Priced: true},
	{ID: "deepseek-flash", Name: "deepseek-flash", Input: 0, Output: 0, Context: 0, MaxOutput: 0, Priced: false},
	{ID: "hy3-preview", Name: "hy3-preview", Input: 0, Output: 0, Context: 0, MaxOutput: 0, Priced: false},
}

var catalogCache struct {
	sync.Mutex
	models  []OpenCodeModel
	fetched time.Time
	// live records that the list came from the gateway rather than from this
	// build's table. Only a live list can say a model does NOT exist, which
	// is the claim OpenCodeReady refuses a run on.
	live bool
}

// OpenCodeCatalog is the model list the dropdown offers: the live one when a
// refresh has succeeded within the last hour, the baked-in one otherwise. It
// never blocks and never touches the network — RefreshOpenCodeCatalog does
// that, from a goroutine the UI owns.
func OpenCodeCatalog() []OpenCodeModel {
	models, _ := openCodeCatalogNow()
	return models
}

func openCodeCatalogNow() (models []OpenCodeModel, live bool) {
	catalogCache.Lock()
	defer catalogCache.Unlock()
	if len(catalogCache.models) > 0 && time.Since(catalogCache.fetched) < openCodeCatalogTTL {
		return append([]OpenCodeModel(nil), catalogCache.models...), catalogCache.live
	}
	return append([]OpenCodeModel(nil), openCodeCatalog...), false
}

// RefreshOpenCodeCatalog fetches the plan's current model list and caches it
// for the process.
//
// The gateway decides which models exist — that is the list a run is actually
// allowed to name, and the one models.dev disagrees with — and models.dev
// supplies the prices and context windows the gateway's own listing omits. A
// models.dev that cannot be reached therefore costs labels, not correctness;
// a gateway that cannot be reached leaves the baked-in list in place, because
// a catalogue that cannot say what exists is worse than one that is a month
// old.
func RefreshOpenCodeCatalog(ctx context.Context) ([]OpenCodeModel, error) {
	catalogCache.Lock()
	fresh := len(catalogCache.models) > 0 && time.Since(catalogCache.fetched) < openCodeCatalogTTL
	cached := append([]OpenCodeModel(nil), catalogCache.models...)
	catalogCache.Unlock()
	if fresh {
		return cached, nil
	}

	served, err := fetchOpenCodeModelIDs(ctx)
	if err != nil {
		return OpenCodeCatalog(), err
	}
	meta, metaErr := fetchModelsDev(ctx)

	out := make([]OpenCodeModel, 0, len(served))
	for _, id := range served {
		if m, ok := meta[id]; ok {
			m.ID = id
			out = append(out, m)
			continue
		}
		out = append(out, OpenCodeModel{ID: id, Name: id})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priced != out[j].Priced {
			return out[i].Priced // unpriced models sort last
		}
		if out[i].Input != out[j].Input {
			return out[i].Input < out[j].Input
		}
		return out[i].ID < out[j].ID
	})

	catalogCache.Lock()
	catalogCache.models, catalogCache.fetched, catalogCache.live = out, time.Now(), true
	catalogCache.Unlock()
	return append([]OpenCodeModel(nil), out...), metaErr
}

// fetchOpenCodeModelIDs reads the gateway's OpenAI-style model listing. It
// needs no credential — the endpoint answers unauthenticated — which is what
// lets the settings page populate before a key is exported.
func fetchOpenCodeModelIDs(ctx context.Context) ([]string, error) {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, strings.TrimSuffix(OpenCodeUpstream(), "/")+"/models", &doc); err != nil {
		return nil, err
	}
	if len(doc.Data) == 0 {
		return nil, errors.New(OpenCodeUpstream() + "/models: served no models")
	}
	ids := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// fetchModelsDev reads the published prices and context windows.
func fetchModelsDev(ctx context.Context) (map[string]OpenCodeModel, error) {
	var doc map[string]struct {
		Models map[string]struct {
			Name string `json:"name"`
			Cost struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"cost"`
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := getJSON(ctx, ModelsDevURL, &doc); err != nil {
		return nil, err
	}
	provider, ok := doc[OpenCodeBackend]
	if !ok {
		return nil, errors.New(ModelsDevURL + ": no " + OpenCodeBackend + " provider")
	}
	out := make(map[string]OpenCodeModel, len(provider.Models))
	for id, m := range provider.Models {
		out[id] = OpenCodeModel{
			ID: id, Name: m.Name,
			Input: m.Cost.Input, Output: m.Cost.Output,
			Context: m.Limit.Context, MaxOutput: m.Limit.Output,
			Priced: true,
		}
	}
	return out, nil
}

func getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// models.dev refuses a bare library User-Agent, and naming the client is
	// what OpenCode asks for anyway.
	req.Header.Set("User-Agent", "ggraphify/"+AppVersion)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{url: url, code: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(into)
}

type httpStatusError struct {
	url  string
	code int
}

func (e *httpStatusError) Error() string { return e.url + ": HTTP " + Itoa(e.code) }

// OpenCodeModelByID finds a model in the catalogue. A miss is not a refusal:
// a model this build has never heard of is still passed to the gateway, which
// is the only thing that can actually say whether it exists.
func OpenCodeModelByID(id string) (OpenCodeModel, bool) {
	id = strings.TrimSpace(id)
	for _, m := range OpenCodeCatalog() {
		if m.ID == id {
			return m, true
		}
	}
	return OpenCodeModel{ID: id}, false
}

// OpenCodeModelFor is the model an opencode-go job will actually run, and
// where that answer came from — the same shape as ClaudeCLIModelFor, and for
// the same reason: the settings page and the confirm dialog must name the
// same model for the same reason.
//
// The order is the one that actually applies at run time: the chosen model
// goes in as `--model`, which llm.py prefers over everything; the overlay's
// GRAPHIFY_OPENCODE_MODEL is the entry's model_env_key, read when there is no
// flag; and the entry's own default is the floor.
func OpenCodeModelFor(overlay Env, model string) (m OpenCodeModel, origin string) {
	if v := strings.TrimSpace(model); v != "" {
		found, known := OpenCodeModelByID(v)
		if !known {
			return found, "the model setting (not in this build's catalogue — the gateway decides)"
		}
		return found, "the model setting"
	}
	if v := strings.TrimSpace(overlay[OpenCodeModelVar]); v != "" {
		found, _ := OpenCodeModelByID(v)
		return found, "the overlay's " + OpenCodeModelVar
	}
	found, _ := OpenCodeModelByID(DefaultOpenCodeModel)
	return found, "the board's default for this plan"
}

// OpenCodeUpstream is the gateway itself — what the proxy forwards to, and
// what OPENCODE_BASE_URL moves: a corporate proxy, or OpenCode Zen's
// https://opencode.ai/zen/v1 for somebody on that plan instead.
//
// It is NOT what goes into the provider entry. That is the loopback proxy,
// because graphify cannot send the session header the gateway requires; see
// opencodeproxy.go.
func OpenCodeUpstream() string {
	if v := strings.TrimSpace(os.Getenv(OpenCodeBaseURLVar)); v != "" {
		return v
	}
	return DefaultOpenCodeBaseURL
}

// HasOpenCodeKey reports whether an OpenCode key is visible in this
// environment. Like every other credential here it is asked about, never read.
func HasOpenCodeKey() bool { return strings.TrimSpace(os.Getenv(OpenCodeKeyVar)) != "" }

// ProvidersPath is graphify's global custom-provider file. The project-local
// .graphify/providers.json is deliberately not touched: llm.py ignores that
// one unless GRAPHIFY_ALLOW_LOCAL_PROVIDERS is set, precisely because a file
// that travels with a cloned repository can redirect a corpus and a key.
func ProvidersPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".graphify", "providers.json")
}

// OpenCodeProvider is what ~/.graphify/providers.json currently says about
// this provider: the endpoint it points at, the model it defaults to, and
// whether it is there at all.
func OpenCodeProvider() (baseURL, model string, registered bool) {
	path := ProvidersPath()
	if path == "" {
		return "", "", false
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a fixed path under the user's own home
	if err != nil {
		return "", "", false
	}
	var all map[string]map[string]any
	if json.Unmarshal(b, &all) != nil {
		return "", "", false
	}
	cfg, ok := all[OpenCodeBackend]
	if !ok {
		return "", "", false
	}
	base, _ := cfg["base_url"].(string)
	mdl, _ := cfg["default_model"].(string)
	return base, mdl, true
}

// EnsureOpenCodeProvider registers this provider in
// ~/.graphify/providers.json when a job is about to ask for it, and does
// nothing at all for any other backend.
//
// The six fields below are the board's, and are rewritten whenever the
// chosen model or the endpoint changes. Pricing is the reason it cannot be
// write-once: llm.py estimates a run's cost from the entry's single price
// pair, so an entry left on last month's model reports the wrong number for
// this month's — and an entry with no pricing at all is read as 0.0/0.0,
// which reports a metered run as free. max_tokens is model-derived for the
// same reason and joined the set for it; see OpenCodeOutputBudget. Any other
// field a user adds by hand survives, as does every other provider in the
// file: it is decoded as raw JSON per name and re-encoded.
func EnsureOpenCodeProvider(backend, model string) (path string, wrote bool, err error) {
	if strings.TrimSpace(backend) != OpenCodeBackend {
		return "", false, nil
	}
	path = ProvidersPath()
	if path == "" {
		return "", false, os.ErrNotExist
	}
	// The entry points at the proxy, so the proxy has to exist before the
	// entry claims it does. Starting it here rather than at the call site
	// keeps the two facts — where graphify will connect, and what is
	// listening there — written by one function.
	proxy, err := StartOpenCodeProxy()
	if err != nil {
		return path, false, err
	}

	all := map[string]json.RawMessage{}
	switch b, rerr := os.ReadFile(path); { // #nosec G304 -- a fixed path under the user's own home
	case rerr == nil:
		if len(strings.TrimSpace(string(b))) > 0 {
			if uerr := json.Unmarshal(b, &all); uerr != nil {
				// Refusing beats overwriting: the file holds other providers,
				// and a parse failure is as likely to be a half-finished edit
				// as it is corruption.
				return path, false, uerr
			}
		}
	case !os.IsNotExist(rerr):
		return path, false, rerr
	}

	// Start from whatever is already there, so a field this board does not
	// know about — a header map a future graphify grows, a comment key — is
	// not deleted by a refresh of the price.
	entry := map[string]any{}
	if raw, ok := all[OpenCodeBackend]; ok {
		if uerr := json.Unmarshal(raw, &entry); uerr != nil {
			entry = map[string]any{}
		}
	}
	// A blank model means "whatever is registered" rather than "reset to the
	// board's default": ggraphify-job takes its model from a flag and a run
	// without one must not undo the model chosen in settings.
	if strings.TrimSpace(model) == "" {
		if prev, _ := entry["default_model"].(string); strings.TrimSpace(prev) != "" {
			model = prev
		}
	}
	m, _ := OpenCodeModelFor(nil, model)
	managed := map[string]any{
		"base_url":      proxy,
		"default_model": m.ID,
		"env_key":       OpenCodeKeyVar,
		"model_env_key": OpenCodeModelVar,
		"pricing":       map[string]float64{"input": m.Input, "output": m.Output},
		// Managed for the same reason pricing is, and it took the same kind of
		// evidence to learn it: the reply cap belongs to the model, not to the
		// board. Written once it would hold the last model's ceiling, so
		// moving from a 32_768-token model to one that allows 384_000 would
		// keep truncating every chunk at the old number.
		"max_tokens": OpenCodeOutputBudget(m),
	}
	same := true
	for k, v := range managed {
		if !sameJSON(entry[k], v) {
			entry[k] = v
			same = false
		}
	}
	if _, ok := entry["temperature"]; !ok {
		entry["temperature"], same = 0, false
	}
	if same {
		return path, false, nil
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		return path, false, err
	}
	all[OpenCodeBackend] = raw

	// Sorted keys and an indent, because this is a file a human edits after
	// we have written it, and Go's map order would reshuffle it on every
	// rewrite for no reason.
	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)
	var buf strings.Builder
	buf.WriteString("{\n")
	for i, k := range names {
		var pretty bytes.Buffer
		if ierr := json.Indent(&pretty, all[k], "  ", "  "); ierr != nil {
			return path, false, ierr
		}
		key, _ := json.Marshal(k)
		buf.WriteString("  " + string(key) + ": " + pretty.String())
		if i < len(names)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, false, err
	}
	// 0600: the file names an endpoint the corpus and the key are sent to, so
	// it is only writable by the account whose key that is.
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// sameJSON compares a value decoded from the file with one about to be
// written to it. Decoded JSON numbers are float64 and decoded maps are
// map[string]any, so a direct == against a map[string]float64 would report
// every entry as changed and rewrite the file on every submit.
func sameJSON(have, want any) bool {
	a, err1 := json.Marshal(have)
	b, err2 := json.Marshal(want)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

// A model can be in the gateway's own /models and still refuse every request:
//
//	muse-spark-1.2-contributor → 500 "Internal server error"
//	muse-spark-1.3-contributor → 500 "Internal server error"
//	glm-5.3-flash              → 200
//
// The plan's documentation marks the Muse Spark Contributor models "limited
// regions", and a region this account is not in presents as that 500 rather
// than as an absence from the listing. Nothing readable — not models.dev, not
// /models — distinguishes the two, so the only honest test is to ask the
// gateway for one token and see what comes back.
//
// That is what this is: one probe per model per process, cached, costing
// single-digit tokens. It turns graphify's "all semantic chunks failed for
// backend 'opencode-go' (25 uncached files)" — the same sentence for every
// possible cause — into the gateway's own words, before a run rather than
// twenty-five failures into one.
type openCodeVerdict struct {
	ok     bool
	detail string
	at     time.Time
}

var openCodeVerdicts sync.Map // model id -> openCodeVerdict

// openCodeVerdictTTL keeps a verdict long enough to cover a sweep and short
// enough that a model fixed upstream is retried the same afternoon.
const openCodeVerdictTTL = 30 * time.Minute

// OpenCodeVerdict is the cached answer for a model, without touching the
// network: known is false when it has never been asked.
func OpenCodeVerdict(model string) (known, ok bool, detail string) {
	v, hit := openCodeVerdicts.Load(strings.TrimSpace(model))
	if !hit {
		return false, false, ""
	}
	verdict := v.(openCodeVerdict)
	if time.Since(verdict.at) > openCodeVerdictTTL {
		return false, false, ""
	}
	return true, verdict.ok, verdict.detail
}

// VerifyOpenCodeModel asks the gateway to answer one token with this model and
// caches what it said. A model that has already answered inside the TTL is not
// asked again.
//
// A network failure is NOT a verdict: it says something about this machine's
// connection, not about the model, and recording it would refuse a run for the
// wrong reason. Only an answer from the gateway is cached.
func VerifyOpenCodeModel(ctx context.Context, model string) (bool, string) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultOpenCodeModel
	}
	if known, ok, detail := OpenCodeVerdict(model); known {
		return ok, detail
	}
	key := strings.TrimSpace(os.Getenv(OpenCodeKeyVar))
	if key == "" {
		return false, OpenCodeKeyVar + " is not set, so the gateway cannot be asked about " + model + "."
	}

	body := `{"model":` + jsonString(model) +
		`,"messages":[{"role":"user","content":"ping"}],"max_tokens":8,"temperature":0}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(OpenCodeUpstream(), "/")+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return true, "" // cannot even build the request: not the model's fault
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	// The same two headers the proxy adds to a job's requests, because a probe
	// that skipped them would fail for a reason the real traffic does not have.
	req.Header.Set("x-opencode-session", OpenCodeSession())
	req.Header.Set("User-Agent", "ggraphify/"+AppVersion)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return true, "" // unreachable now says nothing about the model
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	verdict := openCodeVerdict{ok: resp.StatusCode == http.StatusOK, at: time.Now()}
	if !verdict.ok {
		verdict.detail = model + ": the gateway answered HTTP " + Itoa(resp.StatusCode)
		if msg := gatewayErrorMessage(raw); msg != "" {
			verdict.detail += " — " + msg
		}
	}
	openCodeVerdicts.Store(model, verdict)
	return verdict.ok, verdict.detail
}

// gatewayErrorMessage digs the human sentence out of the gateway's error
// envelope: {"type":"error","error":{"type":"...","message":"..."}}.
func gatewayErrorMessage(raw []byte) string {
	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &doc) == nil && doc.Error.Message != "" {
		return doc.Error.Message
	}
	return ""
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// PreflightOpenCode is the check a job runs before it is queued: is this model
// one the gateway will actually answer with. It is a no-op for every other
// backend, and for a model already verified in this process.
func PreflightOpenCode(backend, model string) error {
	if strings.TrimSpace(backend) != OpenCodeBackend {
		return nil
	}
	m, _ := OpenCodeModelFor(nil, model)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if ok, detail := VerifyOpenCodeModel(ctx, m.ID); !ok {
		return errors.New(detail + ". Pick another model under Settings ▸ OpenCode Go — " +
			"a model can be listed by the gateway and still refuse this account or region")
	}
	return nil
}

// OpenCodeReady is BackendReady for this provider: the key, the model, and
// the provider entry that makes the name mean anything to graphify.
//
// The entry not being there yet is not an error — SubmitCmd writes it on the
// way to launching the job — so it is reported as what will happen rather than
// as something wrong.
func OpenCodeReady(model string) (bool, string) {
	if !HasOpenCodeKey() {
		return false, "The " + OpenCodeBackend + " backend needs " + OpenCodeKeyVar +
			" exported in this environment, and none is visible. Subscribe at " +
			"https://opencode.ai/docs/go, copy the key, export it and relaunch the board — " +
			"ggraphify never stores a key itself."
	}
	_, _, registered := OpenCodeProvider()
	// The endpoint worth naming is the gateway, not the loopback address the
	// entry carries: the proxy is an implementation detail of getting the
	// session header on, and saying "127.0.0.1" here would only puzzle
	// somebody checking where their corpus goes.
	where := OpenCodeUpstream()
	m, origin := OpenCodeModelFor(nil, model)
	// A model the gateway does not serve fails every chunk of a run with
	// "Model <id> is not supported", which surfaces as graphify's opaque "all
	// semantic chunks failed". It is worth catching in the confirm dialog
	// instead — but only against a list actually fetched from the gateway,
	// never against this build's table, which is a month old by construction.
	if catalogue, live := openCodeCatalogNow(); live {
		served := false
		for _, c := range catalogue {
			if c.ID == m.ID {
				served = true
				break
			}
		}
		if !served {
			return false, "The gateway's own model list does not include " + m.ID +
				" — a run against it fails every chunk with \"Model " + m.ID +
				" is not supported\". Pick another model under OpenCode Go."
		}
	}
	// A model already caught refusing is worth saying before the run, not
	// after twenty-five chunks have failed on it.
	if known, ok, detail := OpenCodeVerdict(m.ID); known && !ok {
		return false, detail + ". Pick another model below — a model can be listed by the " +
			"gateway and still refuse this account or region."
	}
	s := OpenCodeKeyVar + " is visible in this environment; requests go to " + where +
		", billed to that OpenCode Go subscription.\nModel: " + m.Label() + " (from " + origin + ")."
	if registered {
		return true, s + " The provider is registered in " + Tilde(ProvidersPath()) + "."
	}
	return true, s + " The provider will be registered in " + Tilde(ProvidersPath()) +
		" when the first job is submitted — graphify has no built-in backend by this " +
		"name, and reads custom ones from that file."
}
