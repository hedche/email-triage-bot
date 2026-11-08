package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/backfill"
	"github.com/hedche/email-triage-bot/internal/config"
)

// messageIDReader is the one Gmail read a backfill needs.
type messageIDReader interface {
	RFCMessageID(ctx context.Context, id string) (string, error)
}

// cmdBackfill rebuilds archive objects for past runs from saved log lines. It
// reads Message-IDs from the mailbox and changes nothing there, and it never
// replaces an object already in the bucket.
func cmdBackfill(args []string) int {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	logsPath := fs.String("logs", "", "comma-separated files of the bot's JSON log lines")
	source := fs.String("source", "", "where the logs came from, recorded on every object")
	before := fs.String("before", "", "only runs finished before this RFC 3339 time")
	dryRun := fs.Bool("dry-run", true, "report and check, write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *logsPath == "" || *source == "" || *before == "" {
		fs.Usage()
		return fail(errors.New("backfill needs --logs, --source and --before"))
	}
	cutoff, err := time.Parse(time.RFC3339, *before)
	if err != nil {
		return fail(err)
	}

	settings, err := config.Load()
	if err != nil {
		return fail(err)
	}
	log := newLogger(os.Stdout, settings.LogFormat, settings.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, ok := buildArchive(settings).(archive.ReadStore)
	if !ok {
		return fail(errors.New("backfill needs the archive bucket to be configured"))
	}
	accounts, err := config.LoadAccounts(settings.AccountsFile)
	if err != nil {
		return fail(err)
	}
	providers, err := buildProviders(ctx, settings, accounts)
	if err != nil {
		return fail(err)
	}
	readers := make(map[string]messageIDReader, len(providers))
	for _, p := range providers {
		if r, ok := p.(messageIDReader); ok {
			readers[strings.ToLower(p.Account())] = r
		}
	}

	var readersOfLogs []io.Reader
	for _, path := range strings.Split(*logsPath, ",") {
		f, err := os.Open(path)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		readersOfLogs = append(readersOfLogs, f)
	}
	runs, err := backfill.Parse(io.MultiReader(readersOfLogs...))
	if err != nil {
		return fail(err)
	}

	res, err := backfill.Execute(ctx, runs, backfill.Options{
		Before: cutoff,
		Source: *source,
		DryRun: *dryRun,
		Store:  store,
		Log:    log,
		Lookup: func(ctx context.Context, account, id string) (string, error) {
			r, ok := readers[strings.ToLower(account)]
			if !ok {
				return "", nil
			}
			return r.RFCMessageID(ctx, id)
		},
	})
	log.Info("backfill finished",
		"dry_run", *dryRun,
		"runs_written", res.RunsWritten,
		"records_written", res.RecordsWritten,
		"records_without_ref", res.RecordsNoRef,
		"runs_already_archived", res.RunsExisting,
		"runs_after_cutoff", res.RunsTooLate,
		"runs_incomplete", res.RunsIncomplete,
		"first_run", res.First,
		"last_run", res.Last,
	)
	if err != nil {
		return fail(err)
	}
	return 0
}
