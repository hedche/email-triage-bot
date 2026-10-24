package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// ---- fakes -----------------------------------------------------------------

type fakeProvider struct {
	account string
	ids     []string
	msgs    map[string]triage.Message

	ensureErr error
	listErr   error
	fetchErr  map[string]error
	markErr   error
	labelErr  error
	panicOn   string // "list" makes ListUnread panic

	ensureCalls int
	markCalls   [][]string
	markLabels  []string
	labelCalls  [][]string
	labelLabels []string
}

func (f *fakeProvider) Account() string { return f.account }

func (f *fakeProvider) EnsureLabels(_ context.Context, names []string) (map[string]string, error) {
	f.ensureCalls++
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = "id-" + n
	}
	return out, nil
}

func (f *fakeProvider) ListUnread(_ context.Context, _ mail.Query) ([]string, error) {
	if f.panicOn == "list" {
		panic("provider exploded")
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.ids, nil
}

func (f *fakeProvider) Fetch(_ context.Context, id string, bodyMax int) (*triage.Message, error) {
	if err := f.fetchErr[id]; err != nil {
		return nil, err
	}
	m, ok := f.msgs[id]
	if !ok {
		return nil, fmt.Errorf("no such message %q", id)
	}
	if bodyMax > 0 && len([]rune(m.Body)) > bodyMax {
		m.Body = string([]rune(m.Body)[:bodyMax])
	}
	return &m, nil
}

func (f *fakeProvider) MarkRead(_ context.Context, ids []string, labelID string) error {
	f.markCalls = append(f.markCalls, append([]string(nil), ids...))
	f.markLabels = append(f.markLabels, labelID)
	return f.markErr
}

func (f *fakeProvider) Label(_ context.Context, ids []string, labelID string) error {
	f.labelCalls = append(f.labelCalls, append([]string(nil), ids...))
	f.labelLabels = append(f.labelLabels, labelID)
	return f.labelErr
}

type fakeJudge struct {
	name       string
	calls      int
	batchSizes []int
	err        error
	// judgements maps message ID to the confidence returned; absent IDs are
	// simply not returned, which the run must treat as a failure.
	confidence map[string]float64
}

func (j *fakeJudge) Name() string {
	if j.name == "" {
		return "fake/model"
	}
	return j.name
}

func (j *fakeJudge) Judge(_ context.Context, batch []triage.Candidate) ([]triage.Judgement, error) {
	j.calls++
	j.batchSizes = append(j.batchSizes, len(batch))
	if j.err != nil {
		return nil, j.err
	}
	out := make([]triage.Judgement, 0, len(batch))
	for _, c := range batch {
		conf, ok := j.confidence[c.Message.ID]
		if !ok {
			continue
		}
		out = append(out, triage.Judgement{MessageID: c.Message.ID, Confidence: conf, Reason: "routine " + c.Brand.Name + " mail"})
	}
	return out, nil
}

// ---- helpers ---------------------------------------------------------------

func testSettings() *config.Settings {
	return &config.Settings{
		DryRun:                false,
		LookbackDays:          7,
		MaxMessagesPerAccount: 200,
		BodyMaxChars:          2000,
		LabelRead:             "Triage/Read",
		LabelLeft:             "Triage/Left",
		JudgeProvider:         config.JudgeGemini,
		JudgeBatchSize:        2,
		JudgeMaxCallsPerRun:   100,
		JudgeTimeout:          time.Minute,
	}
}

func msg(id, account, fromName, fromEmail, subject string) triage.Message {
	return triage.Message{
		ID:       id,
		Account:  account,
		From:     triage.Address{Name: fromName, Email: fromEmail},
		Subject:  subject,
		Snippet:  "snippet for " + id,
		Body:     "body of " + id + " which must never be logged",
		Received: time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC),
	}
}

func provider(account string, msgs ...triage.Message) *fakeProvider {
	p := &fakeProvider{account: account, msgs: map[string]triage.Message{}}
	for _, m := range msgs {
		p.ids = append(p.ids, m.ID)
		p.msgs[m.ID] = m
	}
	return p
}

