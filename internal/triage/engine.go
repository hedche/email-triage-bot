package triage

import "strings"

// Engine evaluates one Account's RuleSet against Messages. It is read-only
// after construction and safe for concurrent use.
type Engine struct {
	threshold float64

	protect  []compiledProtect
	senders  []compiledSender
	keywords []compiledKeyword
	brands   []compiledBrand
}

type compiledProtect struct {
	rule ProtectRule
	// exactly one of the three is non-empty, lower-cased
	sender  string
	domain  string
	keyword string
}

type compiledSender struct {
	rule    SenderRule
	address string
	domain  string
}

type compiledKeyword struct {
	rule    KeywordRule
	subject string
}

type compiledBrand struct {
	rule BrandRule
	// name lower-cased, and the same with spaces removed for domain matching
	name        string
	nameNoSpace string
}

// NewEngine compiles a RuleSet for repeated evaluation. threshold is the
// Confidence at or above which a judged Candidate's Verdict is mark-read.
func NewEngine(rs RuleSet, threshold float64) *Engine {
	e := &Engine{threshold: threshold}
	for _, r := range rs.Protect {
		e.protect = append(e.protect, compiledProtect{
			rule:    r,
			sender:  strings.ToLower(strings.TrimSpace(r.Sender)),
			domain:  strings.ToLower(strings.TrimSpace(r.Domain)),
			keyword: strings.ToLower(strings.TrimSpace(r.Keyword)),
		})
	}
	for _, r := range rs.Senders {
		e.senders = append(e.senders, compiledSender{
			rule:    r,
			address: strings.ToLower(strings.TrimSpace(r.Address)),
			domain:  strings.ToLower(strings.TrimSpace(r.Domain)),
		})
	}
	for _, r := range rs.Keywords {
		e.keywords = append(e.keywords, compiledKeyword{
			rule:    r,
			subject: strings.ToLower(strings.TrimSpace(r.Subject)),
		})
	}
	for _, r := range rs.Brands {
		name := strings.ToLower(strings.TrimSpace(r.Name))
		e.brands = append(e.brands, compiledBrand{
			rule:        r,
			name:        name,
			nameNoSpace: strings.ReplaceAll(name, " ", ""),
		})
	}
	return e
}

// Threshold is the Confidence at or above which a judged Candidate is marked read.
func (e *Engine) Threshold() float64 { return e.threshold }

// fields holds the lower-cased parts of a Message the Rules match against.
type fields struct {
	address string // sender address only, never the display name
	domain  string
	name    string // sender display name
	subject string
}

func messageFields(m Message) fields {
	addr := strings.ToLower(strings.TrimSpace(m.From.Email))
	f := fields{
		address: addr,
		domain:  Address{Email: addr}.Domain(),
		name:    strings.ToLower(strings.TrimSpace(m.From.Name)),
		subject: strings.ToLower(m.Subject),
	}
	return f
}

// domainMatches reports whether a message domain is the rule domain or a
// subdomain of it.
func domainMatches(msgDomain, ruleDomain string) bool {
	if msgDomain == "" || ruleDomain == "" {
		return false
	}
	return msgDomain == ruleDomain || strings.HasSuffix(msgDomain, "."+ruleDomain)
}

// match walks the Tiers in order and returns the first matching Rule: the
// Tier, the rendered rule and, for the Brand tier, the Brand Rule matched.
func (e *Engine) match(m Message) (Tier, string, *BrandRule) {
	f := messageFields(m)

	for _, p := range e.protect {
		switch {
		case p.sender != "" && f.address == p.sender:
			return TierProtect, "sender:" + p.sender, nil
		case p.domain != "" && domainMatches(f.domain, p.domain):
			return TierProtect, "domain:" + p.domain, nil
		case p.keyword != "" && strings.Contains(f.subject, p.keyword):
			return TierProtect, "keyword:" + p.keyword, nil
		}
	}

	for _, s := range e.senders {
		switch {
		case s.address != "" && f.address == s.address:
			return TierSender, "address:" + s.address, nil
		case s.domain != "" && domainMatches(f.domain, s.domain):
			return TierSender, "domain:" + s.domain, nil
		}
	}

	for _, k := range e.keywords {
		if k.subject != "" && strings.Contains(f.subject, k.subject) {
			return TierKeyword, "subject:" + k.subject, nil
		}
	}

	for i, b := range e.brands {
		if b.name == "" {
			continue
		}
		if strings.Contains(f.name, b.name) ||
			(b.nameNoSpace != "" && strings.Contains(f.domain, b.nameNoSpace)) ||
			strings.Contains(f.subject, b.name) {
			rule := e.brands[i].rule
			return TierBrand, "brand:" + rule.Name, &rule
		}
	}

	return TierNone, "", nil
}

// Evaluate applies the Tiers in order and returns the Decision for a Message.
// A Brand match returns Tier brand with an empty Verdict: the caller pairs it
// with Candidate and a Judge. Action, RunID, DryRun and Judge belong to the
// run layer and are left zero here.
func (e *Engine) Evaluate(m Message) Decision {
	tier, rule, brand := e.match(m)
	d := Decision{
		Account:   m.Account,
		MessageID: m.ID,
		Tier:      tier,
		Rule:      rule,
	}
	switch tier {
	case TierProtect:
		d.Verdict = VerdictLeave
	case TierSender, TierKeyword:
		d.Verdict = VerdictMarkRead
	case TierBrand:
		d.Brand = brand.Name
		d.Verdict = "" // needs judge
	default:
		d.Verdict = VerdictLeave
	}
	return d
}

// Candidate returns the Message paired with the Brand Rule it matched, and
// false when the Message does not reach the Brand tier.
func (e *Engine) Candidate(m Message) (Candidate, bool) {
	tier, _, brand := e.match(m)
	if tier != TierBrand || brand == nil {
		return Candidate{}, false
	}
	return Candidate{Message: m, Brand: *brand}, true
}

// ApplyJudgement folds a Judge's Confidence and Reason into a Decision: the
// Verdict is mark-read when the Confidence is at or above the Threshold and
// leave below it.
func ApplyJudgement(d Decision, j Judgement, threshold float64) Decision {
	confidence := j.Confidence
	d.Confidence = &confidence
	d.Reason = j.Reason
	if confidence >= threshold {
		d.Verdict = VerdictMarkRead
	} else {
		d.Verdict = VerdictLeave
	}
	return d
}
