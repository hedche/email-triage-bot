package command

import (
	"time"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// State is the whole Overlay document. The daily run reads only Changes
// (triage.LoadOverlay); the rest is the command subcommand's bookkeeping.
type State struct {
	Changes        []triage.Change `yaml:"changes"`
	Pending        []Proposal      `yaml:"pending,omitempty"`
	TelegramOffset int64           `yaml:"telegram_offset,omitempty"`
	// Seen holds "account/message-id" for every Email Command already handled,
	// newest last, so a command is never acted on twice even if labelling it
	// failed. Capped at maxSeen.
	Seen []string `yaml:"seen,omitempty"`
}

// Proposal is a set of Changes waiting for the owner to tap Apply or Discard.
type Proposal struct {
	ID                string          `yaml:"id"`
	Account           string          `yaml:"account"`
	Instruction       string          `yaml:"instruction"`
	Add               []triage.Change `yaml:"add,omitempty"`
	Remove            []string        `yaml:"remove,omitempty"`
	CreatedAt         time.Time       `yaml:"created_at"`
	TelegramMessageID int64           `yaml:"telegram_message_id,omitempty"`
}

const maxSeen = 500

// pendingTTL is how long a Proposal waits for a tap before it is dropped.
const pendingTTL = 7 * 24 * time.Hour

func (s *State) seen(key string) bool {
	for _, k := range s.Seen {
		if k == key {
			return true
		}
	}
	return false
}

func (s *State) markSeen(key string) {
	if s.seen(key) {
		return
	}
	s.Seen = append(s.Seen, key)
	if len(s.Seen) > maxSeen {
		s.Seen = s.Seen[len(s.Seen)-maxSeen:]
	}
}

func (s *State) change(id string) (triage.Change, bool) {
	for _, c := range s.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return triage.Change{}, false
}

func (s *State) removeChanges(ids []string) []triage.Change {
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	var removed []triage.Change
	kept := s.Changes[:0:0]
	for _, c := range s.Changes {
		if drop[c.ID] {
			removed = append(removed, c)
			continue
		}
		kept = append(kept, c)
	}
	s.Changes = kept
	return removed
}

func (s *State) takePending(id string) (Proposal, bool) {
	for i, p := range s.Pending {
		if p.ID == id {
			s.Pending = append(s.Pending[:i:i], s.Pending[i+1:]...)
			return p, true
		}
	}
	return Proposal{}, false
}
