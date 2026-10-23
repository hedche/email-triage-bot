package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hedche/email-triage-bot/internal/mail/gmail"
)

// cmdAuth runs the local OAuth consent flow and prints the refresh token, and
// nothing else, to stdout so it can be piped straight into a secret. It reads
// the two OAuth variables from the environment directly: authorising a new
// mailbox must work before rules, accounts or a Judge are configured.
func cmdAuth(args []string) int {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	email := fs.String("email", "", "the mailbox you are about to sign in as (used only in the hint)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	clientID := strings.TrimSpace(os.Getenv("GMAIL_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("GMAIL_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		return fail(errors.New("GMAIL_CLIENT_ID and GMAIL_CLIENT_SECRET must be set to authorise an account"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	token, err := gmail.Authorize(ctx, clientID, clientSecret, os.Stderr)
	if err != nil {
		return fail(err)
	}

	fmt.Println(token)

	who := *email
	if who == "" {
		who = "the account you signed in as"
	}
	fmt.Fprintf(os.Stderr, "\nPaste this refresh token into accounts.yaml as the refresh_token of %s,\n"+
		"then update the email-triage-accounts secret. Treat it like a password.\n", who)
	return 0
}
