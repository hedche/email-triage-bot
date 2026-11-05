package triage

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Op is the kind of Rule an Email Change adds.
type Op string

const (
	OpProtectSender  Op = "protect_sender"  // Protect Rule on an exact address
	OpProtectDomain  Op = "protect_domain"  // Protect Rule on a domain and its subdomains
	OpProtectKeyword Op = "protect_keyword" // Protect Rule on a subject substring
	OpReadSender     Op = "read_sender"     // Sender Rule on an exact address
	OpReadDomain     Op = "read_domain"     // Sender Rule on a domain and its subdomains
	OpReadKeyword    Op = "read_keyword"    // Keyword Rule on a subject substring
	OpBrandNote      Op = "brand_note"      // add to a Brand's note, creating the Brand if needed
	OpBrandExample   Op = "brand_example"   // add an Example to a Brand, creating the Brand if needed
)

// Ops lists every Op, in the order a prompt or schema should present them.
var Ops = []Op{
	OpProtectSender, OpProtectDomain, OpProtectKeyword,
	OpReadSender, OpReadDomain, OpReadKeyword,
	OpBrandNote, OpBrandExample,
}

// minKeywordLen keeps a Keyword Rule added by email from being so short that
// it catches far more than the person meant ("re" would match half the inbox).
const minKeywordLen = 4

