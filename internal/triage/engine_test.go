package triage

import "testing"

// sampleRules is the RuleSet every engine test evaluates against.
func sampleRules() RuleSet {
	return RuleSet{
		Protect: []ProtectRule{
			{Sender: "alice@example.com"},
			{Domain: "example-corp.com"},
			{Keyword: "invoice"},
		},
		Senders: []SenderRule{
			{Address: "noreply@github.com"},
			{Domain: "royalmail.com"},
		},
		Keywords: []KeywordRule{
			{Subject: "your weekly digest"},
		},
		Brands: []BrandRule{
			{Name: "Amazon", Note: "Dispatch mail is routine."},
			{Name: "Octopus Energy"},
		},
	}
}

func msg(name, email, subject string) Message {
	return Message{
		ID:      "m1",
		Account: "me@example.com",
		From:    Address{Name: name, Email: email},
		Subject: subject,
	}
}

func TestEngineEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		msg         Message
		wantTier    Tier
		wantRule    string
		wantBrand   string
		wantVerdict Verdict
	}{
		{
			name:        "protect sender wins",
			msg:         msg("Alice", "Alice@Example.com", "Your weekly digest of invoices"),
			wantTier:    TierProtect,
			wantRule:    "sender:alice@example.com",
			wantVerdict: VerdictLeave,
		},
		{
			name:        "protect domain",
			msg:         msg("Payroll", "pay@example-corp.com", "Hello"),
			wantTier:    TierProtect,
			wantRule:    "domain:example-corp.com",
			wantVerdict: VerdictLeave,
		},
		{
			name:        "protect domain matches a subdomain",
			msg:         msg("Payroll", "pay@mail.corp.example-corp.com", "Hello"),
			wantTier:    TierProtect,
			wantRule:    "domain:example-corp.com",
			wantVerdict: VerdictLeave,
		},
		{
			name:        "protect keyword is a case-insensitive subject substring",
			msg:         msg("Someone", "someone@random.example", "Your INVOICE is ready"),
			wantTier:    TierProtect,
			wantRule:    "keyword:invoice",
			wantVerdict: VerdictLeave,
		},
		{
			name:        "protect beats sender rule",
			msg:         msg("Royal Mail", "post@royalmail.com", "Invoice for postage"),
			wantTier:    TierProtect,
			wantRule:    "keyword:invoice",
			wantVerdict: VerdictLeave,
		},
		{
			name:        "sender address",
			msg:         msg("GitHub", "NoReply@GitHub.com", "New release"),
			wantTier:    TierSender,
			wantRule:    "address:noreply@github.com",
			wantVerdict: VerdictMarkRead,
		},
		{
			name:        "sender domain matches a subdomain",
			msg:         msg("Royal Mail", "track@notifications.royalmail.com", "Your parcel"),
			wantTier:    TierSender,
			wantRule:    "domain:royalmail.com",
			wantVerdict: VerdictMarkRead,
		},
		{
			name:        "sender rule beats keyword rule",
			msg:         msg("GitHub", "noreply@github.com", "Your weekly digest"),
			wantTier:    TierSender,
			wantRule:    "address:noreply@github.com",
			wantVerdict: VerdictMarkRead,
		},
		{
			name:        "keyword rule",
			msg:         msg("Some List", "list@random.example", "Here is YOUR WEEKLY DIGEST for March"),
			wantTier:    TierKeyword,
			wantRule:    "subject:your weekly digest",
			wantVerdict: VerdictMarkRead,
		},
		{
			name:        "keyword rule beats brand",
			msg:         msg("Amazon", "ship@amazon.co.uk", "Your weekly digest"),
			wantTier:    TierKeyword,
			wantRule:    "subject:your weekly digest",
			wantVerdict: VerdictMarkRead,
		},
		{
			name:      "brand via sender display name",
			msg:       msg("amazon.co.uk Dispatch", "ship-confirm@marketplace.example", "Your order"),
			wantTier:  TierBrand,
			wantRule:  "brand:Amazon",
			wantBrand: "Amazon",
		},
		{
			name:      "brand via sender domain",
			msg:       msg("Dispatch", "ship@amazon.co.uk", "Your order has shipped"),
			wantTier:  TierBrand,
			wantRule:  "brand:Amazon",
			wantBrand: "Amazon",
		},
		{
			name:      "brand via subject",
			msg:       msg("Courier", "hello@courier.example", "Your AMAZON order is late"),
			wantTier:  TierBrand,
			wantRule:  "brand:Amazon",
			wantBrand: "Amazon",
		},
		{
			name:      "multi-word brand matches a domain with spaces removed",
			msg:       msg("Energy", "hello@octopusenergy.com", "Statement"),
			wantTier:  TierBrand,
			wantRule:  "brand:Octopus Energy",
			wantBrand: "Octopus Energy",
		},
		{
			name:      "multi-word brand matches the display name with its space",
			msg:       msg("Octopus Energy", "hello@oe.example", "Statement"),
			wantTier:  TierBrand,
			wantRule:  "brand:Octopus Energy",
			wantBrand: "Octopus Energy",
		},
		{
			name:        "nothing matches",
			msg:         msg("A Human", "human@random.example", "Lunch on Thursday?"),
			wantTier:    TierNone,
			wantRule:    "",
			wantVerdict: VerdictLeave,
		},
	}

	engine := NewEngine(sampleRules(), 0.8)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Evaluate(tt.msg)
			if got.Tier != tt.wantTier {
				t.Errorf("tier = %q, want %q", got.Tier, tt.wantTier)
			}
			if got.Rule != tt.wantRule {
				t.Errorf("rule = %q, want %q", got.Rule, tt.wantRule)
			}
			if got.Brand != tt.wantBrand {
				t.Errorf("brand = %q, want %q", got.Brand, tt.wantBrand)
			}
			if got.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
			if got.Account != tt.msg.Account || got.MessageID != tt.msg.ID {
				t.Errorf("account/message = %q/%q, want %q/%q", got.Account, got.MessageID, tt.msg.Account, tt.msg.ID)
			}
			// The run layer owns these; the engine must leave them alone.
			if got.Action != "" || got.RunID != "" || got.DryRun || got.Judge != "" || got.Confidence != nil {
				t.Errorf("engine set run-layer fields: %+v", got)
			}
		})
	}
}

