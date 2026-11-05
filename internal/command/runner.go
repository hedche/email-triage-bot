package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// Runner handles one pass of Email Commands: it collects Apply/Discard taps,
// reads new commands from every Account, and saves the Overlay once.
type Runner struct {
	Store       Store
	Sources     []mail.CommandSource
	Interpreter Interpreter
	Messenger   Messenger
	Log         *slog.Logger

	Tag          string // plus-address tag, e.g. "triage" for someone+triage@gmail.com
	Label        string // applied to handled commands, e.g. "Triage/Command"
	LookbackDays int

	Now   func() time.Time
	NewID func() string
}

// Report counts what one pass did.
type Report struct {
	Commands int // verified commands read
	Applied  int // Changes applied (directly or by tap)
	Proposed int // Proposals sent for confirmation
	Ignored  int // messages to the plus-address that failed verification
}

// after is work done only once the Overlay is saved, so Telegram and the
// mailbox never claim something that did not persist.
type after struct {
	notices []string
	edits   map[int64]string
	answers map[string]string
	handled map[int]map[string]bool // source index -> message ids to label
}

// Run does one pass. A failure to save the Overlay fails the pass and nothing
// after it (notices, labels) happens; the commands are read again next time.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	r.defaults()
	var rep Report
	st, version, err := r.Store.Load(ctx)
	if err != nil {
		return rep, err
	}
	post := &after{edits: map[int64]string{}, answers: map[string]string{}, handled: map[int]map[string]bool{}}

	if err := r.handleTaps(ctx, st, post, &rep); err != nil {
		return rep, err
	}
	r.expire(st, post)
	for i, src := range r.Sources {
		if err := r.handleCommands(ctx, i, src, st, post, &rep); err != nil {
			return rep, fmt.Errorf("account %s: %w", src.Account(), err)
		}
	}

	if err := r.Store.Save(ctx, st, version); err != nil {
		return rep, fmt.Errorf("save overlay: %w", err)
	}

	for id, text := range post.answers {
		r.warn(r.Messenger.Answer(ctx, id, text), "answering a tap")
	}
	for msgID, text := range post.edits {
		r.warn(r.Messenger.Edit(ctx, msgID, text), "editing a proposal")
	}
	for _, n := range post.notices {
		_, err := r.Messenger.Send(ctx, n, nil)
		r.warn(err, "sending a notice")
	}
	for i, ids := range post.handled {
		if len(ids) == 0 {
			continue
		}
		r.label(ctx, r.Sources[i], ids)
	}
	return rep, nil
}

func (r *Runner) defaults() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.NewID == nil {
		r.NewID = randomID
	}
	if r.Log == nil {
		r.Log = slog.New(slog.DiscardHandler)
	}
	if r.Tag == "" {
		r.Tag = "triage"
	}
	if r.LookbackDays <= 0 {
		r.LookbackDays = 3
	}
}

func (r *Runner) handleTaps(ctx context.Context, st *State, post *after, rep *Report) error {
	taps, next, err := r.Messenger.Callbacks(ctx, st.TelegramOffset)
	if err != nil {
		return err
	}
	st.TelegramOffset = next
	for _, tap := range taps {
		if tap.ChatID != r.Messenger.ChatID() {
			post.answers[tap.ID] = "Not allowed."
			r.Log.Warn("ignored a tap from another chat", "chat_id", tap.ChatID)
			continue
		}
		action, id, _ := strings.Cut(tap.Data, ":")
		p, ok := st.takePending(id)
		if !ok {
			post.answers[tap.ID] = "That proposal is no longer pending."
			continue
		}
		// Describe before applying: a removed rule can no longer be looked up.
		text := describe(p, st)
		switch action {
		case "apply":
			rep.Applied += r.apply(st, p.Add, p.Remove)
			post.answers[tap.ID] = "Applied."
			post.edits[tap.MessageID] = "✅ <b>Applied</b>\n" + text
		default:
			post.answers[tap.ID] = "Discarded."
			post.edits[tap.MessageID] = "🗑 <b>Discarded</b>\n" + text
		}
	}
	return nil
}

func (r *Runner) expire(st *State, post *after) {
	now := r.Now()
	kept := st.Pending[:0:0]
	for _, p := range st.Pending {
		if now.Sub(p.CreatedAt) > pendingTTL {
			if p.TelegramMessageID != 0 {
				post.edits[p.TelegramMessageID] = "⌛ <b>Expired</b>, nothing changed\n" + describe(p, st)
			}
			continue
		}
		kept = append(kept, p)
	}
	st.Pending = kept
}

