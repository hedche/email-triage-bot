// Package openai implements judge.Judge against any OpenAI-compatible chat
// completions endpoint (Ollama, llama.cpp, Groq, OpenAI itself). It speaks
// plain HTTP rather than depending on an SDK, because the endpoints differ
// mainly in which optional fields they tolerate.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hedche/email-triage-bot/internal/judge"
	"github.com/hedche/email-triage-bot/internal/triage"
)

const (
	defaultTimeout    = 60 * time.Second
	defaultRetryDelay = 2 * time.Second
	temperature       = 0.1
	maxErrBodyChars   = 300
)

var _ judge.Judge = (*Judge)(nil)

// Judge scores Candidates with an OpenAI-compatible chat model.
type Judge struct {
	baseURL    string
	apiKey     string
	model      string
	client     *http.Client
	jsonMode   bool
	retryDelay time.Duration
}

// Option tunes a Judge at construction.
type Option func(*Judge)

// JSONMode controls whether the request carries
// response_format={"type":"json_object"}. It defaults to true; turn it off for
// local servers that reject the field. The prompt asks for JSON either way.
func JSONMode(on bool) Option {
	return func(j *Judge) { j.jsonMode = on }
}

// HTTPClient overrides the HTTP client, mainly for tests.
func HTTPClient(c *http.Client) Option {
	return func(j *Judge) {
		if c != nil {
			j.client = c
		}
	}
}

// New builds a Judge against an OpenAI-compatible endpoint. baseURL is the
// API root, e.g. "http://ollama:11434/v1"; apiKey may be empty for local
// servers, in which case no Authorization header is sent.
func New(baseURL, apiKey, model string, timeout time.Duration, opts ...Option) (*Judge, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("openai: base url is required")
	}
	if model == "" {
		return nil, errors.New("openai: model is required")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	j := &Judge{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		client:     &http.Client{Timeout: timeout},
		jsonMode:   true,
		retryDelay: defaultRetryDelay,
	}
	for _, opt := range opts {
		opt(j)
	}
	return j, nil
}

// Name identifies the provider and model for telemetry.
func (j *Judge) Name() string { return "openai/" + j.model }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Temperature    float64         `json:"temperature"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Judge scores one batch of Candidates. It retries once, after a short pause,
// on a rate limit or a server-side failure.
func (j *Judge) Judge(ctx context.Context, batch []triage.Candidate) ([]triage.Judgement, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	system, user := judge.BuildPrompt(batch)
	reqBody := chatRequest{
		Model:       j.model,
		Temperature: temperature,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	if j.jsonMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	var content string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(j.retryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var retry bool
		content, retry, err = j.post(ctx, payload)
		if err == nil {
			break
		}
		if attempt == 0 && retry {
			continue
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	return judge.ParseJudgements(content, batch)
}

// post sends one chat completion. The bool reports whether the failure is
// worth retrying.
func (j *Judge) post(ctx context.Context, payload []byte) (content string, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", false, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if j.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+j.apiKey)
	}

	resp, err := j.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("openai: post %s: %w", j.baseURL+"/chat/completions", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		retry := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusRequestTimeout ||
			(resp.StatusCode >= 500 && resp.StatusCode <= 599)
		return "", retry, fmt.Errorf("openai: %s returned %d: %s", j.model, resp.StatusCode, truncate(string(body), maxErrBodyChars))
	}
	if readErr != nil {
		return "", true, fmt.Errorf("openai: read response: %w", readErr)
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, fmt.Errorf("openai: decode response: %w: %s", err, truncate(string(body), maxErrBodyChars))
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("openai: response contained no choices: %s", truncate(string(body), maxErrBodyChars))
	}
	return parsed.Choices[0].Message.Content, false, nil
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
