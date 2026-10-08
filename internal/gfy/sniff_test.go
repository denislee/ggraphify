package gfy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// endlessX is an infinite stream of 'x'. It lets the over-limit test feed the
// proxy maxProxyBody+1 bytes without allocating 64 MiB up front.
type endlessX struct{}

func (endlessX) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// sniffModel reads the top-level "model" off the first bytes of a body. ok is
// false when the prefix ends before that can be decided; a prefix that is
// decidedly not an object, or an object that closes without a model, is "-"
// and ok.
func TestSniffModel(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		model string
		ok    bool
	}{
		{"model first", `{"model":"m1","messages":[]}`, "m1", true},
		{"model after messages", `{"messages":[],"model":"m2"}`, "m2", true},
		{"case-insensitive key", `{"Model":"m3"}`, "m3", true},
		{"no model, object closes", `{"messages":[]}`, "-", true},
		{"not json", `not json`, "-", true},
		{"top level array", `[1,2]`, "-", true},
		{"truncated prefix", `{"messages":[{"role":"user","content":"abc`, "-", false},
		{"model not a string", `{"model":123}`, "-", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model, ok := sniffModel([]byte(tc.body))
			if model != tc.model || ok != tc.ok {
				t.Fatalf("sniffModel(%q) = %q, %t; want %q, %t", tc.body, model, ok, tc.model, tc.ok)
			}
		})
	}
}

// An early model is read off the sniff prefix and the rest of the body is
// streamed, never held whole.
func TestProxyRequestBodyStreamsWhenModelIsEarly(t *testing.T) {
	body := `{"model":"early","messages":[{"content":"` + strings.Repeat("x", 10000) + `"}]}`

	rd, length, model, err := proxyRequestBody(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatalf("proxyRequestBody: %v", err)
	}
	if model != "early" {
		t.Errorf("model = %q, want early", model)
	}
	if length != int64(len(body)) {
		t.Errorf("length = %d, want %d", length, len(body))
	}
	if _, isBytesReader := rd.(*bytes.Reader); isBytesReader {
		t.Errorf("the body was buffered whole; an early model must stream")
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(got) != body {
		t.Errorf("the streamed body differs from the original (got %d bytes, want %d)", len(got), len(body))
	}
}

// When the model sits after a long prompt, the proxy has no choice but to read
// the whole body — and still reports the model and the correct length.
func TestProxyRequestBodyFallsBackWhenModelIsLate(t *testing.T) {
	body := `{"messages":[{"content":"` + strings.Repeat("x", 10000) + `"}],"model":"late"}`

	rd, length, model, err := proxyRequestBody(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatalf("proxyRequestBody: %v", err)
	}
	if model != "late" {
		t.Errorf("model = %q, want late", model)
	}
	if length != int64(len(body)) {
		t.Errorf("length = %d, want %d", length, len(body))
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(got) != body {
		t.Errorf("the returned body differs from the original (got %d bytes, want %d)", len(got), len(body))
	}
}

// A body of unknown length takes the fallback, so upstream is always sent a
// Content-Length — but the model is still read.
func TestProxyRequestBodyUnknownLengthFallsBack(t *testing.T) {
	body := `{"model":"early","messages":[{"content":"` + strings.Repeat("x", 10000) + `"}]}`

	rd, length, model, err := proxyRequestBody(bytes.NewReader([]byte(body)), -1)
	if err != nil {
		t.Fatalf("proxyRequestBody: %v", err)
	}
	if model != "early" {
		t.Errorf("model = %q, want early", model)
	}
	if length != int64(len(body)) {
		t.Errorf("length = %d, want %d", length, len(body))
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(got) != body {
		t.Errorf("the returned body differs from the original (got %d bytes, want %d)", len(got), len(body))
	}
}

// A body that ends inside the sniff prefix is parsed whole, exactly as before.
func TestProxyRequestBodyShortBody(t *testing.T) {
	body := `{"model":"s"}`
	rd, length, model, err := proxyRequestBody(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("proxyRequestBody: %v", err)
	}
	if model != "s" {
		t.Errorf("model = %q, want s", model)
	}
	if length != int64(len(body)) {
		t.Errorf("length = %d, want %d", length, len(body))
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}

	// An empty body has no model and no length.
	rd, length, model, err = proxyRequestBody(strings.NewReader(""), 100)
	if err != nil {
		t.Fatalf("proxyRequestBody(empty): %v", err)
	}
	if length != 0 {
		t.Errorf("length = %d, want 0", length)
	}
	if model != "-" {
		t.Errorf("model = %q, want -", model)
	}
	_ = rd
}

// A body over maxProxyBody must be a 413, never a silent truncation into
// broken JSON.
func TestProxyRequestBodyOverLimitIs413(t *testing.T) {
	src := io.NopCloser(io.LimitReader(endlessX{}, maxProxyBody+1))
	limited := http.MaxBytesReader(httptest.NewRecorder(), src, maxProxyBody)

	_, _, _, err := proxyRequestBody(limited, -1)
	if err == nil {
		t.Fatal("a body over maxProxyBody was accepted")
	}
	if got := bodyErrorStatus(err); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("bodyErrorStatus(%v) = %d, want 413", err, got)
	}
}

// Anything that is not an over-limit body error is a 400.
func TestBodyErrorStatusOtherIs400(t *testing.T) {
	if got := bodyErrorStatus(errors.New("x")); got != http.StatusBadRequest {
		t.Fatalf("bodyErrorStatus = %d, want 400", got)
	}
}
