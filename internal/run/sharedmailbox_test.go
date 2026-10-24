package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// Two credentials onto one mailbox list the same provider message IDs. Both
// Accounts must be skipped: acting on either would triage one mailbox twice
// under two names, and nothing says which name the credentials belong to.
func TestSharedMailboxSkipsBothAccounts(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	shared := msg("m1", "", "GitHub", "noreply@github.com", "PR merged")

	a := provider("a@example.com", shared)
	b := provider("b@example.com", shared)
	c := provider("c@example.com", msg("m9", "c@example.com", "GitHub", "noreply@github.com", "PR merged"))

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{a, b, c}})
	if err == nil {
		t.Fatal("a shared mailbox must fail the run")
	}

	for _, p := range []*fakeProvider{a, b} {
		if len(p.markCalls) > 0 {
			t.Errorf("%s was acted on despite sharing a mailbox: %v", p.account, p.markCalls)
		}
	}
	if len(c.markCalls) == 0 {
		t.Error("the unaffected account should still have been triaged")
	}

	byAccount := map[string]AccountSummary{}
	for _, as := range sum.Accounts {
		byAccount[as.Account] = as
	}
	for _, want := range []struct{ account, other string }{{"a@example.com", "b@example.com"}, {"b@example.com", "a@example.com"}} {
		as := byAccount[want.account]
		if as.Err == nil {
			t.Fatalf("%s has no error", want.account)
		}
		if !strings.Contains(as.Err.Error(), want.other) {
			t.Errorf("%s error should name %s, got %v", want.account, want.other, as.Err)
		}
		if as.Scanned != 0 {
			t.Errorf("%s scanned %d messages, want 0", want.account, as.Scanned)
		}
	}
	if as := byAccount["c@example.com"]; as.Err != nil {
		t.Errorf("c@example.com should be unaffected, got %v", as.Err)
	}
}

// The clash has to be found before anything is acted on. Detecting it partway
// through the loop would be too late: the first copy has already marked mail
// read under the wrong account's name.
func TestSharedMailboxIsDetectedBeforeAnyAccountActs(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	shared := msg("m1", "", "GitHub", "noreply@github.com", "PR merged")
	a := provider("a@example.com", shared)
	b := provider("b@example.com", shared)

	runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{a, b}})

	if a.ensureCalls > 0 || b.ensureCalls > 0 {
		t.Errorf("a skipped account must not reach EnsureLabels (a=%d b=%d)", a.ensureCalls, b.ensureCalls)
	}
}

// Distinct mailboxes never share a provider message ID, so an ordinary run
// must be untouched by the check.
func TestDistinctMailboxesAreNotFlagged(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	a := provider("a@example.com", msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"))
	b := provider("b@example.com", msg("m2", "b@example.com", "GitHub", "noreply@github.com", "PR merged"))

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{a, b}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, as := range sum.Accounts {
		if as.Err != nil {
			t.Errorf("%s flagged in error: %v", as.Account, as.Err)
		}
	}
}

// Two empty mailboxes share no IDs and so cannot be told apart. That is the
// harmless direction of the check: there was nothing to process either way.
func TestEmptyAccountsAreNotFlagged(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8}
	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules,
		Providers: []mail.Provider{provider("a@example.com"), provider("b@example.com")}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, as := range sum.Accounts {
		if as.Err != nil {
			t.Errorf("%s flagged in error: %v", as.Account, as.Err)
		}
	}
}

// A listing error in the preflight belongs to that Account's own run, which
// hits it again and reports it once.
func TestPreflightListErrorIsReportedOnceByTheAccount(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8}
	bad := provider("a@example.com")
	bad.listErr = errors.New("token expired")

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules,
		Providers: []mail.Provider{bad, provider("b@example.com")}})
	if err == nil {
		t.Fatal("want the listing error to fail the run")
	}
	if n := len(sum.FailedAccounts()); n != 1 {
		t.Errorf("want 1 failed account, got %d: %v", n, sum.FailedAccounts())
	}
	if as := sum.Accounts[0]; !strings.Contains(as.Err.Error(), "token expired") {
		t.Errorf("want the real listing error, got %v", as.Err)
	}
}

// The preflight runs outside runAccount's recover, so it needs its own.
func TestPreflightContainsAProviderPanic(t *testing.T) {
	p := provider("a@example.com")
	p.panicOn = "list"
	ids, err := listForPreflight(context.Background(), p, mail.Query{})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Errorf("want a panic turned into an error, got ids=%v err=%v", ids, err)
	}
}
