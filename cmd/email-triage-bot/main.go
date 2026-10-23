// Command email-triage-bot clears routine, no-action email from the unread
// queue of every configured Account.
//
//	email-triage-bot run     [--dry-run[=false]] [--account EMAIL] [--decisions-out PATH]
//	email-triage-bot auth    [--email EMAIL]
//	email-triage-bot replay  --decisions PATH --rules PATH [--account EMAIL]
//	email-triage-bot version
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const usageText = `email-triage-bot - triage the unread queue of one or more mailboxes

Usage:
  email-triage-bot run     [--dry-run[=false]] [--account EMAIL] [--decisions-out PATH]
  email-triage-bot auth    [--email EMAIL]
  email-triage-bot replay  --decisions PATH --rules PATH [--account EMAIL]
  email-triage-bot version

Configuration comes from the environment; see docs/design.md for the full table.
`

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch runs one subcommand and returns the process exit code: 0 on
// success, 1 when the command failed, 2 on a usage error.
func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "auth":
		return cmdAuth(args[1:])
	case "replay":
		return cmdReplay(args[1:])
	case "version":
		fmt.Println(version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
}

// fail prints an error the way every subcommand should and returns the exit code.
func fail(err error) int {
	fmt.Fprintf(os.Stderr, "email-triage-bot: %v\n", err)
	return 1
}

// newLogger builds the structured logger every command writes to stdout, so
// logs and the shell's own output interleave in the expected order.
func newLogger(w io.Writer, format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if strings.EqualFold(format, "text") {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
