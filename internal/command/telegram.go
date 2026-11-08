package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Button is one inline keyboard button; Data comes back in a Callback.
type Button struct {
	Text string
	Data string
}

// Callback is one tap on an inline button.
type Callback struct {
	ID        string
	ChatID    int64
	MessageID int64
	Data      string
}

// Messenger is the Telegram surface the runner needs. The command subcommand
// runs as a short scheduled Job, so taps are collected with getUpdates on the
// next run rather than by a webhook.
type Messenger interface {
	Send(ctx context.Context, html string, buttons []Button) (messageID int64, err error)
	Edit(ctx context.Context, messageID int64, html string) error
	Answer(ctx context.Context, callbackID, text string) error
	// Callbacks returns taps with update_id >= offset and the offset to pass
	// next time, which also confirms these updates to Telegram. It waits up to
	// wait for the first one (long polling); zero returns at once.
	Callbacks(ctx context.Context, offset int64, wait time.Duration) ([]Callback, int64, error)
	ChatID() int64
}

// Telegram talks to the Bot API.
type Telegram struct {
	token   string
	chatID  int64
	baseURL string
	client  *http.Client
}

// NewTelegram builds a Messenger for one chat. The bot token must not be one
// that has a webhook set: Telegram refuses getUpdates while a webhook exists.
func NewTelegram(token, chatID string) (*Telegram, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram chat id %q: must be a number", chatID)
	}
	if token == "" {
		return nil, fmt.Errorf("telegram bot token is required for email commands")
	}
	return &Telegram{token: token, chatID: id, baseURL: "https://api.telegram.org", client: &http.Client{}}, nil
}

func (t *Telegram) ChatID() int64 { return t.chatID }

func (t *Telegram) Send(ctx context.Context, html string, buttons []Button) (int64, error) {
	req := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if len(buttons) > 0 {
		row := make([]map[string]string, 0, len(buttons))
		for _, b := range buttons {
			row = append(row, map[string]string{"text": b.Text, "callback_data": b.Data})
		}
		req["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{row}}
	}
	var res struct {
		MessageID int64 `json:"message_id"`
	}
	if err := t.call(ctx, "sendMessage", req, &res); err != nil {
		return 0, err
	}
	return res.MessageID, nil
}

// Edit replaces a message's text and removes its buttons.
func (t *Telegram) Edit(ctx context.Context, messageID int64, html string) error {
	return t.call(ctx, "editMessageText", map[string]any{
		"chat_id":                  t.chatID,
		"message_id":               messageID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": [][]any{}},
	}, nil)
}

func (t *Telegram) Answer(ctx context.Context, callbackID, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}

func (t *Telegram) Callbacks(ctx context.Context, offset int64, wait time.Duration) ([]Callback, int64, error) {
	var updates []struct {
		UpdateID      int64 `json:"update_id"`
		CallbackQuery *struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	err := t.callWithin(ctx, wait+requestTimeout, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(wait / time.Second),
		"allowed_updates": []string{"callback_query"},
	}, &updates)
	if err != nil {
		return nil, offset, err
	}
	next := offset
	var out []Callback
	for _, u := range updates {
		if u.UpdateID >= next {
			next = u.UpdateID + 1
		}
		q := u.CallbackQuery
		if q == nil || q.Message == nil {
			continue
		}
		out = append(out, Callback{ID: q.ID, ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, Data: q.Data})
	}
	return out, next, nil
}

// requestTimeout bounds every Bot API call except the wait of a long poll.
const requestTimeout = 15 * time.Second

func (t *Telegram) call(ctx context.Context, method string, body any, result any) error {
	return t.callWithin(ctx, requestTimeout, method, body, result)
}

func (t *Telegram) callWithin(ctx context.Context, limit time.Duration, method string, body any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(t.baseURL, "/")+"/bot"+t.token+"/"+method, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// The token is in the URL; never let the transport error quote it.
		return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), t.token, "REDACTED"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: status %d", method, resp.StatusCode)
	}
	if !env.OK {
		if resp.StatusCode == http.StatusConflict {
			return fmt.Errorf("telegram %s: %s — this bot token has a webhook or another poller; give email commands their own bot (COMMANDS_TELEGRAM_BOT_TOKEN)", method, env.Description)
		}
		return fmt.Errorf("telegram %s: status %d: %s", method, resp.StatusCode, env.Description)
	}
	if result != nil {
		return json.Unmarshal(env.Result, result)
	}
	return nil
}
