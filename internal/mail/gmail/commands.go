package gmail

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	mailpkg "github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/textclean"
	gmailapi "google.golang.org/api/gmail/v1"
)

var _ mailpkg.CommandSource = (*Provider)(nil)

// sentLabel is Gmail's system label for mail the mailbox itself sent. Nobody
// outside the account can apply it, which is what makes it proof of origin.
const sentLabel = "SENT"

// commandBodyMax caps the body read from an Email Command. The instruction is
// at the top; the forwarded headers follow it closely.
const commandBodyMax = 8000

// ListCommands returns messages sent to plusAddress. The search is only a
// narrowing step: the caller verifies the SENT label, From and recipients.
func (p *Provider) ListCommands(ctx context.Context, plusAddress string, lookbackDays, max int) ([]mailpkg.CommandMessage, error) {
	query := fmt.Sprintf("to:%s newer_than:%dd", plusAddress, lookbackDays)

	var resp *gmailapi.ListMessagesResponse
	err := p.do(ctx, "messages.list(commands)", "", func() error {
		var lerr error
		resp, lerr = p.svc.Users.Messages.List(userID).Q(query).MaxResults(int64(max)).Context(ctx).Do()
		return lerr
	})
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}

	out := make([]mailpkg.CommandMessage, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		var msg *gmailapi.Message
		err := p.do(ctx, "messages.get(full)", m.Id, func() error {
			var gerr error
			msg, gerr = p.svc.Users.Messages.Get(userID, m.Id).Format("full").Context(ctx).Do()
			return gerr
		})
		if err != nil {
			return nil, err
		}
		out = append(out, convertCommand(msg))
	}
	return out, nil
}

func convertCommand(msg *gmailapi.Message) mailpkg.CommandMessage {
	hdr := headers(msg.Payload)
	plain, htmlBody := bodies(msg.Payload)
	body := plain
	if strings.TrimSpace(body) == "" {
		body = textclean.Strip("", htmlBody, commandBodyMax)
	}
	if r := []rune(body); len(r) > commandBodyMax {
		body = string(r[:commandBodyMax])
	}

	var recipients []string
	for _, name := range []string{"To", "Cc", "Delivered-To"} {
		recipients = append(recipients, addressList(allHeaders(msg.Payload, name))...)
	}

	sent := false
	for _, l := range msg.LabelIds {
		if l == sentLabel {
			sent = true
		}
	}
	return mailpkg.CommandMessage{
		ID:         msg.Id,
		From:       parseFrom(hdr.get("From")).Email,
		Recipients: recipients,
		Subject:    hdr.get("Subject"),
		Body:       body,
		Sent:       sent,
	}
}

// allHeaders returns every value of a header: Delivered-To can repeat.
func allHeaders(part *gmailapi.MessagePart, name string) []string {
	if part == nil {
		return nil
	}
	var out []string
	for _, h := range part.Headers {
		if strings.EqualFold(h.Name, name) {
			out = append(out, h.Value)
		}
	}
	return out
}

func addressList(values []string) []string {
	var out []string
	for _, v := range values {
		list, err := mail.ParseAddressList(v)
		if err != nil {
			if a := strings.ToLower(strings.TrimSpace(v)); a != "" {
				out = append(out, a)
			}
			continue
		}
		for _, a := range list {
			out = append(out, strings.ToLower(strings.TrimSpace(a.Address)))
		}
	}
	return out
}
