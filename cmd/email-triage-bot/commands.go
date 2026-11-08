package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hedche/email-triage-bot/internal/command"
	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/mail"
)

// cmdCommands handles Email Commands once: messages an Account owner sends to
// their own plus-address to change the rules. See docs/adr/0005.
func cmdCommands(args []string) int { return commandsMain("commands", args) }

// cmdListen runs Email Commands continuously: Apply/Discard taps are answered
// as they happen (Telegram long polling) and the mailboxes are read every
// --mail-every. It is what the cluster runs; `commands` is the one-shot form.
func cmdListen(args []string) int { return commandsMain("listen", args) }

func commandsMain(name string, args []string) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	mailEvery := fs.Duration("mail-every", time.Minute, "listen only: how often to read the mailboxes for new commands")
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
	if name == "listen" {
		listen(ctx, runner, *mailEvery, log)
		return 0
	}
	rep, err := runner.Run(ctx)
	log.Info("email commands done", "commands", rep.Commands, "applied", rep.Applied,
		"proposed", rep.Proposed, "ignored", rep.Ignored)
	if err != nil {
		return fail(err)
	}
	return 0
}

// longPoll is the most a listen pass waits for a tap. Telegram allows more,
// but proxies and NAT tables are kinder to a request that ends within a minute.
const longPoll = 50 * time.Second

// listen loops until ctx is cancelled. Every pass waits for taps; a pass that
// is due also reads the mailboxes. Errors are logged and retried after a
// pause: nothing a pass failed to save is lost, because Telegram redelivers
// unconfirmed taps and unseen commands are read again.
func listen(ctx context.Context, runner *command.Runner, mailEvery time.Duration, log *slog.Logger) {
	log.Info("listening for email commands", "mail_every", mailEvery.String())
	nextMail := time.Now()
	for ctx.Err() == nil {
		readMail := !time.Now().Before(nextMail)
		if readMail {
			nextMail = time.Now().Add(mailEvery)
		}
		wait := time.Until(nextMail)
		if wait > longPoll {
			wait = longPoll
		}
		if wait < time.Second {
			wait = time.Second
		}
		runner.PollTimeout = wait

		rep, err := runner.RunOnce(ctx, readMail)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Error("email commands pass failed", "error", err.Error())
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		if rep != (command.Report{}) {
			log.Info("email commands pass", "commands", rep.Commands, "applied", rep.Applied,
				"proposed", rep.Proposed, "ignored", rep.Ignored)
		}
	}
	log.Info("stopped listening")
}
