package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/mail"
	gmailapi "google.golang.org/api/gmail/v1"
)

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func hdr(name, value string) *gmailapi.MessagePartHeader {
	return &gmailapi.MessagePartHeader{Name: name, Value: value}
}

// multipartMessage is the common shape of marketing mail: a multipart/alternative
// with a plain and an HTML alternative, both base64url encoded.
func multipartMessage() *gmailapi.Message {
	return &gmailapi.Message{
		Id:           "m-multipart",
		ThreadId:     "t-1",
		Snippet:      "Your order has &quot;shipped&quot; &amp; is on its way",
		LabelIds:     []string{"INBOX", "UNREAD"},
		InternalDate: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC).UnixMilli(),
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/alternative",
			Headers: []*gmailapi.MessagePartHeader{
				hdr("From", `"Amazon.co.uk" <shipment-tracking@amazon.co.uk>`),
				hdr("Subject", "Your order has shipped"),
				hdr("Date", "Sun, 01 Mar 2026 09:00:00 +0000"),
				hdr("Content-Type", `multipart/alternative; boundary="b1"`),
			},
			Parts: []*gmailapi.MessagePart{
				{
					MimeType: "text/plain",
					Headers:  []*gmailapi.MessagePartHeader{hdr("Content-Type", "text/plain; charset=UTF-8")},
					Body:     &gmailapi.MessagePartBody{Data: b64("Your parcel is on its way.\nTrack it online.")},
				},
				{
					MimeType: "text/html",
					Headers:  []*gmailapi.MessagePartHeader{hdr("Content-Type", "text/html; charset=UTF-8")},
					Body:     &gmailapi.MessagePartBody{Data: b64("<html><body><p>Your parcel is on its way.</p></body></html>")},
				},
			},
		},
	}
}

func plainMessage() *gmailapi.Message {
	return &gmailapi.Message{
		Id:           "m-plain",
		ThreadId:     "t-2",
		Snippet:      "Weekly digest",
		LabelIds:     []string{"INBOX", "UNREAD"},
		InternalDate: time.Date(2026, 3, 2, 8, 30, 0, 0, time.UTC).UnixMilli(),
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Headers: []*gmailapi.MessagePartHeader{
				hdr("from", "digest@example.com"),
				hdr("subject", "Your weekly digest"),
			},
			Body: &gmailapi.MessagePartBody{Data: b64("Here is what happened this week.")},
		},
	}
}

func TestListUnreadQueryAndPaging(t *testing.T) {
	f := newFakeGmail(t)
	f.page("page-2", "a", "b")
	f.page("", "c")
	p, _ := f.start()

	ids, err := p.ListUnread(context.Background(), mail.Query{
		LookbackDays:  7,
		ExcludeLabels: []string{"Triage/Read", "Triage/Left"},
		Max:           10,
	})
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if got, want := strings.Join(ids, ","), "a,b,c"; got != want {
		t.Errorf("ids = %q, want %q", got, want)
	}

	wantQuery := `in:inbox is:unread newer_than:7d -label:"Triage/Read" -label:"Triage/Left"`
	for i, q := range f.listQueries {
		if q != wantQuery {
			t.Errorf("list call %d query = %q, want %q", i, q, wantQuery)
		}
	}
	if len(f.listQueries) != 2 {
		t.Fatalf("got %d list calls, want 2", len(f.listQueries))
	}
	if f.listPageToks[0] != "" || f.listPageToks[1] != "page-2" {
		t.Errorf("page tokens = %q, want [\"\", \"page-2\"]", f.listPageToks)
	}
	// No excluded label exists in this mailbox, so no per-message get is needed.
	if len(f.getCalls) != 0 {
		t.Errorf("got %d messages.get calls, want 0 when nothing can be excluded", len(f.getCalls))
	}
}

func TestListUnreadQueryOmitsLookbackWhenUnset(t *testing.T) {
	f := newFakeGmail(t)
	f.page("")
	p, _ := f.start()

	if _, err := p.ListUnread(context.Background(), mail.Query{Max: 5}); err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if got, want := f.listQueries[0], "in:inbox is:unread"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
}

