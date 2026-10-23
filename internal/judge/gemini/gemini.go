// Package gemini implements judge.Judge against the Gemini API.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/genai"

	"github.com/hedche/email-triage-bot/internal/judge"
	"github.com/hedche/email-triage-bot/internal/triage"
)

const (
	defaultTimeout    = 60 * time.Second
	defaultRetryDelay = 2 * time.Second
	temperature       = float32(0.1)
)

var _ judge.Judge = (*Judge)(nil)

// Judge scores Candidates with a Gemini model.
type Judge struct {
	client     *genai.Client
	model      string
	timeout    time.Duration
	retryDelay time.Duration
}

// New builds a Judge against the public Gemini API.
func New(apiKey, model string, timeout time.Duration) (*Judge, error) {
	if apiKey == "" {
		return nil, errors.New("gemini: api key is required")
	}
	if model == "" {
		return nil, errors.New("gemini: model is required")
	}
	return newWithConfig(genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI}, model, timeout)
}

// newWithConfig is the seam tests use: a ClientConfig lets a test point the
// SDK at an httptest server via HTTPOptions.BaseURL.
func newWithConfig(cc genai.ClientConfig, model string, timeout time.Duration) (*Judge, error) {
	if model == "" {
		return nil, errors.New("gemini: model is required")
	}
	if cc.Backend == genai.BackendUnspecified {
		cc.Backend = genai.BackendGeminiAPI
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client, err := genai.NewClient(context.Background(), &cc)
	if err != nil {
		return nil, fmt.Errorf("gemini: new client: %w", err)
	}
	return &Judge{client: client, model: model, timeout: timeout, retryDelay: defaultRetryDelay}, nil
}

// Name identifies the provider and model for telemetry.
func (j *Judge) Name() string { return "gemini/" + j.model }

// Judge scores one batch of Candidates. It retries once, after a short pause,
// when the API reports a rate limit or a server-side failure.
func (j *Judge) Judge(ctx context.Context, batch []triage.Candidate) ([]triage.Judgement, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	system, user := judge.BuildPrompt(batch)
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
		Temperature:       genai.Ptr(temperature),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    responseSchema(),
	}
	contents := []*genai.Content{genai.NewContentFromText(user, genai.RoleUser)}

	var resp *genai.GenerateContentResponse
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(j.retryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		resp, err = j.generate(ctx, contents, cfg)
		if err == nil {
			break
		}
		if attempt == 0 && retryable(err) {
			continue
		}
		return nil, fmt.Errorf("gemini: generate content: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("gemini: generate content: %w", err)
	}

	return judge.ParseJudgements(resp.Text(), batch)
}

func (j *Judge) generate(ctx context.Context, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	return j.client.Models.GenerateContent(callCtx, j.model, contents, cfg)
}

// retryable reports whether the Gemini API said "try again": a rate limit, a
// request timeout, or any 5xx.
func retryable(err error) bool {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch {
	case apiErr.Code == 429, apiErr.Code == 408:
		return true
	case apiErr.Code >= 500 && apiErr.Code <= 599:
		return true
	}
	return false
}

// responseSchema constrains the model to the array of judgements the parser
// expects, so the common case needs no salvaging.
func responseSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeArray,
		Items: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"id":         {Type: genai.TypeString, Description: "the id of the email, copied exactly"},
				"confidence": {Type: genai.TypeNumber, Description: "0 to 1: certainty this is routine mail genuinely from the brand"},
				"reason":     {Type: genai.TypeString, Description: "at most 200 characters"},
			},
			Required:         []string{"id", "confidence", "reason"},
			PropertyOrdering: []string{"id", "confidence", "reason"},
		},
	}
}
