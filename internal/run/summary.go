package run

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

// FailedAccounts lists the addresses of the Accounts whose triage errored.
func (s Summary) FailedAccounts() []string {
	out := make([]string, 0, len(s.Accounts))
	for _, a := range s.failures() {
		out = append(out, a.Account)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// failures is the Accounts that errored, in run order.
func (s Summary) failures() []AccountSummary {
	var out []AccountSummary
	for _, a := range s.Accounts {
		if a.Err != nil {
			out = append(out, a)
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

// TelegramHTML renders the summary as the message the run sends to Telegram.
//
// HTML is the parse mode rather than MarkdownV2 because it needs only three
// characters escaped, so an address, a brand or a provider error interpolates
// without the escaping bugs MarkdownV2's eighteen reserved characters invite.
// The layout uses no <pre> block: Telegram scrolls those horizontally on a
// phone instead of wrapping, which is what makes an aligned table unreadable
// there. Anything that needs attention — a failed Account, a failed-closed
// Candidate — sits above the per-account detail, never inside the collapsed
// quote, so a problem is never a tap away.
func (s Summary) TelegramHTML() string {
	var b strings.Builder

	icon := "✅"
	switch {
	case s.Failed():
		icon = "❌"
	case s.DryRun:
		icon = "🧪"
	}
	b.WriteString(icon + " <b>Email triage</b>")
	if s.DryRun {
		b.WriteString("\n<i>Dry run — no mail was changed.</i>")
	}

	verb := "marked read"
	if s.DryRun {
		verb = "would be marked read"
	}
	fmt.Fprintf(&b, "\n\n<b>%d</b> %s · %d left · %d unmatched", s.MarkedRead, verb, s.Left, s.Unmatched)
	fmt.Fprintf(&b, "\n<i>%d scanned across %s in %s</i>",
		s.Scanned, plural(len(s.Accounts), "account"), shortDuration(s.Duration))

	// What went wrong is announced in plain sight and detailed in its own
	// quote. The banner is the part that must survive a glance at a
	// notification; the error text under it is for when you sit down.
	failed := s.failures()
	if len(failed) > 0 || s.JudgeErrors > 0 {
		var banner []string
		if len(failed) > 0 {
			banner = append(banner, fmt.Sprintf("❌ <b>%s failed</b>", plural(len(failed), "account")))
		}
		if s.JudgeErrors > 0 {
			banner = append(banner, fmt.Sprintf("⚠️ <b>%s failed closed</b>", plural(s.JudgeErrors, "candidate")))
		}
		fmt.Fprintf(&b, "\n\n%s", strings.Join(banner, " · "))

		if len(failed) > 0 {
			b.WriteString("\n<blockquote expandable>")
			for i, a := range failed {
				if i > 0 {
					b.WriteString("\n\n")
				}
				fmt.Fprintf(&b, "<b>%s</b>\n%s", escapeHTML(a.Account), escapeHTML(truncate(a.Err.Error(), errMax)))
			}
			b.WriteString("</blockquote>")
		}
	}

	if len(s.Accounts) > 0 {
		b.WriteString("\n\n<blockquote expandable>")
		for i, a := range s.Accounts {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "%s <b>%s</b>\n%s", accountIcon(a), escapeHTML(a.Account), accountCounts(a))
		}
		b.WriteString("</blockquote>")
	}

	fmt.Fprintf(&b, "\n\n<code>%s</code>", escapeHTML(s.RunID))
	return b.String()
}

// errMax caps one Account's error text. Provider errors are unbounded, and a
// message over Telegram's 4096 characters is rejected outright — losing the
// whole summary exactly when it reports a failure.
const errMax = 300

// accountIcon is the one-glyph verdict for an Account, so a run can be read
// down the left margin without parsing any numbers.
func accountIcon(a AccountSummary) string {
	switch {
	case a.Err != nil:
		return "❌"
	case a.JudgeErrors > 0:
		return "⚠️"
	case a.MarkedRead > 0:
		return "📭"
	default:
		return "💤"
	}
}

// accountCounts renders one Account's counts, dropping the zeros. A quiet
// mailbox should read as one short line, not as five fields of nothing.
func accountCounts(a AccountSummary) string {
	parts := []string{fmt.Sprintf("%d scanned", a.Scanned)}
	if a.MarkedRead > 0 {
		parts = append(parts, fmt.Sprintf("<b>%d</b> read", a.MarkedRead))
	}
	if a.Left > 0 {
		parts = append(parts, fmt.Sprintf("%d left", a.Left))
	}
	if a.Unmatched > 0 {
		parts = append(parts, fmt.Sprintf("%d unmatched", a.Unmatched))
	}
	if a.JudgeCalls > 0 {
		parts = append(parts, fmt.Sprintf("judge %s", plural(a.JudgeCalls, "call")))
	}
	if a.JudgeErrors > 0 {
		parts = append(parts, fmt.Sprintf("<b>%s</b>", plural(a.JudgeErrors, "judge error")))
	}
	return strings.Join(parts, " · ")
}

// escapeHTML escapes the three characters Telegram's HTML parse mode reserves.
// html.EscapeString is not used: it also emits numeric references for quotes,
// which Telegram's parser does not define.
var escapeHTML = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// shortDuration trades precision for width: whole seconds for a real run, and
// milliseconds only when rounding would print a bare "0s".
func shortDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// truncate cuts s to max bytes on a rune boundary. A cut mid-rune would put
// invalid UTF-8 on the wire, which Telegram rejects outright.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + "…"
}