func TestListUnreadRespectsMax(t *testing.T) {
	f := newFakeGmail(t)
	f.page("more", "a", "b", "c", "d")
	p, _ := f.start()

	ids, err := p.ListUnread(context.Background(), mail.Query{LookbackDays: 7, Max: 2})
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if got, want := strings.Join(ids, ","), "a,b"; got != want {
		t.Errorf("ids = %q, want %q", got, want)
	}
	if len(f.listQueries) != 1 {
		t.Errorf("got %d list calls, want 1 once Max is reached", len(f.listQueries))
	}
}

func TestListUnreadFiltersExcludedLabelsClientSide(t *testing.T) {
	f := newFakeGmail(t)
	f.addLabel("Label_read", "Triage/Read")
	f.addLabel("Label_left", "Triage/Left")
	f.page("", "keep-1", "already-read", "keep-2", "already-left")
	f.addMessage(&gmailapi.Message{Id: "keep-1", LabelIds: []string{"INBOX", "UNREAD"}})
	f.addMessage(&gmailapi.Message{Id: "already-read", LabelIds: []string{"INBOX", "UNREAD", "Label_read"}})
	f.addMessage(&gmailapi.Message{Id: "keep-2", LabelIds: []string{"INBOX", "UNREAD"}})
	f.addMessage(&gmailapi.Message{Id: "already-left", LabelIds: []string{"INBOX", "UNREAD", "Label_left"}})
	p, _ := f.start()

	ids, err := p.ListUnread(context.Background(), mail.Query{
		LookbackDays:  7,
		ExcludeLabels: []string{"Triage/Read", "Triage/Left"},
		Max:           100,
	})
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if got, want := strings.Join(ids, ","), "keep-1,keep-2"; got != want {
		t.Errorf("ids = %q, want %q (order must be preserved)", got, want)
	}
	for _, id := range []string{"keep-1", "already-read", "keep-2", "already-left"} {
		if got := f.getFormats[id]; got != "minimal" {
			t.Errorf("messages.get(%s) format = %q, want minimal", id, got)
		}
	}
}

func TestFetchMultipart(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage(multipartMessage())
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-multipart", 2000)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := f.getFormats["m-multipart"]; got != "full" {
		t.Errorf("format = %q, want full", got)
	}
	if msg.Account != "someone@gmail.com" {
		t.Errorf("Account = %q", msg.Account)
	}
	if msg.ThreadID != "t-1" {
		t.Errorf("ThreadID = %q, want t-1", msg.ThreadID)
	}
	if msg.From.Name != "Amazon.co.uk" || msg.From.Email != "shipment-tracking@amazon.co.uk" {
		t.Errorf("From = %+v", msg.From)
	}
	if msg.From.Domain() != "amazon.co.uk" {
		t.Errorf("From.Domain = %q", msg.From.Domain())
	}
	if msg.Subject != "Your order has shipped" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if want := `Your order has "shipped" & is on its way`; msg.Snippet != want {
		t.Errorf("Snippet = %q, want %q (html entities must be unescaped)", msg.Snippet, want)
	}
	// The plain alternative wins over the HTML one.
	if !strings.Contains(msg.Body, "Track it online.") {
		t.Errorf("Body = %q, want the text/plain part", msg.Body)
	}
	if strings.Contains(msg.Body, "<") {
		t.Errorf("Body = %q, want no markup", msg.Body)
	}
	want := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if !msg.Received.Equal(want) {
		t.Errorf("Received = %v, want %v", msg.Received, want)
	}
	if strings.Join(msg.Labels, ",") != "INBOX,UNREAD" {
		t.Errorf("Labels = %v", msg.Labels)
	}
}

func TestFetchPlainOnlyAndLowerCasedFrom(t *testing.T) {
	f := newFakeGmail(t)
	m := plainMessage()
	m.Payload.Headers[0].Value = "Digest@Example.COM"
	f.addMessage(m)
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-plain", 2000)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if msg.From.Email != "digest@example.com" {
		t.Errorf("From.Email = %q, want lower-cased", msg.From.Email)
	}
	if msg.From.Name != "" {
		t.Errorf("From.Name = %q, want empty", msg.From.Name)
	}
	if msg.Subject != "Your weekly digest" {
		t.Errorf("Subject = %q (headers must match case-insensitively)", msg.Subject)
	}
	if msg.Body != "Here is what happened this week." {
		t.Errorf("Body = %q", msg.Body)
	}
	// No Date header: internalDate is the fallback.
	want := time.Date(2026, 3, 2, 8, 30, 0, 0, time.UTC)
	if !msg.Received.Equal(want) {
		t.Errorf("Received = %v, want %v", msg.Received, want)
	}
}