// capture collects the slog records the run emits.
type capture struct {
	records []map[string]any
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capture) WithGroup(string) slog.Handler            { return c }
func (c *capture) Handle(_ context.Context, rec slog.Record) error {
	m := map[string]any{"msg": rec.Message}
	rec.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	c.records = append(c.records, m)
	return nil
}
func (c *capture) byMsg(msg string) []map[string]any {
	var out []map[string]any
	for _, r := range c.records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func fixedNow() func() time.Time {
	t := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(250 * time.Millisecond)
		return t
	}
}

func runWith(t *testing.T, opts Options) (Summary, error, *capture) {
	t.Helper()
	cap := &capture{}
	if opts.Log == nil {
		opts.Log = slog.New(cap)
	}
	if opts.Now == nil {
		opts.Now = fixedNow()
	}
	if opts.RunID == "" {
		opts.RunID = "20260916T050000Z-test"
	}
	sum, err := New(opts).Run(context.Background())
	return sum, err, cap
}

// ---- tests -----------------------------------------------------------------

func TestSenderRuleIsMarkedReadAndLabelledInOneBatch(t *testing.T) {
	rules := &triage.Rules{
		Threshold: 0.8,
		RuleSet: triage.RuleSet{
			Senders: []triage.SenderRule{{Domain: "github.com"}},
		},
	}
	p := provider("a@example.com",
		msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", "a@example.com", "GitHub", "notifications@github.com", "New issue"),
		msg("m3", "a@example.com", "A Human", "human@elsewhere.test", "lunch?"),
	)

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.markCalls) != 1 {
		t.Fatalf("MarkRead calls = %d, want 1 batched call", len(p.markCalls))
	}
	if got := strings.Join(p.markCalls[0], ","); got != "m1,m2" {
		t.Errorf("marked ids = %q, want m1,m2", got)
	}
	if p.markLabels[0] != "id-Triage/Read" {
		t.Errorf("mark label = %q", p.markLabels[0])
	}
	if len(p.labelCalls) != 0 {
		t.Errorf("Label calls = %v, want none (unmatched mail is never labelled)", p.labelCalls)
	}
	if p.ensureCalls != 1 {
		t.Errorf("EnsureLabels calls = %d, want 1", p.ensureCalls)
	}
	if sum.Scanned != 3 || sum.MarkedRead != 2 || sum.Unmatched != 1 || sum.Left != 0 {
		t.Errorf("summary = %+v", sum.Accounts[0])
	}
	if sum.Failed() {
		t.Error("Failed() = true, want false")
	}

	decisions := cap.byMsg("decision")
	if len(decisions) != 3 {
		t.Fatalf("decision records = %d, want 3", len(decisions))
	}
	if decisions[0]["action"] != string(triage.ActionMarkedRead) || decisions[0]["tier"] != string(triage.TierSender) {
		t.Errorf("first decision = %v", decisions[0])
	}
	if decisions[0]["run_id"] != "20260916T050000Z-test" {
		t.Errorf("run_id = %v", decisions[0]["run_id"])
	}
	if decisions[2]["action"] != string(triage.ActionNone) || decisions[2]["tier"] != string(triage.TierNone) {
		t.Errorf("unmatched decision = %v", decisions[2])
	}
	if _, ok := decisions[0]["confidence"]; ok {
		t.Error("confidence attr present for a deterministic decision")
	}
}

func TestProtectRuleGetsTheLeftLabel(t *testing.T) {
	rules := &triage.Rules{
		Threshold: 0.8,
		RuleSet:   triage.RuleSet{Protect: []triage.ProtectRule{{Domain: "bank.test"}}},
	}
	p := provider("a@example.com", msg("m1", "a@example.com", "Bank", "alerts@bank.test", "Statement ready"))

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.labelCalls) != 1 || p.labelCalls[0][0] != "m1" {
		t.Fatalf("Label calls = %v, want one call with m1", p.labelCalls)
	}
	if p.labelLabels[0] != "id-Triage/Left" {
		t.Errorf("left label = %q", p.labelLabels[0])
	}
	if len(p.markCalls) != 0 {
		t.Errorf("MarkRead calls = %v, want none", p.markCalls)
	}
	if sum.Left != 1 || sum.MarkedRead != 0 {
		t.Errorf("summary = %+v", sum)
	}
	d := cap.byMsg("decision")[0]
	if d["action"] != string(triage.ActionLeft) || d["verdict"] != string(triage.VerdictLeave) {
		t.Errorf("decision = %v", d)
	}
}

