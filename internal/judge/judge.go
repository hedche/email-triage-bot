// Package judge defines the Judge interface: the AI component that decides
// whether a Candidate is routine mail from its Brand. Gemini and any
// OpenAI-compatible chat endpoint are the implementations.
package judge

import (
	"context"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// Judge scores a batch of Candidates. Implementations must return exactly one
// Judgement per Candidate, keyed by MessageID; the run treats a missing or
// out-of-range entry as a failure for that Candidate (fail closed). Returning
// an error fails the whole batch closed.
type Judge interface {
	// Name identifies the provider and model for telemetry, e.g. "gemini/gemini-3.5-flash-lite".
	Name() string
	Judge(ctx context.Context, batch []triage.Candidate) ([]triage.Judgement, error)
}
