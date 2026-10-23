package judge

import (
	"errors"
	"strings"
	"testing"

	"github.com/hedche/email-triage-bot/internal/triage"
)

func batchOf(ids ...string) []triage.Candidate {
	out := make([]triage.Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, candidate(id, "Amazon"))
	}
	return out
}

func TestParseJudgementsCleanArray(t *testing.T) {
	raw := `[{"id":"m1","confidence":0.92,"reason":"dispatch note"},{"id":"m2","confidence":0.1,"reason":"payment problem"}]`
	got, err := ParseJudgements(raw, batchOf("m1", "m2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d judgements, want 2", len(got))
	}
	if got[0].MessageID != "m1" || got[0].Confidence != 0.92 || got[0].Reason != "dispatch note" {
		t.Errorf("first judgement = %+v", got[0])
	}
	if got[1].MessageID != "m2" || got[1].Confidence != 0.1 {
		t.Errorf("second judgement = %+v", got[1])
	}
}

func TestParseJudgementsFenced(t *testing.T) {
	raw := "```json\n[{\"id\":\"m1\",\"confidence\":0.5,\"reason\":\"maybe\"}]\n```"
	got, err := ParseJudgements(raw, batchOf("m1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Confidence != 0.5 {
		t.Errorf("confidence = %v, want 0.5", got[0].Confidence)
	}
}

func TestParseJudgementsProseWrapped(t *testing.T) {
	raw := "Sure! Here are the judgements you asked for:\n[{\"id\":\"m1\",\"confidence\":0.8,\"reason\":\"routine\"}]\nLet me know if you need anything else."
	got, err := ParseJudgements(raw, batchOf("m1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Confidence != 0.8 {
		t.Errorf("confidence = %v, want 0.8", got[0].Confidence)
	}
}

func TestParseJudgementsWrappedInObject(t *testing.T) {
	raw := `{"judgements":[{"id":"m1","confidence":0.7,"reason":"ok"}]}`
	got, err := ParseJudgements(raw, batchOf("m1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Confidence != 0.7 {
		t.Errorf("confidence = %v, want 0.7", got[0].Confidence)
	}
}

func TestParseJudgementsPreservesBatchOrder(t *testing.T) {
	raw := `[{"id":"m3","confidence":0.3,"reason":"c"},{"id":"m1","confidence":0.1,"reason":"a"},{"id":"m2","confidence":0.2,"reason":"b"}]`
	got, err := ParseJudgements(raw, batchOf("m1", "m2", "m3"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"m1", "m2", "m3"}
	for i, id := range want {
		if got[i].MessageID != id {
			t.Errorf("position %d = %q, want %q", i, got[i].MessageID, id)
		}
	}
	if got[0].Confidence != 0.1 || got[2].Confidence != 0.3 {
		t.Errorf("confidences follow the wrong messages: %+v", got)
	}
}

func TestParseJudgementsMissingCandidateFailsClosed(t *testing.T) {
	raw := `[{"id":"m1","confidence":0.9,"reason":"routine"}]`
	got, err := ParseJudgements(raw, batchOf("m1", "m2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d judgements, want 2", len(got))
	}
	if got[1].MessageID != "m2" || got[1].Confidence != 0 || got[1].Reason != ReasonOmitted {
		t.Errorf("omitted candidate = %+v", got[1])
	}
}

func TestParseJudgementsDuplicateIDFailsClosed(t *testing.T) {
	raw := `[{"id":"m1","confidence":0.9,"reason":"routine"},{"id":"m1","confidence":0.1,"reason":"actually not"}]`
	got, err := ParseJudgements(raw, batchOf("m1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Confidence != 0 || got[0].Reason != ReasonOmitted {
		t.Errorf("duplicated candidate = %+v, want fail closed", got[0])
	}
}

func TestParseJudgementsConfidenceRange(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantConf float64
		omitted  bool
	}{
		{"clamps tiny overshoot", `[{"id":"m1","confidence":1.0000001,"reason":"r"}]`, 1, false},
		{"clamps tiny undershoot", `[{"id":"m1","confidence":-0.0000001,"reason":"r"}]`, 0, false},
		{"rejects above range", `[{"id":"m1","confidence":1.5,"reason":"r"}]`, 0, true},
		{"rejects below range", `[{"id":"m1","confidence":-3,"reason":"r"}]`, 0, true},
		{"rejects percentage", `[{"id":"m1","confidence":95,"reason":"r"}]`, 0, true},
		{"rejects missing confidence", `[{"id":"m1","reason":"r"}]`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJudgements(tc.raw, batchOf("m1"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[0].Confidence != tc.wantConf {
				t.Errorf("confidence = %v, want %v", got[0].Confidence, tc.wantConf)
			}
			if tc.omitted && got[0].Reason != ReasonOmitted {
				t.Errorf("reason = %q, want %q", got[0].Reason, ReasonOmitted)
			}
			if !tc.omitted && got[0].Reason != "r" {
				t.Errorf("reason = %q, want %q", got[0].Reason, "r")
			}
		})
	}
}

func TestParseJudgementsIgnoresUnknownIDs(t *testing.T) {
	raw := `[{"id":"ghost","confidence":0.99,"reason":"invented"},{"id":"m1","confidence":0.4,"reason":"real"}]`
	got, err := ParseJudgements(raw, batchOf("m1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].MessageID != "m1" || got[0].Confidence != 0.4 {
		t.Errorf("got %+v", got)
	}
}

func TestParseJudgementsTrimsAndCapsReason(t *testing.T) {
	long := strings.Repeat("x", 500)
	raw := `[{"id":"m1","confidence":0.4,"reason":"  padded  "},{"id":"m2","confidence":0.4,"reason":"` + long + `"}]`
	got, err := ParseJudgements(raw, batchOf("m1", "m2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Reason != "padded" {
		t.Errorf("reason = %q, want %q", got[0].Reason, "padded")
	}
	if n := len([]rune(got[1].Reason)); n > 200 {
		t.Errorf("reason length = %d, want <= 200", n)
	}
}

func TestParseJudgementsEmptyArray(t *testing.T) {
	got, err := ParseJudgements("[]", batchOf("m1", "m2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, j := range got {
		if j.Confidence != 0 || j.Reason != ReasonOmitted {
			t.Errorf("judgement %d = %+v, want fail closed", i, j)
		}
	}
}

func TestParseJudgementsUnparseable(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"prose only":  "I'm sorry, I can't help with that.",
		"broken json": "[{\"id\": \"m1\", confidence: }]",
		"object only": `{"id":"m1","confidence":0.5}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseJudgements(raw, batchOf("m1"))
			if !errors.Is(err, ErrUnparseable) {
				t.Fatalf("err = %v, want ErrUnparseable", err)
			}
		})
	}
}
