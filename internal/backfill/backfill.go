// Package backfill rebuilds archive objects for Triage Runs that finished
// before the archive existed, from the bot's own structured log lines. A log
// line holds less than a Decision Log does (no snippet, a truncated subject),
// so every object written here is marked as backfilled and never replaces one
// that is already in the bucket.
package backfill

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/run"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// logLine is the union of the "decision" and "run summary" log lines.
type logLine struct {
	Time  time.Time `json:"time"`
	Msg   string    `json:"msg"`
	RunID string    `json:"run_id"`

	Account    string   `json:"account"`
	MessageID  string   `json:"message_id"`
	Tier       string   `json:"tier"`
	Rule       string   `json:"rule"`
	Brand      string   `json:"brand"`
	Reason     string   `json:"reason"`
	Verdict    string   `json:"verdict"`
	Action     string   `json:"action"`
	Judge      string   `json:"judge"`
	JudgeError string   `json:"judge_error"`
	DryRun     bool     `json:"dry_run"`
	From       string   `json:"from"`
	FromName   string   `json:"from_name"`
	Subject    string   `json:"subject"`
	Confidence *float64 `json:"confidence"`

	Scanned     int          `json:"scanned"`
	MarkedRead  int          `json:"marked_read"`
	Left        int          `json:"left"`
	Unmatched   int          `json:"unmatched"`
	Candidates  int          `json:"candidates"`
	JudgeCalls  int          `json:"judge_calls"`
	JudgeErrors int          `json:"judge_errors"`
	DurationMS  int64        `json:"duration_ms"`
	Accounts    []accountLog `json:"accounts"`
}

type accountLog struct {
	Account     string `json:"account"`
	Scanned     int    `json:"scanned"`
	MarkedRead  int    `json:"marked_read"`
	Left        int    `json:"left"`
	Unmatched   int    `json:"unmatched"`
	Candidates  int    `json:"candidates"`
	JudgeCalls  int    `json:"judge_calls"`
	JudgeErrors int    `json:"judge_errors"`
	Skipped     string `json:"skipped"`
}

// Run is what the logs say about one Triage Run.
type Run struct {
	ID        string
	decisions []logLine
	summary   *logLine
}

