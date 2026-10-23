package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/hedche/email-triage-bot/internal/judge"
	"github.com/hedche/email-triage-bot/internal/triage"
)

func testBatch(ids ...string) []triage.Candidate {
	out := make([]triage.Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, triage.Candidate{
			Message: triage.Message{
				ID:      id,
				From:    triage.Address{Name: "Amazon", Email: "noreply@amazon.com"},
				Subject: "Dispatched",
				Body:    "Your parcel is on its way.",
			},
			Brand: triage.BrandRule{
				Name:     "Amazon",
				Note:     "Dispatch mail is routine.",
				Examples: []triage.Example{{Description: "Dispatch confirmation", Verdict: triage.VerdictMarkRead}},
			},
		})
	}
	return out
}

// generateContentResponse renders a Gemini response carrying text.
func generateContentResponse(text string) string {
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role":  "model",
				"parts": []any{map[string]any{"text": text}},
			},
			"finishReason": "STOP",
		}},
	})
	return string(b)
}

// newTestJudge points the SDK at an httptest server through
// ClientConfig.HTTPOptions.BaseURL, which getBaseURL honours ahead of the
// real endpoint.
func newTestJudge(t *testing.T, srvURL string) *Judge {
	t.Helper()
	j, err := newWithConfig(genai.ClientConfig{
		APIKey:      "test-key-not-real",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srvURL},
	}, "gemini-3.5-flash-lite", 5*time.Second)
	if err != nil {
		t.Fatalf("newWithConfig: %v", err)
	}
	j.retryDelay = time.Millisecond
	return j
}

func TestJudgeRequestShape(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, generateContentResponse(`[{"id":"m1","confidence":0.91,"reason":"dispatch note"}]`))
	}))
	defer srv.Close()

	out, err := newTestJudge(t, srv.URL).Judge(context.Background(), testBatch("m1"))
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}

	if !strings.Contains(path, "gemini-3.5-flash-lite:generateContent") {
		t.Errorf("path = %q", path)
	}

	// System instruction.
	sysInst, ok := body["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("no systemInstruction in request: %v", body)
	}
	sysText := partsText(t, sysInst)
	if !strings.Contains(sysText, "untrusted") {
		t.Errorf("system instruction missing the untrusted-data rule: %q", sysText)
	}

	// Candidate content.
	contents, _ := body["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %v, want 1", contents)
	}
	userText := partsText(t, contents[0].(map[string]any))
	for _, want := range []string{"m1", "Amazon", "Dispatch mail is routine.", "Dispatch confirmation"} {
		if !strings.Contains(userText, want) {
			t.Errorf("user content missing %q", want)
		}
	}

	// Generation config: temperature, mime type and schema.
	cfg, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("no generationConfig in request: %v", body)
	}
	if temp, _ := cfg["temperature"].(float64); temp < 0.09 || temp > 0.11 {
		t.Errorf("temperature = %v, want 0.1", cfg["temperature"])
	}
	if cfg["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType = %v", cfg["responseMimeType"])
	}
	schema, ok := cfg["responseSchema"].(map[string]any)
	if !ok {
		t.Fatalf("no responseSchema in request: %v", cfg)
	}
	if schema["type"] != "ARRAY" {
		t.Errorf("schema type = %v, want ARRAY", schema["type"])
	}
	items, _ := schema["items"].(map[string]any)
	if items["type"] != "OBJECT" {
		t.Errorf("items type = %v, want OBJECT", items["type"])
	}
	props, _ := items["properties"].(map[string]any)
	for _, key := range []string{"id", "confidence", "reason"} {
		if _, present := props[key]; !present {
			t.Errorf("schema missing property %q", key)
		}
	}
	required := toStrings(items["required"])
	if len(required) != 3 {
		t.Errorf("required = %v, want all three properties", required)
	}

	if len(out) != 1 || out[0].MessageID != "m1" || out[0].Confidence != 0.91 || out[0].Reason != "dispatch note" {
		t.Errorf("judgements = %+v", out)
	}
}

func TestJudgeParsesFencedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, generateContentResponse("```json\n[{\"id\":\"m2\",\"confidence\":0.2,\"reason\":\"security alert\"},{\"id\":\"m1\",\"confidence\":0.9,\"reason\":\"routine\"}]\n```"))
	}))
	defer srv.Close()

	out, err := newTestJudge(t, srv.URL).Judge(context.Background(), testBatch("m1", "m2"))
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out[0].MessageID != "m1" || out[0].Confidence != 0.9 {
		t.Errorf("first = %+v, want m1 in batch order", out[0])
	}
	if out[1].MessageID != "m2" || out[1].Confidence != 0.2 {
		t.Errorf("second = %+v", out[1])
	}
}

func TestJudgeRetriesOnceOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		io.WriteString(w, generateContentResponse(`[{"id":"m1","confidence":0.6,"reason":"second try"}]`))
	}))
	defer srv.Close()

	out, err := newTestJudge(t, srv.URL).Judge(context.Background(), testBatch("m1"))
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	if out[0].Confidence != 0.6 {
		t.Errorf("confidence = %v, want 0.6", out[0].Confidence)
	}
}

func TestJudgeDoesNotRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`)
	}))
	defer srv.Close()

	if _, err := newTestJudge(t, srv.URL).Judge(context.Background(), testBatch("m1")); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestJudgeUnparseableResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, generateContentResponse("I cannot comply."))
	}))
	defer srv.Close()

	_, err := newTestJudge(t, srv.URL).Judge(context.Background(), testBatch("m1"))
	if err == nil || !strings.Contains(err.Error(), judge.ErrUnparseable.Error()) {
		t.Fatalf("err = %v, want ErrUnparseable", err)
	}
}

func TestNameAndConstructorValidation(t *testing.T) {
	j, err := New("test-key-not-real", "gemini-3.5-flash-lite", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if j.Name() != "gemini/gemini-3.5-flash-lite" {
		t.Errorf("Name = %q", j.Name())
	}
	if j.timeout != defaultTimeout {
		t.Errorf("timeout = %v, want the default", j.timeout)
	}
	if _, err := New("", "m", time.Second); err == nil {
		t.Error("want an error for an empty api key")
	}
	if _, err := New("k", "", time.Second); err == nil {
		t.Error("want an error for an empty model")
	}
}

func TestJudgeEmptyBatch(t *testing.T) {
	j := newTestJudge(t, "http://127.0.0.1:1")
	out, err := j.Judge(context.Background(), nil)
	if err != nil || out != nil {
		t.Errorf("empty batch = %v, %v; want nil, nil", out, err)
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{{429, true}, {408, true}, {500, true}, {503, true}, {400, false}, {401, false}, {404, false}}
	for _, tc := range cases {
		if got := retryable(genai.APIError{Code: tc.code}); got != tc.want {
			t.Errorf("retryable(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if retryable(context.DeadlineExceeded) {
		t.Error("a non-API error must not be retried")
	}
}

func partsText(t *testing.T, content map[string]any) string {
	t.Helper()
	parts, _ := content["parts"].([]any)
	var b strings.Builder
	for _, p := range parts {
		m, _ := p.(map[string]any)
		s, _ := m["text"].(string)
		b.WriteString(s)
	}
	return b.String()
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}
	return out
}