func TestFetchUnparseableFromKeepsRawAddress(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage(&gmailapi.Message{
		Id: "m-bad-from",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Headers: []*gmailapi.MessagePartHeader{
				hdr("From", "Noreply Bulk Mailer"),
				hdr("Subject", "Hello"),
			},
			Body: &gmailapi.MessagePartBody{Data: b64("body")},
		},
	})
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-bad-from", 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if msg.From.Email != "noreply bulk mailer" || msg.From.Name != "" {
		t.Errorf("From = %+v, want the raw header lower-cased as Email", msg.From)
	}
}

func TestFetchHTMLOnlyAndNestedParts(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage(&gmailapi.Message{
		Id: "m-html",
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/mixed",
			Headers:  []*gmailapi.MessagePartHeader{hdr("From", "news@example.com")},
			Parts: []*gmailapi.MessagePart{
				{
					MimeType: "application/pdf",
					Filename: "invoice.pdf",
					Body:     &gmailapi.MessagePartBody{Data: b64("not the body")},
				},
				{
					MimeType: "multipart/alternative",
					Parts: []*gmailapi.MessagePart{{
						MimeType: "text/html",
						Headers:  []*gmailapi.MessagePartHeader{hdr("Content-Type", "text/html; charset=utf-8")},
						Body:     &gmailapi.MessagePartBody{Data: b64("<div>Hello <b>there</b> &amp; welcome</div>")},
					}},
				},
			},
		},
	})
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-html", 2000)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(msg.Body, "Hello there & welcome") {
		t.Errorf("Body = %q, want the nested html part stripped", msg.Body)
	}
	if strings.Contains(msg.Body, "not the body") {
		t.Errorf("Body = %q, attachments must be skipped", msg.Body)
	}
}

func TestFetchLatin1Body(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage(&gmailapi.Message{
		Id: "m-latin1",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Headers:  []*gmailapi.MessagePartHeader{hdr("Content-Type", `text/plain; charset="ISO-8859-1"`)},
			// 0xE9 is 'é' in Latin-1 and invalid UTF-8 on its own.
			Body: &gmailapi.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte{'c', 'a', 'f', 0xE9})},
		},
	})
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-latin1", 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if msg.Body != "café" {
		t.Errorf("Body = %q, want café", msg.Body)
	}
}

func TestFetchBodyIsCapped(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage(&gmailapi.Message{
		Id: "m-long",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Body:     &gmailapi.MessagePartBody{Data: b64(strings.Repeat("x", 500))},
		},
	})
	p, _ := f.start()

	msg, err := p.Fetch(context.Background(), "m-long", 50)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len([]rune(msg.Body)) > 60 {
		t.Errorf("Body has %d runes, want it capped near 50", len([]rune(msg.Body)))
	}
}

func TestEnsureLabelsCreatesOnlyMissing(t *testing.T) {
	f := newFakeGmail(t)
	f.addLabel("Label_existing", "Triage/Read")
	p, _ := f.start()

	got, err := p.EnsureLabels(context.Background(), []string{"Triage/Read", "Triage/Left"})
	if err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if got["Triage/Read"] != "Label_existing" {
		t.Errorf("Triage/Read = %q, want the existing id", got["Triage/Read"])
	}
	if got["Triage/Left"] == "" {
		t.Error("Triage/Left was not created")
	}
	if strings.Join(f.createdNames, ",") != "Triage/Left" {
		t.Errorf("created %v, want only Triage/Left", f.createdNames)
	}
	created := f.labels[len(f.labels)-1]
	if created.LabelListVisibility != "labelShow" || created.MessageListVisibility != "show" {
		t.Errorf("created label visibility = %q/%q, want labelShow/show",
			created.LabelListVisibility, created.MessageListVisibility)
	}
}

