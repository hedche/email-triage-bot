package openai

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
			Brand: triage.BrandRule{Name: "Amazon", Note: "Dispatch mail is routine."},
		})
	}
	return out
}

// completion renders an OpenAI-shaped chat completion carrying content.
func completion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
	})
	return string(b)
}

type capture struct {
	path string
	auth string
	body map[string]any
}

func newJudge(t *testing.T, srvURL, apiKey string, opts ...Option) *Judge {
	t.Helper()
	j, err := New(srvURL+"/v1", apiKey, "llama3", 5*time.Second, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	j.retryDelay = time.Millisecond
	return j
}

func TestJudgeRequestShape(t *testing.T) {
	var got capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, completion(`[{"id":"m1","confidence":0.88,"reason":"dispatch note"}]`))
	}))
	defer srv.Close()

	j := newJudge(t, srv.URL, "sk-test-not-a-real-key")
	out, err := j.Judge(context.Background(), testBatch("m1"))
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}

	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", got.path)
	}
	if got.auth != "Bearer sk-test-not-a-real-key" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.body["model"] != "llama3" {
		t.Errorf("model = %v, want llama3", got.body["model"])
	}
	if temp, _ := got.body["temperature"].(float64); temp != 0.1 {
		t.Errorf("temperature = %v, want 0.1", got.body["temperature"])
	}
	rf, ok := got.body["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Errorf("response_format = %v, want json_object", got.body["response_format"])
	}
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if first["role"] != "system" || second["role"] != "user" {
		t.Errorf("roles = %v, %v, want system, user", first["role"], second["role"])
	}
	if sys, _ := first["content"].(string); !strings.Contains(sys, "JSON") {
		t.Error("system message must contain the word JSON for strict local servers")
	}
	if user, _ := second["content"].(string); !strings.Contains(user, "m1") {
		t.Error("user message must carry the candidate id")
	}

	if len(out) != 1 || out[0].MessageID != "m1" || out[0].Confidence != 0.88 {
		t.Errorf("judgements = %+v", out)
	}
}

func TestJudgeOmitsAuthorizationWhenNoAPIKey(t *testing.T) {
	var hasAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasAuth = r.Header["Authorization"]
		io.WriteString(w, completion(`[{"id":"m1","confidence":0.5,"reason":"r"}]`))
	}))
	defer srv.Close()

	if _, err := newJudge(t, srv.URL, "").Judge(context.Background(), testBatch("m1")); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if hasAuth {
		t.Error("Authorization header sent despite empty api key")
	}
}

func TestJudgeJSONModeOff(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, completion(`[{"id":"m1","confidence":0.5,"reason":"r"}]`))
	}))
	defer srv.Close()

	if _, err := newJudge(t, srv.URL, "", JSONMode(false)).Judge(context.Background(), testBatch("m1")); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if _, present := body["response_format"]; present {
		t.Errorf("response_format sent with JSONMode(false): %v", body["response_format"])
	}
	sys, _ := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "JSON") {
		t.Error("system message must still ask for JSON when json mode is off")
	}
}

func TestJudgeRetriesOnceOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"rate limited"}`)
			return
		}
		io.WriteString(w, completion(`[{"id":"m1","confidence":0.75,"reason":"second try"}]`))
	}))
	defer srv.Close()

	out, err := newJudge(t, srv.URL, "").Judge(context.Background(), testBatch("m1"))
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	if out[0].Confidence != 0.75 {
		t.Errorf("confidence = %v, want 0.75", out[0].Confidence)
	}
}

func TestJudgeRetriesOnceThenFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "upstream exploded")
	}))
	defer srv.Close()

	_, err := newJudge(t, srv.URL, "").Judge(context.Background(), testBatch("m1"))
	if err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", got)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("error should carry status and body: %v", err)
	}
}

func TestJudgeDoesNotRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"response_format is not supported"}`)
	}))
	defer srv.Close()

	if _, err := newJudge(t, srv.URL, "").Judge(context.Background(), testBatch("m1")); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestJudgeUnparseableContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, completion("I'm sorry, I can't help with that."))
	}))
	defer srv.Close()

	_, err := newJudge(t, srv.URL, "").Judge(context.Background(), testBatch("m1"))
	if err == nil || !strings.Contains(err.Error(), judge.ErrUnparseable.Error()) {
		t.Fatalf("err = %v, want ErrUnparseable", err)
	}
}

func TestNameAndConstructorValidation(t *testing.T) {
	j, err := New("http://localhost:11434/v1/", "", "llama3", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if j.Name() != "openai/llama3" {
		t.Errorf("Name = %q", j.Name())
	}
	if j.baseURL != "http://localhost:11434/v1" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", j.baseURL)
	}
	if _, err := New("", "", "llama3", 0); err == nil {
		t.Error("want an error for an empty base url")
	}
	if _, err := New("http://x/v1", "", "", 0); err == nil {
		t.Error("want an error for an empty model")
	}
}

func TestJudgeEmptyBatch(t *testing.T) {
	j := newJudge(t, "http://127.0.0.1:1", "")
	out, err := j.Judge(context.Background(), nil)
	if err != nil || out != nil {
		t.Errorf("empty batch = %v, %v; want nil, nil", out, err)
	}
}
