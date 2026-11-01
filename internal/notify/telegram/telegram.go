// Package telegram sends alerts through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf16"

	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/notify"
)

const (
	maxText        = 4096 // Telegram's limit, in UTF-16 code units after entity parsing
	maxBody        = 64 << 10
	maxUpdatesBody = 1 << 20 // up to 100 updates of up to 4096 characters
)

// Bot sends messages to one chat.
type Bot struct {
	name   string
	api    string
	token  string
	chatID string
	http   *http.Client
}

// New returns a bot. api is the Bot API root, normally https://api.telegram.org.
func New(name, api, token, chatID string, hc *http.Client) *Bot {
	return &Bot{name: name, api: api, token: token, chatID: chatID, http: hc}
}

func (b *Bot) Name() string { return b.name }

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send implements notify.Notifier. The subject is shown in bold. All text is
// HTML-escaped, so node data (e.g. error strings) can't inject markup.
func (b *Bot) Send(ctx context.Context, m notify.Message) error {
	// The limit applies to the unescaped text, measured in UTF-16 units.
	text := truncate(m.Body, maxText-utf16Len(m.Subject)-2)
	payload, err := json.Marshal(map[string]any{
		"chat_id":                  b.chatID,
		"text":                     "<b>" + html.EscapeString(m.Subject) + "</b>\n\n" + html.EscapeString(text),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return notify.Permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/bot"+b.token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return notify.Permanent(fmt.Errorf("build request: %w", httpx.Redact(err)))
	}
	req.Header.Set("Content-Type", "application/json")

	status, body, err := httpx.Do(b.http, req, maxBody)
	if err != nil {
		return err // network trouble: retry
	}
	var r apiResponse
	_ = json.Unmarshal(body, &r) // best effort: error bodies may be anything
	if status == http.StatusOK && r.OK {
		return nil
	}
	err = fmt.Errorf("telegram: HTTP %d: %s", status, r.Description)
	switch {
	case status == http.StatusTooManyRequests:
		return &notify.RetryAfter{After: max(time.Duration(r.Parameters.RetryAfter)*time.Second, time.Second), Err: err}
	case status >= 500:
		return err
	default:
		// 400 bad chat id, 401 bad token, 403 bot blocked: retrying won't help.
		return notify.Permanent(err)
	}
}

// ErrBadToken is returned by CheckToken when Telegram rejects the token.
var ErrBadToken = errors.New("bot token rejected")

// CheckToken calls getMe, which needs only a valid token, and returns the
// bot's username. It sends nothing, so the chat id is not tested.
func (b *Bot) CheckToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api+"/bot"+b.token+"/getMe", nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", httpx.Redact(err))
	}
	status, body, err := httpx.Do(b.http, req, maxBody)
	if err != nil {
		return "", err
	}
	var r struct {
		apiResponse
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	_ = json.Unmarshal(body, &r) // best effort, as in Send
	switch {
	case status == http.StatusOK && r.OK:
		return r.Result.Username, nil
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		return "", fmt.Errorf("telegram: %w (HTTP %d)", ErrBadToken, status)
	}
	return "", fmt.Errorf("telegram: HTTP %d: %s", status, r.Description)
}

// ErrConflict is Updates' error for HTTP 409: the bot has a webhook, or
// another program reads its updates.
var ErrConflict = errors.New("getUpdates conflicts with a webhook or another program reading the bot's updates")

// Update is one update the bot received. Text and Chat are empty for
// updates that are not messages.
type Update struct {
	ID   int64
	Text string
	Chat Chat
}

// Chat is the chat of a message. The names come from Telegram users: they
// may contain anything.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // private, group, supergroup or channel
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Updates calls getUpdates, which waits up to wait for updates (a long
// poll). offset is the ID of the first update to return: one more than the
// last one seen confirms that one and all before it. ctx must outlast wait.
func (b *Bot) Updates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	q := url.Values{"offset": {strconv.FormatInt(offset, 10)}, "timeout": {strconv.Itoa(int(wait.Seconds()))}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api+"/bot"+b.token+"/getUpdates?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", httpx.Redact(err))
	}
	status, body, err := httpx.Do(b.http, req, maxUpdatesBody)
	if err != nil {
		return nil, err
	}
	var r struct {
		apiResponse
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				Text string `json:"text"`
				Chat Chat   `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	jerr := json.Unmarshal(body, &r)
	switch {
	case status == http.StatusConflict:
		return nil, fmt.Errorf("telegram: %w (HTTP 409: %s)", ErrConflict, r.Description)
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		return nil, fmt.Errorf("telegram: %w (HTTP %d)", ErrBadToken, status)
	case status != http.StatusOK || !r.OK:
		return nil, fmt.Errorf("telegram: HTTP %d: %s", status, r.Description)
	case jerr != nil:
		return nil, fmt.Errorf("telegram: decode getUpdates: %w", jerr)
	}
	us := make([]Update, 0, len(r.Result))
	for _, u := range r.Result {
		up := Update{ID: u.UpdateID}
		if u.Message != nil {
			up.Text, up.Chat = u.Message.Text, u.Message.Chat
		}
		us = append(us, up)
	}
	return us, nil
}

func utf16Len(s string) (n int) {
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// truncate cuts s to at most limit UTF-16 units, ending with "…" if cut.
func truncate(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}
	n := 1 // the ellipsis
	for i, r := range s {
		if n += utf16.RuneLen(r); n > limit {
			return s[:i] + "…"
		}
	}
	return s
}
