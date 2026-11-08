// Package search answers questions about the archive: what the bot decided
// about an email, across every run that saw it, and what Email Commands did.
// It only lists and reads objects (ADR 0006); nothing here can write.
package search

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/command"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// DefaultWindowDays is how far back a search looks unless told otherwise.
const DefaultWindowDays = 30

const dayLayout = "2006-01-02"

var refPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Query selects what a search returns. Every filter left empty matches
// everything, and the ones that are set must all match.
type Query struct {
	Ref     string
	From    string
	Subject string
	Account string
	Verdict string
	Tier    string
	Rule    string
	// Since and Until are whole UTC days, both included. They are compared with
	// the date of the run or event, which is the date in the object's key.
	Since time.Time
	Until time.Time
	Limit int
}

// Normalise trims and lower-cases the filters and rejects values that could
// never match.
func (q *Query) Normalise() error {
	for _, f := range []*string{&q.Ref, &q.From, &q.Subject, &q.Account, &q.Verdict, &q.Tier, &q.Rule} {
		*f = strings.ToLower(strings.TrimSpace(*f))
	}
	if q.Ref != "" && !refPattern.MatchString(q.Ref) {
		return fmt.Errorf("--ref %q: a Ref is 12 hex characters", q.Ref)
	}
	switch triage.Verdict(q.Verdict) {
	case "", triage.VerdictMarkRead, triage.VerdictLeave:
	default:
		return fmt.Errorf("--verdict %q: must be %s or %s", q.Verdict, triage.VerdictMarkRead, triage.VerdictLeave)
	}
	switch triage.Tier(q.Tier) {
	case "", triage.TierProtect, triage.TierSender, triage.TierKeyword, triage.TierBrand, triage.TierNone:
	default:
		return fmt.Errorf("--tier %q: must be protect, sender, keyword, brand or none", q.Tier)
	}
	if q.Limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	return nil
}

// SetWindow parses --since and --until, either of which may be empty. Until
// defaults to today and Since to DefaultWindowDays before Until.
func (q *Query) SetWindow(since, until string, now time.Time) error {
	q.Until = now.UTC().Truncate(24 * time.Hour)
	if until != "" {
		t, err := time.Parse(dayLayout, until)
		if err != nil {
			return fmt.Errorf("--until %q: use YYYY-MM-DD", until)
		}
		q.Until = t
	}
	q.Since = q.Until.AddDate(0, 0, -DefaultWindowDays)
	if since != "" {
		t, err := time.Parse(dayLayout, since)
		if err != nil {
			return fmt.Errorf("--since %q: use YYYY-MM-DD", since)
		}
		q.Since = t
	}
	if q.Since.After(q.Until) {
		return fmt.Errorf("--since %s is after --until %s", q.Since.Format(dayLayout), q.Until.Format(dayLayout))
	}
	return nil
}

// DayPrefixes returns the key prefix of every UTC day in the window, newest
// first, so a search lists exactly the days it was asked about.
func DayPrefixes(root string, since, until time.Time) []string {
	var prefixes []string
	for d := until.UTC(); !d.Before(since.UTC()); d = d.AddDate(0, 0, -1) {
		prefixes = append(prefixes, root+"/"+d.Format("2006/01/02")+"/")
	}
	return prefixes
}

// Hit is one Decision Log record found in the archive.
type Hit struct {
	Key     string    `json:"key"`
	RunTime time.Time `json:"run_time"`
	Role    string    `json:"role,omitempty"`  // with --ref: "record" or "other_run"
	Gmail   string    `json:"gmail,omitempty"` // with --ref: the Gmail search string
	Notes   []string  `json:"notes,omitempty"`
	decisionlog.Record
}

// Result is what a search over the runs found.
type Result struct {
	Query   Query
	Hits    []Hit    // the newest Limit matches; with Query.Ref, just the newest match
	Others  []Hit    // with Query.Ref: every other record of that email in the window
	Total   int      // matches before Limit was applied
	Objects int      // objects read
	Skipped []string // objects that could not be decoded, with why
}