func TestEnsureLabelsReusesLabelDifferingOnlyByCase(t *testing.T) {
	f := newFakeGmail(t)
	f.addLabel("Label_x", "triage/read")
	p, _ := f.start()

	got, err := p.EnsureLabels(context.Background(), []string{"Triage/Read"})
	if err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if got["Triage/Read"] != "Label_x" {
		t.Errorf("Triage/Read = %q, want the case-insensitive match", got["Triage/Read"])
	}
	if len(f.createdNames) != 0 {
		t.Errorf("created %v, want nothing", f.createdNames)
	}
}

func TestEnsureLabelsEmpty(t *testing.T) {
	f := newFakeGmail(t)
	p, _ := f.start()

	got, err := p.EnsureLabels(context.Background(), nil)
	if err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}

func TestMarkReadAndLabel(t *testing.T) {
	f := newFakeGmail(t)
	p, _ := f.start()
	ctx := context.Background()

	if err := p.MarkRead(ctx, []string{"a", "b"}, "Label_read"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := p.Label(ctx, []string{"c"}, "Label_left"); err != nil {
		t.Fatalf("Label: %v", err)
	}
	if len(f.batchModify) != 2 {
		t.Fatalf("got %d batchModify calls, want 2", len(f.batchModify))
	}

	read := f.batchModify[0]
	if strings.Join(read.Ids, ",") != "a,b" {
		t.Errorf("markRead ids = %v", read.Ids)
	}
	if strings.Join(read.AddLabelIds, ",") != "Label_read" {
		t.Errorf("markRead add = %v", read.AddLabelIds)
	}
	if strings.Join(read.RemoveLabelIds, ",") != "UNREAD" {
		t.Errorf("markRead remove = %v", read.RemoveLabelIds)
	}

	left := f.batchModify[1]
	if strings.Join(left.Ids, ",") != "c" {
		t.Errorf("label ids = %v", left.Ids)
	}
	if strings.Join(left.AddLabelIds, ",") != "Label_left" {
		t.Errorf("label add = %v", left.AddLabelIds)
	}
	if len(left.RemoveLabelIds) != 0 {
		t.Errorf("label remove = %v, want nothing removed", left.RemoveLabelIds)
	}
}

func TestBatchModifyChunksAtOneThousand(t *testing.T) {
	f := newFakeGmail(t)
	p, _ := f.start()

	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%04d", i)
	}
	if err := p.MarkRead(context.Background(), ids, "Label_read"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if len(f.batchModify) != 2 {
		t.Fatalf("got %d batchModify calls for 1001 ids, want 2", len(f.batchModify))
	}
	if got := len(f.batchModify[0].Ids); got != 1000 {
		t.Errorf("first chunk has %d ids, want 1000", got)
	}
	if got := len(f.batchModify[1].Ids); got != 1 {
		t.Errorf("second chunk has %d ids, want 1", got)
	}
	if f.batchModify[1].Ids[0] != "m1000" {
		t.Errorf("second chunk = %v, want the last id", f.batchModify[1].Ids)
	}
}

func TestBatchModifyNoOpOnEmptyIDs(t *testing.T) {
	f := newFakeGmail(t)
	p, _ := f.start()
	ctx := context.Background()

	if err := p.MarkRead(ctx, nil, "Label_read"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := p.Label(ctx, []string{}, "Label_left"); err != nil {
		t.Fatalf("Label: %v", err)
	}
	if len(f.batchModify) != 0 {
		t.Errorf("got %d batchModify calls, want none", len(f.batchModify))
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	f := newFakeGmail(t)
	f.failures["/messages"] = 1 // one 429, then the real answer
	f.page("", "a", "b")
	p, _ := f.start()

	ids, err := p.ListUnread(context.Background(), mail.Query{LookbackDays: 7, Max: 10})
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("ids = %v", ids)
	}
	if len(f.listQueries) != 1 {
		t.Errorf("got %d successful list calls, want 1", len(f.listQueries))
	}
}

func TestRetriesServerErrorThenGivesUp(t *testing.T) {
	f := newFakeGmail(t)
	f.failStatus = http.StatusServiceUnavailable
	f.failures["/messages"] = 10
	p, _ := f.start()

	_, err := p.ListUnread(context.Background(), mail.Query{LookbackDays: 7, Max: 10})
	if err == nil {
		t.Fatal("ListUnread succeeded, want an error after the retries ran out")
	}
	if !strings.Contains(err.Error(), "messages.list") || !strings.Contains(err.Error(), "someone@gmail.com") {
		t.Errorf("error = %v, want it to name the operation and the account", err)
	}
	// 4 attempts: the initial one plus three retries.
	if got := 10 - f.failures["/messages"]; got != 4 {
		t.Errorf("made %d attempts, want 4", got)
	}
}

func TestDoesNotRetryPermanentError(t *testing.T) {
	f := newFakeGmail(t)
	f.failStatus = http.StatusNotFound
	f.failures["/messages/m-gone"] = 10
	p, _ := f.start()

	_, err := p.Fetch(context.Background(), "m-gone", 100)
	if err == nil {
		t.Fatal("Fetch succeeded, want a 404")
	}
	if !strings.Contains(err.Error(), "m-gone") {
		t.Errorf("error = %v, want it to name the message", err)
	}
	if got := 10 - f.failures["/messages/m-gone"]; got != 1 {
		t.Errorf("made %d attempts, want 1 for a permanent error", got)
	}
}

func TestCancelledContextStopsRetrying(t *testing.T) {
	f := newFakeGmail(t)
	f.failures["/messages"] = 10
	p, _ := f.start()
	p.backoff = []time.Duration{time.Hour, time.Hour, time.Hour}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := p.ListUnread(ctx, mail.Query{LookbackDays: 7, Max: 10}); err == nil {
		t.Fatal("ListUnread succeeded, want a context error")
	}
	if got := 10 - f.failures["/messages"]; got != 1 {
		t.Errorf("made %d attempts, want 1 before the context expired", got)
	}
}

func TestAccount(t *testing.T) {
	f := newFakeGmail(t)
	p, _ := f.start()
	if p.Account() != "someone@gmail.com" {
		t.Errorf("Account = %q", p.Account())
	}
}

func TestNewRejectsMissingCredentials(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name                                          string
		clientID, clientSecret, refreshToken, account string
	}{
		{"no client id", "", "s", "r", "a@b.com"},
		{"no client secret", "c", "", "r", "a@b.com"},
		{"no refresh token", "c", "s", "", "a@b.com"},
		{"no account", "c", "s", "r", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(ctx, tc.clientID, tc.clientSecret, tc.refreshToken, tc.account); err == nil {
				t.Fatal("New succeeded, want an error")
			}
		})
	}
}

