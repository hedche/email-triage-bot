package decisionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/triage"
)

func sampleMessage() triage.Message {
	return triage.Message{
		ID:       "msg-1",
		ThreadID: "thread-1",
		Account:  "me@example.com",
		From:     triage.Address{Name: "Royal Mail", Email: "track@royalmail.com"},
		Subject:  "Your parcel is on its way",
		Snippet:  "Track your parcel",
		Body:     "SECRET BODY that must never reach the decision log",
		Received: time.Date(2026, 3, 5, 9, 30, 0, 0, time.UTC),
		Labels:   []string{"INBOX", "UNREAD"},
	}
}

func confidence(v float64) *float64 { return &v }

func sampleRecords() []Record {
	msg := sampleMessage()
	return []Record{
		{
			Decision: triage.Decision{
				RunID: "run-1", Account: msg.Account, MessageID: msg.ID,
				Tier: triage.TierSender, Rule: "domain:royalmail.com",
				Verdict: triage.VerdictMarkRead, Action: triage.ActionMarkedRead,
			},
			Message: Summarise(msg),
		},
		{
			Decision: triage.Decision{
				RunID: "run-1", Account: msg.Account, MessageID: "msg-2",
				Tier: triage.TierBrand, Rule: "brand:Amazon", Brand: "Amazon",
				Confidence: confidence(0.93), Reason: "routine dispatch mail",
				Verdict: triage.VerdictMarkRead, Action: triage.ActionDryRun,
				DryRun: true, Judge: "gemini/gemini-3.5-flash-lite",
			},
			Message: MessageSummary{
				ID: "msg-2", Account: msg.Account,
				From:     triage.Address{Name: "Amazon.co.uk", Email: "ship@amazon.co.uk"},
				Subject:  "Your order has dispatched",
				Snippet:  "Arriving Thursday",
				Received: time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
			},
		},
	}
}

func TestSummariseDropsTheBody(t *testing.T) {
	msg := sampleMessage()
	got := Summarise(msg)

	want := MessageSummary{
		ID:       msg.ID,
		Account:  msg.Account,
		From:     msg.From,
		Subject:  msg.Subject,
		Snippet:  msg.Snippet,
		Received: msg.Received,
	}
	if got != want {
		t.Errorf("Summarise = %+v, want %+v", got, want)
	}

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "SECRET BODY") {
		t.Errorf("body leaked into the summary: %s", data)
	}
	for _, field := range []string{`"id"`, `"account"`, `"from"`, `"subject"`, `"snippet"`, `"received"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("summary JSON is missing %s: %s", field, data)
		}
	}
	if strings.Contains(string(data), `"body"`) || strings.Contains(string(data), `"labels"`) {
		t.Errorf("summary JSON carries fields it should not: %s", data)
	}
}

func TestWriterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "decisions.jsonl") // parent dir does not exist yet
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if w.Path() != path {
		t.Errorf("Path() = %q, want %q", w.Path(), path)
	}

	records := sampleRecords()
	for _, rec := range records {
		if err := w.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Every Append flushes, so the log is readable before Close.
	partial, err := Read(path)
	if err != nil {
		t.Fatalf("Read before Close: %v", err)
	}
	if len(partial) != len(records) {
		t.Errorf("read %d records before Close, want %d", len(partial), len(records))
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := w.Append(records[0]); err == nil {
		t.Errorf("Append after Close: want an error")
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != len(records) {
		t.Fatalf("read %d records, want %d", len(got), len(records))
	}
	for i := range records {
		if got[i].Message != records[i].Message {
			t.Errorf("record %d message = %+v, want %+v", i, got[i].Message, records[i].Message)
		}
		if !sameDecision(got[i].Decision, records[i].Decision) {
			t.Errorf("record %d decision = %+v, want %+v", i, got[i].Decision, records[i].Decision)
		}
	}
	if got[1].Decision.Confidence == nil || *got[1].Decision.Confidence != 0.93 {
		t.Errorf("confidence did not survive the round trip: %v", got[1].Decision.Confidence)
	}
	if !got[1].Message.Received.Equal(records[1].Message.Received) {
		t.Errorf("received time = %v, want %v", got[1].Message.Received, records[1].Message.Received)
	}
}

func sameDecision(a, b triage.Decision) bool {
	ac, bc := a.Confidence, b.Confidence
	a.Confidence, b.Confidence = nil, nil
	if a != b {
		return false
	}
	switch {
	case ac == nil && bc == nil:
		return true
	case ac == nil || bc == nil:
		return false
	default:
		return *ac == *bc
	}
}

func TestWriterOneObjectPerLineAndNoBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, rec := range sampleRecords() {
		if err := w.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "SECRET BODY") || strings.Contains(text, `"body"`) {
		t.Errorf("decision log contains a message body: %s", text)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), text)
	}
	for i, line := range lines {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Errorf("line %d is not one JSON object: %v", i+1, err)
			continue
		}
		if _, ok := probe["decision"]; !ok {
			t.Errorf("line %d has no decision key: %s", i+1, line)
		}
		if _, ok := probe["message"]; !ok {
			t.Errorf("line %d has no message key: %s", i+1, line)
		}
	}
}

func TestNewWriterTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	if err := os.WriteFile(path, []byte("stale line from an earlier run\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(sampleRecords()[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(raw), "stale line") {
		t.Errorf("NewWriter did not truncate: %s", raw)
	}
}

func TestReadFromTestdata(t *testing.T) {
	records, err := Read(filepath.Join("testdata", "decisions.jsonl"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3 (blank lines skipped)", len(records))
	}
	if records[0].Decision.Tier != triage.TierSender || records[0].Message.From.Email != "track@royalmail.com" {
		t.Errorf("record 0 = %+v", records[0])
	}
	if records[2].Decision.Tier != triage.TierNone || records[2].Decision.Verdict != triage.VerdictLeave {
		t.Errorf("record 2 = %+v", records[2])
	}
}

func TestReadErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := Read(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil ||
			!strings.Contains(err.Error(), "open decision log") {
			t.Fatalf("err = %v, want an open error", err)
		}
	})
	t.Run("malformed line names its number", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "decisions.jsonl")
		body := `{"decision":{},"message":{}}` + "\n" + "{not json\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := Read(path)
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("err = %v, want an error naming line 2", err)
		}
	})
}

func TestReadEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	records, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("got %d records, want 0", len(records))
	}
}
