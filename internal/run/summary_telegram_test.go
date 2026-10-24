package run

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTelegramHTMLCleanRun(t *testing.T) {
	sum := Summary{
		RunID:    "20260916T050000Z-3f9a",
		Duration: 70371 * time.Millisecond,
		Accounts: []AccountSummary{
			{Account: "a@example.com", Scanned: 25, MarkedRead: 18, Left: 5, Unmatched: 2, JudgeCalls: 2},
			{Account: "b@example.com", Scanned: 72, Unmatched: 71, Left: 1},
		},
		Scanned: 97, MarkedRead: 18, Left: 6, Unmatched: 73, JudgeCalls: 2,
	}
	got := sum.TelegramHTML()

	for _, want := range []string{
		"✅ <b>Email triage</b>",
		"<b>18</b> marked read · 6 left · 73 unmatched",
		"<i>97 scanned across 2 accounts in 1m10s</i>",
		"<blockquote expandable>",
		"📭 <b>a@example.com</b>\n25 scanned · <b>18</b> read · 5 left · 2 unmatched · judge 2 calls",
		"💤 <b>b@example.com</b>\n72 scanned · 1 left · 71 unmatched",
		"</blockquote>",
		"<code>20260916T050000Z-3f9a</code>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	// A clean run says nothing about dry runs, failures or zero counts.
	for _, unwanted := range []string{"Dry run", "❌", "⚠️", "0 read", "judge 0 calls"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("message should not contain %q:\n%s", unwanted, got)
		}
	}
}

func TestTelegramHTMLDryRunIsMarkedAndCountsAreConditional(t *testing.T) {
	sum := Summary{RunID: "r1", DryRun: true, MarkedRead: 36}
	got := sum.TelegramHTML()
	if !strings.Contains(got, "🧪 <b>Email triage</b>\n<i>Dry run — no mail was changed.</i>") {
		t.Errorf("dry run header missing:\n%s", got)
	}
	if !strings.Contains(got, "<b>36</b> would be marked read") {
		t.Errorf("dry run should not claim mail was changed:\n%s", got)
	}
}

// Failures must be visible without expanding the quote: the collapsed view is
// what a phone shows, and a failed run is the one that needs reading.
func TestTelegramHTMLSurfacesFailuresAboveTheQuote(t *testing.T) {
	sum := Summary{
		RunID:       "r1",
		JudgeErrors: 4,
		Accounts:    []AccountSummary{{Account: "b@example.com", Scanned: 2, Err: errors.New("list unread: token expired")}},
	}
	got := sum.TelegramHTML()
	if !strings.HasPrefix(got, "❌ <b>Email triage</b>") {
		t.Errorf("failed run needs a failure icon:\n%s", got)
	}
	quote := strings.Index(got, "<blockquote")
	for _, want := range []string{"❌ <b>b@example.com</b>\nlist unread: token expired", "⚠️ <b>4 candidates failed closed</b>"} {
		at := strings.Index(got, want)
		if at < 0 {
			t.Fatalf("message missing %q:\n%s", want, got)
		}
		if at > quote {
			t.Errorf("%q is inside the collapsed quote, where it can be missed", want)
		}
	}
}

// Telegram rejects the whole message when an entity fails to parse, so every
// interpolated value has to be escaped. Addresses and provider errors are the
// values the bot does not control.
func TestTelegramHTMLEscapesInterpolatedValues(t *testing.T) {
	sum := Summary{
		RunID:    "r<1>",
		Accounts: []AccountSummary{{Account: "a<b>&c@example.com", Err: errors.New(`bad tag <script> & "quote"`)}},
	}
	got := sum.TelegramHTML()
	for _, want := range []string{"a&lt;b&gt;&amp;c@example.com", "bad tag &lt;script&gt; &amp; \"quote\"", "<code>r&lt;1&gt;</code>"} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing escaped %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("raw tag survived escaping:\n%s", got)
	}
}

func TestTelegramHTMLCapsUnboundedErrorText(t *testing.T) {
	sum := Summary{
		RunID:    "r1",
		Accounts: []AccountSummary{{Account: "a@example.com", Err: errors.New(strings.Repeat("é", 4000))}},
	}
	got := sum.TelegramHTML()
	if len([]rune(got)) > 4096 {
		t.Errorf("message is %d runes, over Telegram's 4096 cap", len([]rune(got)))
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncated error should be marked with an ellipsis:\n%s", got)
	}
	if !strings.ContainsRune(got, 'é') || strings.Contains(got, "\uFFFD") {
		t.Errorf("truncation split a rune:\n%s", got)
	}
}

// Text stays plain: it goes to stderr and the pod logs, where markup is noise.
func TestTextStaysPlain(t *testing.T) {
	sum := Summary{RunID: "r1", Accounts: []AccountSummary{{Account: "a@example.com", Scanned: 1}}}
	if got := sum.Text(); strings.ContainsAny(got, "<>") {
		t.Errorf("Text() leaked markup: %q", got)
	}
}
