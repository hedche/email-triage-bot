package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/search"
)

// searchOptions is a parsed `search` command line.
type searchOptions struct {
	query    search.Query
	json     bool
	commands bool
}

// parseSearch reads the `search` flags. Every flag is checked here, before the
// bucket is touched.
func parseSearch(args []string, now time.Time, stderr io.Writer) (searchOptions, error) {
	var opts searchOptions
	q := &opts.query
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&q.Ref, "ref", "", "show one email: its full record and every other run that saw it")
	fs.StringVar(&q.From, "from", "", "sender address, or a domain (subdomains included)")
	fs.StringVar(&q.Subject, "subject", "", "case-insensitive substring of the subject")
	fs.StringVar(&q.Account, "account", "", "only this Account")
	fs.StringVar(&q.Verdict, "verdict", "", "mark-read or leave")
	fs.StringVar(&q.Tier, "tier", "", "protect, sender, keyword, brand or none")
	fs.StringVar(&q.Rule, "rule", "", "case-insensitive substring of the matched Rule or Brand")
	since := fs.String("since", "", "first day, YYYY-MM-DD UTC (default 30 days before --until)")
	until := fs.String("until", "", "last day, YYYY-MM-DD UTC (default today)")
	fs.IntVar(&q.Limit, "limit", 50, "most rows to show, newest first (--ref always shows every run)")
	fs.BoolVar(&opts.json, "json", false, "print JSON lines instead of a table")
	fs.BoolVar(&opts.commands, "commands", false, "search Email Command events instead of runs")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	// An empty value is usually an unset shell variable, and as "no filter" it
	// would quietly return everything.
	var empty error
	fs.Visit(func(f *flag.Flag) {
		if f.Value.String() == "" {
			empty = fmt.Errorf("--%s was given with no value", f.Name)
		}
	})
	if empty != nil {
		return opts, empty
	}
	if err := q.Normalise(); err != nil {
		return opts, err
	}
	if err := q.SetWindow(*since, *until, now); err != nil {
		return opts, err
	}
	if opts.commands {
		if err := q.CommandFiltersRejected(); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

// cmdSearch answers questions from the archive. It holds only a read-only view
// of the bucket and builds no Provider, Judge or Notifier.
func cmdSearch(args []string) int {
	opts, err := parseSearch(args, time.Now(), os.Stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "email-triage-bot: %v\n", err)
		}
		return 2
	}
	s, err := config.LoadForArchive()
	if err != nil {
		return fail(err)
	}
	reader := archive.NewReader(archive.Config{
		Host: s.ArchiveHost, Port: s.ArchivePort, Bucket: s.ArchiveBucket, Region: s.ArchiveRegion,
		AccessKey: s.ArchiveAccessKey, SecretKey: s.ArchiveSecretKey,
	})
	if reader == nil {
		return fail(errors.New("search needs the archive bucket to be configured (BUCKET_* and AWS_*)"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runSearch(ctx, reader, opts, os.Stdout)
}

// runSearch runs a parsed search and prints it. A search that had to skip an
// object still prints what it found, and exits 1 because that is incomplete.
func runSearch(ctx context.Context, r archive.Reader, opts searchOptions, w io.Writer) int {
	var skipped []string
	var err error
	if opts.commands {
		var res *search.CommandResult
		if res, err = search.Commands(ctx, r, opts.query); err != nil {
			return fail(err)
		}
		skipped = res.Skipped
		if opts.json {
			err = search.WriteCommandsJSON(w, res)
		} else {
			err = search.WriteCommandsTable(w, res)
		}
	} else {
		var res *search.Result
		if res, err = search.Records(ctx, r, opts.query); err != nil {
			return fail(err)
		}
		skipped = res.Skipped
		switch {
		case opts.json:
			err = search.WriteRecordsJSON(w, res)
		case opts.query.Ref != "":
			err = search.WriteRecord(w, res)
		default:
			err = search.WriteTable(w, res)
		}
	}
	if err != nil {
		return fail(err)
	}
	if len(skipped) > 0 {
		return 1
	}
	return 0
}
