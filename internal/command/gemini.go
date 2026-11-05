package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/genai"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// Gemini is the Interpreter backed by the same paid-tier Gemini project as the
// Judge (ADR 0002).
type Gemini struct {
	client  *genai.Client
	model   string
	timeout time.Duration
}

// NewGemini builds a Gemini Interpreter.
func NewGemini(apiKey, model string, timeout time.Duration) (*Gemini, error) {
	return newGemini(genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI}, model, timeout)
}

func newGemini(cc genai.ClientConfig, model string, timeout time.Duration) (*Gemini, error) {
	if cc.APIKey == "" {
		return nil, errors.New("interpreter: GEMINI_API_KEY is required")
	}
	if model == "" {
		return nil, errors.New("interpreter: model is required")
	}
	if cc.Backend == genai.BackendUnspecified {
		cc.Backend = genai.BackendGeminiAPI
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client, err := genai.NewClient(context.Background(), &cc)
	if err != nil {
		return nil, fmt.Errorf("interpreter: new client: %w", err)
	}
	return &Gemini{client: client, model: model, timeout: timeout}, nil
}

func (g *Gemini) Name() string { return "gemini/" + g.model }

func (g *Gemini) Interpret(ctx context.Context, req Request) (Raw, error) {
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		Temperature:       genai.Ptr(float32(0)),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    rawSchema(),
	}
	callCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	resp, err := g.client.Models.GenerateContent(callCtx, g.model,
		[]*genai.Content{genai.NewContentFromText(BuildPrompt(req), genai.RoleUser)}, cfg)
	if err != nil {
		return Raw{}, fmt.Errorf("interpreter: generate content: %w", err)
	}
	return ParseRaw(resp.Text())
}

func rawSchema() *genai.Schema {
	ops := make([]string, 0, len(triage.Ops))
	for _, op := range triage.Ops {
		ops = append(ops, string(op))
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"changes": {
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"op":      {Type: genai.TypeString, Enum: ops},
						"value":   {Type: genai.TypeString, Description: "address, domain or subject text; empty for brand ops"},
						"subject": {Type: genai.TypeString, Description: "read_sender/read_domain only: stable part of the subject the rule also requires; empty otherwise"},
						"brand":   {Type: genai.TypeString, Description: "brand ops only"},
						// No enum: Gemini rejects an empty enum value, and most
						// ops have no verdict. Ground validates it instead.
						"verdict":      {Type: genai.TypeString, Description: "brand_example only: mark-read or leave; empty for every other op"},
						"note":         {Type: genai.TypeString, Description: "why, in a few words; for brand ops the guidance or example description"},
						"all_accounts": {Type: genai.TypeBoolean},
					},
					Required:         []string{"op", "value", "note", "all_accounts"},
					PropertyOrdering: []string{"op", "value", "subject", "brand", "verdict", "note", "all_accounts"},
				},
			},
			"remove": {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString}},
			"reply":  {Type: genai.TypeString},
		},
		Required:         []string{"changes", "remove", "reply"},
		PropertyOrdering: []string{"changes", "remove", "reply"},
	}
}
