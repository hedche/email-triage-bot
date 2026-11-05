package gmail

import (
	"encoding/base64"
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