func (r *Runner) handleCommands(ctx context.Context, i int, src mail.CommandSource, st *State, post *after, rep *Report) error {
	account := strings.ToLower(src.Account())
	plus, err := PlusAddress(account, r.Tag)
	if err != nil {
		return err
	}
	msgs, err := src.ListCommands(ctx, plus, r.LookbackDays, 20)
	if err != nil {
		return err
	}
	// Mail to your own plus-address is stored twice: the sent message, and a
	// delivered copy without the SENT label but with the same Message-ID. The
	// copy is not a spoof; it is handled silently alongside the command.
	verifiedIDs := map[string]bool{}
	for _, m := range msgs {
		if m.MessageID != "" && Verified(m, account, plus) {
			verifiedIDs[m.MessageID] = true
		}
	}
	if post.handled[i] == nil {
		post.handled[i] = map[string]bool{}
	}

	// Oldest first, so two commands sent a minute apart apply in order.
	for j := len(msgs) - 1; j >= 0; j-- {
		m := msgs[j]
		key := account + "/" + m.ID
		if st.seen(key) {
			continue
		}
		st.markSeen(key)
		if !m.Sent && m.MessageID != "" && verifiedIDs[m.MessageID] {
			post.handled[i][m.ID] = true
			continue
		}
		if !Verified(m, account, plus) {
			rep.Ignored++
			r.Log.Warn("ignored a message to the command address that the account did not send",
				"account", account, "message_id", m.ID, "from", m.From, "sent", m.Sent)
			post.notices = append(post.notices, fmt.Sprintf(
				"⚠️ Ignored a message to <code>%s</code> from <code>%s</code>: only mail sent from %s itself is accepted.",
				esc(plus), esc(m.From), esc(account)))
			continue
		}
		rep.Commands++
		post.handled[i][m.ID] = true
		r.handleOne(ctx, account, m, st, post, rep)
	}
	return nil
}

func (r *Runner) handleOne(ctx context.Context, account string, m mail.CommandMessage, st *State, post *after, rep *Report) {
	instruction, fwd := Split(m.Subject, m.Body)
	if strings.TrimSpace(m.Body) == "" {
		instruction, fwd = Split(m.Subject, m.HTMLText)
	} else if fwd == nil && m.HTMLText != "" {
		// The plain text lost the forwarded headers (some clients put them only
		// in the HTML part). Take the sender and subject from there; the note
		// still comes from the plain text.
		if _, htmlFwd := Split("", m.HTMLText); htmlFwd != nil {
			fwd = htmlFwd
			instruction, _ = Split("", m.Body)
		}
	}
	fwdDomain := ""
	if fwd != nil {
		fwdDomain = fwd.Domain()
	}
	r.Log.Info("email command read", "account", account, "message_id", m.ID,
		"forwarded", fwd != nil, "forwarded_domain", fwdDomain,
		"plain_chars", len(m.Body), "html_chars", len(m.HTMLText))
	head := fmt.Sprintf("📮 <b>%s</b>: “%s”", esc(account), esc(shorten(instruction, 200)))
	if instruction == "" {
		post.notices = append(post.notices, head+"\nNo instruction found. Write what you want above the forwarded message.")
		return
	}

	req := Request{Account: account, Instruction: instruction, Forwarded: fwd, Existing: st.Changes}
	raw, err := r.Interpreter.Interpret(ctx, req)
	if err != nil {
		r.Log.Error("interpreting an email command failed", "account", account, "message_id", m.ID, "error", err.Error())
		post.notices = append(post.notices, head+"\n❌ Couldn't interpret it ("+esc(err.Error())+"). Nothing changed; send it again.")
		return
	}
	in := Ground(req, raw)
	r.Log.Info("email command interpreted", "account", account, "message_id", m.ID,
		"add", len(in.Add), "remove", len(in.Remove), "rejected", len(in.Rejected))

	var now, later []triage.Change
	for _, c := range in.Add {
		if c.Protective() {
			now = append(now, c)
		} else {
			later = append(later, c)
		}
	}
	// Removing a rule that marks mail read is protective; removing a protect
	// rule is not.
	var removeNow, removeLater []string
	for _, id := range in.Remove {
		if c, ok := st.change(id); ok && !c.Protective() {
			removeNow = append(removeNow, id)
		} else {
			removeLater = append(removeLater, id)
		}
	}

	var b strings.Builder
	b.WriteString(head)
	if fwd == nil {
		b.WriteString("\n<i>No forwarded email found in your message, so only your note was used.</i>")
	}
	if len(now) > 0 || len(removeNow) > 0 {
		lines := removedLines(st, removeNow)
		rep.Applied += r.apply(st, now, removeNow)
		b.WriteString("\n✅ <b>Applied</b>")
		for _, c := range now {
			b.WriteString("\n• " + esc(c.Describe()))
		}
		for _, l := range lines {
			b.WriteString("\n• removed: " + esc(l))
		}
	}
	if len(in.Rejected) > 0 {
		b.WriteString("\n⚠️ <b>Skipped</b>")
		for _, l := range in.Rejected {
			b.WriteString("\n• " + esc(l))
		}
	}
	if in.Reply != "" {
		b.WriteString("\n<i>" + esc(in.Reply) + "</i>")
	}

	if len(later) == 0 && len(removeLater) == 0 {
		if len(now) == 0 && len(removeNow) == 0 && len(in.Rejected) == 0 && in.Reply == "" {
			b.WriteString("\nNothing to change.")
		}
		post.notices = append(post.notices, b.String())
		return
	}

	p := Proposal{
		ID:          r.NewID(),
		Account:     account,
		Instruction: shorten(instruction, 300),
		Add:         later,
		Remove:      removeLater,
		CreatedAt:   r.Now().UTC(),
	}
	b.WriteString("\n❓ <b>Needs your OK</b>\n" + describe(p, st))
	msgID, err := r.Messenger.Send(ctx, b.String(), []Button{
		{Text: "Apply", Data: "apply:" + p.ID},
		{Text: "Discard", Data: "discard:" + p.ID},
	})
	if err != nil {
		r.Log.Error("sending a proposal failed; it was dropped", "account", account, "error", err.Error())
		return
	}
	p.TelegramMessageID = msgID
	st.Pending = append(st.Pending, p)
	rep.Proposed++
}

