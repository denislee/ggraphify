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

	"github.com/dns/ggraphify/internal/applog"
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

	// OpenCodeZenBackend is the same idea for the other plan. Zen is
	// pay-as-you-go over a wider estate — the same open models, the frontier
	// ones, and a handful the gateway serves at no charge at all — and it is
	// a DIFFERENT BILL reached over a different path with the same key. It
	// gets its own backend name for exactly that reason: a run billed to Zen
	// must not appear in the argv, the log and the cost notice as a run
	// against the Go subscription's monthly allowance. Its provider entry is
	// a second, separate entry in ~/.graphify/providers.json.
	OpenCodeZenBackend = "opencode-zen"

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

	// OpenCodeZenBaseURLVar is the same override for the Zen plan. Two
	// variables rather than one, because the two plans now exist side by side
	// in one process: a single variable could only move whichever endpoint was
	// selected at the moment it was read, and a board that had moved BOTH
	// gateways to one URL would be quietly billing one plan's runs to the
	// other.
	OpenCodeZenBaseURLVar = "OPENCODE_ZEN_BASE_URL"

	// OpenCodeModelVar is the registered entry's `model_env_key`, so it
	// overrides the entry's default the same way GRAPHIFY_OPENAI_MODEL does
	// for the openai backend. The model chosen in settings still wins: it goes
	// in as `--model`, which llm.py prefers over any env default.
	OpenCodeModelVar = "GRAPHIFY_OPENCODE_MODEL"

	// OpenCodeZenModelVar is the Zen entry's own model_env_key. Separate for
	// the reason store.Settings keeps a separate model per plan: a model id is
	// not portable between the two catalogues, and one variable feeding both
	// entries would send a Go id to Zen the first time the backend moved.
	OpenCodeZenModelVar = "GRAPHIFY_OPENCODE_ZEN_MODEL"

	// DefaultOpenCodeBaseURL is OpenCode Go's OpenAI-compatible endpoint. Note
	// the `/go/` segment: dropping it is Zen, which is a different plan and a
	// different bill.
	DefaultOpenCodeBaseURL = "https://opencode.ai/zen/go/v1"

	// DefaultOpenCodeZenBaseURL is the same gateway without the `/go/`
	// segment: OpenCode Zen, pay-as-you-go, over the same key.
	DefaultOpenCodeZenBaseURL = "https://opencode.ai/zen/v1"

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

// An OpenCodePlan is one of the two OpenCode subscriptions, and everything
// that differs between them. They differ in more than a URL: the endpoint
// decides the bill, the models.dev provider decides the prices, the model
// env key and the provider entry are per plan, and the proxy needs a path of
// its own per plan so one loopback port can serve both.
//
// It is a value rather than a mode switch on purpose. A package-level
// "current plan" would be read by the proxy handler, the catalogue fetch and
// the verifier at three different moments, and a settings change between two
// of them would bill a Zen run to the Go allowance. Every function that can
// reach a gateway takes the backend name and resolves the plan itself.
type OpenCodePlan struct {
	// Backend is the provider name in the argv and in providers.json.
	Backend string
	// Name is the plan as its documentation calls it.
	Name string
	// DefaultURL is the gateway, and URLVar the variable that moves it.
	DefaultURL string
	URLVar     string
	// ModelsDevProvider is the provider id to read prices and context windows
	// from. The two plans are published separately there, and joining one
	// plan's models to the other's prices would put a number on screen that
	// nobody will be charged.
	ModelsDevProvider string
	// ModelVar is the entry's model_env_key.
	ModelVar string
	// ProxyPath is the path prefix the loopback proxy serves this plan on.
	// The Go plan keeps "/v1" so a provider entry written by an older build
	// still points somewhere real.
	ProxyPath string
	// DefaultModel is what a run with nothing chosen asks for, or "" when the
	// plan has no fixed answer and the catalogue decides; see
	// DefaultModelFor.
	DefaultModel string
	// Billing is the sentence the confirm dialog says about where the money
	// comes from.
	Billing string
}

