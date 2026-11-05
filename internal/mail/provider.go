// Package mail defines the Provider interface the triage run drives. Gmail is
// the only implementation today; see CONTRIBUTING.md for adding another.
package mail

import (
	"context"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// Query bounds one account's work queue.
type Query struct {
	LookbackDays  int      // only messages received within this many days
	ExcludeLabels []string // label names whose messages are skipped (the Audit Labels)
	Max           int      // hard cap on messages returned, newest first
}

// Provider is one authorised mailbox. Implementations must be safe to call
// sequentially from a single goroutine; the run does not call them concurrently.
type Provider interface {
	// Account returns the mailbox address this provider is bound to.
	Account() string
	// ListUnread returns IDs of unread inbox messages matching q, newest first.
	// Implementations must also filter out ExcludeLabels client-side, because
	// provider search syntax for nested labels is not reliable.
	ListUnread(ctx context.Context, q Query) ([]string, error)
	// Fetch returns the scanned Message with Body already stripped and capped
	// to bodyMax characters.
	Fetch(ctx context.Context, id string, bodyMax int) (*triage.Message, error)
	// EnsureLabels creates any of the named labels that do not exist and
	// returns name -> provider label ID.
	EnsureLabels(ctx context.Context, names []string) (map[string]string, error)
	// MarkRead clears the unread state of ids and applies the label. Must be
	// idempotent and use a batch call where the provider offers one.
	MarkRead(ctx context.Context, ids []string, labelID string) error
	// Label applies the label to ids without changing unread state.
	Label(ctx context.Context, ids []string, labelID string) error
}

// CommandMessage is one message that may be an Email Command: a message the
// Account owner sent to their own plus-address. The fields are what the
// command subcommand needs to verify it and read the instruction.
type CommandMessage struct {
	ID         string
	MessageID  string   // the RFC 5322 Message-ID header
	From       string   // the From address, lower-cased
	Recipients []string // To, Cc and Delivered-To addresses, lower-cased
	Subject    string
	Body       string // every text/plain part, joined in order, capped
	HTMLText   string // every text/html part as plain text, joined, capped
	Sent       bool   // carries the provider's SENT label: the owner sent it
}

// CommandSource is implemented by Providers that can find Email Commands.
type CommandSource interface {
	Account() string
	// ListCommands returns messages addressed to plusAddress, received within
	// lookbackDays, newest first, at most max of them.
	ListCommands(ctx context.Context, plusAddress string, lookbackDays, max int) ([]CommandMessage, error)
	EnsureLabels(ctx context.Context, names []string) (map[string]string, error)
	MarkRead(ctx context.Context, ids []string, labelID string) error
}
