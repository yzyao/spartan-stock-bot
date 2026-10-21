package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
	Ambiguous   bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram API code=%d: %s", e.Code, e.Description)
}
func PermanentEdit(err error) bool {
	var e *APIError
	if !errors.As(err, &e) || e.Code != 400 {
		return false
	}
	s := strings.ToLower(e.Description)
	return strings.Contains(s, "message to edit not found") || strings.Contains(s, "message can't be edited") || strings.Contains(s, "message can not be edited") || strings.Contains(s, "message_id_invalid")
}
func AmbiguousSend(err error) bool { var e *APIError; return errors.As(err, &e) && e.Ambiguous }

type Message struct {
	MessageID int64 `json:"message_id"`
	From      struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Bot struct {
	HTTP        *http.Client
	BaseURL     string
	Token       string
	ChatID      int64
	AdminIDs    map[int64]bool
	Log         *slog.Logger
	mu          sync.Mutex
	nextRequest time.Time
}

func (b *Bot) call(ctx context.Context, method string, payload any, result any) error {
	if err := b.throttle(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/bot"+b.Token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SpartanStockBot/1.0")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		// net/http errors include the token-bearing URL. Never return/log the raw error.
		return &APIError{Description: "transport failure (response unknown)", Ambiguous: true}
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
		return &APIError{Code: resp.StatusCode, Description: "invalid API response", Ambiguous: true}
	}
	if !envelope.OK {
		e := &APIError{Code: envelope.ErrorCode, Description: strings.ReplaceAll(envelope.Description, b.Token, "[REDACTED]"), RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second, Ambiguous: resp.StatusCode >= 500}
		if e.Code == 0 {
			e.Code = resp.StatusCode
		}
		if e.RetryAfter > 0 {
			b.mu.Lock()
			b.nextRequest = time.Now().Add(e.RetryAfter)
			b.mu.Unlock()
		}
		if method == "editMessageText" && e.Code == 400 && strings.Contains(strings.ToLower(e.Description), "message is not modified") {
			return nil
		}
		return e
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{Code: resp.StatusCode, Description: "unexpected HTTP status", Ambiguous: true}
	}
	if result != nil && json.Unmarshal(envelope.Result, result) != nil {
		return &APIError{Description: "invalid API result", Ambiguous: true}
	}
	return nil
}

func (b *Bot) throttle(ctx context.Context) error {
	for {
		b.mu.Lock()
		delay := time.Until(b.nextRequest)
		if delay <= 0 {
			b.nextRequest = time.Now().Add(time.Second)
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (b *Bot) Send(ctx context.Context, chat int64, n Notification) (int64, error) {
	var result Message
	err := b.call(ctx, "sendMessage", messagePayload(chat, n), &result)
	if err == nil && result.MessageID <= 0 {
		err = &APIError{Description: "missing message ID", Ambiguous: true}
	}
	return result.MessageID, err
}
func (b *Bot) Edit(ctx context.Context, chat, id int64, n Notification) error {
	payload := messagePayload(chat, n)
	payload["message_id"] = id
	return b.call(ctx, "editMessageText", payload, nil)
}

func messagePayload(chat int64, n Notification) map[string]any {
	payload := map[string]any{"chat_id": chat, "text": n.Text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if len(n.Markup.InlineKeyboard) > 0 {
		payload["reply_markup"] = n.Markup
	}
	return payload
}
func (b *Bot) Reply(ctx context.Context, text string) error {
	return b.ReplyTo(ctx, b.ChatID, text)
}
func (b *Bot) ReplyTo(ctx context.Context, chatID int64, text string) error {
	_, err := b.Send(ctx, chatID, Notification{Text: text})
	return err
}

// Poll handles commands synchronously; the caller launches /check separately so polling stays responsive.
func (b *Bot) Poll(ctx context.Context, handle func(context.Context, int64, string)) {
	var offset int64
	backoff := time.Second
	pollSeconds := int(b.HTTP.Timeout.Seconds()) - 3
	if pollSeconds > 25 {
		pollSeconds = 25
	}
	if pollSeconds < 1 {
		pollSeconds = 1
	}
	for ctx.Err() == nil {
		var updates []Update
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": pollSeconds, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.Log.Error("telegram_poll_error", "error", err)
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			if command, ok := AuthorizedCommand(u, b.AdminIDs); ok {
				handle(ctx, u.Message.Chat.ID, command)
			}
		}
	}
}

func AuthorizedCommand(u Update, admins map[int64]bool) (string, bool) {
	if u.Message == nil || u.Message.From.IsBot || !admins[u.Message.From.ID] || u.Message.Chat.ID != u.Message.From.ID || u.Message.Chat.ID <= 0 {
		return "", false
	}
	fields := strings.Fields(u.Message.Text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	return strings.ToLower(strings.SplitN(fields[0], "@", 2)[0]), true
}