// Records searches the Decision Logs of every run in the window.
func Records(ctx context.Context, r archive.Reader, q Query) (*Result, error) {
	res := &Result{Query: q}
	var hits, sameRef []Hit
	err := eachObject(ctx, r, "runs", ".jsonl.gz", q, &res.Objects, &res.Skipped, func(key string, body []byte) error {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer zr.Close()
		records, err := decisionlog.ReadFrom(zr)
		if err != nil {
			return err
		}
		runID := strings.TrimSuffix(path.Base(key), ".jsonl.gz")
		for _, rec := range records {
			h := Hit{Key: key, RunTime: runTime(key, runID), Notes: notes(rec), Record: rec}
			if q.Ref != "" && rec.Message.Ref == q.Ref {
				sameRef = append(sameRef, h)
			}
			if q.matches(rec) {
				hits = append(hits, h)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortHits(hits)
	res.Total = len(hits)
	if q.Ref == "" {
		res.Hits = hits[:min(q.Limit, len(hits))]
		return res, nil
	}
	if len(hits) == 0 {
		return res, nil
	}
	// A Ref names one email, so the answer is one record and every other run
	// that saw it, whichever of those the other filters would have kept.
	res.Hits = hits[:1]
	sortHits(sameRef)
	for _, h := range sameRef {
		if h.Key != res.Hits[0].Key {
			res.Others = append(res.Others, h)
		}
	}
	return res, nil
}

func sortHits(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].RunTime.Equal(hits[j].RunTime) {
			return hits[i].RunTime.After(hits[j].RunTime)
		}
		if !hits[i].Message.Received.Equal(hits[j].Message.Received) {
			return hits[i].Message.Received.After(hits[j].Message.Received)
		}
		return hits[i].Key > hits[j].Key
	})
}

// eachObject calls fn with every object in the window under root whose key
// ends in suffix. A listing or fetch failure stops the search; an object that
// fn cannot decode is reported in skipped and the search carries on, because
// the archive keeps files forever and one bad file must not block every search
// that spans it.
func eachObject(ctx context.Context, r archive.Reader, root, suffix string, q Query, objects *int, skipped *[]string, fn func(key string, body []byte) error) error {
	for _, prefix := range DayPrefixes(root, q.Since, q.Until) {
		keys, err := r.List(ctx, prefix)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if !strings.HasSuffix(key, suffix) {
				continue
			}
			body, err := r.Get(ctx, key)
			if errors.Is(err, archive.ErrNotFound) {
				*skipped = append(*skipped, key+": listed but no longer there")
				continue
			}
			if err != nil {
				return err
			}
			*objects++
			if err := fn(key, body); err != nil {
				*skipped = append(*skipped, key+": "+err.Error())
			}
		}
	}
	return nil
}

// runTime is when a run started, from its ID, or the day in its key for a run
// whose ID is not in the usual form.
func runTime(key, runID string) time.Time {
	if len(runID) >= 16 {
		if t, err := time.Parse("20060102T150405Z", runID[:16]); err == nil {
			return t
		}
	}
	return keyDay(key)
}

func keyDay(key string) time.Time {
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		return time.Time{}
	}
	t, _ := time.Parse("2006/01/02", strings.Join(parts[1:4], "/"))
	return t
}

// notes flags what a reader must know before trusting a record.
func notes(rec decisionlog.Record) []string {
	var out []string
	if rec.Backfilled {
		out = append(out, "backfilled")
	}
	if rec.Message.Ref == "" {
		out = append(out, "no ref")
	}
	if rec.Backfilled && rec.Message.Snippet == "" {
		out = append(out, "no snippet")
	}
	if rec.Decision.DryRun {
		out = append(out, "dry run")
	}
	return out
}

// GmailSearch is the string that opens a Message in Gmail, or "" when the
// record holds no Message-ID.
func GmailSearch(rec decisionlog.Record) string {
	id := strings.Trim(strings.TrimSpace(rec.Message.RFCMessageID), "<>")
	if id == "" {
		return ""
	}
	return "rfc822msgid:" + id
}

func (q Query) matches(rec decisionlog.Record) bool {
	m, d := rec.Message, rec.Decision
	switch {
	case q.Ref != "" && m.Ref != q.Ref,
		q.From != "" && !fromMatches(q.From, m.From.Email),
		q.Subject != "" && !containsFold(m.Subject, q.Subject),
		q.Account != "" && !strings.EqualFold(strings.TrimSpace(m.Account), q.Account),
		q.Verdict != "" && string(d.Verdict) != q.Verdict,
		q.Tier != "" && string(d.Tier) != q.Tier,
		q.Rule != "" && !containsFold(d.Rule, q.Rule) && !containsFold(d.Brand, q.Rule):
		return false
	}
	return true
}

