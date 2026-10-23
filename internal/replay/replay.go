// Package replay re-evaluates a saved Decision Log against the current Rules
// without contacting a Provider or a Judge, to show which Verdicts a Rule
// change would alter.
package replay

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// Change is one Message whose Tier or Verdict the current Rules would alter.
// When NeedsJudge is set the Message would now be a Candidate, so After holds
// a Brand Tier and an empty Verdict rather than a decided one.
type Change struct {
	MessageID  string
	Account    string
	From       string
	Subject    string
	Before     triage.Decision
	After      triage.Decision
	NeedsJudge bool
}

// Report is the outcome of a Replay. Changed counts the Changes listed;
// NeedsJudge counts the subset of them that would now need a Judge.
type Report struct {
	Total      int
	Changed    int
	NeedsJudge int
	Changes    []Change
}

// Run re-evaluates every Record against rules. When account is non-empty only
// that Account's Records are replayed. Records whose Tier and Verdict are
// unchanged are not listed; a Message that stays in the Brand Tier under the
// same Brand is unchanged, because only a Judge could decide it either way.
func Run(records []decisionlog.Record, rules *triage.Rules, account string) Report {
	want := strings.ToLower(strings.TrimSpace(account))

	var report Report
	engines := make(map[string]*triage.Engine)
	engineFor := func(acct string) *triage.Engine {
		key := strings.ToLower(strings.TrimSpace(acct))
		if e, ok := engines[key]; ok {
			return e
		}
		threshold := triage.DefaultThreshold
		if rules != nil {
			threshold = rules.Threshold
		}
		e := triage.NewEngine(rules.For(key), threshold)
		engines[key] = e
		return e
	}

	for _, rec := range records {
		acct := rec.Message.Account
		if acct == "" {
			acct = rec.Decision.Account
		}
		if want != "" && strings.ToLower(strings.TrimSpace(acct)) != want {
			continue
		}
		report.Total++

		msg := message(rec, acct)
		before := rec.Decision
		after := engineFor(acct).Evaluate(msg)
		after.RunID = before.RunID

		if !changed(before, after) {
			continue
		}
		change := Change{
			MessageID:  msg.ID,
			Account:    acct,
			From:       formatFrom(msg.From),
			Subject:    msg.Subject,
			Before:     before,
			After:      after,
			NeedsJudge: after.Tier == triage.TierBrand,
		}
		report.Changes = append(report.Changes, change)
		if change.NeedsJudge {
			report.NeedsJudge++
		}
	}
	report.Changed = len(report.Changes)
	return report
}

// message rebuilds the scanned Message from its Decision Log summary. Bodies
// are never logged, so a Replay sees headers, subject and snippet only.
func message(rec decisionlog.Record, account string) triage.Message {
	return triage.Message{
		ID:       rec.Message.ID,
		Account:  account,
		From:     rec.Message.From,
		Subject:  rec.Message.Subject,
		Snippet:  rec.Message.Snippet,
		Received: rec.Message.Received,
	}
}

func changed(before, after triage.Decision) bool {
	if before.Tier != after.Tier {
		return true
	}
	if after.Tier == triage.TierBrand {
		// Only a Judge decides a Candidate, so the Brand is the only thing
		// that can have changed here.
		return before.Brand != after.Brand
	}
	return before.Verdict != after.Verdict
}

func formatFrom(a triage.Address) string {
	switch {
	case a.Name != "" && a.Email != "":
		return fmt.Sprintf("%s <%s>", a.Name, a.Email)
	case a.Email != "":
		return a.Email
	default:
		return a.Name
	}
}

// Write prints the Report as a table: one line per Change, then the totals.
func (r Report) Write(w io.Writer) {
	if len(r.Changes) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ACCOUNT\tFROM\tSUBJECT\tBEFORE\tAFTER")
		for _, c := range r.Changes {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t→ %s\n",
				c.Account,
				truncate(c.From, 40),
				truncate(c.Subject, 60),
				outcome(c.Before),
				afterOutcome(c),
			)
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "%d replayed, %d changed, %d need judge\n", r.Total, r.Changed, r.NeedsJudge)
}

func outcome(d triage.Decision) string {
	tier := string(d.Tier)
	if tier == "" {
		tier = string(triage.TierNone)
	}
	verdict := string(d.Verdict)
	if verdict == "" {
		verdict = "-"
	}
	return tier + "/" + verdict
}

func afterOutcome(c Change) string {
	if c.NeedsJudge {
		return string(c.After.Tier) + " (needs judge)"
	}
	return outcome(c.After)
}

// truncate shortens s to max runes, marking it with an ellipsis when cut.
func truncate(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}
