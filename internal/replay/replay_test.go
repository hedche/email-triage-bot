package replay

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// record builds one Decision Log entry: the saved Decision plus the Message
// summary a Replay re-evaluates.
func record(account, id, name, email, subject string, tier triage.Tier, rule string, verdict triage.Verdict) decisionlog.Record {
	return decisionlog.Record{
		Decision: triage.Decision{
			RunID:     "run-1",
			Account:   account,
			MessageID: id,
			Tier:      tier,
			Rule:      rule,
			Verdict:   verdict,
			Action:    triage.ActionMarkedRead,
		},
		Message: decisionlog.MessageSummary{
			ID:       id,
			Account:  account,
			From:     triage.Address{Name: name, Email: email},
			Subject:  subject,
			Snippet:  "snippet",
			Received: time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC),
		},
	}
}

func rules(t *testing.T, body string) *triage.Rules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	loaded, err := triage.LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return loaded
}

func TestRunDetectsChanges(t *testing.T) {
	records := []decisionlog.Record{
		// Still marked read by the same sender rule: unchanged.
		record("me@example.com", "m1", "Royal Mail", "track@royalmail.com", "Your parcel",
			triage.TierSender, "domain:royalmail.com", triage.VerdictMarkRead),
		// A new protect rule now spares it.
		record("me@example.com", "m2", "Alice", "alice@example.com", "Numbers for Q1",
			triage.TierSender, "domain:example.com", triage.VerdictMarkRead),
		// A new brand rule makes it a candidate again.
		record("me@example.com", "m3", "Amazon.co.uk", "ship@amazon.co.uk", "Your order has dispatched",
			triage.TierNone, "", triage.VerdictLeave),
		// Another account's message: untouched by this account's rules.
		record("other@example.com", "m4", "Royal Mail", "track@royalmail.com", "Your parcel",
			triage.TierSender, "domain:royalmail.com", triage.VerdictMarkRead),
	}

	loaded := rules(t, `
threshold: 0.8
protect:
  - sender: alice@example.com
senders:
  - domain: royalmail.com
brands:
  - name: Amazon
`)

	report := Run(records, loaded, "")
	if report.Total != 4 {
		t.Errorf("Total = %d, want 4", report.Total)
	}
	if report.Changed != 2 || len(report.Changes) != 2 {
		t.Fatalf("Changed = %d (%d listed), want 2: %+v", report.Changed, len(report.Changes), report.Changes)
	}
	if report.NeedsJudge != 1 {
		t.Errorf("NeedsJudge = %d, want 1", report.NeedsJudge)
	}

	protect := report.Changes[0]
	if protect.MessageID != "m2" {
		t.Fatalf("first change = %+v, want m2", protect)
	}
	if protect.Before.Verdict != triage.VerdictMarkRead || protect.After.Verdict != triage.VerdictLeave {
		t.Errorf("m2 verdict %q → %q, want mark-read → leave", protect.Before.Verdict, protect.After.Verdict)
	}
	if protect.After.Tier != triage.TierProtect || protect.After.Rule != "sender:alice@example.com" {
		t.Errorf("m2 after = %+v", protect.After)
	}
	if protect.NeedsJudge {
		t.Errorf("m2 should not need a judge")
	}
	if protect.From != "Alice <alice@example.com>" {
		t.Errorf("m2 from = %q", protect.From)
	}
	if protect.Account != "me@example.com" || protect.Subject != "Numbers for Q1" {
		t.Errorf("m2 change = %+v", protect)
	}

	brand := report.Changes[1]
	if brand.MessageID != "m3" {
		t.Fatalf("second change = %+v, want m3", brand)
	}
	if !brand.NeedsJudge {
		t.Errorf("m3 should need a judge")
	}
	if brand.After.Tier != triage.TierBrand || brand.After.Brand != "Amazon" {
		t.Errorf("m3 after = %+v", brand.After)
	}
	if brand.After.Verdict != "" {
		t.Errorf("m3 after verdict = %q, want empty: only a Judge decides a Candidate", brand.After.Verdict)
	}
}

