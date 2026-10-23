package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRules puts a rules file in a temp dir and returns its path.
func writeRules(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return path
}

const fullRules = `
threshold: 0.7
protect:
  - sender: "  Alice@Example.COM "
  - domain: Example-Corp.com
  - keyword: "  invoice  "
senders:
  - address: NoReply@GitHub.com
  - domain: RoyalMail.com
    note: tracking updates
keywords:
  - subject: Your Weekly Digest
brands:
  - name: Amazon
    note: Dispatch mail is routine.
    examples:
      - description: Dispatch confirmation
        verdict: mark-read
      - description: Unusual sign-in
        verdict: leave
accounts:
  SomeOne@Gmail.com:
    protect:
      - sender: Boss@Example.com
    senders:
      - domain: Octopus.Energy
    keywords:
      - subject: Statement ready
    brands:
      - name: Octopus Energy
`

func TestLoadRulesNormalises(t *testing.T) {
	rules, err := LoadRules(writeRules(t, fullRules))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if rules.Threshold != 0.7 {
		t.Errorf("threshold = %v, want 0.7", rules.Threshold)
	}
	if got, want := rules.Protect[0].Sender, "alice@example.com"; got != want {
		t.Errorf("protect sender = %q, want %q", got, want)
	}
	if got, want := rules.Protect[1].Domain, "example-corp.com"; got != want {
		t.Errorf("protect domain = %q, want %q", got, want)
	}
	if got, want := rules.Protect[2].Keyword, "invoice"; got != want {
		t.Errorf("protect keyword = %q, want %q", got, want)
	}
	if got, want := rules.Senders[0].Address, "noreply@github.com"; got != want {
		t.Errorf("sender address = %q, want %q", got, want)
	}
	if got, want := rules.Senders[1].Domain, "royalmail.com"; got != want {
		t.Errorf("sender domain = %q, want %q", got, want)
	}
	if got, want := rules.Keywords[0].Subject, "Your Weekly Digest"; got != want {
		t.Errorf("keyword subject = %q, want %q (case is kept; matching lowers it)", got, want)
	}
	if got, want := rules.Brands[0].Name, "Amazon"; got != want {
		t.Errorf("brand name = %q, want %q", got, want)
	}
	if len(rules.Brands[0].Examples) != 2 {
		t.Fatalf("examples = %d, want 2", len(rules.Brands[0].Examples))
	}
	if _, ok := rules.Accounts["someone@gmail.com"]; !ok {
		t.Errorf("account key not lower-cased: %v keys", keys(rules.Accounts))
	}
}

func keys(m map[string]RuleSet) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLoadRulesThresholdDefault(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    float64
		wantErr string
	}{
		{name: "absent defaults", body: "senders:\n  - address: a@b.com\n", want: DefaultThreshold},
		{name: "explicit zero is kept", body: "threshold: 0\n", want: 0},
		{name: "one is allowed", body: "threshold: 1\n", want: 1},
		{name: "above one", body: "threshold: 1.5\n", wantErr: "between 0 and 1"},
		{name: "below zero", body: "threshold: -0.1\n", wantErr: "between 0 and 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := LoadRules(writeRules(t, tt.body))
			if tt.wantErr != "" {
				requireErr(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("LoadRules: %v", err)
			}
			if rules.Threshold != tt.want {
				t.Errorf("threshold = %v, want %v", rules.Threshold, tt.want)
			}
		})
	}
}

func TestLoadRulesValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "protect with no field",
			body:    "protect:\n  - note: nothing to match\n",
			wantErr: "protect[0]: exactly one of sender, domain or keyword",
		},
		{
			name:    "protect with two fields",
			body:    "protect:\n  - sender: a@b.com\n    domain: b.com\n",
			wantErr: "protect[0]: exactly one of sender, domain or keyword",
		},
		{
			name:    "sender with no field",
			body:    "senders:\n  - note: nothing\n",
			wantErr: "senders[0]: exactly one of address or domain",
		},
		{
			name:    "sender with both fields",
			body:    "senders:\n  - address: a@b.com\n    domain: b.com\n",
			wantErr: "senders[0]: exactly one of address or domain",
		},
		{
			name:    "empty keyword subject",
			body:    "keywords:\n  - subject: \"   \"\n",
			wantErr: "keywords[0]: subject must not be empty",
		},
		{
			name:    "empty brand name",
			body:    "brands:\n  - note: no name\n",
			wantErr: "brands[0]: name must not be empty",
		},
		{
			name:    "bad example verdict",
			body:    "brands:\n  - name: Amazon\n    examples:\n      - description: Dispatch\n        verdict: archive\n",
			wantErr: `verdict "archive"`,
		},
		{
			name:    "empty example description",
			body:    "brands:\n  - name: Amazon\n    examples:\n      - description: \"\"\n        verdict: leave\n",
			wantErr: "description must not be empty",
		},
		{
			name:    "per-account rule is validated too",
			body:    "accounts:\n  me@example.com:\n    senders:\n      - note: nothing\n",
			wantErr: "accounts.me@example.com.senders[0]",
		},
		{
			name:    "duplicate account after lower-casing",
			body:    "accounts:\n  Me@Example.com:\n    senders:\n      - domain: a.com\n  me@example.com:\n    senders:\n      - domain: b.com\n",
			wantErr: "duplicate account",
		},
		{
			name:    "malformed yaml",
			body:    "threshold: [oops\n",
			wantErr: "parse rules file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRules(writeRules(t, tt.body))
			requireErr(t, err, tt.wantErr)
		})
	}
}

func TestLoadRulesExampleVerdictCaseInsensitive(t *testing.T) {
	rules, err := LoadRules(writeRules(t, "brands:\n  - name: Amazon\n    examples:\n      - description: Dispatch\n        verdict: MARK-READ\n"))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if got := rules.Brands[0].Examples[0].Verdict; got != VerdictMarkRead {
		t.Errorf("verdict = %q, want %q", got, VerdictMarkRead)
	}
}

func TestLoadRulesMissingFile(t *testing.T) {
	_, err := LoadRules(filepath.Join(t.TempDir(), "nope.yaml"))
	requireErr(t, err, "read rules file")
}

func TestRulesForIsAdditive(t *testing.T) {
	rules, err := LoadRules(writeRules(t, fullRules))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	global := rules.For("nobody@example.com")
	if len(global.Protect) != 3 || len(global.Senders) != 2 || len(global.Keywords) != 1 || len(global.Brands) != 1 {
		t.Fatalf("unknown account got %d protect, %d senders, %d keywords, %d brands; want the global set",
			len(global.Protect), len(global.Senders), len(global.Keywords), len(global.Brands))
	}

	merged := rules.For("SomeOne@Gmail.com") // lookup is case-insensitive
	if len(merged.Protect) != 4 || len(merged.Senders) != 3 || len(merged.Keywords) != 2 || len(merged.Brands) != 2 {
		t.Fatalf("merged got %d protect, %d senders, %d keywords, %d brands; want 4/3/2/2",
			len(merged.Protect), len(merged.Senders), len(merged.Keywords), len(merged.Brands))
	}
	if got, want := merged.Protect[0].Sender, "alice@example.com"; got != want {
		t.Errorf("global protect rule should come first, got %q", got)
	}
	if got, want := merged.Protect[3].Sender, "boss@example.com"; got != want {
		t.Errorf("per-account protect rule should be appended, got %q want %q", got, want)
	}
	if got, want := merged.Brands[1].Name, "Octopus Energy"; got != want {
		t.Errorf("per-account brand = %q, want %q", got, want)
	}

	// Merging must not mutate the global rules for the next account.
	again := rules.For("nobody@example.com")
	if len(again.Protect) != 3 {
		t.Errorf("global protect leaked per-account rules: %d", len(again.Protect))
	}
}

func TestRulesForNilReceiver(t *testing.T) {
	var rules *Rules
	if rs := rules.For("me@example.com"); len(rs.Protect)+len(rs.Senders)+len(rs.Keywords)+len(rs.Brands) != 0 {
		t.Errorf("nil Rules should yield an empty RuleSet, got %+v", rs)
	}
}

func requireErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}
