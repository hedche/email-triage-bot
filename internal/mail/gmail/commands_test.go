package gmail

import (
	"encoding/base64"
	"strings"
	"testing"

	gmailapi "google.golang.org/api/gmail/v1"
)

func TestConvertCommand(t *testing.T) {
	msg := &gmailapi.Message{
		Id:       "m1",
		LabelIds: []string{"SENT", "INBOX", "UNREAD"},
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Headers: []*gmailapi.MessagePartHeader{
				{Name: "From", Value: "Me <Me@Example.com>"},
				{Name: "To", Value: "me+triage@example.com, Other <o@example.com>"},
				{Name: "Delivered-To", Value: "me@example.com"},
				{Name: "Subject", Value: "Fwd: hi"},
			},
			Body: &gmailapi.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte("hide these"))},
		},
	}
	got := convertCommand(msg)
	if !got.Sent || got.From != "me@example.com" || got.Body != "hide these" {
		t.Errorf("got %+v", got)
	}
	want := []string{"me+triage@example.com", "o@example.com", "me@example.com"}
	if len(got.Recipients) != len(want) {
		t.Fatalf("recipients %v", got.Recipients)
	}
	for i := range want {
		if got.Recipients[i] != want[i] {
			t.Errorf("recipients %v", got.Recipients)
		}
	}
}

// Apple Mail: multipart/mixed with the note, an attachment, then the rest.
func TestConvertCommandJoinsSplitTextParts(t *testing.T) {
	enc := func(s string) *gmailapi.MessagePartBody {
		return &gmailapi.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte(s))}
	}
	msg := &gmailapi.Message{
		Id: "m1",
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/mixed",
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: enc("mark these as done\n")},
				{MimeType: "image/png", Filename: "logo.png", Body: &gmailapi.MessagePartBody{AttachmentId: "a1"}},
				{MimeType: "text/plain", Body: enc("\nBegin forwarded message:\n\nFrom: Shop <news@shop.example>\nSubject: Sale\n")},
				{MimeType: "text/html", Body: enc("<div><b>From: </b>Shop &lt;news@shop.example&gt;<br><b>Subject: </b>Sale</div>")},
			},
		},
	}
	got := convertCommand(msg)
	if !strings.Contains(got.Body, "mark these as done") || !strings.Contains(got.Body, "From: Shop <news@shop.example>") {
		t.Errorf("body %q", got.Body)
	}
	if !strings.Contains(got.HTMLText, "From: Shop <news@shop.example>") {
		t.Errorf("html text %q", got.HTMLText)
	}
}