func brandRules() *triage.Rules {
	return &triage.Rules{
		Threshold: 0.8,
		RuleSet:   triage.RuleSet{Brands: []triage.BrandRule{{Name: "Amazon", Note: "orders are routine"}}},
	}
}

func TestBrandCandidateAboveThresholdIsMarkedRead(t *testing.T) {
	p := provider("a@example.com", msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Your order has dispatched"))
	j := &fakeJudge{confidence: map[string]float64{"m1": 0.95}}

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: brandRules(), Providers: []mail.Provider{p}, Judge: j})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if j.calls != 1 || j.batchSizes[0] != 1 {
		t.Fatalf("judge calls = %d, sizes = %v", j.calls, j.batchSizes)
	}
	if len(p.markCalls) != 1 || p.markCalls[0][0] != "m1" {
		t.Fatalf("MarkRead calls = %v", p.markCalls)
	}
	if sum.Candidates != 1 || sum.MarkedRead != 1 || sum.JudgeCalls != 1 || sum.JudgeErrors != 0 {
		t.Errorf("summary = %+v", sum)
	}
	d := cap.byMsg("decision")[0]
	if d["judge"] != "fake/model" || d["confidence"] != 0.95 || d["brand"] != "Amazon" {
		t.Errorf("decision = %v", d)
	}
}

func TestBrandCandidateBelowThresholdIsLeftAndLabelled(t *testing.T) {
	p := provider("a@example.com", msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Problem with your payment"))
	j := &fakeJudge{confidence: map[string]float64{"m1": 0.2}}

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: brandRules(), Providers: []mail.Provider{p}, Judge: j})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.labelCalls) != 1 || p.labelCalls[0][0] != "m1" {
		t.Fatalf("Label calls = %v, want m1 labelled left", p.labelCalls)
	}
	if sum.Left != 1 || sum.JudgeErrors != 0 {
		t.Errorf("summary = %+v", sum)
	}
	if d := cap.byMsg("decision")[0]; d["action"] != string(triage.ActionLeft) {
		t.Errorf("decision = %v", d)
	}
}

func TestJudgeErrorLeavesMessagesUnlabelledAndFailsTheRun(t *testing.T) {
	p := provider("a@example.com",
		msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Order dispatched"),
		msg("m2", "a@example.com", "Amazon news", "news@other.test", "Amazon deals"),
	)
	j := &fakeJudge{err: errors.New("429 over quota")}

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: brandRules(), Providers: []mail.Provider{p}, Judge: j})
	if err == nil {
		t.Fatal("want a non-nil error when the judge failed")
	}
	if !sum.Failed() {
		t.Error("Failed() = false, want true")
	}
	if len(p.labelCalls) != 0 || len(p.markCalls) != 0 {
		t.Errorf("provider was mutated: mark=%v label=%v", p.markCalls, p.labelCalls)
	}
	if sum.JudgeErrors != 2 || sum.Left != 2 {
		t.Errorf("summary = %+v", sum)
	}
	for _, d := range cap.byMsg("decision") {
		if d["action"] != string(triage.ActionNone) || d["verdict"] != string(triage.VerdictLeave) {
			t.Errorf("decision = %v", d)
		}
		if !strings.Contains(fmt.Sprint(d["judge_error"]), "over quota") {
			t.Errorf("judge_error = %v", d["judge_error"])
		}
	}
}

func TestMissingJudgementFailsClosedForThatMessageOnly(t *testing.T) {
	p := provider("a@example.com",
		msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Order dispatched"),
		msg("m2", "a@example.com", "Amazon", "auto@amazon.test", "Order delivered"),
	)
	j := &fakeJudge{confidence: map[string]float64{"m1": 0.9}} // m2 missing

	sum, _, _ := runWith(t, Options{Settings: testSettings(), Rules: brandRules(), Providers: []mail.Provider{p}, Judge: j})
	if sum.MarkedRead != 1 || sum.JudgeErrors != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if len(p.markCalls) != 1 || p.markCalls[0][0] != "m1" {
		t.Errorf("MarkRead calls = %v", p.markCalls)
	}
	if len(p.labelCalls) != 0 {
		t.Errorf("Label calls = %v, want none: the unjudged message keeps no label", p.labelCalls)
	}
}

