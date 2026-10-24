// Package run orchestrates one Triage Run: for every Account it scans the
// unread queue, decides each Message with the rules engine and (for Brand
// Candidates) the Judge, applies the two batched Provider actions, records a
// Decision per Message and reports a Summary.
//
// Failure semantics follow docs/adr/0003: anything the Judge could not answer
// for is left unread WITHOUT the Left Audit Label, so the next run tries again,
// and the run is reported as failed.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"strings"
	"time"

	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/judge"
	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/notify"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// Options is everything a Runner needs. Only Settings, Rules and Providers are
// required; the rest have safe zero behaviour.
type Options struct {
	Settings  *config.Settings
	Rules     *triage.Rules
	Providers []mail.Provider
	Judge     judge.Judge // nil when JUDGE_PROVIDER=none: the Brand tier is disabled
	Log       *slog.Logger
	Decisions *decisionlog.Writer // nil when no Decision Log is wanted
	Notifier  notify.Notifier     // nil means no summary is delivered
	RunID     string              // generated when empty
	Now       func() time.Time    // time.Now when nil
}

// Runner executes one Triage Run. It is not safe for concurrent use and drives
// Providers one at a time, in the order given.
type Runner struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	runID string
	// judgeCallsLeft is the whole run's Judge budget, shared across Accounts.
	judgeCallsLeft int
}

// New builds a Runner from opts, filling in the optional pieces.
func New(opts Options) *Runner {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.Notifier == nil {
		opts.Notifier = notify.Nop()
	}
	runID := strings.TrimSpace(opts.RunID)
	if runID == "" {
		runID = NewRunID(now())
	}
	budget := 0
	if opts.Settings != nil {
		budget = opts.Settings.JudgeMaxCallsPerRun
	}
	return &Runner{opts: opts, log: log, now: now, runID: runID, judgeCallsLeft: budget}
}

// RunID is the identifier stamped on every Decision of this run.
func (r *Runner) RunID() string { return r.runID }

// NewRunID returns a sortable, time-based run identifier such as
// 20260916T050000Z-3f9a.
func NewRunID(t time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A collision in the suffix is harmless; the timestamp still orders runs.
		return t.UTC().Format("20060102T150405Z")
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// Run triages every Account in turn and returns the Summary. An error from one
// Account is recorded against it and the run continues with the next; the
// returned error is non-nil exactly when Summary.Failed reports true.
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	start := r.now()
	sum := Summary{RunID: r.runID}
	if r.opts.Settings != nil {
		sum.DryRun = r.opts.Settings.DryRun
	}

	r.log.Info("run started",
		"run_id", r.runID,
		"accounts", len(r.opts.Providers),
		"dry_run", sum.DryRun,
		"judge", r.judgeName(),
	)

	for _, p := range r.opts.Providers {
		sum.add(r.runAccount(ctx, p))
	}
	sum.Duration = r.now().Sub(start)

	r.log.Info("run summary",
		"run_id", r.runID,
		"scanned", sum.Scanned,
		"marked_read", sum.MarkedRead,
		"left", sum.Left,
		"unmatched", sum.Unmatched,
		"candidates", sum.Candidates,
		"judge_calls", sum.JudgeCalls,
		"judge_errors", sum.JudgeErrors,
		"duration_ms", sum.Duration.Milliseconds(),
		"dry_run", sum.DryRun,
		"failed_accounts", sum.FailedAccounts(),
		"accounts", sum.logAccounts(),
	)

	// Notifying and pinging are best-effort: neither can fail a run that
	// otherwise did its job, and neither can rescue one that did not.
	if err := r.opts.Notifier.Notify(ctx, sum.TelegramHTML()); err != nil {
		r.log.Error("notify failed", "run_id", r.runID, "error", err.Error())
	}
	if !sum.Failed() && r.opts.Settings != nil && r.opts.Settings.HealthcheckPingURL != "" {
		if err := notify.Healthcheck(ctx, r.opts.Settings.HealthcheckPingURL); err != nil {
			r.log.Error("healthcheck ping failed", "run_id", r.runID, "error", err.Error())
		}
	}

	return sum, sum.Err()
}

