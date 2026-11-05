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
	// Apple Mail splits a forward into several text parts with the forwarded
	// message's attachments between them, so the first text/plain part is often
	// only the note. Read every part.
	var plains, htmls []string
	allTextParts(msg.Payload, &plains, &htmls)
	body := capText(strings.Join(plains, "\n"))
	htmlText := capText(textclean.Text(strings.Join(htmls, "\n")))

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
		MessageID:  strings.TrimSpace(hdr.get("Message-ID")),
		From:       parseFrom(hdr.get("From")).Email,
		Recipients: recipients,
		Subject:    hdr.get("Subject"),
		Body:       body,
		HTMLText:   htmlText,
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

// allTextParts collects every text/plain and text/html part that is not an
// attachment, depth-first, in document order.
func allTextParts(part *gmailapi.MessagePart, plains, htmls *[]string) {
	if part == nil {
		return
	}
	mediaType, params := contentType(part)
	if part.Filename == "" && part.Body != nil && part.Body.Data != "" {
		switch mediaType {
		case "text/plain":
			*plains = append(*plains, decodeBody(part.Body.Data, params["charset"]))
		case "text/html":
			*htmls = append(*htmls, decodeBody(part.Body.Data, params["charset"]))
		}
	}
	for _, child := range part.Parts {
		allTextParts(child, plains, htmls)
	}
}

func capText(s string) string {
	if r := []rune(s); len(r) > commandBodyMax {
		return string(r[:commandBodyMax])
	}
	return s
}