func TestEngineAddressRulesIgnoreDisplayName(t *testing.T) {
	rs := RuleSet{
		Protect: []ProtectRule{{Sender: "alice@example.com"}},
		Senders: []SenderRule{{Address: "noreply@github.com"}, {Domain: "royalmail.com"}},
	}
	engine := NewEngine(rs, 0.8)

	tests := []struct {
		name string
		msg  Message
	}{
		{"display name holding a protected address", msg("alice@example.com", "spoof@random.example", "Hello")},
		{"display name holding a sender address", msg("noreply@github.com", "spoof@random.example", "Hello")},
		{"display name holding a sender domain", msg("royalmail.com tracking", "spoof@random.example", "Hello")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.Evaluate(tt.msg); got.Tier != TierNone {
				t.Errorf("tier = %q (rule %q), want %q: address rules match the address only",
					got.Tier, got.Rule, TierNone)
			}
		})
	}
}

func TestEngineSubdomainBoundary(t *testing.T) {
	engine := NewEngine(RuleSet{Senders: []SenderRule{{Domain: "royalmail.com"}}}, 0.8)
	tests := []struct {
		email string
		want  Tier
	}{
		{"a@royalmail.com", TierSender},
		{"a@mail.royalmail.com", TierSender},
		{"a@deep.mail.royalmail.com", TierSender},
		{"a@notroyalmail.com", TierNone},           // suffix but not a subdomain
		{"a@royalmail.com.evil.example", TierNone}, // prefix only
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			if got := engine.Evaluate(msg("X", tt.email, "Hi")); got.Tier != tt.want {
				t.Errorf("tier = %q, want %q", got.Tier, tt.want)
			}
		})
	}
}