func TestRunFiltersByAccount(t *testing.T) {
	records := []decisionlog.Record{
		record("me@example.com", "m1", "Bank", "alerts@bank.example", "Statement",
			triage.TierNone, "", triage.VerdictLeave),
		record("other@example.com", "m2", "Bank", "alerts@bank.example", "Statement",
			triage.TierNone, "", triage.VerdictLeave),
	}
	loaded := rules(t, `
senders:
  - domain: bank.example
`)

	tests := []struct {
		name        string
		account     string
		wantTotal   int
		wantChanged int
	}{
		{name: "every account", account: "", wantTotal: 2, wantChanged: 2},
		{name: "one account", account: "me@example.com", wantTotal: 1, wantChanged: 1},
		{name: "account matching is case-insensitive", account: "Other@Example.com", wantTotal: 1, wantChanged: 1},
		{name: "unknown account", account: "nobody@example.com", wantTotal: 0, wantChanged: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := Run(records, loaded, tt.account)
			if report.Total != tt.wantTotal || report.Changed != tt.wantChanged {
				t.Errorf("Total/Changed = %d/%d, want %d/%d", report.Total, report.Changed, tt.wantTotal, tt.wantChanged)
			}
		})
	}
}

func TestRunUsesPerAccountRules(t *testing.T) {
	records := []decisionlog.Record{
		record("me@example.com", "m1", "Octopus Energy", "hello@octopus.energy", "Your statement",
			triage.TierNone, "", triage.VerdictLeave),
		record("other@example.com", "m2", "Octopus Energy", "hello@octopus.energy", "Your statement",
			triage.TierNone, "", triage.VerdictLeave),
	}
	loaded := rules(t, `
accounts:
  me@example.com:
    senders:
      - domain: octopus.energy
`)

	report := Run(records, loaded, "")
	if report.Changed != 1 {
		t.Fatalf("Changed = %d, want 1: only me@example.com has the rule\n%+v", report.Changed, report.Changes)
	}
	if got := report.Changes[0].Account; got != "me@example.com" {
		t.Errorf("changed account = %q, want me@example.com", got)
	}
}

func TestRunUnchangedBrandIsNotAChange(t *testing.T) {
	// The message was a Candidate before and still is: only a Judge could
	// decide it either way, so a Replay has nothing to report.
	rec := record("me@example.com", "m1", "Amazon.co.uk", "ship@amazon.co.uk", "Your order",
		triage.TierBrand, "brand:Amazon", triage.VerdictMarkRead)
	rec.Decision.Brand = "Amazon"
	confidence := 0.9
	rec.Decision.Confidence = &confidence

	loaded := rules(t, "brands:\n  - name: Amazon\n")
	report := Run([]decisionlog.Record{rec}, loaded, "")
	if report.Changed != 0 || report.NeedsJudge != 0 {
		t.Errorf("Changed/NeedsJudge = %d/%d, want 0/0: %+v", report.Changed, report.NeedsJudge, report.Changes)
	}

	// A different Brand, though, is a change worth reporting.
	renamed := rules(t, "brands:\n  - name: Amazon.co.uk\n")
	report = Run([]decisionlog.Record{rec}, renamed, "")
	if report.Changed != 1 || report.NeedsJudge != 1 {
		t.Errorf("Changed/NeedsJudge = %d/%d, want 1/1: %+v", report.Changed, report.NeedsJudge, report.Changes)
	}
}

func TestRunFallsBackToTheDecisionAccount(t *testing.T) {
	rec := record("me@example.com", "m1", "Bank", "alerts@bank.example", "Statement",
		triage.TierNone, "", triage.VerdictLeave)
	rec.Message.Account = "" // older logs may not repeat the account on the message
	loaded := rules(t, "accounts:\n  me@example.com:\n    senders:\n      - domain: bank.example\n")

	report := Run([]decisionlog.Record{rec}, loaded, "me@example.com")
	if report.Total != 1 || report.Changed != 1 {
		t.Fatalf("Total/Changed = %d/%d, want 1/1", report.Total, report.Changed)
	}
}

