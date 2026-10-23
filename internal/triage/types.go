// Package triage holds the domain model: messages, rules, decisions and the
// engine that turns the first two into the third. Vocabulary follows
// CONTEXT.md at the repo root; keep the two in step.
package triage

import "time"

// Verdict is what a Decision says should happen to a Message.
type Verdict string

const (
	VerdictMarkRead Verdict = "mark-read"
	VerdictLeave    Verdict = "leave"
)

// Tier is the precedence class of the Rule that produced a Decision.
// Tiers are evaluated in the order listed; the first match wins.
type Tier string

const (
	TierProtect Tier = "protect"
	TierSender  Tier = "sender"
	TierKeyword Tier = "keyword"
	TierBrand   Tier = "brand"
	// TierNone means no Rule matched: the Message is left and never judged.
	TierNone Tier = "none"
)

// Action is what the bot actually did to a Message after deciding.
type Action string

const (
	ActionMarkedRead Action = "marked-read" // UNREAD removed, Read label applied
	ActionLeft       Action = "left"        // Left label applied, otherwise untouched
	ActionNone       Action = "none"        // unmatched, or judge failed: nothing applied
	ActionDryRun     Action = "dry-run"     // would have acted; dry run suppressed it
)

// Address is a parsed From header.
type Address struct {
	Name  string `json:"name"`
	Email string `json:"email"` // lower-cased
}

// Domain returns the part after '@', lower-cased, or "" if malformed.
func (a Address) Domain() string {
	for i := len(a.Email) - 1; i >= 0; i-- {
		if a.Email[i] == '@' {
			return a.Email[i+1:]
		}
	}
	return ""
}

// Message is one email as scanned. Body is already stripped and capped by
// the Provider; the engine never sees raw HTML.
type Message struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"thread_id,omitempty"`
	Account  string    `json:"account"`
	From     Address   `json:"from"`
	Subject  string    `json:"subject"`
	Snippet  string    `json:"snippet"`
	Body     string    `json:"body,omitempty"` // omitted from the Decision Log
	Received time.Time `json:"received"`
	Labels   []string  `json:"labels,omitempty"` // provider label IDs/names as fetched
}

// ---- Rules -----------------------------------------------------------------

// ProtectRule names mail the bot must never touch. Exactly one of the
// matching fields should be set.
type ProtectRule struct {
	Sender  string `yaml:"sender,omitempty" json:"sender,omitempty"`   // exact address
	Domain  string `yaml:"domain,omitempty" json:"domain,omitempty"`   // domain incl. subdomains
	Keyword string `yaml:"keyword,omitempty" json:"keyword,omitempty"` // case-insensitive subject substring
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

// SenderRule names an exact address or a domain whose mail is always marked read.
type SenderRule struct {
	Address string `yaml:"address,omitempty" json:"address,omitempty"`
	Domain  string `yaml:"domain,omitempty" json:"domain,omitempty"`
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

// KeywordRule names a case-insensitive subject substring whose mail is always marked read.
type KeywordRule struct {
	Subject string `yaml:"subject" json:"subject"`
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

// Example guides the Judge: a short description of a kind of Message from a
// Brand and the correct Verdict for it. Never contains message text.
type Example struct {
	Description string  `yaml:"description" json:"description"`
	Verdict     Verdict `yaml:"verdict" json:"verdict"`
}

// BrandRule names a company or product. A Message that mentions the brand in
// its sender name, sender domain or subject becomes a Candidate for the Judge.
type BrandRule struct {
	Name     string    `yaml:"name" json:"name"`
	Note     string    `yaml:"note,omitempty" json:"note,omitempty"` // what is routine from this brand
	Examples []Example `yaml:"examples,omitempty" json:"examples,omitempty"`
}

// RuleSet is one tier-grouped set of rules. The global RuleSet and each
// per-account RuleSet are merged additively (see Rules.For).
type RuleSet struct {
	Protect  []ProtectRule `yaml:"protect,omitempty" json:"protect,omitempty"`
	Senders  []SenderRule  `yaml:"senders,omitempty" json:"senders,omitempty"`
	Keywords []KeywordRule `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Brands   []BrandRule   `yaml:"brands,omitempty" json:"brands,omitempty"`
}

// Rules is the whole rules file: a threshold, a global RuleSet, and optional
// per-account RuleSets keyed by account email (lower-cased).
type Rules struct {
	Threshold float64 `yaml:"threshold" json:"threshold"`
	RuleSet   `yaml:",inline"`
	Accounts  map[string]RuleSet `yaml:"accounts,omitempty" json:"accounts,omitempty"`
}

// ---- Decisions -------------------------------------------------------------

// Judgement is what the Judge returns for one Candidate.
type Judgement struct {
	MessageID  string  `json:"message_id"`
	Confidence float64 `json:"confidence"` // 0..1
	Reason     string  `json:"reason"`
}

// Decision is the recorded outcome for one Message in one Triage Run.
type Decision struct {
	RunID      string   `json:"run_id"`
	Account    string   `json:"account"`
	MessageID  string   `json:"message_id"`
	Tier       Tier     `json:"tier"`
	Rule       string   `json:"rule,omitempty"`  // the matched rule, rendered (e.g. "domain:royalmail.com")
	Brand      string   `json:"brand,omitempty"` // for TierBrand
	Confidence *float64 `json:"confidence"`      // nil unless a Judge was consulted
	Reason     string   `json:"reason,omitempty"`
	Verdict    Verdict  `json:"verdict"`
	Action     Action   `json:"action"`
	JudgeError string   `json:"judge_error,omitempty"` // set when the Judge failed for this message
	DryRun     bool     `json:"dry_run"`
	Judge      string   `json:"judge,omitempty"` // provider/model name when consulted
}

// Candidate is a Message that reached the Brand tier, paired with the Brand
// Rule it matched. Only Candidates are sent to a Judge.
type Candidate struct {
	Message Message
	Brand   BrandRule
}