func containsFold(s, lowerNeedle string) bool {
	return strings.Contains(strings.ToLower(s), lowerNeedle)
}

// fromMatches reports whether address is the one asked for or, when want is a
// domain, on that domain or one of its subdomains, as a Sender Rule matches.
func fromMatches(want, address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	if open := strings.LastIndexByte(address, '<'); open >= 0 {
		address = strings.TrimSuffix(address[open+1:], ">")
	}
	if address == "" {
		return false
	}
	if strings.IndexByte(want, '@') > 0 {
		return address == want
	}
	want = strings.TrimPrefix(want, "@")
	domain := address[strings.LastIndexByte(address, '@')+1:]
	return domain == want || strings.HasSuffix(domain, "."+want)
}

// CommandHit is one Email Command event found in the archive.
type CommandHit struct {
	Key string `json:"key"`
	command.Event
}

// CommandResult is what a search over the command events found.
type CommandResult struct {
	Query   Query
	Hits    []CommandHit
	Total   int
	Objects int
	Skipped []string
}

// CommandFiltersRejected names the filters that mean nothing for a command
// event: it has no Ref, Verdict or Tier.
func (q Query) CommandFiltersRejected() error {
	var bad []string
	if q.Ref != "" {
		bad = append(bad, "--ref")
	}
	if q.Verdict != "" {
		bad = append(bad, "--verdict")
	}
	if q.Tier != "" {
		bad = append(bad, "--tier")
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s cannot be used with --commands", strings.Join(bad, ", "))
	}
	return nil
}

// Commands searches the Email Command events in the window.
func Commands(ctx context.Context, r archive.Reader, q Query) (*CommandResult, error) {
	res := &CommandResult{Query: q}
	var hits []CommandHit
	err := eachObject(ctx, r, "commands", ".json", q, &res.Objects, &res.Skipped, func(key string, body []byte) error {
		var events []command.Event
		if err := json.Unmarshal(body, &events); err != nil {
			return err
		}
		for _, e := range events {
			if q.matchesEvent(e) {
				hits = append(hits, CommandHit{Key: key, Event: e})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].At.Equal(hits[j].At) {
			return hits[i].At.After(hits[j].At)
		}
		return hits[i].Key > hits[j].Key
	})
	res.Total = len(hits)
	res.Hits = hits[:min(q.Limit, len(hits))]
	return res, nil
}

func (q Query) matchesEvent(e command.Event) bool {
	if q.Account != "" && !strings.EqualFold(strings.TrimSpace(e.Account), q.Account) {
		return false
	}
	// Only an ignored event records who sent it; the rest name senders through
	// the Rules they add.
	if q.From != "" && !fromMatches(q.From, e.From) && !anyChange(e, func(c triage.Change) bool {
		return isAddressOp(c.Op) && fromMatches(q.From, senderValue(c))
	}) {
		return false
	}
	if q.Subject != "" && !containsFold(e.Instruction, q.Subject) && !anyChange(e, func(c triage.Change) bool {
		return containsFold(c.Subject, q.Subject) || (isKeywordOp(c.Op) && containsFold(c.Value, q.Subject))
	}) {
		return false
	}
	if q.Rule != "" && !anyChange(e, func(c triage.Change) bool {
		return containsFold(string(c.Op)+":"+c.Value, q.Rule) || containsFold(c.Brand, q.Rule)
	}) {
		return false
	}
	return true
}

func anyChange(e command.Event, ok func(triage.Change) bool) bool {
	for _, c := range e.Add {
		if ok(c) {
			return true
		}
	}
	return false
}

func isAddressOp(op triage.Op) bool {
	switch op {
	case triage.OpProtectSender, triage.OpProtectDomain, triage.OpReadSender, triage.OpReadDomain:
		return true
	}
	return false
}

func isKeywordOp(op triage.Op) bool {
	return op == triage.OpProtectKeyword || op == triage.OpReadKeyword
}

// senderValue gives a Change's address or domain the shape fromMatches expects:
// a domain Change has no local part, so it is matched as an address on it.
func senderValue(c triage.Change) string {
	if strings.Contains(c.Value, "@") {
		return c.Value
	}
	return "@" + c.Value
}