// Change is one Rule added by email rather than by editing the rules file.
// Changes live in the Overlay, which the bot owns; they are laid on top of the
// rules file additively and never replace or remove anything in it.
type Change struct {
	ID      string `yaml:"id" json:"id"`
	Account string `yaml:"account,omitempty" json:"account,omitempty"` // "" applies to every Account
	Op      Op     `yaml:"op" json:"op"`
	Value   string `yaml:"value,omitempty" json:"value,omitempty"` // address, domain or subject substring
	// Subject narrows read_sender and read_domain: the Rule then matches only
	// mail whose subject also contains it (SenderRule.Subject).
	Subject   string    `yaml:"subject,omitempty" json:"subject,omitempty"`
	Brand     string    `yaml:"brand,omitempty" json:"brand,omitempty"` // brand_note and brand_example only
	Verdict   Verdict   `yaml:"verdict,omitempty" json:"verdict,omitempty"`
	Note      string    `yaml:"note,omitempty" json:"note,omitempty"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// Normalise trims and lower-cases the matching fields, the same way LoadRules
// treats the rules file.
func (c Change) Normalise() Change {
	c.Account = strings.ToLower(strings.TrimSpace(c.Account))
	c.Op = Op(strings.ToLower(strings.TrimSpace(string(c.Op))))
	c.Value = strings.TrimSpace(c.Value)
	switch c.Op {
	case OpProtectSender, OpProtectDomain, OpReadSender, OpReadDomain:
		c.Value = strings.ToLower(strings.TrimPrefix(c.Value, "@"))
	}
	c.Subject = strings.TrimSpace(c.Subject)
	if c.Op != OpReadSender && c.Op != OpReadDomain {
		c.Subject = ""
	}
	c.Brand = strings.TrimSpace(c.Brand)
	c.Verdict = Verdict(strings.ToLower(strings.TrimSpace(string(c.Verdict))))
	c.Note = strings.TrimSpace(c.Note)
	return c
}

// Validate reports whether the Change is well-formed for its Op.
func (c Change) Validate() error {
	if c.Subject != "" && len([]rune(c.Subject)) < minKeywordLen {
		return fmt.Errorf("%s: subject %q is shorter than %d characters", c.Op, c.Subject, minKeywordLen)
	}
	switch c.Op {
	case OpProtectSender, OpReadSender:
		if a, err := mail.ParseAddress(c.Value); err != nil || !strings.EqualFold(a.Address, c.Value) {
			return fmt.Errorf("%s: %q is not an email address", c.Op, c.Value)
		}
	case OpProtectDomain, OpReadDomain:
		if !validDomain(c.Value) {
			return fmt.Errorf("%s: %q is not a domain", c.Op, c.Value)
		}
	case OpProtectKeyword, OpReadKeyword:
		if len([]rune(c.Value)) < minKeywordLen {
			return fmt.Errorf("%s: %q is shorter than %d characters", c.Op, c.Value, minKeywordLen)
		}
	case OpBrandNote:
		if c.Brand == "" || c.Note == "" {
			return fmt.Errorf("%s: brand and note are required", c.Op)
		}
	case OpBrandExample:
		if c.Brand == "" || c.Note == "" {
			return fmt.Errorf("%s: brand and note (the example's description) are required", c.Op)
		}
		if c.Verdict != VerdictMarkRead && c.Verdict != VerdictLeave {
			return fmt.Errorf("%s: verdict %q: must be %q or %q", c.Op, c.Verdict, VerdictMarkRead, VerdictLeave)
		}
	default:
		return fmt.Errorf("unknown op %q", c.Op)
	}
	return nil
}

// Protective reports whether the Change can only ever cause the bot to leave
// more mail alone. Protective Changes are safe to apply without confirmation;
// anything that could cause mail to be marked read is not.
func (c Change) Protective() bool {
	switch c.Op {
	case OpProtectSender, OpProtectDomain, OpProtectKeyword:
		return true
	case OpBrandExample:
		return c.Verdict == VerdictLeave
	}
	return false
}

// Describe renders the Change as one line a person can check at a glance.
func (c Change) Describe() string {
	scope := "all accounts"
	if c.Account != "" {
		scope = c.Account
	}
	var what string
	switch c.Op {
	case OpProtectSender:
		what = "never touch mail from " + c.Value
	case OpProtectDomain:
		what = "never touch mail from *@" + c.Value
	case OpProtectKeyword:
		what = fmt.Sprintf("never touch mail whose subject contains %q", c.Value)
	case OpReadSender:
		what = "always mark read mail from " + c.Value
	case OpReadDomain:
		what = "always mark read mail from *@" + c.Value
	case OpReadKeyword:
		what = fmt.Sprintf("always mark read mail whose subject contains %q", c.Value)
	case OpBrandNote:
		what = fmt.Sprintf("tell the Judge about %s: %s", c.Brand, c.Note)
	case OpBrandExample:
		what = fmt.Sprintf("%s example: %s → %s", c.Brand, c.Note, c.Verdict)
	default:
		what = string(c.Op) + " " + c.Value
	}
	if c.Subject != "" {
		what += fmt.Sprintf(" whose subject contains %q", c.Subject)
	}
	return what + " (" + scope + ")"
}

func validDomain(d string) bool {
	if len(d) < 3 || len(d) > 253 || !strings.Contains(d, ".") ||
		strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return false
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// overlayFile is the part of the Overlay the daily run reads. The command
// subcommand stores more in the same document (pending proposals, Telegram
// offset); unknown keys are ignored here.
type overlayFile struct {
	Changes []Change `yaml:"changes"`
}

// LoadOverlay reads the Changes from an Overlay file. A missing file is not an
// error: the Overlay Secret is mounted optionally and may not exist yet.
func LoadOverlay(path string) ([]Change, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read overlay file: %w", err)
	}
	var f overlayFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse overlay file %s: %w", path, err)
	}
	out := make([]Change, 0, len(f.Changes))
	for i, c := range f.Changes {
		c = c.Normalise()
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("overlay changes[%d] (%s): %w", i, c.ID, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// WithOverlay returns a copy of r with the Changes laid on top. Address, domain
// and keyword Changes are appended to their Tier, so tier order alone decides
// precedence, exactly as for per-account rules. Brand Changes are merged into
// the Brand of the same name (case-insensitively) in the RuleSet they apply to,
// because only the first Brand with a given name is ever matched.
func (r *Rules) WithOverlay(changes []Change) *Rules {
	out := &Rules{Threshold: DefaultThreshold}
	if r != nil {
		out.Threshold = r.Threshold
		out.RuleSet = r.RuleSet.clone()
		if len(r.Accounts) > 0 {
			out.Accounts = make(map[string]RuleSet, len(r.Accounts))
			for k, v := range r.Accounts {
				out.Accounts[k] = v.clone()
			}
		}
	}
	for _, c := range changes {
		if c.Account == "" {
			out.RuleSet = out.RuleSet.apply(c)
			continue
		}
		if out.Accounts == nil {
			out.Accounts = map[string]RuleSet{}
		}
		out.Accounts[c.Account] = out.Accounts[c.Account].apply(c)
	}
	return out
}

func (rs RuleSet) clone() RuleSet {
	brands := make([]BrandRule, len(rs.Brands))
	for i, b := range rs.Brands {
		b.Examples = append([]Example(nil), b.Examples...)
		brands[i] = b
	}
	if rs.Brands == nil {
		brands = nil
	}
	return RuleSet{
		Protect:  append([]ProtectRule(nil), rs.Protect...),
		Senders:  append([]SenderRule(nil), rs.Senders...),
		Keywords: append([]KeywordRule(nil), rs.Keywords...),
		Brands:   brands,
	}
}

// apply adds one Change to rs. A per-account Brand Change that names a global
// Brand lands in the per-account RuleSet; For merges the two by name.
func (rs RuleSet) apply(c Change) RuleSet {
	note := c.Note
	if note == "" {
		note = "added by email"
	}
	switch c.Op {
	case OpProtectSender:
		rs.Protect = append(rs.Protect, ProtectRule{Sender: c.Value, Note: note})
	case OpProtectDomain:
		rs.Protect = append(rs.Protect, ProtectRule{Domain: c.Value, Note: note})
	case OpProtectKeyword:
		rs.Protect = append(rs.Protect, ProtectRule{Keyword: c.Value, Note: note})
	case OpReadSender:
		rs.Senders = append(rs.Senders, SenderRule{Address: c.Value, Subject: c.Subject, Note: note})
	case OpReadDomain:
		rs.Senders = append(rs.Senders, SenderRule{Domain: c.Value, Subject: c.Subject, Note: note})
	case OpReadKeyword:
		rs.Keywords = append(rs.Keywords, KeywordRule{Subject: c.Value, Note: note})
	case OpBrandNote:
		rs.Brands = mergeBrand(rs.Brands, BrandRule{Name: c.Brand, Note: c.Note})
	case OpBrandExample:
		rs.Brands = mergeBrand(rs.Brands, BrandRule{Name: c.Brand, Examples: []Example{{Description: c.Note, Verdict: c.Verdict}}})
	}
	return rs
}

// mergeBrand folds add into the Brand of the same name (case-insensitively),
// appending its note and examples, or appends it as a new Brand. brands is
// never modified in place.
func mergeBrand(brands []BrandRule, add BrandRule) []BrandRule {
	out := append([]BrandRule(nil), brands...)
	for i := range out {
		if !strings.EqualFold(out[i].Name, add.Name) {
			continue
		}
		b := out[i]
		switch {
		case b.Note == "":
			b.Note = add.Note
		case add.Note != "":
			b.Note = b.Note + " " + add.Note
		}
		b.Examples = append(append([]Example(nil), b.Examples...), add.Examples...)
		out[i] = b
		return out
	}
	add.Examples = append([]Example(nil), add.Examples...)
	return append(out, add)
}
