package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/judge"
	"github.com/hedche/email-triage-bot/internal/judge/gemini"
	"github.com/hedche/email-triage-bot/internal/judge/openai"
	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/mail/gmail"
	"github.com/hedche/email-triage-bot/internal/notify"
	"github.com/hedche/email-triage-bot/internal/run"
	"github.com/hedche/email-triage-bot/internal/triage"
)

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	// The default shown here is only cosmetic: DRY_RUN decides unless the flag
	// is actually given, so --dry-run=false can turn a dry run off.
	dryRun := fs.Bool("dry-run", true, "scan and decide but change nothing (default from DRY_RUN)")
	account := fs.String("account", "", "triage only this account (default: every configured account)")
	decisionsOut := fs.String("decisions-out", "", "write the decision log here (default from DECISIONS_OUT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	settings, err := config.Load()
	if err != nil {
		return fail(err)
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dry-run" {
			settings.DryRun = *dryRun
		}
	})
	if *decisionsOut != "" {
		settings.DecisionsOut = *decisionsOut
	}

	log := newLogger(os.Stdout, settings.LogFormat, settings.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rules, err := triage.LoadRules(settings.RulesFile)
	if err != nil {
		return fail(err)
	}
	accounts, err := config.LoadAccounts(settings.AccountsFile)
	if err != nil {
		return fail(err)
	}
	accounts, err = selectAccounts(accounts, *account)
	if err != nil {
		return fail(err)
	}

	providers, err := buildProviders(ctx, settings, accounts)
	if err != nil {
		return fail(err)
	}
	judgeImpl, err := buildJudge(settings)
	if err != nil {
		return fail(err)
	}

	var decisions *decisionlog.Writer
	if settings.DecisionsOut != "" {
		decisions, err = decisionlog.NewWriter(settings.DecisionsOut)
		if err != nil {
			return fail(err)
		}
		defer func() {
			if err := decisions.Close(); err != nil {
				log.Error("closing the decision log failed", "error", err.Error())
			}
		}()
	}

	runner := run.New(run.Options{
		Settings:  settings,
		Rules:     rules,
		Providers: providers,
		Judge:     judgeImpl,
		Log:       log,
		Decisions: decisions,
		Notifier:  buildNotifier(settings),
	})

	summary, err := runner.Run(ctx)
	fmt.Fprintln(os.Stderr, summary.Text())
	if err != nil {
		return fail(err)
	}
	return 0
}

// selectAccounts narrows the configured Accounts to the one --account names.
func selectAccounts(accounts []config.Account, only string) ([]config.Account, error) {
	only = strings.ToLower(strings.TrimSpace(only))
	if only == "" {
		return accounts, nil
	}
	for _, a := range accounts {
		if a.Email == only {
			return []config.Account{a}, nil
		}
	}
	return nil, fmt.Errorf("account %q is not in the accounts file", only)
}

func buildProviders(ctx context.Context, s *config.Settings, accounts []config.Account) ([]mail.Provider, error) {
	providers := make([]mail.Provider, 0, len(accounts))
	for _, a := range accounts {
		switch a.Provider {
		case config.ProviderGmail:
			if s.GmailClientID == "" || s.GmailClientSecret == "" {
				return nil, fmt.Errorf("account %s: GMAIL_CLIENT_ID and GMAIL_CLIENT_SECRET are required", a.Email)
			}
			p, err := gmail.New(ctx, s.GmailClientID, s.GmailClientSecret, a.RefreshToken, a.Email)
			if err != nil {
				return nil, fmt.Errorf("account %s: %w", a.Email, err)
			}
			providers = append(providers, p)
		default:
			return nil, fmt.Errorf("account %s: unknown provider %q", a.Email, a.Provider)
		}
	}
	return providers, nil
}

// buildJudge returns nil (the interface, not a typed nil) when JUDGE_PROVIDER
// is none, which disables the Brand tier for the whole run.
func buildJudge(s *config.Settings) (judge.Judge, error) {
	switch s.JudgeProvider {
	case config.JudgeGemini:
		j, err := gemini.New(s.GeminiAPIKey, s.GeminiModel, s.JudgeTimeout)
		if err != nil {
			return nil, err
		}
		return j, nil
	case config.JudgeOpenAI:
		j, err := openai.New(s.OpenAIBaseURL, s.OpenAIAPIKey, s.OpenAIModel, s.JudgeTimeout)
		if err != nil {
			return nil, err
		}
		return j, nil
	case config.JudgeNone:
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown judge provider %q", s.JudgeProvider)
	}
}

func buildNotifier(s *config.Settings) notify.Notifier {
	if s.TelegramBotToken != "" && s.TelegramChatID != "" {
		return notify.NewTelegram(s.TelegramBotToken, s.TelegramChatID)
	}
	return notify.Nop()
}
