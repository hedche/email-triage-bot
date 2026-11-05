package command

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

type memStore struct {
	st    *State
	saves int
}

func (m *memStore) Load(context.Context) (*State, string, error) {
	if m.st == nil {
		m.st = &State{}
	}
	cp := *m.st
	cp.Changes = append([]triage.Change(nil), m.st.Changes...)
	cp.Pending = append([]Proposal(nil), m.st.Pending...)
	cp.Seen = append([]string(nil), m.st.Seen...)
	return &cp, "v", nil
}

func (m *memStore) Save(_ context.Context, st *State, _ string) error {
	m.st = st
	m.saves++
	return nil
}

type fakeSource struct {
	account string
	msgs    []mail.CommandMessage
	read    []string
}

func (f *fakeSource) Account() string { return f.account }
func (f *fakeSource) ListCommands(_ context.Context, plus string, _, _ int) ([]mail.CommandMessage, error) {
	if plus != "me+triage@example.com" {
		return nil, fmt.Errorf("plus address %q", plus)
	}
	return f.msgs, nil
}
func (f *fakeSource) EnsureLabels(_ context.Context, names []string) (map[string]string, error) {
	return map[string]string{names[0]: "L1"}, nil
}
func (f *fakeSource) MarkRead(_ context.Context, ids []string, _ string) error {
	f.read = append(f.read, ids...)
	return nil
}

type fakeInterp struct{ raw Raw }

func (f fakeInterp) Name() string { return "fake" }
func (f fakeInterp) Interpret(context.Context, Request) (Raw, error) {
	return f.raw, nil
}

type sent struct {
	text    string
	buttons []Button
}

type fakeMessenger struct {
	sent  []sent
	edits map[int64]string
	taps  []Callback
}

func (f *fakeMessenger) ChatID() int64 { return 42 }
func (f *fakeMessenger) Send(_ context.Context, text string, b []Button) (int64, error) {
	f.sent = append(f.sent, sent{text, b})
	return int64(100 + len(f.sent)), nil
}
func (f *fakeMessenger) Edit(_ context.Context, id int64, text string) error {
	if f.edits == nil {
		f.edits = map[int64]string{}
	}
	f.edits[id] = text
	return nil
}
func (f *fakeMessenger) Answer(context.Context, string, string) error { return nil }
func (f *fakeMessenger) Callbacks(_ context.Context, offset int64) ([]Callback, int64, error) {
	taps := f.taps
	f.taps = nil
	return taps, offset + int64(len(taps)), nil
}

func command(id string, sent bool, from string) mail.CommandMessage {
	return mail.CommandMessage{
		ID: id, Sent: sent, From: from, Recipients: []string{"me+triage@example.com"},
		Subject: "Fwd: Weekly news",
		Body:    "hide these but never touch the boss\n\n---------- Forwarded message ---------\nFrom: News <digest@news.example>\nSubject: Weekly news\n",
	}
}

func newRunner(store Store, src *fakeSource, msgr *fakeMessenger, raw Raw) *Runner {
	n := 0
	return &Runner{
		Store: store, Sources: []mail.CommandSource{src}, Interpreter: fakeInterp{raw}, Messenger: msgr,
		Label: "Triage/Command",
		Now:   func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) },
		NewID: func() string { n++; return fmt.Sprintf("id%d", n) },
	}
}

func TestRunAppliesProtectiveAndProposesTheRest(t *testing.T) {
	store := &memStore{}
	src := &fakeSource{account: "me@example.com", msgs: []mail.CommandMessage{command("m1", true, "me@example.com")}}
	msgr := &fakeMessenger{}
	raw := Raw{Changes: []RawChange{
		{Op: "read_sender", Value: "digest@news.example", Note: "newsletter"},
		{Op: "protect_keyword", Value: "the boss"},
	}}
	r := newRunner(store, src, msgr, raw)

	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Commands != 1 || rep.Applied != 1 || rep.Proposed != 1 {
		t.Errorf("report %+v", rep)
	}
	if len(store.st.Changes) != 1 || store.st.Changes[0].Op != triage.OpProtectKeyword {
		t.Errorf("changes %+v", store.st.Changes)
	}
	if len(store.st.Pending) != 1 || store.st.Pending[0].Add[0].Op != triage.OpReadSender {
		t.Fatalf("pending %+v", store.st.Pending)
	}
	if len(msgr.sent) != 1 || len(msgr.sent[0].buttons) != 2 || !strings.Contains(msgr.sent[0].text, "Needs your OK") {
		t.Errorf("sent %+v", msgr.sent)
	}
	if strings.Join(src.read, ",") != "m1" {
		t.Errorf("marked read %v", src.read)
	}

	// The same command is not acted on twice.
	if rep, _ := r.Run(context.Background()); rep.Commands != 0 {
		t.Errorf("second pass re-read the command: %+v", rep)
	}

	// Tapping Apply moves the proposal into the Overlay.
	msgr.taps = []Callback{{ID: "c1", ChatID: 42, MessageID: 101, Data: "apply:" + store.st.Pending[0].ID}}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.st.Pending) != 0 || len(store.st.Changes) != 2 {
		t.Errorf("after apply: pending %d, changes %+v", len(store.st.Pending), store.st.Changes)
	}
	if !strings.Contains(msgr.edits[101], "Applied") {
		t.Errorf("proposal not edited: %q", msgr.edits[101])
	}
}