// openCodePlans is the pair, Go first.
var openCodePlans = []OpenCodePlan{{
	Backend:           OpenCodeBackend,
	Name:              "OpenCode Go",
	DefaultURL:        DefaultOpenCodeBaseURL,
	URLVar:            OpenCodeBaseURLVar,
	ModelsDevProvider: "opencode-go",
	ModelVar:          OpenCodeModelVar,
	ProxyPath:         "/v1",
	DefaultModel:      DefaultOpenCodeModel,
	Billing: "drawn from that $10/month subscription's monthly allowance for this " +
		"model rather than from an API balance",
}, {
	Backend:           OpenCodeZenBackend,
	Name:              "OpenCode Zen",
	DefaultURL:        DefaultOpenCodeZenBaseURL,
	URLVar:            OpenCodeZenBaseURLVar,
	ModelsDevProvider: "opencode",
	ModelVar:          OpenCodeZenModelVar,
	ProxyPath:         "/zen/v1",
	// No constant: Zen's catalogue is not this build's to know — it is wider
	// than Go's, it moves faster, and the models worth defaulting to are the
	// ones it serves at no charge, which is a fact only the live catalogue
	// carries. DefaultModelFor picks from what the gateway actually said.
	DefaultModel: "",
	Billing: "billed per token to that OpenCode account, at the price shown against the " +
		"model — except the models the gateway serves free, which are labelled as such",
}}

// OpenCodePlans is both plans, for a caller building a picker.
func OpenCodePlans() []OpenCodePlan {
	return append([]OpenCodePlan(nil), openCodePlans...)
}

// IsOpenCodeBackend reports whether a backend name is one of the two OpenCode
// plans. Every `== OpenCodeBackend` that meant "is this OpenCode" rather than
// "is this specifically the Go plan" is this instead.
func IsOpenCodeBackend(backend string) bool {
	b := strings.TrimSpace(backend)
	return b == OpenCodeBackend || b == OpenCodeZenBackend
}

// OpenCodePlanFor resolves a backend name to its plan. Anything that is not
// Zen resolves to Go, so a caller that has not been taught about plans at all
// behaves exactly as it did before this existed.
func OpenCodePlanFor(backend string) OpenCodePlan {
	if strings.TrimSpace(backend) == OpenCodeZenBackend {
		return openCodePlans[1]
	}
	return openCodePlans[0]
}

// DefaultModelFor is the model a run against this plan asks for when nothing
// has been chosen.
//
// For the Go plan that is a constant. For Zen it is read out of the
// catalogue, because Zen bills per token and the model to fall back to is one
// that costs nothing — which models those are is a fact the gateway and
// models.dev publish rather than one this build can bake in. Of the free ones
// it takes the widest context window, for the reason OpenCodeOutputBudget
// documents at length: a chunk is 60_000 tokens of corpus and the reply runs
// at six tenths of it, so a narrow window is what makes a run split, retry
// and lose files. Among free models that is the only axis left to choose on.
//
// An empty answer — no catalogue yet, no network since launch — is honest: it
// means the board has nothing to claim, and the settings page says so rather
// than naming an id it invented.
func DefaultModelFor(backend string) string {
	plan := OpenCodePlanFor(backend)
	if plan.DefaultModel != "" {
		return plan.DefaultModel
	}
	catalogue, _ := openCodeCatalogNow(plan.Backend)
	best := ""
	bestCtx := -1
	for _, m := range catalogue {
		if m.IsFree() && m.Context > bestCtx {
			best, bestCtx = m.ID, m.Context
		}
	}
	if best != "" {
		return best
	}
	// No free model on offer: the catalogue is already cheapest-first, so the
	// first priced entry is the cheapest one that has a published price.
	for _, m := range catalogue {
		if m.Priced {
			return m.ID
		}
	}
	if len(catalogue) > 0 {
		return catalogue[0].ID
	}
	return ""
}

// IsFree reports a model the gateway serves at no charge — published as a
// zero price, which is only meaningful when a price was published at all.
func (m OpenCodeModel) IsFree() bool { return m.Priced && m.Input == 0 && m.Output == 0 }

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