func TestJudgeBudgetExhaustionFailsRemainingCandidatesClosed(t *testing.T) {
	var msgs []triage.Message
	conf := map[string]float64{}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("m%d", i)
		msgs = append(msgs, msg(id, "a@example.com", "Amazon", "auto@amazon.test", "Order "+id))
		conf[id] = 0.99
	}
	p := provider("a@example.com", msgs...)
	j := &fakeJudge{confidence: conf}

	s := testSettings()
	s.JudgeBatchSize = 2
	s.JudgeMaxCallsPerRun = 1 // only the first batch of two is judged

	sum, err, _ := runWith(t, Options{Settings: s, Rules: brandRules(), Providers: []mail.Provider{p}, Judge: j})
	if err == nil {
		t.Fatal("want an error: candidates failed closed")
	}
	if j.calls != 1 {
		t.Errorf("judge calls = %d, want 1 (budget)", j.calls)
	}
	if sum.MarkedRead != 2 || sum.JudgeErrors != 3 {
		t.Errorf("summary = %+v", sum)
	}
	if len(p.markCalls) != 1 || len(p.markCalls[0]) != 2 {
		t.Errorf("MarkRead calls = %v", p.markCalls)
	}
	if len(p.labelCalls) != 0 {
		t.Errorf("Label calls = %v, want none", p.labelCalls)
	}
}

func TestJudgeBudgetIsSharedAcrossAccounts(t *testing.T) {
	p1 := provider("a@example.com", msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Order 1"))
	p2 := provider("b@example.com", msg("m2", "b@example.com", "Amazon", "auto@amazon.test", "Order 2"))
	j := &fakeJudge{confidence: map[string]float64{"m1": 0.9, "m2": 0.9}}

	s := testSettings()
	s.JudgeMaxCallsPerRun = 1

	sum, _, _ := runWith(t, Options{Settings: s, Rules: brandRules(), Providers: []mail.Provider{p1, p2}, Judge: j})
	if j.calls != 1 {
		t.Errorf("judge calls = %d, want 1 across both accounts", j.calls)
	}
	if sum.JudgeErrors != 1 {
		t.Errorf("judge errors = %d, want 1", sum.JudgeErrors)
	}
}

func TestNoJudgeConfiguredLeavesCandidatesUntouched(t *testing.T) {
	p := provider("a@example.com", msg("m1", "a@example.com", "Amazon", "auto@amazon.test", "Order dispatched"))

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: brandRules(), Providers: []mail.Provider{p}})
	if err != nil {
		t.Fatalf("Run: %v (a disabled brand tier is not a failure)", err)
	}
	if len(p.markCalls) != 0 || len(p.labelCalls) != 0 {
		t.Errorf("provider mutated: mark=%v label=%v", p.markCalls, p.labelCalls)
	}
	if sum.JudgeErrors != 0 || sum.Left != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if d := cap.byMsg("decision")[0]; d["action"] != string(triage.ActionNone) {
		t.Errorf("decision = %v", d)
	}
}

func TestDryRunActsOnNothingButRecordsWhatItWouldDo(t *testing.T) {
	rules := &triage.Rules{
		Threshold: 0.8,
		RuleSet: triage.RuleSet{
			Senders: []triage.SenderRule{{Domain: "github.com"}},
			Protect: []triage.ProtectRule{{Domain: "bank.test"}},
		},
	}
	p := provider("a@example.com",
		msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", "a@example.com", "Bank", "alerts@bank.test", "Statement"),
		msg("m3", "a@example.com", "Someone", "someone@elsewhere.test", "hello"),
	)
	s := testSettings()
	s.DryRun = true

	sum, err, cap := runWith(t, Options{Settings: s, Rules: rules, Providers: []mail.Provider{p}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.markCalls) != 0 || len(p.labelCalls) != 0 {
		t.Fatalf("dry run touched the provider: mark=%v label=%v", p.markCalls, p.labelCalls)
	}
	if p.ensureCalls != 1 {
		t.Errorf("EnsureLabels calls = %d, want 1 even in a dry run", p.ensureCalls)
	}
	if !sum.DryRun || sum.MarkedRead != 1 || sum.Left != 1 || sum.Unmatched != 1 {
		t.Errorf("summary = %+v", sum)
	}
	got := map[string]string{}
	for _, d := range cap.byMsg("decision") {
		got[fmt.Sprint(d["message_id"])] = fmt.Sprint(d["action"])
		if d["dry_run"] != true {
			t.Errorf("dry_run = %v for %v", d["dry_run"], d["message_id"])
		}
	}
	want := map[string]string{
		"m1": string(triage.ActionDryRun),
		"m2": string(triage.ActionDryRun),
		"m3": string(triage.ActionNone),
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("action for %s = %q, want %q", id, got[id], w)
		}
	}
}

