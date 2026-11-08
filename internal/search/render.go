package search

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/hedche/email-triage-bot/internal/command"
)

const timeLayout = "2006-01-02 15:04"

// WriteTable prints a search over the runs for a person.
func WriteTable(w io.Writer, res *Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DATE (UTC)\tREF\tACCOUNT\tSENDER\tSUBJECT\tVERDICT\tNOTES\tRULE OR AI REASON")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.RunTime.Format(timeLayout), orDash(h.Message.Ref), cell(h.Message.Account, 30),
			cell(sender(h), 32), cell(h.Message.Subject, 48), orDash(string(h.Decision.Verdict)),
			orDash(strings.Join(h.Notes, ", ")), cell(ruleOrReason(h), 56))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return footer(w, res.Query, len(res.Hits), res.Total, res.Objects, res.Skipped)
}

// WriteRecord prints a --ref search: the whole record, every other run that saw
// the email, and how to open it in Gmail.
func WriteRecord(w io.Writer, res *Result) error {
	if len(res.Hits) == 0 {
		fmt.Fprintf(w, "No record with Ref %s between %s and %s. A backfilled record may have no Ref: search by --from or --subject instead.\n",
			res.Query.Ref, res.Query.Since.Format(dayLayout), res.Query.Until.Format(dayLayout))
		return skippedLines(w, res.Skipped)
	}
	h := res.Hits[0]
	m, d := h.Message, h.Decision
	field := func(name, value string) { fmt.Fprintf(w, "%-14s%s\n", name+":", value) }
	field("Ref", m.Ref)
	field("Account", clean(m.Account))
	field("Date (UTC)", h.RunTime.Format(timeLayout))
	field("Run", d.RunID)
	field("Object", h.Key)
	if m.Received.IsZero() {
		field("Received", "(not recorded)")
	} else {
		field("Received", m.Received.UTC().Format(timeLayout))
	}
	field("From", clean(strings.TrimSpace(m.From.Name+" <"+m.From.Email+">")))
	field("Subject", clean(m.Subject))
	switch {
	case m.Snippet != "":
		field("Snippet", clean(m.Snippet))
	case h.Backfilled:
		field("Snippet", "(not recorded: backfilled from logs)")
	default:
		field("Snippet", "(empty)")
	}
	field("Verdict", orDash(string(d.Verdict)))
	field("Action", orDash(string(d.Action)))
	field("Tier", orDash(string(d.Tier)))
	field("Rule", orDash(clean(d.Rule)))
	if d.Brand != "" {
		field("Brand", clean(d.Brand))
	}
	if d.Confidence != nil {
		field("Confidence", strconv.FormatFloat(*d.Confidence, 'f', 2, 64))
	}
	if d.Reason != "" {
		field("Reason", clean(d.Reason))
	}
	if d.Judge != "" {
		field("Judge", clean(d.Judge))
	}
	if d.JudgeError != "" {
		field("Judge error", clean(d.JudgeError))
	}
	if d.DryRun {
		field("Dry run", "yes: nothing was changed in the mailbox")
	}
	if h.Backfilled {
		field("Backfilled", "yes, from "+orDash(clean(h.Source))+": rebuilt from logs, not written by the run")
	}
	if gmail := GmailSearch(h.Record); gmail != "" {
		field("Gmail search", clean(gmail))
	} else {
		field("Gmail search", "(no Message-ID recorded)")
	}

	fmt.Fprintf(w, "\nOther runs that saw this email between %s and %s: %d\n",
		res.Query.Since.Format(dayLayout), res.Query.Until.Format(dayLayout), len(res.Others))
	if len(res.Others) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DATE (UTC)\tRUN\tVERDICT\tACTION\tNOTES\tRULE OR AI REASON")
		for _, o := range res.Others {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", o.RunTime.Format(timeLayout), orDash(o.Decision.RunID),
				orDash(string(o.Decision.Verdict)), orDash(string(o.Decision.Action)),
				orDash(strings.Join(o.Notes, ", ")), cell(ruleOrReason(o), 56))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return skippedLines(w, res.Skipped)
}

// WriteRecordsJSON prints one JSON object per record, ending with a
// {"warning"} line for each object that could not be read.
func WriteRecordsJSON(w io.Writer, res *Result) error {
	enc := newEncoder(w)
	for _, group := range []struct {
		hits []Hit
		role string
	}{{res.Hits, "record"}, {res.Others, "other_run"}} {
		for _, h := range group.hits {
			if res.Query.Ref != "" {
				h.Role, h.Gmail = group.role, GmailSearch(h.Record)
			}
			if err := enc.Encode(h); err != nil {
				return err
			}
		}
	}
	return warningsJSON(enc, res.Skipped)
}

// WriteCommandsTable prints a search over the command events for a person.
func WriteCommandsTable(w io.Writer, res *CommandResult) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DATE (UTC)\tTYPE\tACCOUNT\tFROM\tPROPOSAL\tCHANGES\tNOTE")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.At.UTC().Format(timeLayout), h.Type, cell(h.Account, 30),
			cell(h.From, 32), orDash(h.ProposalID), cell(changes(h.Event), 56), cell(h.Instruction, 48))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return footer(w, res.Query, len(res.Hits), res.Total, res.Objects, res.Skipped)
}

// WriteCommandsJSON prints one JSON object per event, ending with a {"warning"}
// line for each object that could not be read.
func WriteCommandsJSON(w io.Writer, res *CommandResult) error {
	enc := newEncoder(w)
	for _, h := range res.Hits {
		if err := enc.Encode(h); err != nil {
			return err
		}
	}
	return warningsJSON(enc, res.Skipped)
}

func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func warningsJSON(enc *json.Encoder, skipped []string) error {
	for _, s := range skipped {
		if err := enc.Encode(map[string]string{"warning": "skipped " + s}); err != nil {
			return err
		}
	}
	return nil
}

func footer(w io.Writer, q Query, shown, total, objects int, skipped []string) error {
	fmt.Fprintf(w, "\nShowing %d of %d matches, newest first. Searched %d objects from %s to %s (UTC, by run date).\n",
		shown, total, objects, q.Since.Format(dayLayout), q.Until.Format(dayLayout))
	return skippedLines(w, skipped)
}

func skippedLines(w io.Writer, skipped []string) error {
	for _, s := range skipped {
		if _, err := fmt.Fprintf(w, "WARNING: results are incomplete, skipped %s\n", s); err != nil {
			return err
		}
	}
	return nil
}

func sender(h Hit) string {
	if h.Message.From.Email != "" {
		return h.Message.From.Email
	}
	return h.Message.From.Name
}

func ruleOrReason(h Hit) string {
	switch d := h.Decision; {
	case d.Reason != "":
		return d.Reason
	case d.Rule != "":
		return d.Rule
	case d.JudgeError != "":
		return "judge error: " + d.JudgeError
	}
	return ""
}

func changes(e command.Event) string {
	var parts []string
	for _, c := range e.Add {
		part := string(c.Op)
		if c.Value != "" {
			part += ":" + c.Value
		} else if c.Brand != "" {
			part += ":" + c.Brand
		}
		parts = append(parts, part)
	}
	for _, id := range e.Remove {
		parts = append(parts, "remove:"+id)
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cell makes archived text safe for one table cell, at most n characters wide.
func cell(s string, n int) string {
	s = clean(s)
	if s == "" {
		return "-"
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// clean strips what a sender could use to disturb a terminal, since subjects
// and snippets are written by whoever sent the mail: control characters, escape
// sequences and invisible formatting such as bidi overrides.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
