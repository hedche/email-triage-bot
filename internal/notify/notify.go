// Package notify delivers the per-run summary to a person and pings an
// external healthcheck. Both are optional: a Triage Run that cannot notify
// still succeeds, and the run never blocks on either for long.
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// timeout bounds every outbound call this package makes.
const timeout = 10 * time.Second

// telegramAPI is the default Telegram Bot API root. Tests override it through
// the unexported baseURL field of the telegram Notifier.
const telegramAPI = "https://api.telegram.org"

// Notifier delivers one summary message. Implementations must be safe to call
// from a single goroutine and must not retry for longer than their timeout.
type Notifier interface {
	Notify(ctx context.Context, text string) error
}

// telegram posts summaries to one chat through the Telegram Bot API.
type telegram struct {
	token   string
	chatID  string
	baseURL string // overridable in tests; defaults to telegramAPI
	client  *http.Client
}

// NewTelegram returns a Notifier that posts to chatID as the given bot.
func NewTelegram(token, chatID string) Notifier {
	return &telegram{
		token:   token,
		chatID:  chatID,
		baseURL: telegramAPI,
		client:  &http.Client{Timeout: timeout},
	}
}

// Notify posts text to the chat as a plain-text message with link previews
// disabled, so a summary full of sender domains does not unfurl.
func (t *telegram) Notify(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	form := url.Values{
		"chat_id":                  {t.chatID},
		"text":                     {text},
		"disable_web_page_preview": {"true"},
	}
	endpoint := strings.TrimSuffix(t.baseURL, "/") + "/bot" + t.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		// The token lives in the URL, so never let the transport error, which
		// quotes the URL, reach the logs verbatim.
		return fmt.Errorf("telegram sendMessage: %w", redact(err, t.token))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("telegram sendMessage: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// nop is the Notifier used when no notification channel is configured.
type nop struct{}

func (nop) Notify(context.Context, string) error { return nil }

// Nop returns a Notifier that discards every summary.
func Nop() Notifier { return nop{} }

// Healthcheck GETs url to report that a Triage Run finished cleanly. The run
// calls it only after a fully successful run, so a failure here is a failure
// of the ping, never of the triage.
func Healthcheck(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck ping: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("healthcheck ping: status %d", resp.StatusCode)
	}
	return nil
}

// redact replaces every occurrence of secret in err's message with "REDACTED".
func redact(err error, secret string) error {
	if secret == "" || err == nil {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), secret, "REDACTED")
	return fmt.Errorf("%s", msg)
}