func TestOneFailingAccountDoesNotStopTheNext(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	bad := provider("bad@example.com")
	bad.listErr = errors.New("token expired")
	good := provider("good@example.com", msg("m1", "good@example.com", "GitHub", "noreply@github.com", "PR merged"))

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{bad, good}})
	if err == nil {
		t.Fatal("want an error naming the failed account")
	}
	if !strings.Contains(err.Error(), "bad@example.com") || !strings.Contains(err.Error(), "token expired") {
		t.Errorf("error = %v", err)
	}
	if len(good.markCalls) != 1 {
		t.Errorf("second account was not triaged: %v", good.markCalls)
	}
	if len(sum.Accounts) != 2 || sum.Accounts[0].Err == nil || sum.Accounts[1].Err != nil {
		t.Errorf("accounts = %+v", sum.Accounts)
	}
	if got := sum.FailedAccounts(); len(got) != 1 || got[0] != "bad@example.com" {
		t.Errorf("FailedAccounts = %v", got)
	}
}

func TestPanicInOneAccountIsRecordedAndTheNextStillRuns(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	boom := provider("boom@example.com")
	boom.panicOn = "list"
	good := provider("good@example.com", msg("m1", "good@example.com", "GitHub", "noreply@github.com", "PR merged"))

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{boom, good}})
	if err == nil {
		t.Fatal("want an error")
	}
	if sum.Accounts[0].Err == nil || !strings.Contains(sum.Accounts[0].Err.Error(), "provider exploded") {
		t.Errorf("account error = %v", sum.Accounts[0].Err)
	}
	if len(good.markCalls) != 1 {
		t.Errorf("second account did not run")
	}
}

func TestFailedBatchActionDowngradesActionsAndFailsTheAccount(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	p := provider("a@example.com", msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"))
	p.markErr = errors.New("500 backend error")

	sum, err, cap := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}})
	if err == nil {
		t.Fatal("want an error when the batch action failed")
	}
	if d := cap.byMsg("decision")[0]; d["action"] != string(triage.ActionNone) {
		t.Errorf("action = %v, want none after a failed MarkRead", d["action"])
	}
	if !sum.Failed() {
		t.Error("Failed() = false")
	}
}

func TestFetchErrorSkipsTheMessageAndFailsTheAccount(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	p := provider("a@example.com",
		msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", "a@example.com", "GitHub", "noreply@github.com", "Issue closed"),
	)
	p.fetchErr = map[string]error{"m1": errors.New("404 not found")}

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}})
	if err == nil {
		t.Fatal("want an error")
	}
	if sum.Scanned != 1 || sum.MarkedRead != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if len(p.markCalls) != 1 || p.markCalls[0][0] != "m2" {
		t.Errorf("MarkRead calls = %v, want the fetchable message only", p.markCalls)
	}
}

func TestDecisionLogRecordsEveryMessageWithoutTheBody(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	p := provider("a@example.com",
		msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", "a@example.com", "Someone", "someone@elsewhere.test", "hello"),
	)
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	w, err := decisionlog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if _, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}, Decisions: w}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), "must never be logged") {
		t.Error("decision log contains the message body")
	}
	var lines []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(lines))
	}

	recs, err := decisionlog.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if recs[0].Decision.Verdict != triage.VerdictMarkRead || recs[0].Decision.RunID != "20260916T050000Z-test" {
		t.Errorf("record 0 = %+v", recs[0].Decision)
	}
	if recs[0].Message.Subject != "PR merged" || recs[0].Message.From.Email != "noreply@github.com" {
		t.Errorf("record 0 message = %+v", recs[0].Message)
	}
	if recs[1].Decision.Tier != triage.TierNone {
		t.Errorf("record 1 = %+v", recs[1].Decision)
	}
}

