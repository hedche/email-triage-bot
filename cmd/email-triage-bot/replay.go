package main

import (
	"errors"
	"flag"
	"os"

	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/replay"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// cmdReplay re-evaluates a saved Decision Log against a rules file. It is
// entirely offline: no Provider, no Judge, nothing mutated.
func cmdReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	decisions := fs.String("decisions", "", "path to a decision log written by run --decisions-out")
	rulesPath := fs.String("rules", "", "path to the rules file to replay against")
	overlayPath := fs.String("overlay", "", "path to an Overlay whose Changes are laid over the rules (optional)")
	account := fs.String("account", "", "replay only this account")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *decisions == "" || *rulesPath == "" {
		fs.Usage()
		return fail(errors.New("replay needs both --decisions and --rules"))
	}

	records, err := decisionlog.Read(*decisions)
	if err != nil {
		return fail(err)
	}
	rules, err := triage.LoadRules(*rulesPath)
	if err != nil {
		return fail(err)
	}
	if *overlayPath != "" {
		if _, err := os.Stat(*overlayPath); err != nil {
			return fail(err) // unlike the run, a named overlay that is missing is a mistake
		}
		changes, err := triage.LoadOverlay(*overlayPath)
		if err != nil {
			return fail(err)
		}
		rules = rules.WithOverlay(changes)
	}

	replay.Run(records, rules, *account).Write(os.Stdout)
	return 0
}