// runAccount triages one Account, turning a panic or an error into
// AccountSummary.Err so the next Account still runs.
func (r *Runner) runAccount(ctx context.Context, p mail.Provider) (as AccountSummary) {
	as.Account = p.Account()
	defer func() {
		if rec := recover(); rec != nil {
			as.Err = fmt.Errorf("panic: %v", rec)
			r.log.Error("account panicked",
				"run_id", r.runID, "account", as.Account,
				"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
		}
	}()
	if err := r.triageAccount(ctx, p, &as); err != nil {
		as.Err = err
		r.log.Error("account failed", "run_id", r.runID, "account", as.Account, "error", err.Error())
	}
	return as
}

// pending is one Message on its way to a Decision: evaluated, maybe judged,
// not yet acted on or recorded.
type pending struct {
	msg       triage.Message
	decision  triage.Decision
	candidate triage.Candidate
}

func (r *Runner) triageAccount(ctx context.Context, p mail.Provider, as *AccountSummary) error {
	account := p.Account()
	s := r.opts.Settings
	engine := triage.NewEngine(r.opts.Rules.For(account), r.opts.Rules.Threshold)

	// EnsureLabels only creates labels, so it runs in a Dry Run too: the
	// Audit Labels are part of the mailbox's shape, not of this run's actions.
	labelIDs, err := p.EnsureLabels(ctx, []string{s.LabelRead, s.LabelLeft})
	if err != nil {
		return fmt.Errorf("ensure labels: %w", err)
	}

	ids, err := p.ListUnread(ctx, mail.Query{
		LookbackDays:  s.LookbackDays,
		ExcludeLabels: []string{s.LabelRead, s.LabelLeft},
		Max:           s.MaxMessagesPerAccount,
	})
	if err != nil {
		return fmt.Errorf("list unread: %w", err)
	}
	r.log.Debug("listed unread", "run_id", r.runID, "account", account, "messages", len(ids))

	var (
		decided    []*pending
		candidates []*pending
		fetchErrs  []string
	)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, err := p.Fetch(ctx, id, s.BodyMaxChars)
		if err != nil {
			// One unfetchable Message must not cost the Account its whole run,
			// but it does make the Account, and so the run, a failure.
			fetchErrs = append(fetchErrs, id)
			r.log.Error("fetch failed", "run_id", r.runID, "account", account, "message_id", id, "error", err.Error())
			continue
		}
		as.Scanned++

		pd := &pending{msg: *m, decision: engine.Evaluate(*m)}
		if pd.decision.Tier == triage.TierBrand {
			if c, ok := engine.Candidate(*m); ok {
				pd.candidate = c
				as.Candidates++
				candidates = append(candidates, pd)
			} else {
				// Defensive: Evaluate and Candidate disagreed. Fail closed.
				pd.decision.Verdict = triage.VerdictLeave
			}
		}
		decided = append(decided, pd)
	}

	r.judgeCandidates(ctx, candidates, engine.Threshold(), as)

	actionErr := r.applyAndRecord(ctx, p, labelIDs, decided, as)

	if len(fetchErrs) > 0 {
		fetchErr := fmt.Errorf("%d of %d messages could not be fetched (%s)",
			len(fetchErrs), len(ids), strings.Join(firstN(fetchErrs, 5), ", "))
		if actionErr != nil {
			return fmt.Errorf("%w; %w", actionErr, fetchErr)
		}
		return fetchErr
	}
	return actionErr
}

