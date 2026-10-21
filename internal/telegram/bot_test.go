package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendAndEditPayload(t *testing.T) {
	var methods []string
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		payloads = append(payloads, body)
		io.WriteString(w, `{"ok":true,"result":{"message_id":987,"chat":{"id":42}}}`)
	}))
	defer server.Close()
	b := &Bot{HTTP: server.Client(), BaseURL: server.URL, Token: "123:FAKE", ChatID: 42}
	n := Notification{Text: "stock 3", Markup: Markup{InlineKeyboard: [][]Button{{{Text: "Buy", URL: "https://example.com/order?aff=1194"}}}}}
	id, err := b.Send(context.Background(), 42, n)
	if err != nil || id != 987 {
		t.Fatalf("send id=%d err=%v", id, err)
	}
	b.nextRequest = time.Time{}
	if err = b.Edit(context.Background(), 42, id, n); err != nil {
		t.Fatal(err)
	}
	if methods[0] != "/bot123:FAKE/sendMessage" || methods[1] != "/bot123:FAKE/editMessageText" {
		t.Fatalf("methods %v", methods)
	}
	if payloads[1]["message_id"] != float64(987) || payloads[0]["chat_id"] != float64(42) || payloads[0]["parse_mode"] != nil {
		t.Fatalf("payloads %+v", payloads)
	}
	markup := payloads[0]["reply_markup"].(map[string]any)
	if markup["inline_keyboard"] == nil {
		t.Fatal("purchase button missing")
	}
}

func TestPlainReplyOmitsEmptyMarkup(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["reply_markup"]; exists {
			t.Error("empty keyboard must not be sent")
		}
		if body["chat_id"] != float64(43) {
			t.Error("reply went to wrong admin")
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":10}}`)
	}))
	defer s.Close()
	b := &Bot{HTTP: s.Client(), BaseURL: s.URL, Token: "123:FAKE"}
	if err := b.ReplyTo(context.Background(), 43, "help"); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name, body                    string
		status                        int
		permanent, ambiguous, success bool
	}{
		{"not modified", `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`, 400, false, false, true},
		{"deleted", `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`, 400, true, false, false},
		{"not editable", `{"ok":false,"error_code":400,"description":"Bad Request: message can't be edited"}`, 400, true, false, false},
		{"temporary", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, 500, false, true, false},
		{"rate limit", `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":5}}`, 429, false, false, false},
		{"blocked chat", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, 403, false, false, false},
		{"bad JSON", `gateway failure`, 502, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer s.Close()
			b := &Bot{HTTP: s.Client(), BaseURL: s.URL, Token: "123:FAKE"}
			err := b.Edit(context.Background(), 42, 1, Notification{Text: "test"})
			if (err == nil) != tc.success || PermanentEdit(err) != tc.permanent || AmbiguousSend(err) != tc.ambiguous {
				t.Fatalf("classifications err=%v", err)
			}
			if tc.status == 429 && time.Until(b.nextRequest) < 4*time.Second {
				t.Fatal("retry_after not honored")
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("https://api.telegram.org/bot123:SECRET/sendMessage timed out")
}
func TestTransportErrorRedactsToken(t *testing.T) {
	b := &Bot{HTTP: &http.Client{Transport: failingTransport{}}, BaseURL: "https://api.telegram.org", Token: "123:SECRET"}
	_, err := b.Send(context.Background(), 42, Notification{Text: "test"})
	if err == nil || strings.Contains(err.Error(), "SECRET") || !AmbiguousSend(err) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func update(chat, from int64, text string) Update {
	var u Update
	u.Message = &Message{Text: text}
	u.Message.Chat.ID = chat
	u.Message.From.ID = from
	return u
}
func TestOnlyConfiguredAdminsInPrivateChats(t *testing.T) {
	admins := map[int64]bool{42: true, 43: true}
	for _, tc := range []struct {
		u    Update
		want string
		ok   bool
	}{
		{update(42, 42, "/check"), "/check", true}, {update(43, 43, "/STATUS@SpartanStockBot arg"), "/status", true},
		{update(44, 44, "/check"), "", false}, {update(-100123, 42, "/check"), "", false},
		{update(42, 44, "/check"), "", false}, {update(42, 42, "hello"), "", false}, {Update{}, "", false},
	} {
		command, ok := AuthorizedCommand(tc.u, admins)
		if command != tc.want || ok != tc.ok {
			t.Fatalf("auth got %s %t for %+v", command, ok, tc)
		}
	}
}

func TestPollingIgnoresUnauthorizedAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"message":{"from":{"id":999},"chat":{"id":999},"text":"/check"}},{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42},"text":"/status"}}]}`)
	}))
	defer s.Close()
	b := &Bot{HTTP: s.Client(), BaseURL: s.URL, Token: "123:FAKE", AdminIDs: map[int64]bool{42: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	count := 0
	b.Poll(ctx, func(_ context.Context, chat int64, command string) {
		count++
		if chat != 42 || command != "/status" {
			t.Errorf("unexpected command %d %s", chat, command)
		}
		cancel()
	})
	if count != 1 {
		t.Fatalf("handler count %d", count)
	}
}