func TestRunWithoutRules(t *testing.T) {
	records := []decisionlog.Record{
		record("me@example.com", "m1", "Royal Mail", "track@royalmail.com", "Your parcel",
			triage.TierSender, "domain:royalmail.com", triage.VerdictMarkRead),
	}
	report := Run(records, nil, "")
	if report.Total != 1 || report.Changed != 1 {
		t.Fatalf("Total/Changed = %d/%d, want 1/1", report.Total, report.Changed)
	}
	if report.Changes[0].After.Tier != triage.TierNone {
		t.Errorf("after = %+v, want the none tier with no rules at all", report.Changes[0].After)
	}
}

func TestReportWrite(t *testing.T) {
	records := []decisionlog.Record{
		record("me@example.com", "m1", "Alice", "alice@example.com",
			"A very long subject line that goes on and on and really must be shortened for the table",
			triage.TierSender, "domain:example.com", triage.VerdictMarkRead),
		record("me@example.com", "m2", "Amazon.co.uk", "ship@amazon.co.uk", "Your order has dispatched",
			triage.TierNone, "", triage.VerdictLeave),
	}
	loaded := rules(t, "protect:\n  - sender: alice@example.com\nbrands:\n  - name: Amazon\n")

	var buf bytes.Buffer
	Run(records, loaded, "").Write(&buf)
	out := buf.String()

	for _, want := range []string{
		"me@example.com",
		"Alice <alice@example.com>",
		"sender/mark-read",
		"protect/leave",
		"needs judge",
		"2 replayed, 2 changed, 1 need judge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "…") && strings.Contains(line, "really must be shortened") {
			t.Errorf("long subject was not truncated: %s", line)
		}
	}
	if strings.Contains(out, "A very long subject line that goes on and on and really must be") {
		t.Errorf("subject longer than 60 runes was printed in full:\n%s", out)
	}
}

func TestReportWriteWithNoChanges(t *testing.T) {
	var buf bytes.Buffer
	Report{Total: 12}.Write(&buf)
	if got := buf.String(); got != "12 replayed, 0 changed, 0 need judge\n" {
		t.Errorf("output = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{in: "short", max: 10, want: "short"},
		{in: "0123456789", max: 10, want: "0123456789"},
		{in: "0123456789x", max: 10, want: "012345678…"},
		{in: "line\nbreak", max: 20, want: "line break"},
		{in: "  padded  ", max: 20, want: "padded"},
		{in: "ααααα", max: 3, want: "αα…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.max); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
		if n := len([]rune(truncate(tt.in, tt.max))); n > tt.max {
			t.Errorf("truncate(%q, %d) is %d runes long", tt.in, tt.max, n)
		}
	}
}

// TestRunOverASavedDecisionLog replays a real Decision Log file against a real
// rules file, the way the replay command does.
func TestRunOverASavedDecisionLog(t *testing.T) {
	records, err := decisionlog.Read(filepath.Join("testdata", "decisions.jsonl"))
	if err != nil {
		t.Fatalf("read decision log: %v", err)
	}
	loaded, err := triage.LoadRules(filepath.Join("testdata", "rules.yaml"))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	report := Run(records, loaded, "")
	if report.Total != 3 {
		t.Fatalf("Total = %d, want 3", report.Total)
	}
	if report.Changed != 3 || report.NeedsJudge != 1 {
		t.Fatalf("Changed/NeedsJudge = %d/%d, want 3/1: %+v", report.Changed, report.NeedsJudge, report.Changes)
	}

	byID := map[string]Change{}
	for _, c := range report.Changes {
		byID[c.MessageID] = c
	}
	if c, ok := byID["msg-1"]; !ok || c.After.Tier != triage.TierProtect {
		t.Errorf("msg-1 should now be protected, got %+v (present=%v)", c.After, ok)
	}
	if c, ok := byID["msg-3"]; !ok || !c.NeedsJudge || c.After.Brand != "Octopus Energy" {
		t.Errorf("msg-3 should now need a judge for Octopus Energy, got %+v (present=%v)", c.After, ok)
	}
}
