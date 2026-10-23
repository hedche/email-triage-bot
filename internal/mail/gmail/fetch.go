package gmail

import (
	"context"
	"encoding/base64"
	"html"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/hedche/email-triage-bot/internal/textclean"
	"github.com/hedche/email-triage-bot/internal/triage"
	gmailapi "google.golang.org/api/gmail/v1"
)

// Fetch scans one message: headers, snippet and a stripped, capped body. It
// changes nothing in the mailbox.
func (p *Provider) Fetch(ctx context.Context, id string, bodyMax int) (*triage.Message, error) {
	var msg *gmailapi.Message
	err := p.do(ctx, "messages.get(full)", id, func() error {
		var gerr error
		msg, gerr = p.svc.Users.Messages.Get(userID, id).Format("full").Context(ctx).Do()
		return gerr
	})
	if err != nil {
		return nil, err
	}
	return p.convert(msg, bodyMax), nil
}

func (p *Provider) convert(msg *gmailapi.Message, bodyMax int) *triage.Message {
	hdr := headers(msg.Payload)
	plain, htmlBody := bodies(msg.Payload)

	return &triage.Message{
		ID:       msg.Id,
		ThreadID: msg.ThreadId,
		Account:  p.account,
		From:     parseFrom(hdr.get("From")),
		Subject:  hdr.get("Subject"),
		Snippet:  html.UnescapeString(msg.Snippet),
		Body:     textclean.Strip(plain, htmlBody, bodyMax),
		Received: received(hdr.get("Date"), msg.InternalDate),
		Labels:   msg.LabelIds,
	}
}

// headerMap holds the top-level RFC 2822 headers, keyed case-insensitively:
// Gmail returns them with whatever capitalisation the sender used.
type headerMap map[string]string

func headers(part *gmailapi.MessagePart) headerMap {
	h := headerMap{}
	if part == nil {
		return h
	}
	for _, x := range part.Headers {
		key := strings.ToLower(x.Name)
		if _, seen := h[key]; !seen { // first occurrence wins, as RFC 5322 intends
			h[key] = x.Value
		}
	}
	return h
}

func (h headerMap) get(name string) string { return h[strings.ToLower(name)] }

// parseFrom turns a From header into an Address. A header the mail parser
// rejects (they are common enough in bulk mail) is kept verbatim as the email
// so a Sender Rule still has something to match, rather than being discarded.
func parseFrom(raw string) triage.Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return triage.Address{}
	}
	if addr, err := mail.ParseAddress(raw); err == nil {
		return triage.Address{
			Name:  strings.TrimSpace(addr.Name),
			Email: strings.ToLower(strings.TrimSpace(addr.Address)),
		}
	}
	return triage.Address{Email: strings.ToLower(raw)}
}

// received prefers the Date header, because that is what a person sees, and
// falls back to Gmail's internalDate (milliseconds since the epoch) when the
// header is missing or unparseable.
func received(dateHdr string, internalDate int64) time.Time {
	if dateHdr != "" {
		if t, err := mail.ParseDate(dateHdr); err == nil {
			return t
		}
	}
	if internalDate > 0 {
		return time.UnixMilli(internalDate).UTC()
	}
	return time.Time{}
}

// bodies walks the MIME tree depth-first and returns the first text/plain and
// the first text/html body it finds. Attachments are skipped: a part with a
// filename is a document, not the message a person reads.
func bodies(part *gmailapi.MessagePart) (plain, htmlBody string) {
	walkParts(part, &plain, &htmlBody)
	return plain, htmlBody
}

func walkParts(part *gmailapi.MessagePart, plain, htmlBody *string) {
	if part == nil || (*plain != "" && *htmlBody != "") {
		return
	}

	mediaType, params := contentType(part)
	if part.Filename == "" && part.Body != nil && part.Body.Data != "" {
		switch mediaType {
		case "text/plain":
			if *plain == "" {
				*plain = decodeBody(part.Body.Data, params["charset"])
			}
		case "text/html":
			if *htmlBody == "" {
				*htmlBody = decodeBody(part.Body.Data, params["charset"])
			}
		}
	}

	for _, child := range part.Parts {
		walkParts(child, plain, htmlBody)
	}
}

// contentType prefers the part's declared mimeType and reads the charset from
// its Content-Type header.
func contentType(part *gmailapi.MessagePart) (mediaType string, params map[string]string) {
	params = map[string]string{}
	raw := headers(part).get("Content-Type")
	if raw != "" {
		if mt, ps, err := mime.ParseMediaType(raw); err == nil {
			mediaType, params = mt, ps
		}
	}
	if part.MimeType != "" {
		mediaType = strings.ToLower(strings.TrimSpace(part.MimeType))
	}
	return mediaType, params
}

// decodeBody base64url-decodes a part body and converts it to UTF-8 for the
// charsets worth handling without a dependency. Anything else is passed through
// as-is: textclean caps and collapses it, and a Rule matching mojibake is a
// better outcome than dropping the body.
func decodeBody(data, charset string) string {
	raw, err := decodeBase64URL(data)
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.Trim(charset, `" `)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return string(raw)
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1", "windows-1252", "cp1252":
		// Latin-1 bytes are Unicode code points 0..255 one for one. Windows-1252
		// differs only in the 0x80-0x9F range (curly quotes and the like), which
		// this maps to control pictures rather than the intended punctuation --
		// acceptable for substring matching, and better than raw bytes.
		var b strings.Builder
		b.Grow(len(raw))
		for _, c := range raw {
			b.WriteRune(rune(c))
		}
		return b.String()
	default:
		return string(raw)
	}
}

// decodeBase64URL accepts both padded and unpadded base64url; Gmail has been
// seen emitting either, and standard base64 turns up in hand-crafted fixtures.
func decodeBase64URL(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