func TestAuthorizeRejectsMissingCredentials(t *testing.T) {
	if _, err := Authorize(context.Background(), "", "", &strings.Builder{}); err == nil {
		t.Fatal("Authorize succeeded, want an error")
	}
}

// syncBuffer is a writer safe to read from the test goroutine while Authorize
// writes to it from another.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestAuthorizeStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}

	done := make(chan error, 1)
	go func() {
		_, err := Authorize(ctx, "client-id", "client-secret", out)
		done <- err
	}()

	// Give Authorize time to print the consent URL before cancelling.
	deadline := time.After(2 * time.Second)
	for out.String() == "" {
		select {
		case <-deadline:
			t.Fatal("Authorize printed nothing")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Authorize succeeded, want a cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authorize did not return after cancellation")
	}

	printed := out.String()
	for _, want := range []string{
		"https://accounts.google.com/o/oauth2/auth",
		"access_type=offline",
		"prompt=consent",
		"client_id=client-id",
		"127.0.0.1",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("consent output missing %q:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "client-secret") {
		t.Error("the client secret must not appear in the consent URL")
	}
}

func TestBuildQuery(t *testing.T) {
	got := buildQuery(mail.Query{LookbackDays: 3, ExcludeLabels: []string{"Triage/Read", ""}})
	want := `in:inbox is:unread newer_than:3d -label:"Triage/Read"`
	if got != want {
		t.Errorf("buildQuery = %q, want %q", got, want)
	}
}

func TestChunk(t *testing.T) {
	if got := chunk(nil, 2); got != nil {
		t.Errorf("chunk(nil) = %v, want nil", got)
	}
	got := chunk([]string{"a", "b", "c"}, 2)
	if len(got) != 2 || strings.Join(got[0], "") != "ab" || strings.Join(got[1], "") != "c" {
		t.Errorf("chunk = %v", got)
	}
}
