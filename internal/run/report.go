package run

import (
	"fmt"
	"html"
	"strings"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// Bucket is where a Message ended up, in the words the summary uses.
type Bucket string

const (
	BucketRead      Bucket = "read"
	BucketLeft      Bucket = "left"
	BucketUnmatched Bucket = "unmatched"
)

// ReportItem is one scanned Message as the run report shows it: enough to
// recognise the mail and see why it went where it did, and no body.
type ReportItem struct {
	Bucket  Bucket
	From    string // display name, or the address when there is none
	Subject string // capped at subjectMax runes
	Why     string // the matched rule, or the Judge's reason and confidence
}

// subjectMax caps a subject in the report. The report is for recognising a
// mail at a glance, not reading it.
const subjectMax = 45

// reportItem builds the report line for one decided Message.
func reportItem(m triage.Message, d triage.Decision) ReportItem {
	it := ReportItem{
		From:    m.From.Name,
		Subject: truncateRunes(strings.TrimSpace(m.Subject), subjectMax),
	}
	if it.From == "" {
		it.From = m.From.Email
	}
	switch {
	case d.Verdict == triage.VerdictMarkRead:
		it.Bucket = BucketRead
	case d.Tier == triage.TierNone:
		it.Bucket = BucketUnmatched
	default:
		it.Bucket = BucketLeft
	}

	switch {
	case d.JudgeError != "":
		it.Why = "judge failed: " + truncateRunes(d.JudgeError, 60)
	case d.Confidence != nil:
		it.Why = fmt.Sprintf("%s %.2f", d.Reason, *d.Confidence)
		if d.Reason == "" {
			it.Why = fmt.Sprintf("judge %.2f", *d.Confidence)
		}
	case d.Tier == triage.TierBrand:
		it.Why = d.Reason
	case d.Tier != triage.TierNone:
		it.Why = string(d.Tier) + " " + d.Rule
	}
	// A live batch action that failed leaves the Verdict but no Action: say
	// so, rather than list the mail as read when it is still unread.
	if !d.DryRun && d.Action == triage.ActionNone && (d.Verdict == triage.VerdictMarkRead || it.Bucket == BucketLeft && d.Tier == triage.TierProtect) {
		it.Why += " · not applied, the Gmail action failed"
	}
	return it
}

// ReportHTML renders the run report as a standalone HTML page: one section per
// Account, each with collapsible Read, Left and Unmatched lists. A Dry Run's
// report says what would have happened, a live run's what did. It is sent
// as a file rather than a message because a full lookback window is far past
// Telegram's 4096-character limit, and a file opens in the phone's viewer,
// where <details> gives the dropdowns.
func (s Summary) ReportHTML() string {
	var b strings.Builder
	esc := html.EscapeString

	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Email triage ` + esc(s.RunID) + `</title>
<style>
body{font:15px/1.4 -apple-system,system-ui,sans-serif;margin:16px;color:#1d1d1f;background:#fff}
@media (prefers-color-scheme:dark){body{color:#f2f2f7;background:#111}li{border-color:#333!important}.why{color:#98989d!important}}
h1{font-size:20px;margin:0 0 4px}
.meta{color:#86868b;font-size:13px;margin-bottom:16px}
details{margin:6px 0}
summary{cursor:pointer;font-weight:600;padding:6px 0}
details details{margin-left:14px}
details details summary{font-weight:500}
ul{list-style:none;padding:0 0 0 14px;margin:0}
li{padding:5px 0;border-bottom:1px solid #eee}
.from{font-weight:600}
.why{color:#6e6e73;font-size:13px}
.warn{color:#c93400}
</style></head><body>
`)
	heading, readTitle, readVerb := "📬 Email triage report", "📭 Marked read", "marked read"
	if s.DryRun {
		heading, readTitle, readVerb = "🧪 Email triage dry run", "📭 Would be read", "would be read"
	}
	b.WriteString("<h1>" + heading + "</h1>\n")
	fmt.Fprintf(&b, `<div class="meta">%s · %d scanned · %d %s · %d left · %d unmatched</div>`+"\n",
		esc(s.RunID), s.Scanned, s.MarkedRead, readVerb, s.Left, s.Unmatched)

	for _, a := range s.Accounts {
		fmt.Fprintf(&b, "<details><summary>%s %s <span class=\"why\">%s</span></summary>\n",
			accountIcon(a), esc(a.Account), esc(stripTags(accountCounts(a))))
		switch {
		case a.Skipped != "":
			fmt.Fprintf(&b, "<p class=\"warn\">Skipped: %s</p>\n", esc(a.Skipped))
		case a.Err != nil:
			fmt.Fprintf(&b, "<p class=\"warn\">Failed: %s</p>\n", esc(truncate(a.Err.Error(), errMax)))
		}
		for _, sec := range []struct {
			bucket Bucket
			title  string
		}{
			{BucketRead, readTitle},
			{BucketLeft, "✋ Left"},
			{BucketUnmatched, "❔ Unmatched"},
		} {
			var items []ReportItem
			for _, it := range a.Items {
				if it.Bucket == sec.bucket {
					items = append(items, it)
				}
			}
			if len(items) == 0 {
				continue
			}
			fmt.Fprintf(&b, "<details><summary>%s (%d)</summary><ul>\n", sec.title, len(items))
			for _, it := range items {
				fmt.Fprintf(&b, "<li><span class=\"from\">%s</span> — %s", esc(it.From), esc(it.Subject))
				if it.Why != "" {
					fmt.Fprintf(&b, "<br><span class=\"why\">%s</span>", esc(it.Why))
				}
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul></details>\n")
		}
		b.WriteString("</details>\n")
	}
	b.WriteString("</body></html>\n")
	return b.String()
}

// stripTags removes the <b> markup accountCounts adds for Telegram.
var stripTags = strings.NewReplacer("<b>", "", "</b>", "").Replace