// The catalogue is cached per plan: the two gateways serve different model
// lists at different prices, and one cache for both would answer the Zen
// dropdown with Go's models the moment Go was refreshed first.
type catalogEntry struct {
	models  []OpenCodeModel
	fetched time.Time
	// live records that the list came from the gateway rather than from this
	// build's table. Only a live list can say a model does NOT exist, which
	// is the claim OpenCodeReady refuses a run on.
	live bool
}

var catalogCache struct {
	sync.Mutex
	byPlan map[string]catalogEntry
}

// OpenCodeCatalog is the model list the dropdown offers for a plan: the live
// one when a refresh has succeeded within the last hour, the baked-in one
// otherwise. It never blocks and never touches the network —
// RefreshOpenCodeCatalog does that, from a goroutine the UI owns.
func OpenCodeCatalog(backend string) []OpenCodeModel {
	models, _ := openCodeCatalogNow(backend)
	return models
}

// bakedCatalog is what a plan offers before the gateway has answered. Go has
// a table; Zen deliberately has none — its catalogue is wider, moves faster,
// and inventing ids for it would put models in a dropdown that no gateway
// ever served. An empty list is the honest starting point, and the settings
// page says the list arrives with the first refresh.
func bakedCatalog(backend string) []OpenCodeModel {
	if OpenCodePlanFor(backend).Backend == OpenCodeZenBackend {
		return nil
	}
	return openCodeCatalog
}

