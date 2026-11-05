package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// reviewRules covers every bucket: a sender rule, a protect rule, a Brand the
// Judge reads above and below threshold, and mail no rule matches.
func reviewRules() *triage.Rules {
	return &triage.Rules{
		Threshold: 0.8,
		RuleSet: triage.RuleSet{
			Protect: []triage.ProtectRule{{Domain: "bank.test"}},
			Senders: []triage.SenderRule{{Domain: "github.com"}},
			Brands:  []triage.BrandRule{{Name: "Amazon"}},
		},
	}
}

func reviewProvider() *fakeProvider {
	const a = "a@example.com"
	return provider(a,
		msg("m1", a, "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", a, "Bank", "alerts@bank.test", "Payment received"),
		msg("m3", a, "Amazon", "auto@amazon.test", "Your order has dispatched"),
		msg("m4", a, "Amazon", "auto@amazon.test", "Refund problem"),
		msg("m5", a, "", "friend@example.org", "Dinner on Friday? Let me know when you are free"),
	)
}

// documentNotifier records both the summary and the review file.
type documentNotifier struct {
	text     string
	filename string
	content  string
	caption  string
	docs     int
}

func (n *documentNotifier) Notify(_ context.Context, text string) error { n.text = text; return nil }

func (n *documentNotifier) NotifyDocument(_ context.Context, filename string, content []byte, caption string) error {
	n.docs++
	n.filename, n.content, n.caption = filename, string(content), caption
	return nil
}

func TestDryRunRecordsWhereEveryMessageWent(t *testing.T) {
	s := testSettings()
	s.DryRun = true
	j := &fakeJudge{confidence: map[string]float64{"m3": 0.95, "m4": 0.2}}

	sum, err, _ := runWith(t, Options{Settings: s, Rules: reviewRules(), Providers: []mail.Provider{reviewProvider()}, Judge: j})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := sum.Accounts[0].Items
	want := []struct {
		bucket Bucket
		from   string
		why    string
	}{
		{BucketRead, "GitHub", "sender domain:github.com"},
		{BucketLeft, "Bank", "protect domain:bank.test"},
		{BucketRead, "Amazon", "routine Amazon mail 0.95"},
		{BucketLeft, "Amazon", "routine Amazon mail 0.20"},
		{BucketUnmatched, "friend@example.org", ""},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for i, w := range want {
		if items[i].Bucket != w.bucket || items[i].From != w.from || items[i].Why != w.why {
			t.Errorf("item %d = %+v, want %+v", i, items[i], w)
		}
	}
	if got := items[4].Subject; got != "Dinner on Friday? Let me know when you are fr…" {
		t.Errorf("subject not capped at %d runes: %q", subjectMax, got)
	}
}

// A live run reports what it did, so a wrong rule is caught the morning it fires.
func TestLiveRunRecordsItems(t *testing.T) {
	j := &fakeJudge{confidence: map[string]float64{"m3": 0.95, "m4": 0.2}}
	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: reviewRules(), Providers: []mail.Provider{reviewProvider()}, Judge: j})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := sum.Accounts[0].Items
	if len(items) != 5 || items[0].Bucket != BucketRead || items[0].Why != "sender domain:github.com" {
		t.Fatalf("items = %+v", items)
	}
}

// When the Gmail action fails the mail is still unread; the report must not
// list it as read without saying so.
func TestLiveRunReportFlagsAFailedAction(t *testing.T) {
	p := reviewProvider()
	p.markErr = errors.New("500 backend error")
	j := &fakeJudge{confidence: map[string]float64{"m3": 0.95, "m4": 0.2}}
	sum, _, _ := runWith(t, Options{Settings: testSettings(), Rules: reviewRules(), Providers: []mail.Provider{p}, Judge: j})
	if got := sum.Accounts[0].Items[0].Why; !strings.Contains(got, "not applied") {
		t.Errorf("why = %q, want it to say the action was not applied", got)
	}
	if got := sum.Accounts[0].Items[1].Why; strings.Contains(got, "not applied") {
		t.Errorf("protect label succeeded but why = %q", got)
	}
}