func TestNotifierReceivesTheSummaryHTML(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8}
	p := provider("a@example.com")
	var got string
	n := notifierFunc(func(_ context.Context, text string) error { got = text; return nil })

	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}, Notifier: n})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != sum.TelegramHTML() {
		t.Errorf("notified %q, want %q", got, sum.TelegramHTML())
	}
}

func TestNotifierErrorDoesNotFailTheRun(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8}
	p := provider("a@example.com")
	n := notifierFunc(func(context.Context, string) error { return errors.New("telegram down") })

	if _, err, _ := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}, Notifier: n}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type notifierFunc func(ctx context.Context, text string) error

func (f notifierFunc) Notify(ctx context.Context, text string) error { return f(ctx, text) }

func TestSummaryTextFormatting(t *testing.T) {
	sum := Summary{
		RunID:    "20260916T050000Z-3f9a",
		DryRun:   true,
		Duration: 1500 * time.Millisecond,
		Accounts: []AccountSummary{
			{Account: "a@example.com", Scanned: 12, MarkedRead: 5, Left: 3, Unmatched: 4, Candidates: 3, JudgeCalls: 1},
			{Account: "b@example.com", Scanned: 2, Err: errors.New("list unread: token expired")},
		},
		Scanned: 14, MarkedRead: 5, Left: 3, Unmatched: 4, Candidates: 3, JudgeCalls: 1,
	}
	want := strings.Join([]string{
		"Triage run 20260916T050000Z-3f9a (dry run)",
		"a@example.com: scanned 12, marked read 5, left 3, unmatched 4, judge 1 calls/0 errors",
		"b@example.com: scanned 2, marked read 0, left 0, unmatched 0, judge 0 calls/0 errors, ERROR: list unread: token expired",
		"totals: scanned 14, marked read 5, left 3, unmatched 4, judge 1 calls/0 errors (1.5s)",
	}, "\n")
	if got := sum.Text(); got != want {
		t.Errorf("Text() =\n%s\nwant\n%s", got, want)
	}

	sum.DryRun = false
	if line := strings.SplitN(sum.Text(), "\n", 2)[0]; line != "Triage run 20260916T050000Z-3f9a" {
		t.Errorf("header = %q", line)
	}
}

func TestRunSummaryLogRecord(t *testing.T) {
	rules := &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
	p := provider("a@example.com", msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"))

	sum, _, cap := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}})
	recs := cap.byMsg("run summary")
	if len(recs) != 1 {
		t.Fatalf("run summary records = %d", len(recs))
	}
	r := recs[0]
	for _, key := range []string{"scanned", "marked_read", "left", "unmatched", "judge_calls", "judge_errors", "duration_ms", "dry_run", "failed_accounts", "run_id"} {
		if _, ok := r[key]; !ok {
			t.Errorf("run summary missing %q: %v", key, r)
		}
	}
	if r["scanned"] != int64(1) || r["marked_read"] != int64(1) {
		t.Errorf("run summary counts = %v", r)
	}
	if r["duration_ms"] != sum.Duration.Milliseconds() {
		t.Errorf("duration_ms = %v, want %d", r["duration_ms"], sum.Duration.Milliseconds())
	}
}

func TestSubjectIsTruncatedInTelemetry(t *testing.T) {
	long := strings.Repeat("é", 200)
	rules := &triage.Rules{Threshold: 0.8}
	p := provider("a@example.com", msg("m1", "a@example.com", "Someone", "someone@elsewhere.test", long))

	if _, err, cap := runWith(t, Options{Settings: testSettings(), Rules: rules, Providers: []mail.Provider{p}}); err == nil {
		subject := fmt.Sprint(cap.byMsg("decision")[0]["subject"])
		if n := len([]rune(subject)); n != 81 { // 80 runes plus the ellipsis
			t.Errorf("subject runes = %d, want 81", n)
		}
	} else {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunIDIsGeneratedWhenEmpty(t *testing.T) {
	r := New(Options{
		Settings: testSettings(),
		Rules:    &triage.Rules{Threshold: 0.8},
		Now:      func() time.Time { return time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC) },
	})
	if !strings.HasPrefix(r.RunID(), "20260916T050000Z-") {
		t.Errorf("RunID = %q", r.RunID())
	}
}