func openCodeCatalogNow(backend string) (models []OpenCodeModel, live bool) {
	plan := OpenCodePlanFor(backend)
	catalogCache.Lock()
	defer catalogCache.Unlock()
	e := catalogCache.byPlan[plan.Backend]
	if len(e.models) > 0 && time.Since(e.fetched) < openCodeCatalogTTL {
		return append([]OpenCodeModel(nil), e.models...), e.live
	}
	return append([]OpenCodeModel(nil), bakedCatalog(plan.Backend)...), false
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
func RefreshOpenCodeCatalog(ctx context.Context, backend string) ([]OpenCodeModel, error) {
	plan := OpenCodePlanFor(backend)
	catalogCache.Lock()
	e := catalogCache.byPlan[plan.Backend]
	fresh := len(e.models) > 0 && time.Since(e.fetched) < openCodeCatalogTTL
	cached := append([]OpenCodeModel(nil), e.models...)
	catalogCache.Unlock()
	if fresh {
		return cached, nil
	}

	served, err := fetchOpenCodeModelIDs(ctx, plan)
	if err != nil {
		return OpenCodeCatalog(plan.Backend), err
	}
	meta, metaErr := fetchModelsDev(ctx, plan)

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
	if catalogCache.byPlan == nil {
		catalogCache.byPlan = map[string]catalogEntry{}
	}
	catalogCache.byPlan[plan.Backend] = catalogEntry{models: out, fetched: time.Now(), live: true}
	catalogCache.Unlock()
	return append([]OpenCodeModel(nil), out...), metaErr
}

// fetchOpenCodeModelIDs reads the gateway's OpenAI-style model listing. It
// needs no credential — the endpoint answers unauthenticated — which is what
// lets the settings page populate before a key is exported.
func fetchOpenCodeModelIDs(ctx context.Context, plan OpenCodePlan) ([]string, error) {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	up := OpenCodeUpstream(plan.Backend)
	if err := getJSON(ctx, strings.TrimSuffix(up, "/")+"/models", &doc); err != nil {
		return nil, err
	}
	if len(doc.Data) == 0 {
		return nil, errors.New(up + "/models: served no models")
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
//
// models.dev publishes every provider it knows in one document, and this needs
// one of them. The document is walked token by token at the top level and only
// plan.ModelsDevProvider's value is decoded; every other provider is skipped
// without being materialized.
func fetchModelsDev(ctx context.Context, plan OpenCodePlan) (map[string]OpenCodeModel, error) {
	var provider struct {
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
	var found bool
	err := getBody(ctx, ModelsDevURL, func(r io.Reader) error {
		var err error
		found, err = decodeMember(json.NewDecoder(r), plan.ModelsDevProvider, &provider)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New(ModelsDevURL + ": no " + plan.ModelsDevProvider + " provider")
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

// decodeMember decodes the value of one top-level key of a JSON object into
// into, skipping every other member token by token, and stops as soon as it
// has it. found is false when the object has no such key.
func decodeMember(dec *json.Decoder, key string, into any) (found bool, err error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false, errors.New("want a JSON object at the top level")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false, err
		}
		if k, _ := tok.(string); k == key {
			return true, dec.Decode(into)
		}
		if err := skipValue(dec); err != nil {
			return false, err
		}
	}
	return false, nil
}

// skipValue consumes one JSON value without building it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			default:
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func getJSON(ctx context.Context, url string, into any) error {
	return getBody(ctx, url, func(r io.Reader) error {
		return json.NewDecoder(r).Decode(into)
	})
}

// getBody GETs url and hands its body, capped at 32 MiB, to read.
func getBody(ctx context.Context, url string, read func(io.Reader) error) error {
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
	return read(io.LimitReader(resp.Body, 32<<20))
}

type httpStatusError struct {
	url  string
	code int
}

func (e *httpStatusError) Error() string { return e.url + ": HTTP " + Itoa(e.code) }

// OpenCodeModelByID finds a model in the catalogue. A miss is not a refusal:
// a model this build has never heard of is still passed to the gateway, which
// is the only thing that can actually say whether it exists.
func OpenCodeModelByID(backend, id string) (OpenCodeModel, bool) {
	id = strings.TrimSpace(id)
	for _, m := range OpenCodeCatalog(backend) {
		if m.ID == id {
			return m, true
		}
	}
	return OpenCodeModel{ID: id}, false
}

// OpenCodeModelFor is the model an OpenCode job will actually run, and where
// that answer came from — the same shape as ClaudeCLIModelFor, and for the
// same reason: the settings page and the confirm dialog must name the same
// model for the same reason.
//
// The order is the one that actually applies at run time: the chosen model
// goes in as `--model`, which llm.py prefers over everything; the overlay's
// model_env_key for this plan is read when there is no flag; and the entry's
// own default is the floor.
func OpenCodeModelFor(backend string, overlay Env, model string) (m OpenCodeModel, origin string) {
	plan := OpenCodePlanFor(backend)
	if v := strings.TrimSpace(model); v != "" {
		found, known := OpenCodeModelByID(plan.Backend, v)
		if !known {
			return found, "the model setting (not in this build's catalogue — the gateway decides)"
		}
		return found, "the model setting"
	}
	if v := strings.TrimSpace(overlay[plan.ModelVar]); v != "" {
		found, _ := OpenCodeModelByID(plan.Backend, v)
		return found, "the overlay's " + plan.ModelVar
	}
	def := DefaultModelFor(plan.Backend)
	if def == "" {
		// Zen before any catalogue has arrived. Naming nothing is the truth:
		// the plan has no fixed default, and the dropdown says the list
		// arrives with the first refresh.
		return OpenCodeModel{}, "no default yet — refresh " + plan.Name + "'s model list"
	}
	found, _ := OpenCodeModelByID(plan.Backend, def)
	if plan.DefaultModel == "" {
		if found.IsFree() {
			return found, "the widest-context model " + plan.Name + " currently serves free"
		}
		return found, "the cheapest model " + plan.Name + " is currently serving"
	}
	return found, "the board's default for this plan"
}

// OpenCodeUpstream is the gateway itself — what the proxy forwards to, and
// what the plan's base-URL variable moves: a corporate proxy, or a gateway
// under test.
//
// It is NOT what goes into the provider entry. That is the loopback proxy,
// because graphify cannot send the session header the gateway requires; see
// opencodeproxy.go.
func OpenCodeUpstream(backend string) string {
	plan := OpenCodePlanFor(backend)
	if v := strings.TrimSpace(os.Getenv(plan.URLVar)); v != "" {
		return v
	}
	return plan.DefaultURL
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
func OpenCodeProvider(backend string) (baseURL, model string, registered bool) {
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
	cfg, ok := all[OpenCodePlanFor(backend).Backend]
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
	if !IsOpenCodeBackend(backend) {
		return "", false, nil
	}
	plan := OpenCodePlanFor(backend)
	path = ProvidersPath()
	if path == "" {
		return "", false, os.ErrNotExist
	}
	// The entry points at the proxy, so the proxy has to exist before the
	// entry claims it does. Starting it here rather than at the call site
	// keeps the two facts — where graphify will connect, and what is
	// listening there — written by one function.
	proxy, err := StartOpenCodeProxy(plan.Backend)
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
	if raw, ok := all[plan.Backend]; ok {
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
	m, _ := OpenCodeModelFor(plan.Backend, nil, model)
	managed := map[string]any{
		"base_url":      proxy,
		"default_model": m.ID,
		"env_key":       OpenCodeKeyVar,
		"model_env_key": plan.ModelVar,
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
	all[plan.Backend] = raw

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

// Keyed by plan AND model: the two gateways answer for their own estates, and
// a model that is fine on Zen can be absent from Go. One key space would let
// the first answer stand for both.
var openCodeVerdicts sync.Map // verdictKey -> openCodeVerdict

func verdictKey(backend, model string) string {
	return OpenCodePlanFor(backend).Backend + "\x00" + strings.TrimSpace(model)
}

// openCodeVerdictTTL keeps a verdict long enough to cover a sweep and short
// enough that a model fixed upstream is retried the same afternoon.
const openCodeVerdictTTL = 30 * time.Minute

// OpenCodeVerdict is the cached answer for a model, without touching the
// network: known is false when it has never been asked.
func OpenCodeVerdict(backend, model string) (known, ok bool, detail string) {
	v, hit := openCodeVerdicts.Load(verdictKey(backend, model))
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
func VerifyOpenCodeModel(ctx context.Context, backend, model string) (bool, string) {
	plan := OpenCodePlanFor(backend)
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModelFor(plan.Backend)
	}
	if model == "" {
		return false, plan.Name + " has not said which models it serves yet."
	}
	if known, ok, detail := OpenCodeVerdict(plan.Backend, model); known {
		applog.Debugf("opencode verify: %s %s — cached verdict ok=%t %s", plan.Backend, model, ok, detail)
		return ok, detail
	}
	key := strings.TrimSpace(os.Getenv(OpenCodeKeyVar))
	if key == "" {
		applog.Warnf("opencode verify: %s %s — %s is not set in ggraphify's environment", plan.Backend, model, OpenCodeKeyVar)
		return false, OpenCodeKeyVar + " is not set, so the gateway cannot be asked about " + model + "."
	}

	body := `{"model":` + jsonString(model) +
		`,"messages":[{"role":"user","content":"ping"}],"max_tokens":8,"temperature":0}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(OpenCodeUpstream(plan.Backend), "/")+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return true, "" // cannot even build the request: not the model's fault
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	// The same two headers the proxy adds to a job's requests, because a probe
	// that skipped them would fail for a reason the real traffic does not have.
	req.Header.Set("x-opencode-session", OpenCodeSession())
	req.Header.Set("User-Agent", "ggraphify/"+AppVersion)

	started := time.Now()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		// Let through, because unreachable says nothing about the model — but
		// said, because the run that follows will hit the same wall.
		applog.Warnf("opencode verify: %s %s — gateway %s unreachable, letting the job through unverified: %v",
			plan.Backend, model, OpenCodeUpstream(plan.Backend), err)
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
	openCodeVerdicts.Store(verdictKey(plan.Backend, model), verdict)
	if verdict.ok {
		applog.Infof("opencode verify: %s %s — gateway answered in %s",
			plan.Backend, model, time.Since(started).Round(time.Millisecond))
	} else {
		applog.Warnf("opencode verify: %s — refused: %s", plan.Backend, verdict.detail)
	}
	return verdict.ok, verdict.detail
}

// openCodeSweepConcurrency is how many models are probed at once. Small on
// purpose: this is a courtesy probe against somebody else's gateway, and a
// catalogue of forty opened forty at a time is the shape that gets an IP rate
// limited — which would then be recorded as forty models refusing.
const openCodeSweepConcurrency = 4

// VerifyOpenCodeCatalog asks the gateway about every model in a list that has
// no cached verdict, and reports each answer as it arrives.
//
// This is what lets the settings dropdown say which models are actually
// reachable rather than only which ones are listed. The distinction is not
// cosmetic: the gateway lists models it serves to some regions and accounts
// and not others, `/models` says nothing about which, and picking one of them
// costs a whole run before the truth arrives. Probing costs eight tokens per
// model.
//
// onResult is called from this goroutine, once per model, with the gateway's
// own words on a refusal. A model that was already verified is reported from
// cache without a request. Only answers are cached — an unreachable gateway
// says nothing about a model, so those models simply stay unknown and are
// asked again next time.
func VerifyOpenCodeCatalog(ctx context.Context, backend string, models []OpenCodeModel, onResult func(id string, ok bool, detail string)) {
	plan := OpenCodePlanFor(backend)
	if !HasOpenCodeKey() || len(models) == 0 {
		return
	}
	ids := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex

	report := func(id string, ok bool, detail string) {
		if onResult == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		onResult(id, ok, detail)
	}

	for i := 0; i < openCodeSweepConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				if ctx.Err() != nil {
					return
				}
				ok, detail := VerifyOpenCodeModel(ctx, plan.Backend, id)
				report(id, ok, detail)
			}
		}()
	}
	for _, m := range models {
		if ctx.Err() != nil {
			break
		}
		if known, ok, detail := OpenCodeVerdict(plan.Backend, m.ID); known {
			report(m.ID, ok, detail)
			continue
		}
		ids <- m.ID
	}
	close(ids)
	wg.Wait()
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
	if !IsOpenCodeBackend(backend) {
		return nil
	}
	plan := OpenCodePlanFor(backend)
	m, _ := OpenCodeModelFor(plan.Backend, nil, model)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if ok, detail := VerifyOpenCodeModel(ctx, plan.Backend, m.ID); !ok {
		return errors.New(detail + ". Pick another model under Settings ▸ " + plan.Name + " — " +
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
func OpenCodeReady(backend, model string) (bool, string) {
	plan := OpenCodePlanFor(backend)
	if !HasOpenCodeKey() {
		return false, "The " + plan.Backend + " backend needs " + OpenCodeKeyVar +
			" exported in this environment, and none is visible. Subscribe at " +
			"https://opencode.ai/docs/go, copy the key, export it and relaunch the board — " +
			"ggraphify never stores a key itself."
	}
	_, _, registered := OpenCodeProvider(plan.Backend)
	// The endpoint worth naming is the gateway, not the loopback address the
	// entry carries: the proxy is an implementation detail of getting the
	// session header on, and saying "127.0.0.1" here would only puzzle
	// somebody checking where their corpus goes.
	where := OpenCodeUpstream(plan.Backend)
	m, origin := OpenCodeModelFor(plan.Backend, nil, model)
	if m.ID == "" {
		// Zen, before the first catalogue. Nothing is wrong and nothing is
		// ready: there is no model to name yet.
		return false, plan.Name + " has not said which models it serves yet — open " +
			"Settings ▸ " + plan.Name + " and refresh the model list, or pick a model there."
	}
	// A model the gateway does not serve fails every chunk of a run with
	// "Model <id> is not supported", which surfaces as graphify's opaque "all
	// semantic chunks failed". It is worth catching in the confirm dialog
	// instead — but only against a list actually fetched from the gateway,
	// never against this build's table, which is a month old by construction.
	if catalogue, live := openCodeCatalogNow(plan.Backend); live {
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
				" is not supported\". Pick another model under " + plan.Name + "."
		}
	}
	// A model already caught refusing is worth saying before the run, not
	// after twenty-five chunks have failed on it.
	if known, ok, detail := OpenCodeVerdict(plan.Backend, m.ID); known && !ok {
		return false, detail + ". Pick another model below — a model can be listed by the " +
			"gateway and still refuse this account or region."
	}
	s := OpenCodeKeyVar + " is visible in this environment; requests go to " + where +
		", " + plan.Billing + ".\nModel: " + m.Label() + " (from " + origin + ")."
	if registered {
		return true, s + " The provider is registered in " + Tilde(ProvidersPath()) + "."
	}
	return true, s + " The provider will be registered in " + Tilde(ProvidersPath()) +
		" when the first job is submitted — graphify has no built-in backend by this " +
		"name, and reads custom ones from that file."
}