func TestDryRunSendsTheReviewFile(t *testing.T) {
	s := testSettings()
	s.DryRun = true
	n := &documentNotifier{}
	j := &fakeJudge{confidence: map[string]float64{"m3": 0.95, "m4": 0.2}}

	sum, err, _ := runWith(t, Options{Settings: s, Rules: reviewRules(), Providers: []mail.Provider{reviewProvider()}, Judge: j, Notifier: n, RunID: "R1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n.text != sum.TelegramHTML() {
		t.Error("summary message not sent")
	}
	if n.docs != 1 || n.filename != "triage-R1.html" || n.content != sum.ReportHTML() {
		t.Fatalf("docs=%d filename=%q", n.docs, n.filename)
	}
	if !strings.Contains(n.caption, "5 messages") {
		t.Errorf("caption = %q", n.caption)
	}
}

func TestLiveRunSendsTheReport(t *testing.T) {
	n := &documentNotifier{}
	j := &fakeJudge{confidence: map[string]float64{"m3": 0.95, "m4": 0.2}}
	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: reviewRules(), Providers: []mail.Provider{reviewProvider()}, Judge: j, Notifier: n, RunID: "R2"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n.docs != 1 || n.filename != "triage-R2.html" || n.content != sum.ReportHTML() {
		t.Fatalf("docs=%d filename=%q", n.docs, n.filename)
	}
	if n.caption != "📬 Triage report · 5 messages" {
		t.Errorf("caption = %q", n.caption)
	}
	for _, want := range []string{"📬 Email triage report", "<summary>📭 Marked read (2)</summary>", "2 marked read"} {
		if !strings.Contains(n.content, want) {
			t.Errorf("live report missing %q", want)
		}
	}
	if strings.Contains(n.content, "dry run") || strings.Contains(n.content, "Would be read") {
		t.Error("live report talks about a dry run")
	}
}

// Nothing scanned, nothing to report: a quiet morning is one message, not two.
func TestRunWithNothingScannedSendsNoReport(t *testing.T) {
	n := &documentNotifier{}
	if _, err, _ := runWith(t, Options{Settings: testSettings(), Rules: reviewRules(), Providers: []mail.Provider{provider("a@example.com")}, Notifier: n}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n.docs != 0 {
		t.Errorf("sent %d reports for an empty run", n.docs)
	}
}

func TestReportHTMLGroupsByAccountAndBucket(t *testing.T) {
	sum := Summary{
		RunID:  "R1",
		DryRun: true,
		Accounts: []AccountSummary{
			{Account: "a@example.com", Scanned: 3, MarkedRead: 1, Left: 1, Unmatched: 1, Items: []ReportItem{
				{Bucket: BucketRead, From: "GitHub", Subject: "PR merged", Why: "sender domain:github.com"},
				{Bucket: BucketLeft, From: "Bank", Subject: "<script>x</script>", Why: "protect domain:bank.test"},
				{Bucket: BucketUnmatched, From: "Friend", Subject: "Dinner"},
			}},
			{Account: "b@example.com", Skipped: "same mailbox as c@example.com"},
		},
		Scanned: 3, MarkedRead: 1, Left: 1, Unmatched: 1,
		Duration: time.Second,
	}
	got := sum.ReportHTML()
	for _, want := range []string{
		"<summary>📭 a@example.com",
		"<summary>📭 Would be read (1)</summary>",
		"<summary>✋ Left (1)</summary>",
		"<summary>❔ Unmatched (1)</summary>",
		"<span class=\"from\">GitHub</span> — PR merged",
		"&lt;script&gt;x&lt;/script&gt;",
		"<summary>⏭️ b@example.com",
		"Skipped: same mailbox as c@example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>") {
		t.Error("report contains unescaped markup")
	}
}

func TestTelegramHTMLShowsSkippedAccountsAsAWarning(t *testing.T) {
	sum := Summary{
		RunID: "R1",
		Accounts: []AccountSummary{
			{Account: "a@example.com", Skipped: "same mailbox as b@example.com"},
			{Account: "c@example.com", Scanned: 2, MarkedRead: 2},
		},
		Scanned: 2, MarkedRead: 2,
	}
	if sum.Failed() {
		t.Fatal("a skipped account must not fail the run")
	}
	got := sum.TelegramHTML()
	for _, want := range []string{"✅", "⏭️ <b>1 account skipped</b>", "⏭️ <b>a@example.com</b>\nsame mailbox as b@example.com", "⏭️ <b>a@example.com</b>\nskipped"} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
}
