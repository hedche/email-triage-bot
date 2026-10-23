package triage

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultThreshold is the Confidence at or above which a Candidate's Verdict
// is mark-read, used when the rules file does not set one.
const DefaultThreshold = 0.8

// rulesFile mirrors the on-disk shape of rules.yaml. Threshold is a pointer so
// an absent threshold can be told apart from an explicit 0.
type rulesFile struct {
	Threshold *float64           `yaml:"threshold"`
	RuleSet   `yaml:",inline"`   //nolint:govet // inline global rules
	Accounts  map[string]RuleSet `yaml:"accounts"`
}

// LoadRules reads and validates a rules file. Addresses and domains are
// trimmed and lower-cased, account keys are lower-cased, and the Threshold
// defaults to DefaultThreshold when absent.
func LoadRules(path string) (*Rules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file: %w", err)
	}
	var f rulesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse rules file %s: %w", path, err)
	}

	rules := &Rules{Threshold: DefaultThreshold}
	if f.Threshold != nil {
		rules.Threshold = *f.Threshold
	}
	if rules.Threshold < 0 || rules.Threshold > 1 {
		return nil, fmt.Errorf("threshold %v: must be between 0 and 1", rules.Threshold)
	}

	global, err := normaliseRuleSet(f.RuleSet, "")
	if err != nil {
		return nil, err
	}
	rules.RuleSet = global

	if len(f.Accounts) > 0 {
		rules.Accounts = make(map[string]RuleSet, len(f.Accounts))
		for account, rs := range f.Accounts {
			key := strings.ToLower(strings.TrimSpace(account))
			if key == "" {
				return nil, fmt.Errorf("accounts: empty account key")
			}
			if _, dup := rules.Accounts[key]; dup {
				return nil, fmt.Errorf("accounts: duplicate account %q", key)
			}
			normalised, err := normaliseRuleSet(rs, key)
			if err != nil {
				return nil, err
			}
			rules.Accounts[key] = normalised
		}
	}
	return rules, nil
}

// normaliseRuleSet trims and lower-cases matching fields and validates every
// Rule in the set. where names the account for error messages ("" is global).
func normaliseRuleSet(rs RuleSet, where string) (RuleSet, error) {
	scope := "rules"
	if where != "" {
		scope = "accounts." + where
	}

	out := RuleSet{}

	for i, r := range rs.Protect {
		r.Sender = strings.ToLower(strings.TrimSpace(r.Sender))
		r.Domain = strings.ToLower(strings.TrimSpace(r.Domain))
		r.Keyword = strings.TrimSpace(r.Keyword)
		r.Note = strings.TrimSpace(r.Note)
		set := 0
		for _, v := range []string{r.Sender, r.Domain, r.Keyword} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			return RuleSet{}, fmt.Errorf("%s.protect[%d]: exactly one of sender, domain or keyword must be set", scope, i)
		}
		out.Protect = append(out.Protect, r)
	}

	for i, r := range rs.Senders {
		r.Address = strings.ToLower(strings.TrimSpace(r.Address))
		r.Domain = strings.ToLower(strings.TrimSpace(r.Domain))
		r.Note = strings.TrimSpace(r.Note)
		set := 0
		for _, v := range []string{r.Address, r.Domain} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			return RuleSet{}, fmt.Errorf("%s.senders[%d]: exactly one of address or domain must be set", scope, i)
		}
		out.Senders = append(out.Senders, r)
	}

	for i, r := range rs.Keywords {
		r.Subject = strings.TrimSpace(r.Subject)
		r.Note = strings.TrimSpace(r.Note)
		if r.Subject == "" {
			return RuleSet{}, fmt.Errorf("%s.keywords[%d]: subject must not be empty", scope, i)
		}
		out.Keywords = append(out.Keywords, r)
	}

	for i, r := range rs.Brands {
		r.Name = strings.TrimSpace(r.Name)
		r.Note = strings.TrimSpace(r.Note)
		if r.Name == "" {
			return RuleSet{}, fmt.Errorf("%s.brands[%d]: name must not be empty", scope, i)
		}
		examples := make([]Example, 0, len(r.Examples))
		for j, ex := range r.Examples {
			ex.Description = strings.TrimSpace(ex.Description)
			ex.Verdict = Verdict(strings.ToLower(strings.TrimSpace(string(ex.Verdict))))
			if ex.Description == "" {
				return RuleSet{}, fmt.Errorf("%s.brands[%d].examples[%d]: description must not be empty", scope, i, j)
			}
			if ex.Verdict != VerdictMarkRead && ex.Verdict != VerdictLeave {
				return RuleSet{}, fmt.Errorf("%s.brands[%d].examples[%d]: verdict %q: must be %q or %q",
					scope, i, j, ex.Verdict, VerdictMarkRead, VerdictLeave)
			}
			examples = append(examples, ex)
		}
		if len(examples) > 0 {
			r.Examples = examples
		} else {
			r.Examples = nil
		}
		out.Brands = append(out.Brands, r)
	}

	return out, nil
}

// For returns the RuleSet that applies to one Account: the global rules with
// that Account's rules appended, tier by tier. Per-account rules never replace
// global ones, so a per-account Protect Rule still wins over a global Sender
// Rule by tier order alone.
func (r *Rules) For(account string) RuleSet {
	if r == nil {
		return RuleSet{}
	}
	merged := RuleSet{
		Protect:  append([]ProtectRule(nil), r.Protect...),
		Senders:  append([]SenderRule(nil), r.Senders...),
		Keywords: append([]KeywordRule(nil), r.Keywords...),
		Brands:   append([]BrandRule(nil), r.Brands...),
	}
	per, ok := r.Accounts[strings.ToLower(strings.TrimSpace(account))]
	if !ok {
		return merged
	}
	merged.Protect = append(merged.Protect, per.Protect...)
	merged.Senders = append(merged.Senders, per.Senders...)
	merged.Keywords = append(merged.Keywords, per.Keywords...)
	merged.Brands = append(merged.Brands, per.Brands...)
	return merged
}
