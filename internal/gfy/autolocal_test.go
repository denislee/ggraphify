package gfy

import "testing"

// The case the auto-fix loop exists in: the board's backend is claude-cli and
// its model setting names a frontier model, so the model asked for is one the
// local server has never heard of. The loop must still get a usable model
// rather than stalling the night's repairs on a name mismatch.
func TestAutoLocalModelFallsBackToTheServersOwnDefault(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, modelServer(t, "qwen2.5-coder:7b")+"/v1")
	InvalidateLocalProbe()

	m, ok, why := AutoLocalModel(OllamaBackend, "")
	if !ok || m != "qwen2.5-coder:7b" {
		t.Fatalf("AutoLocalModel = %q, %v (%s), want the default model", m, ok, why)
	}
}

// A model that IS served wins over the default, because somebody chose it.
func TestAutoLocalModelPrefersTheChosenModel(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, modelServer(t, "qwen2.5-coder:7b", "llama3.2:3b")+"/v1")
	InvalidateLocalProbe()

	m, ok, _ := AutoLocalModel(OllamaBackend, "llama3.2:3b")
	if !ok || m != "llama3.2:3b" {
		t.Fatalf("AutoLocalModel = %q, %v, want llama3.2:3b", m, ok)
	}
}

// Neither the asked-for model nor graphify's default is served, so the loop
// takes what there is — and does NOT take the embedding model, which would
// answer an extraction with a 400 for every chunk of every repository.
func TestAutoLocalModelSkipsEmbeddingModels(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, modelServer(t, "nomic-embed-text", "deepseek-coder:6.7b")+"/v1")
	InvalidateLocalProbe()

	m, ok, _ := AutoLocalModel(OllamaBackend, "claude-opus-4")
	if !ok || m != "deepseek-coder:6.7b" {
		t.Fatalf("AutoLocalModel = %q, %v, want the chat model", m, ok)
	}
}

// A server with nothing but embedders is a refusal, not a bad guess.
func TestAutoLocalModelRefusesAnEmbeddingOnlyServer(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, modelServer(t, "nomic-embed-text", "bge-m3")+"/v1")
	InvalidateLocalProbe()

	if m, ok, why := AutoLocalModel(OllamaBackend, ""); ok {
		t.Fatalf("AutoLocalModel = %q, %v (%s), want a refusal", m, ok, why)
	}
}

// No server at all: a refusal carrying LocalReady's own wording, so the loop
// never has to invent a second explanation of the same failure.
func TestAutoLocalModelRefusesWithoutAServer(t *testing.T) {
	setEnv(t, OllamaHostVar, "-")
	setEnv(t, OllamaBaseURLVar, "http://127.0.0.1:1/v1")
	InvalidateLocalProbe()

	_, ok, why := AutoLocalModel(OllamaBackend, "")
	if ok {
		t.Fatal("AutoLocalModel reported ready with nothing listening")
	}
	if why == "" {
		t.Fatal("a refusal has to say why")
	}
}

func TestEmbeddingModel(t *testing.T) {
	for _, m := range []string{"nomic-embed-text", "mxbai-embed-large", "bge-m3", "all-minilm"} {
		if !EmbeddingModel(m) {
			t.Errorf("%s should be recognised as an embedding model", m)
		}
	}
	for _, m := range []string{"qwen2.5-coder:7b", "llama3.2:3b", "deepseek-coder:6.7b"} {
		if EmbeddingModel(m) {
			t.Errorf("%s is a chat model and must stay pickable", m)
		}
	}
}
