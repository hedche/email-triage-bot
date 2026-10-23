package run

import (
	"fmt"
	"strings"
	"time"
)

// AccountSummary is what one Account's share of a Triage Run came to.
// MarkedRead, Left and Unmatched count Verdicts, not Provider calls: in a Dry
// Run they say what would have happened, and they are unchanged when a batch
// action fails (the failure surfaces through Err instead).
type AccountSummary struct {
	Account     string
	Scanned     int // messages fetched and evaluated
	MarkedRead  int // Verdict mark-read
	Left        int // Verdict leave from a matching Rule (Tier is not none)
	Unmatched   int // Tier none: no Rule matched, nothing applied
	Candidates  int // reached the Brand tier
	JudgeCalls  int // Judge batch calls attempted for this Account
	JudgeErrors int // Candidates that failed closed (call failed, unusable, or over budget)
	Err         error
}

// Summary is the outcome of a whole Triage Run.
type Summary struct {
	RunID    string
	DryRun   bool
	Duration time.Duration
	Accounts []AccountSummary

	Scanned     int
	MarkedRead  int
	Left        int
	Unmatched   int
	Candidates  int
	JudgeCalls  int
	JudgeErrors int
}

// add folds one Account's counts into the totals.
func (s *Summary) add(a AccountSummary) {
	s.Accounts = append(s.Accounts, a)
	s.Scanned += a.Scanned
	s.MarkedRead += a.MarkedRead
	s.Left += a.Left
	s.Unmatched += a.Unmatched
	s.Candidates += a.Candidates
	s.JudgeCalls += a.JudgeCalls
	s.JudgeErrors += a.JudgeErrors
}

// Failed reports whether the run should exit non-zero: any Account errored, or
// any Candidate failed closed. Fail-closed Candidates count (ADR 0003): a run
// that could not judge is a failed run, not a quiet morning.
func (s Summary) Failed() bool {
	if s.JudgeErrors > 0 {
		return true
	}
	for _, a := range s.Accounts {
		if a.Err != nil {
			return true
		}
	}
	return false
}

// FailedAccounts lists the Accounts whose triage errored.
func (s Summary) FailedAccounts() []string {
	var out []string
	for _, a := range s.Accounts {
		if a.Err != nil {
			out = append(out, a.Account)
		}
	}
	return out
}

// Err returns the aggregate error for the run, or nil when nothing failed.
func (s Summary) Err() error {
	if !s.Failed() {
		return nil
	}
	var parts []string
	for _, a := range s.Accounts {
		if a.Err != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", a.Account, a.Err))
		}
	}
	if s.JudgeErrors > 0 {
		parts = append(parts, fmt.Sprintf("%d candidate(s) failed closed", s.JudgeErrors))
	}
	return fmt.Errorf("triage run %s failed: %s", s.RunID, strings.Join(parts, "; "))
}

// Text renders the human-readable summary sent to Telegram and logged at the
// end of a run.
func (s Summary) Text() string {
	var b strings.Builder
	b.WriteString("Triage run " + s.RunID)
	if s.DryRun {
		b.WriteString(" (dry run)")
	}
	b.WriteString("\n")
	for _, a := range s.Accounts {
		fmt.Fprintf(&b, "%s: %s", a.Account, counts(a.Scanned, a.MarkedRead, a.Left, a.Unmatched, a.JudgeCalls, a.JudgeErrors))
		if a.Err != nil {
			fmt.Fprintf(&b, ", ERROR: %v", a.Err)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "totals: %s (%s)",
		counts(s.Scanned, s.MarkedRead, s.Left, s.Unmatched, s.JudgeCalls, s.JudgeErrors),
		s.Duration.Round(time.Millisecond))
	return b.String()
}

func counts(scanned, markedRead, left, unmatched, calls, errs int) string {
	return fmt.Sprintf("scanned %d, marked read %d, left %d, unmatched %d, judge %d calls/%d errors",
		scanned, markedRead, left, unmatched, calls, errs)
}

// logAccounts renders the per-account counts for the run summary log record.
// Plain values only, so both the JSON and text handlers render them usefully.
func (s Summary) logAccounts() []map[string]any {
	out := make([]map[string]any, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		m := map[string]any{
			"account":      a.Account,
			"scanned":      a.Scanned,
			"marked_read":  a.MarkedRead,
			"left":         a.Left,
			"unmatched":    a.Unmatched,
			"candidates":   a.Candidates,
			"judge_calls":  a.JudgeCalls,
			"judge_errors": a.JudgeErrors,
		}
		if a.Err != nil {
			m["error"] = a.Err.Error()
		}
		out = append(out, m)
	}
	return out
}