// Parse groups log lines by run. Lines that are not JSON, or are neither a
// decision nor a run summary, are ignored; a line seen twice counts once, so
// overlapping sources are safe to combine.
func Parse(r io.Reader) ([]*Run, error) {
	runs := map[string]*Run{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var l logLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil || l.RunID == "" {
			continue
		}
		if l.Msg != "decision" && l.Msg != "run summary" {
			continue
		}
		rn := runs[l.RunID]
		if rn == nil {
			rn = &Run{ID: l.RunID}
			runs[l.RunID] = rn
		}
		if l.Msg == "run summary" {
			if rn.summary == nil {
				line := l
				rn.summary = &line
			}
			continue
		}
		key := l.RunID + "\n" + l.Account + "\n" + l.MessageID
		if !seen[key] {
			seen[key] = true
			rn.decisions = append(rn.decisions, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}
	out := make([]*Run, 0, len(runs))
	for _, rn := range runs {
		out = append(out, rn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Options configures Execute.
type Options struct {
	// Before excludes any run that finished at or after this time.
	Before time.Time
	// Source names where the logs came from, recorded on every object.
	Source string
	// DryRun writes nothing, and reports and checks what is already there.
	DryRun bool
	Store  archive.ReadStore
	// Lookup returns a message's Message-ID header, or "" if it cannot be found.
	Lookup func(ctx context.Context, account, messageID string) (string, error)
	Log    *slog.Logger
}

// Result counts what Execute did, across every run it considered.
type Result struct {
	RunsWritten    int
	RecordsWritten int
	RecordsNoRef   int
	RunsExisting   int
	RunsTooLate    int
	RunsIncomplete int
	First, Last    time.Time
}

// Execute writes each complete run that has no object yet. A run is complete
// when it has a summary and exactly as many decisions as it scanned; anything
// else is reported and left alone, because a partial file would look whole.
func Execute(ctx context.Context, runs []*Run, opts Options) (Result, error) {
	var res Result
	for _, rn := range runs {
		if rn.summary == nil || len(rn.decisions) != rn.summary.Scanned {
			res.RunsIncomplete++
			opts.Log.Warn("run incomplete in the logs, skipped", "run_id", rn.ID,
				"decisions", len(rn.decisions), "has_summary", rn.summary != nil)
			continue
		}
		if !rn.summary.Time.Before(opts.Before) {
			res.RunsTooLate++
			continue
		}
		if err := executeRun(ctx, rn, opts, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func executeRun(ctx context.Context, rn *Run, opts Options, res *Result) error {
	at := rn.summary.Time
	recKey, sumKey := archive.RunKey(at, rn.ID), archive.RunSummaryKey(at, rn.ID)

	existing, err := opts.Store.Get(ctx, recKey)
	recExists := err == nil
	if err != nil && !errors.Is(err, archive.ErrNotFound) {
		return err
	}
	if recExists {
		res.RunsExisting++
		n, noRef, err := check(existing)
		if err != nil {
			return fmt.Errorf("run %s: existing object: %w", rn.ID, err)
		}
		opts.Log.Info("run already archived, left as it is", "run_id", rn.ID, "records", n,
			"without_ref", noRef, "scanned", rn.summary.Scanned, "counts_match", n == rn.summary.Scanned)
		return nil
	}

	records, noRef, err := buildRecords(ctx, rn, opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		opts.Log.Info("would write run", "run_id", rn.ID, "records", len(records), "without_ref", noRef)
		return nil
	}
	if _, err := opts.Store.Get(ctx, sumKey); err == nil {
		return fmt.Errorf("run %s: a summary exists without its records; refusing to touch either", rn.ID)
	} else if !errors.Is(err, archive.ErrNotFound) {
		return err
	}

	body, err := archive.GzipJSONL(records)
	if err != nil {
		return err
	}
	if err := opts.Store.Put(ctx, recKey, body, "application/gzip"); err != nil {
		return err
	}
	sum, err := summaryJSON(rn, opts.Source)
	if err != nil {
		return err
	}
	if err := opts.Store.Put(ctx, sumKey, sum, "application/json"); err != nil {
		return err
	}

	back, err := opts.Store.Get(ctx, recKey)
	if err != nil {
		return fmt.Errorf("run %s: read back: %w", rn.ID, err)
	}
	n, _, err := check(back)
	if err != nil {
		return fmt.Errorf("run %s: read back: %w", rn.ID, err)
	}
	if n != rn.summary.Scanned {
		return fmt.Errorf("run %s: read back %d records, run scanned %d", rn.ID, n, rn.summary.Scanned)
	}

	res.RunsWritten++
	res.RecordsWritten += len(records)
	res.RecordsNoRef += noRef
	if res.First.IsZero() || at.Before(res.First) {
		res.First = at
	}
	if at.After(res.Last) {
		res.Last = at
	}
	opts.Log.Info("run backfilled", "run_id", rn.ID, "records", len(records), "without_ref", noRef)
	return nil
}

func buildRecords(ctx context.Context, rn *Run, opts Options) ([]decisionlog.Record, int, error) {
	records := make([]decisionlog.Record, 0, len(rn.decisions))
	noRef := 0
	for _, l := range rn.decisions {
		rfc, err := opts.Lookup(ctx, l.Account, l.MessageID)
		if err != nil {
			return nil, 0, fmt.Errorf("run %s: %w", rn.ID, err)
		}
		msg := decisionlog.MessageSummary{
			ID:      l.MessageID,
			Account: l.Account,
			From:    triage.Address{Name: l.FromName, Email: l.From},
			Subject: l.Subject,
		}
		if rfc != "" {
			msg.RFCMessageID = rfc
			msg.Ref = triage.Message{Account: l.Account, RFCMessageID: rfc}.Ref()
		} else {
			noRef++
		}
		records = append(records, decisionlog.Record{
			Decision: triage.Decision{
				RunID:      l.RunID,
				Account:    l.Account,
				MessageID:  l.MessageID,
				Tier:       triage.Tier(l.Tier),
				Rule:       l.Rule,
				Brand:      l.Brand,
				Confidence: l.Confidence,
				Reason:     l.Reason,
				Verdict:    triage.Verdict(l.Verdict),
				Action:     triage.Action(l.Action),
				JudgeError: l.JudgeError,
				DryRun:     l.DryRun,
				Judge:      l.Judge,
			},
			Message:    msg,
			Backfilled: true,
			Source:     opts.Source,
		})
	}
	return records, noRef, nil
}

// summaryJSON renders the same shape a live run archives, plus the markers.
func summaryJSON(rn *Run, source string) ([]byte, error) {
	l := rn.summary
	sum := run.Summary{
		RunID:       rn.ID,
		DryRun:      l.DryRun,
		Duration:    time.Duration(l.DurationMS) * time.Millisecond,
		Scanned:     l.Scanned,
		MarkedRead:  l.MarkedRead,
		Left:        l.Left,
		Unmatched:   l.Unmatched,
		Candidates:  l.Candidates,
		JudgeCalls:  l.JudgeCalls,
		JudgeErrors: l.JudgeErrors,
	}
	for _, a := range l.Accounts {
		sum.Accounts = append(sum.Accounts, run.AccountSummary{
			Account: a.Account, Scanned: a.Scanned, MarkedRead: a.MarkedRead, Left: a.Left,
			Unmatched: a.Unmatched, Candidates: a.Candidates, JudgeCalls: a.JudgeCalls,
			JudgeErrors: a.JudgeErrors, Skipped: a.Skipped,
		})
	}
	return json.Marshal(struct {
		run.Summary
		Backfilled bool   `json:"backfilled"`
		Source     string `json:"source"`
	}{sum, true, source})
}

// check confirms an object gunzips and parses as a Decision Log, returning its
// record count and how many records have no Ref.
func check(body []byte) (int, int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	records, err := decisionlog.ReadFrom(zr)
	if err != nil {
		return 0, 0, err
	}
	noRef := 0
	for _, r := range records {
		if r.Message.Ref == "" {
			noRef++
		}
	}
	return len(records), noRef, nil
}
