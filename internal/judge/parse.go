package judge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// ErrUnparseable is returned when a Judge response contains no JSON array that
// can be decoded at all. The run fails the whole batch closed on this error.
var ErrUnparseable = errors.New("judge response contained no parseable JSON array")

// ReasonOmitted is the Reason recorded for a Candidate the Judge did not
// usefully answer for. Paired with Confidence 0 it always falls below the
// Threshold, so the Candidate is left.
const ReasonOmitted = "judge omitted this message"

// maxReasonChars caps a Reason so a chatty model cannot bloat the Decision Log.
const maxReasonChars = 200

// confidenceSlack is the float noise tolerated either side of [0,1] before a
// Confidence is treated as invalid rather than clamped.
const confidenceSlack = 0.01

type rawJudgement struct {
	ID         string   `json:"id"`
	Confidence *float64 `json:"confidence"`
	Reason     string   `json:"reason"`
}

// ParseJudgements turns a Judge's raw text response into exactly one Judgement
// per Candidate, in batch order.
//
// It tolerates a ```json fence and prose either side of the array. A Candidate
// the model omitted, duplicated or answered with an out-of-range Confidence
// gets Confidence 0 and Reason ReasonOmitted, which fails closed at the
// Threshold; that is not an error. Only a response with no decodable array at
// all returns an error, which wraps ErrUnparseable.
func ParseJudgements(raw string, batch []triage.Candidate) ([]triage.Judgement, error) {
	arr, ok := extractJSONArray(raw)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnparseable, truncate(strings.TrimSpace(raw), 120))
	}

	var decoded []rawJudgement
	if err := json.Unmarshal([]byte(arr), &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnparseable, err)
	}

	// Index the usable entries by id. A repeated id is poisoned rather than
	// resolved: we cannot tell which of the two the model meant.
	byID := make(map[string]triage.Judgement, len(decoded))
	poisoned := make(map[string]bool)
	wanted := make(map[string]bool, len(batch))
	for _, c := range batch {
		wanted[c.Message.ID] = true
	}

	for _, d := range decoded {
		id := strings.TrimSpace(d.ID)
		if !wanted[id] {
			continue // hallucinated id: nothing in the batch to attach it to
		}
		if _, seen := byID[id]; seen {
			poisoned[id] = true
			continue
		}
		if poisoned[id] {
			continue
		}
		if d.Confidence == nil {
			poisoned[id] = true
			continue
		}
		conf := *d.Confidence
		if conf > 1+confidenceSlack || conf < -confidenceSlack {
			poisoned[id] = true
			continue
		}
		// Clamp float noise such as 1.0000001 or -0.0000001.
		if conf > 1 {
			conf = 1
		}
		if conf < 0 {
			conf = 0
		}
		byID[id] = triage.Judgement{
			MessageID:  id,
			Confidence: conf,
			Reason:     truncate(strings.TrimSpace(d.Reason), maxReasonChars),
		}
	}

	out := make([]triage.Judgement, len(batch))
	for i, c := range batch {
		id := c.Message.ID
		j, ok := byID[id]
		if !ok || poisoned[id] {
			out[i] = triage.Judgement{MessageID: id, Confidence: 0, Reason: ReasonOmitted}
			continue
		}
		out[i] = j
	}
	return out, nil
}

// extractJSONArray finds the outermost [ ... ] in raw, ignoring a surrounding
// markdown fence and any prose before or after it. A JSON object wrapping the
// array (as some servers return in JSON mode) is handled by the same rule.
func extractJSONArray(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < start {
		return "", false
	}
	return s[start : end+1], true
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