// apply adds Changes (with fresh IDs) and removes the named ones. It returns
// how many rules changed.
func (r *Runner) apply(st *State, add []triage.Change, remove []string) int {
	removed := st.removeChanges(remove)
	for _, c := range add {
		c.ID = r.NewID()
		c.CreatedAt = r.Now().UTC()
		st.Changes = append(st.Changes, c)
	}
	return len(add) + len(removed)
}

func (r *Runner) label(ctx context.Context, src mail.CommandSource, ids map[string]bool) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	labelID := ""
	if r.Label != "" {
		got, err := src.EnsureLabels(ctx, []string{r.Label})
		if err != nil {
			r.warn(err, "creating the command label")
		}
		labelID = got[r.Label]
	}
	r.warn(src.MarkRead(ctx, list, labelID), "marking commands read")
}

func (r *Runner) warn(err error, what string) {
	if err != nil {
		r.Log.Warn(what+" failed", "error", err.Error())
	}
}

// Verified reports whether m is a genuine Email Command for account: Gmail
// says the account itself sent it, From is the account, and it was addressed
// to the account's plus-address. The SENT label is the proof; the other two
// stop an ordinary sent message that merely mentions the address from counting.
func Verified(m mail.CommandMessage, account, plus string) bool {
	if !m.Sent || !strings.EqualFold(m.From, account) {
		return false
	}
	for _, rcpt := range m.Recipients {
		if strings.EqualFold(rcpt, plus) {
			return true
		}
	}
	return false
}

// PlusAddress returns local+tag@domain for an account address.
func PlusAddress(account, tag string) (string, error) {
	local, domain, ok := strings.Cut(account, "@")
	if !ok || local == "" || domain == "" {
		return "", fmt.Errorf("account %q is not an email address", account)
	}
	if i := strings.IndexByte(local, '+'); i >= 0 {
		local = local[:i]
	}
	return local + "+" + tag + "@" + domain, nil
}

func describe(p Proposal, st *State) string {
	var b strings.Builder
	for _, c := range p.Add {
		b.WriteString("• " + esc(c.Describe()) + "\n")
	}
	for _, l := range removedLines(st, p.Remove) {
		b.WriteString("• remove: " + esc(l) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func removedLines(st *State, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if c, ok := st.change(id); ok {
			out = append(out, c.Describe())
		} else {
			out = append(out, id+" (already gone)")
		}
	}
	return out
}

func esc(s string) string { return html.EscapeString(s) }

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
