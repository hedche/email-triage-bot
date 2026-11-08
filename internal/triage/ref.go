package triage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// refLen is the length of a Ref in hex characters: 48 bits, far beyond what a
// household's mail could collide in.
const refLen = 12

// Ref returns the Message's stable short reference: a truncated SHA-256 of
// its Account and Message-ID header. The same email in the same mailbox has the
// same Ref in every run, and the Ref reveals nothing about it. A Message with no
// Message-ID falls back to its provider ID, which is also stable per mailbox.
func (m Message) Ref() string {
	id := strings.TrimSpace(m.RFCMessageID)
	if id == "" {
		id = "provider:" + m.ID
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(m.Account)) + "\n" + id))
	return hex.EncodeToString(sum[:])[:refLen]
}
