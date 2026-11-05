package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hedche/email-triage-bot/internal/command"
	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/mail"
)

// cmdCommands handles Email Commands: messages an Account owner sends to their
// own plus-address to change the rules. See docs/adr/0005.
func cmdCommands(args []string) int {
	fs := flag.NewFlagSet("commands", flag.ContinueOnError)
	account := fs.String("account", "", "read commands for this account only (default: every configured account)")
	overlayPath := fs.String("overlay", "", "keep the overlay in this local file instead of the OVERLAY_SECRET Secret")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	settings, err := config.Load()
	if err != nil {
		return fail(err)
	}
	log := newLogger(os.Stdout, settings.LogFormat, settings.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store command.Store
	switch {
	case *overlayPath != "":
		store = command.FileStore{Path: *overlayPath}
	case settings.OverlaySecret != "":
		s, err := command.NewSecretStore(settings.OverlaySecret)
		if err != nil {
			return fail(err)
		}
		store = s
	default:
		return fail(fmt.Errorf("set OVERLAY_SECRET (in the cluster) or pass --overlay PATH"))
	}

	accounts, err := config.LoadAccounts(settings.AccountsFile)
	if err != nil {
		return fail(err)
	}
	if accounts, err = selectAccounts(accounts, *account); err != nil {
		return fail(err)
	}
	providers, err := buildProviders(ctx, settings, accounts)
	if err != nil {
		return fail(err)
	}
	sources := make([]mail.CommandSource, 0, len(providers))
	for _, p := range providers {
		src, ok := p.(mail.CommandSource)
		if !ok {
			return fail(fmt.Errorf("account %s: provider cannot read email commands", p.Account()))
		}
		sources = append(sources, src)
	}

	interp, err := command.NewGemini(settings.GeminiAPIKey, settings.GeminiModel, settings.JudgeTimeout)
	if err != nil {
		return fail(err)
	}
	messenger, err := command.NewTelegram(settings.CommandsTelegramBotToken, settings.TelegramChatID)
	if err != nil {
		return fail(err)
	}

	runner := &command.Runner{
		Store:       store,
		Sources:     sources,
		Interpreter: interp,
		Messenger:   messenger,
		Log:         log,
		Tag:         settings.CommandTag,
		Label:       settings.CommandLabel,
	}
	rep, err := runner.Run(ctx)
	log.Info("email commands done", "commands", rep.Commands, "applied", rep.Applied,
		"proposed", rep.Proposed, "ignored", rep.Ignored)
	if err != nil {
		return fail(err)
	}
	return 0
}