func TestEngineRuleOrderWithinATier(t *testing.T) {
	// The first matching Rule in a Tier wins, in file order.
	rs := RuleSet{Senders: []SenderRule{{Domain: "royalmail.com"}, {Address: "post@royalmail.com"}}}
	engine := NewEngine(rs, 0.8)
	if got := engine.Evaluate(msg("Post", "post@royalmail.com", "Hi")); got.Rule != "domain:royalmail.com" {
		t.Errorf("rule = %q, want the first matching rule", got.Rule)
	}
}

func TestEngineCandidate(t *testing.T) {
	engine := NewEngine(sampleRules(), 0.8)

	candidate, ok := engine.Candidate(msg("Dispatch", "ship@amazon.co.uk", "Your order"))
	if !ok {
		t.Fatalf("Candidate: want a candidate for a brand match")
	}
	if candidate.Brand.Name != "Amazon" {
		t.Errorf("brand = %q, want Amazon", candidate.Brand.Name)
	}
	if candidate.Brand.Note == "" {
		t.Errorf("candidate should carry the whole Brand Rule, including its note")
	}
	if candidate.Message.ID != "m1" {
		t.Errorf("candidate message = %+v", candidate.Message)
	}

	for _, m := range []Message{
		msg("Alice", "alice@example.com", "Amazon order"),        // protected
		msg("GitHub", "noreply@github.com", "Amazon"),            // sender tier
		msg("A Human", "human@random.example", "Lunch Thursday"), // no match
	} {
		if _, ok := engine.Candidate(m); ok {
			t.Errorf("Candidate(%q/%q) = true, want false", m.From.Email, m.Subject)
		}
	}
}

func TestEngineThreshold(t *testing.T) {
	if got := NewEngine(RuleSet{}, 0.65).Threshold(); got != 0.65 {
		t.Errorf("Threshold() = %v, want 0.65", got)
	}
}

func TestApplyJudgement(t *testing.T) {
	base := Decision{Account: "me@example.com", MessageID: "m1", Tier: TierBrand, Rule: "brand:Amazon", Brand: "Amazon"}

	tests := []struct {
		name       string
		confidence float64
		threshold  float64
		want       Verdict
	}{
		{name: "above threshold", confidence: 0.91, threshold: 0.8, want: VerdictMarkRead},
		{name: "exactly at threshold", confidence: 0.8, threshold: 0.8, want: VerdictMarkRead},
		{name: "just below threshold", confidence: 0.79, threshold: 0.8, want: VerdictLeave},
		{name: "zero confidence", confidence: 0, threshold: 0.8, want: VerdictLeave},
		{name: "threshold of zero marks everything read", confidence: 0, threshold: 0, want: VerdictMarkRead},
		{name: "threshold of one needs certainty", confidence: 0.999, threshold: 1, want: VerdictLeave},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyJudgement(base, Judgement{MessageID: "m1", Confidence: tt.confidence, Reason: "routine dispatch mail"}, tt.threshold)
			if got.Verdict != tt.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.want)
			}
			if got.Confidence == nil {
				t.Fatalf("confidence = nil, want %v", tt.confidence)
			}
			if *got.Confidence != tt.confidence {
				t.Errorf("confidence = %v, want %v", *got.Confidence, tt.confidence)
			}
			if got.Reason != "routine dispatch mail" {
				t.Errorf("reason = %q", got.Reason)
			}
			if got.Tier != TierBrand || got.Brand != "Amazon" || got.Rule != "brand:Amazon" {
				t.Errorf("ApplyJudgement changed the match: %+v", got)
			}
			if base.Confidence != nil || base.Verdict != "" {
				t.Errorf("ApplyJudgement mutated its argument: %+v", base)
			}
		})
	}
}

func TestAddressDomain(t *testing.T) {
	tests := []struct{ email, want string }{
		{"a@b.com", "b.com"},
		{"a@sub.b.com", "sub.b.com"},
		{"malformed", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := (Address{Email: tt.email}).Domain(); got != tt.want {
			t.Errorf("Domain(%q) = %q, want %q", tt.email, got, tt.want)
		}
	}
}