func TestRunIgnoresUnverifiedCommandsAndForeignTaps(t *testing.T) {
	store := &memStore{st: &State{Pending: []Proposal{{
		ID: "p1", CreatedAt: time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC),
		Add: []triage.Change{{Op: triage.OpReadDomain, Value: "news.example"}},
	}}}}
	src := &fakeSource{account: "me@example.com", msgs: []mail.CommandMessage{
		command("spoof1", false, "me@example.com"),      // arrived from outside: no SENT label
		command("spoof2", true, "someone@else.example"), // impossible in practice, still refused
	}}
	msgr := &fakeMessenger{taps: []Callback{{ID: "c1", ChatID: 7, MessageID: 5, Data: "apply:p1"}}}
	r := newRunner(store, src, msgr, Raw{Changes: []RawChange{{Op: "read_domain", Value: "news.example"}}})

	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Ignored != 2 || rep.Commands != 0 {
		t.Errorf("report %+v", rep)
	}
	if len(store.st.Changes) != 0 || len(store.st.Pending) != 1 {
		t.Errorf("state changed: %+v", store.st)
	}
	if len(src.read) != 0 {
		t.Errorf("unverified messages were marked read: %v", src.read)
	}
}

func TestRunHandlesTheDeliveredCopySilently(t *testing.T) {
	store := &memStore{}
	sentMsg := command("m-sent", true, "me@example.com")
	sentMsg.MessageID = "<abc@mail.gmail.com>"
	copyMsg := command("m-copy", false, "me@example.com")
	copyMsg.MessageID = "<abc@mail.gmail.com>"
	spoof := command("m-spoof", false, "me@example.com")
	spoof.MessageID = "<other@evil.example>"
	src := &fakeSource{account: "me@example.com", msgs: []mail.CommandMessage{copyMsg, sentMsg, spoof}}
	msgr := &fakeMessenger{}
	r := newRunner(store, src, msgr, Raw{Changes: []RawChange{{Op: "protect_keyword", Value: "the boss"}}})

	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Commands != 1 || rep.Ignored != 1 {
		t.Errorf("report %+v: the copy must not count as ignored, the spoof must", rep)
	}
	read := strings.Join(src.read, ",")
	if !strings.Contains(read, "m-sent") || !strings.Contains(read, "m-copy") || strings.Contains(read, "m-spoof") {
		t.Errorf("marked read %q", read)
	}
	warnings := 0
	for _, s := range msgr.sent {
		if strings.Contains(s.text, "Ignored") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("%d ignore warnings, want 1 (the spoof only)", warnings)
	}
}

func TestRunExpiresOldProposals(t *testing.T) {
	store := &memStore{st: &State{Pending: []Proposal{{ID: "old", TelegramMessageID: 9, CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}}}
	msgr := &fakeMessenger{}
	r := newRunner(store, &fakeSource{account: "me@example.com"}, msgr, Raw{})
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.st.Pending) != 0 || !strings.Contains(msgr.edits[9], "Expired") {
		t.Errorf("pending %+v, edits %v", store.st.Pending, msgr.edits)
	}
}

func TestPlusAddress(t *testing.T) {
	got, err := PlusAddress("first.last+old@gmail.com", "triage")
	if err != nil || got != "first.last+triage@gmail.com" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestRunFallsBackToHTMLForForwardedHeaders(t *testing.T) {
	store := &memStore{}
	m := command("m1", true, "me@example.com")
	m.Body = "mark these as done\n"
	m.HTMLText = "mark these as done\nBegin forwarded message:\nFrom: News <digest@news.example>\nSubject: Weekly news\n"
	src := &fakeSource{account: "me@example.com", msgs: []mail.CommandMessage{m}}
	var seen Request
	interp := recordingInterp{req: &seen}
	r := newRunner(store, src, &fakeMessenger{}, Raw{})
	r.Interpreter = interp
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen.Instruction != "mark these as done" || seen.Forwarded == nil || seen.Forwarded.Address != "digest@news.example" {
		t.Errorf("request %+v (forwarded %+v)", seen, seen.Forwarded)
	}
}

type recordingInterp struct{ req *Request }

func (recordingInterp) Name() string { return "recording" }
func (f recordingInterp) Interpret(_ context.Context, req Request) (Raw, error) {
	*f.req = req
	return Raw{}, nil
}
