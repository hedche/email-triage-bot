package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/command"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

const (
	refA = "aaaaaaaaaaaa"
	refB = "bbbbbbbbbbbb"
)

// bucket is an in-memory archive.Reader that records which prefixes it was
// asked to list.
type bucket struct {
	objects map[string][]byte
	listed  []string
}

func (b *bucket) List(_ context.Context, prefix string) ([]string, error) {
	b.listed = append(b.listed, prefix)
	var keys []string
	for k := range b.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (b *bucket) Get(_ context.Context, key string) ([]byte, error) {
	if body, ok := b.objects[key]; ok {
		return body, nil
	}
	return nil, archive.ErrNotFound
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func window(since, until string) Query {
	return Query{Since: day(since), Until: day(until), Limit: 50}
}

func rec(ref, from, subject string, verdict triage.Verdict, tier triage.Tier, rule string) decisionlog.Record {
	return decisionlog.Record{
		Decision: triage.Decision{Verdict: verdict, Tier: tier, Rule: rule, Action: "marked-read"},
		Message: decisionlog.MessageSummary{
			Ref: ref, RFCMessageID: "<" + ref + "@mail.example.com>", ID: "gm-" + ref, Account: "owner@example.org",
			From: triage.Address{Name: "Sender", Email: from}, Subject: subject, Snippet: "preview",
			Received: day("2026-11-01"),
		},
	}
}

func gz(t *testing.T, records ...decisionlog.Record) []byte {
	t.Helper()
	body, err := archive.GzipJSONL(records)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDayPrefixesCoverOnlyTheWindowNewestFirst(t *testing.T) {
	got := DayPrefixes("runs", day("2026-10-30"), day("2026-11-02"))
	want := "runs/2026/11/02/ runs/2026/11/01/ runs/2026/10/31/ runs/2026/10/30/"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v", got)
	}
	if got := DayPrefixes("commands", day("2026-11-02"), day("2026-11-02")); len(got) != 1 || got[0] != "commands/2026/11/02/" {
		t.Errorf("single day: %v", got)
	}
}

func TestSetWindowDefaultsToThirtyDays(t *testing.T) {
	var q Query
	if err := q.SetWindow("", "", time.Date(2026, 11, 8, 13, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !q.Until.Equal(day("2026-11-08")) || !q.Since.Equal(day("2026-10-09")) {
		t.Errorf("window %v to %v", q.Since, q.Until)
	}
	if err := q.SetWindow("", "2026-11-01", time.Now()); err != nil || !q.Since.Equal(day("2026-10-02")) {
		t.Errorf("until only: since %v err %v", q.Since, err)
	}
	if err := q.SetWindow("2026-11-02", "2026-11-01", time.Now()); err == nil {
		t.Error("since after until should fail")
	}
	if err := q.SetWindow("yesterday", "", time.Now()); err == nil {
		t.Error("a bad date should fail")
	}
}

func TestRecordsListsOnlyWindowPrefixesAndIgnoresSummaries(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/02/20261102T050000Z-aa.jsonl.gz":     gz(t, rec(refA, "a@example.com", "in", triage.VerdictLeave, triage.TierNone, "")),
		"runs/2026/11/02/20261102T050000Z-aa.summary.json": []byte("{not a log"),
		"runs/2026/11/03/20261103T050000Z-bb.jsonl.gz":     gz(t, rec(refB, "b@example.com", "out", triage.VerdictLeave, triage.TierNone, "")),
	}}
	res, err := Records(context.Background(), b, window("2026-11-01", "2026-11-02"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.listed, " ") != "runs/2026/11/02/ runs/2026/11/01/" {
		t.Errorf("listed %v", b.listed)
	}
	if len(res.Hits) != 1 || res.Hits[0].Message.Subject != "in" || len(res.Skipped) != 0 {
		t.Errorf("hits %+v skipped %v", res.Hits, res.Skipped)
	}
}

func TestFilters(t *testing.T) {
	r := rec(refA, "news@mail.example.com", "Your Order Shipped", triage.VerdictMarkRead, triage.TierSender, "domain:example.com")
	brand := rec(refB, "x@shop.example.net", "Sale", triage.VerdictLeave, triage.TierBrand, "")
	brand.Decision.Brand = "Example Shop"
	cases := []struct {
		name  string
		q     Query
		rec   decisionlog.Record
		match bool
	}{
		{"no filters", Query{}, r, true},
		{"ref", Query{Ref: refA}, r, true},
		{"other ref", Query{Ref: refB}, r, false},
		{"exact address", Query{From: "news@mail.example.com"}, r, true},
		{"other address", Query{From: "other@mail.example.com"}, r, false},
		{"domain", Query{From: "mail.example.com"}, r, true},
		{"parent domain", Query{From: "example.com"}, r, true},
		{"at-domain", Query{From: "@example.com"}, r, true},
		{"not a label boundary", Query{From: "ample.com"}, r, false},
		{"subject any case", Query{Subject: "order ship"}, r, true},
		{"subject miss", Query{Subject: "refund"}, r, false},
		{"account", Query{Account: "owner@example.org"}, r, true},
		{"other account", Query{Account: "else@example.org"}, r, false},
		{"verdict", Query{Verdict: "mark-read"}, r, true},
		{"other verdict", Query{Verdict: "leave"}, r, false},
		{"tier", Query{Tier: "sender"}, r, true},
		{"other tier", Query{Tier: "keyword"}, r, false},
		{"rule", Query{Rule: "domain:example"}, r, true},
		{"rule miss", Query{Rule: "keyword:"}, r, false},
		{"rule finds a brand", Query{Rule: "example shop"}, brand, true},
		{"combined", Query{From: "example.com", Verdict: "mark-read", Subject: "order"}, r, true},
		{"combined one miss", Query{From: "example.com", Verdict: "leave", Subject: "order"}, r, false},
	}
	for _, c := range cases {
		if got := c.q.matches(c.rec); got != c.match {
			t.Errorf("%s: matches = %v", c.name, got)
		}
	}
}

func TestNormaliseRejectsValuesThatCouldNeverMatch(t *testing.T) {
	for _, q := range []Query{
		{Limit: 1, Ref: "short"},
		{Limit: 1, Verdict: "archive"},
		{Limit: 1, Tier: "gold"},
		{Limit: 0},
	} {
		if err := q.Normalise(); err == nil {
			t.Errorf("%+v should be rejected", q)
		}
	}
	q := Query{Limit: 1, Ref: " AAAAAAAAAAAA ", Verdict: "Leave"}
	if err := q.Normalise(); err != nil || q.Ref != refA || q.Verdict != "leave" {
		t.Errorf("normalised %+v err %v", q, err)
	}
}

func TestRecordsAreNewestFirstAndLimited(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": gz(t, rec(refA, "a@example.com", "old", triage.VerdictLeave, triage.TierNone, "")),
		"runs/2026/11/03/20261103T050000Z-bb.jsonl.gz": gz(t, rec(refB, "b@example.com", "new", triage.VerdictLeave, triage.TierNone, "")),
	}}
	q := window("2026-11-01", "2026-11-03")
	q.Limit = 1
	res, err := Records(context.Background(), b, q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || len(res.Hits) != 1 || res.Hits[0].Message.Subject != "new" {
		t.Errorf("total %d hits %+v", res.Total, res.Hits)
	}
}

func TestRefShowsEveryOtherRunAndTheGmailSearch(t *testing.T) {
	first := rec(refA, "a@example.com", "s", triage.VerdictLeave, triage.TierNone, "")
	second := rec(refA, "a@example.com", "s", triage.VerdictMarkRead, triage.TierSender, "domain:example.com")
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": gz(t, first, rec(refB, "b@example.com", "x", triage.VerdictLeave, triage.TierNone, "")),
		"runs/2026/11/02/20261102T050000Z-bb.jsonl.gz": gz(t, second),
	}}
	q := window("2026-11-01", "2026-11-02")
	q.Ref, q.Verdict, q.Limit = refA, "leave", 1 // the older run matches; the other is still shown
	res, err := Records(context.Background(), b, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Decision.Verdict != triage.VerdictLeave || len(res.Others) != 1 || res.Others[0].Decision.Verdict != triage.VerdictMarkRead {
		t.Fatalf("hits %+v others %+v", res.Hits, res.Others)
	}
	var out bytes.Buffer
	if err := WriteRecord(&out, res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rfc822msgid:" + refA + "@mail.example.com", "Other runs that saw this email", "2026-11-02 05:00"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "<"+refA) {
		t.Errorf("Gmail search should drop the angle brackets:\n%s", out.String())
	}
}

func backfilled(ref string) decisionlog.Record {
	r := rec(ref, "a@example.com", "from the logs", triage.VerdictLeave, triage.TierNone, "")
	r.Backfilled, r.Source = true, "saved-logs"
	r.Message.Snippet, r.Message.Received = "", time.Time{}
	if ref == "" {
		r.Message.RFCMessageID = ""
	}
	return r
}

func TestBackfilledRecordWithNoRefIsFlaggedEverywhere(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": gz(t, backfilled("")),
	}}
	res, err := Records(context.Background(), b, window("2026-11-01", "2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits %+v", res.Hits)
	}
	if got := strings.Join(res.Hits[0].Notes, ","); got != "backfilled,no ref,no snippet" {
		t.Errorf("notes %q", got)
	}

	var table bytes.Buffer
	if err := WriteTable(&table, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "backfilled, no ref, no snippet") || !strings.Contains(table.String(), "2026-11-01 05:00") {
		t.Errorf("table:\n%s", table.String())
	}

	var out bytes.Buffer
	if err := WriteRecordsJSON(&out, res); err != nil {
		t.Fatal(err)
	}
	var line struct {
		Backfilled bool     `json:"backfilled"`
		Notes      []string `json:"notes"`
	}
	if err := json.Unmarshal(out.Bytes(), &line); err != nil || !line.Backfilled || len(line.Notes) != 3 {
		t.Errorf("json %s err %v", out.String(), err)
	}
}