// judgeCandidates sends the Account's Candidates to the Judge in batches,
// stopping at the run's global call budget. Everything it cannot get a usable
// Judgement for fails closed (ADR 0003).
func (r *Runner) judgeCandidates(ctx context.Context, candidates []*pending, threshold float64, as *AccountSummary) {
	if len(candidates) == 0 {
		return
	}
	if r.opts.Judge == nil {
		// JUDGE_PROVIDER=none disables the Brand tier: Candidates are left
		// unread and unlabelled, exactly as if the Judge were unavailable,
		// but this is configuration rather than failure, so it is not an error.
		for _, c := range candidates {
			c.decision.Verdict = triage.VerdictLeave
			c.decision.Reason = "brand tier disabled: no judge configured"
		}
		return
	}

	size := r.opts.Settings.JudgeBatchSize
	if size <= 0 {
		size = 1
	}
	for start := 0; start < len(candidates); start += size {
		end := min(start+size, len(candidates))
		chunk := candidates[start:end]

		if r.judgeCallsLeft <= 0 {
			for _, c := range chunk {
				r.failClosed(c, "judge call budget exhausted", false, as)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			for _, c := range chunk {
				r.failClosed(c, err.Error(), false, as)
			}
			continue
		}

		batch := make([]triage.Candidate, 0, len(chunk))
		for _, c := range chunk {
			batch = append(batch, c.candidate)
		}

		r.judgeCallsLeft--
		as.JudgeCalls++
		judgements, err := r.opts.Judge.Judge(ctx, batch)
		if err != nil {
			for _, c := range chunk {
				r.failClosed(c, err.Error(), true, as)
			}
			r.log.Error("judge batch failed",
				"run_id", r.runID, "account", as.Account,
				"batch_size", len(chunk), "judge", r.judgeName(), "error", err.Error())
			continue
		}

		byID := make(map[string]triage.Judgement, len(judgements))
		for _, j := range judgements {
			byID[j.MessageID] = j
		}
		for _, c := range chunk {
			j, ok := byID[c.msg.ID]
			switch {
			case !ok:
				r.failClosed(c, "judge returned no judgement for this message", true, as)
			case math.IsNaN(j.Confidence) || j.Confidence < 0 || j.Confidence > 1:
				r.failClosed(c, fmt.Sprintf("judge returned confidence %v, want 0..1", j.Confidence), true, as)
			default:
				c.decision = triage.ApplyJudgement(c.decision, j, threshold)
				c.decision.Judge = r.opts.Judge.Name()
			}
		}
	}
}

// failClosed records that a Candidate could not be judged: Verdict leave, no
// Audit Label (so the next run judges it again) and a failure for the run.
func (r *Runner) failClosed(c *pending, reason string, consulted bool, as *AccountSummary) {
	c.decision.Verdict = triage.VerdictLeave
	c.decision.JudgeError = reason
	if consulted {
		c.decision.Judge = r.judgeName()
	}
	as.JudgeErrors++
}

// applyAndRecord performs the Account's two batched actions, then logs and
// records every Decision. Actions come first so a Decision never claims an
// action the Provider refused.
//
// Labelling policy, in one place:
//   - Verdict mark-read           -> unread cleared and the Read Audit Label applied
//   - Verdict leave, Tier none    -> nothing: unmatched mail is never labelled
//   - Verdict leave, judge failed -> nothing: ADR 0003 wants it re-judged next run
//   - Verdict leave, no Judgement -> nothing (Brand tier disabled)
//   - Verdict leave otherwise     -> the Left Audit Label (protect, or a judged Brand)
//
// In a Dry Run every Decision that would have changed something records Action
// dry-run instead, and no Provider call is made.
func (r *Runner) applyAndRecord(ctx context.Context, p mail.Provider, labelIDs map[string]string, decided []*pending, as *AccountSummary) error {
	s := r.opts.Settings
	dry := s.DryRun

	var markIDs, leftIDs []string
	var marked, left []*pending

	for _, pd := range decided {
		d := &pd.decision
		d.RunID = r.runID
		d.Account = pd.msg.Account
		d.DryRun = dry
		if d.Verdict == "" {
			d.Verdict = triage.VerdictLeave
		}

		switch {
		case d.Verdict == triage.VerdictMarkRead:
			as.MarkedRead++
			if dry {
				d.Action = triage.ActionDryRun
			} else {
				d.Action = triage.ActionMarkedRead
				markIDs = append(markIDs, pd.msg.ID)
				marked = append(marked, pd)
			}
		case d.Tier == triage.TierNone:
			as.Unmatched++
			d.Action = triage.ActionNone
		case d.JudgeError != "" || (d.Tier == triage.TierBrand && d.Confidence == nil):
			as.Left++
			d.Action = triage.ActionNone
		default:
			as.Left++
			if dry {
				d.Action = triage.ActionDryRun
			} else {
				d.Action = triage.ActionLeft
				leftIDs = append(leftIDs, pd.msg.ID)
				left = append(left, pd)
			}
		}
	}

	var errs []string
	if !dry {
		if len(markIDs) > 0 {
			if err := r.markRead(ctx, p, labelIDs, markIDs); err != nil {
				errs = append(errs, err.Error())
				downgrade(marked)
			}
		}
		if len(leftIDs) > 0 {
			if err := r.label(ctx, p, labelIDs, leftIDs); err != nil {
				errs = append(errs, err.Error())
				downgrade(left)
			}
		}
	}

	for _, pd := range decided {
		r.record(pd)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *Runner) markRead(ctx context.Context, p mail.Provider, labelIDs map[string]string, ids []string) error {
	id, ok := labelIDs[r.opts.Settings.LabelRead]
	if !ok {
		return fmt.Errorf("mark read: provider returned no label ID for %q", r.opts.Settings.LabelRead)
	}
	if err := p.MarkRead(ctx, ids, id); err != nil {
		return fmt.Errorf("mark read %d messages: %w", len(ids), err)
	}
	return nil
}

func (r *Runner) label(ctx context.Context, p mail.Provider, labelIDs map[string]string, ids []string) error {
	id, ok := labelIDs[r.opts.Settings.LabelLeft]
	if !ok {
		return fmt.Errorf("label left: provider returned no label ID for %q", r.opts.Settings.LabelLeft)
	}
	if err := p.Label(ctx, ids, id); err != nil {
		return fmt.Errorf("label %d messages left: %w", len(ids), err)
	}
	return nil
}

// downgrade marks Decisions whose batch action failed as having done nothing.
func downgrade(pds []*pending) {
	for _, pd := range pds {
		pd.decision.Action = triage.ActionNone
	}
}

// record emits the per-Message telemetry and appends to the Decision Log.
func (r *Runner) record(pd *pending) {
	d := pd.decision
	attrs := []any{
		"run_id", d.RunID,
		"account", d.Account,
		"message_id", d.MessageID,
		"tier", string(d.Tier),
		"rule", d.Rule,
		"brand", d.Brand,
		"reason", d.Reason,
		"verdict", string(d.Verdict),
		"action", string(d.Action),
		"judge", d.Judge,
		"judge_error", d.JudgeError,
		"dry_run", d.DryRun,
		"from", pd.msg.From.Email,
		"from_name", pd.msg.From.Name,
		"subject", truncateRunes(pd.msg.Subject, 80),
	}
	if d.Confidence != nil {
		attrs = append(attrs, "confidence", *d.Confidence)
	}
	r.log.Info("decision", attrs...)

	if r.opts.Decisions == nil {
		return
	}
	if err := r.opts.Decisions.Append(decisionlog.Record{
		Decision: d,
		Message:  decisionlog.Summarise(pd.msg),
	}); err != nil {
		r.log.Error("decision log append failed",
			"run_id", d.RunID, "account", d.Account, "message_id", d.MessageID, "error", err.Error())
	}
}

func (r *Runner) judgeName() string {
	if r.opts.Judge == nil {
		return "none"
	}
	return r.opts.Judge.Name()
}

// truncateRunes caps s at max runes, appending an ellipsis when it cut.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string(nil), s[:n]...), "…")
}