func TestEmptyRefsAreNeverGroupedAndRefSearchExplainsAMiss(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": gz(t, backfilled(""), backfilled("")),
	}}
	q := window("2026-11-01", "2026-11-01")
	q.Ref = refA
	res, err := Records(context.Background(), b, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || len(res.Others) != 0 {
		t.Fatalf("hits %+v others %+v", res.Hits, res.Others)
	}
	var out bytes.Buffer
	if err := WriteRecord(&out, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "backfilled record may have no Ref") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestBackfilledRecordWithARefSaysWhatIsMissing(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": gz(t, backfilled(refA)),
	}}
	q := window("2026-11-01", "2026-11-01")
	q.Ref = refA
	res, err := Records(context.Background(), b, q)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteRecord(&out, res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"not recorded: backfilled", "yes, from saved-logs", "Received:     (not recorded)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestDryRunIsFlagged(t *testing.T) {
	r := rec(refA, "a@example.com", "s", triage.VerdictMarkRead, triage.TierSender, "domain:example.com")
	r.Decision.DryRun = true
	if got := strings.Join(notes(r), ","); got != "dry run" {
		t.Errorf("notes %q", got)
	}
}

func TestABadObjectIsSkippedAndReported(t *testing.T) {
	b := &bucket{objects: map[string][]byte{
		"runs/2026/11/01/20261101T050000Z-aa.jsonl.gz": []byte("not gzip"),
		"runs/2026/11/02/20261102T050000Z-bb.jsonl.gz": gz(t, rec(refB, "b@example.com", "good", triage.VerdictLeave, triage.TierNone, "")),
	}}
	res, err := Records(context.Background(), b, window("2026-11-01", "2026-11-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "20261101T050000Z-aa") {
		t.Fatalf("hits %+v skipped %v", res.Hits, res.Skipped)
	}
	var out bytes.Buffer
	if err := WriteTable(&out, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WARNING: results are incomplete") {
		t.Errorf("output:\n%s", out.String())
	}
}

type failingBucket struct{ bucket }

func (f *failingBucket) List(context.Context, string) ([]string, error) {
	return nil, errors.New("boom")
}

func TestAListingFailureStopsTheSearch(t *testing.T) {
	if _, err := Records(context.Background(), &failingBucket{}, window("2026-11-01", "2026-11-01")); err == nil {
		t.Error("want an error")
	}
}

func TestTableNeutralisesWhatASenderControls(t *testing.T) {
	res := &Result{Query: window("2026-11-01", "2026-11-01"), Hits: []Hit{{
		RunTime: day("2026-11-01"),
		Record:  rec(refA, "a@example.com", "evil\x1b[2J\ttab\nline\u202e", triage.VerdictLeave, triage.TierNone, ""),
	}}, Total: 1}
	var out bytes.Buffer
	if err := WriteTable(&out, res); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.ContainsAny(s, "\x1b\u202e") || !strings.Contains(s, "evil [2J tab line") {
		t.Errorf("output %q", s)
	}
}

func TestCommandsSearch(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 2, h, 0, 0, 0, time.UTC) }
	events := []command.Event{
		{At: at(9), Type: "ignored", Account: "owner@example.org", From: "Someone <a@example.com>"},
		{At: at(10), Type: "applied", Account: "owner@example.org", Instruction: "stop these receipts", Add: []triage.Change{{Op: triage.OpReadDomain, Value: "shop.example.net"}}},
		{At: at(11), Type: "proposed", Account: "other@example.org", Add: []triage.Change{{Op: triage.OpReadKeyword, Value: "weekly digest"}}},
	}
	body, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	b := &bucket{objects: map[string][]byte{
		"commands/2026/11/02/090000-aa.json": body,
		"commands/2026/11/05/090000-bb.json": []byte("[]"),
	}}
	base := window("2026-11-01", "2026-11-02")

	run := func(q Query) []CommandHit {
		t.Helper()
		res, err := Commands(context.Background(), b, q)
		if err != nil {
			t.Fatal(err)
		}
		return res.Hits
	}
	if got := run(base); len(got) != 3 || got[0].Type != "proposed" {
		t.Errorf("all: %+v", got)
	}
	for _, p := range b.listed {
		if !strings.HasPrefix(p, "commands/") || strings.Contains(p, "11/05") {
			t.Errorf("listed %q", p)
		}
	}
	q := base
	q.From = "a@example.com"
	if got := run(q); len(got) != 1 || got[0].Type != "ignored" {
		t.Errorf("from address: %+v", got)
	}
	q = base
	q.From = "example.net"
	if got := run(q); len(got) != 1 || got[0].Type != "applied" {
		t.Errorf("from a domain named by a Change: %+v", got)
	}
	q = base
	q.Subject = "receipts"
	if got := run(q); len(got) != 1 || got[0].Type != "applied" {
		t.Errorf("subject: %+v", got)
	}
	q = base
	q.Rule = "read_keyword"
	if got := run(q); len(got) != 1 || got[0].Type != "proposed" {
		t.Errorf("rule: %+v", got)
	}
	q = base
	q.Account = "other@example.org"
	if got := run(q); len(got) != 1 {
		t.Errorf("account: %+v", got)
	}
	if err := (Query{Verdict: "leave", Ref: refA}).CommandFiltersRejected(); err == nil || !strings.Contains(err.Error(), "--ref, --verdict") {
		t.Errorf("err = %v", err)
	}
}
