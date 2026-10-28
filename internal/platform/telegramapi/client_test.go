package telegramapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	options = append([]Option{WithBaseURL(server.URL)}, options...)
	client, err := NewClient("123:dummy_secret", options...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestJSONMethods(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("invalid request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch strings.TrimPrefix(r.URL.Path, "/bot123:dummy_secret/") {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":9007199254740,"is_bot":true,"username":"demo"}}`)
		case "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":"","pending_update_count":2}}`)
		case "getUpdates":
			if body["offset"] != float64(12) || body["timeout"] != float64(20) || len(body["allowed_updates"].([]any)) != 0 {
				t.Error(body)
			}
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":12,"callback_query":{"id":"cb","from":{"id":42},"data":"next","message":{"message_id":8,"chat":{"id":42,"type":"private"}}}}]}`)
		case "deleteWebhook":
			if _, exists := body["drop_pending_updates"]; !exists {
				t.Error("explicit flag omitted")
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "sendMessage":
			if body["chat_id"] != float64(42) || body["text"] != "hello" || body["reply_markup"] == nil {
				t.Error(body)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":42}}}`)
		case "answerCallbackQuery":
			if body["callback_query_id"] != "cb" {
				t.Error(body)
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			t.Error(r.URL.Path)
		}
	})
	ctx := context.Background()
	me, err := client.GetMe(ctx)
	if err != nil || me.ID != 9007199254740 {
		t.Fatal(me, err)
	}
	webhook, err := client.GetWebhookInfo(ctx)
	if err != nil || webhook.PendingUpdateCount != 2 {
		t.Fatal(webhook, err)
	}
	allowed := []string{}
	updates, err := client.GetUpdates(ctx, GetUpdatesRequest{Offset: 12, Timeout: 20, AllowedUpdates: &allowed})
	if err != nil || len(updates) != 1 || updates[0].CallbackQuery.Message.MessageID != 8 {
		t.Fatal(updates, err)
	}
	for _, drop := range []bool{false, true} {
		if err := client.DeleteWebhook(ctx, drop); err != nil {
			t.Fatal(err)
		}
	}
	message, err := client.SendMessage(ctx, SendMessageRequest{ChatID: 42, Text: "hello", ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "next", CallbackData: "next"}}}}})
	if err != nil || message.MessageID != 99 {
		t.Fatal(message, err)
	}
	if err := client.AnswerCallbackQuery(ctx, AnswerCallbackQueryRequest{CallbackQueryID: "cb"}); err != nil {
		t.Fatal(err)
	}
}

func TestMultipart(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nexample")
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		field, contentType, expected := "photo", "image/png", png
		if strings.HasSuffix(r.URL.Path, "sendDocument") {
			field, contentType, expected = "document", "application/octet-stream", []byte("document bytes")
		}
		file, header, err := r.FormFile(field)
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, expected) || header.Header.Get("Content-Type") != contentType || r.FormValue("chat_id") != "42" || r.FormValue("caption") != "caption" || r.FormValue("reply_markup") == "" {
			t.Error("invalid multipart")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7}}`)
	})
	keyboard := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	photo, err := client.SendPhoto(context.Background(), SendPhotoRequest{ChatID: 42, PNG: png, Caption: "caption", ReplyMarkup: keyboard})
	if err != nil || photo.MessageID != 7 {
		t.Fatal(photo, err)
	}
	doc, err := client.SendDocument(context.Background(), SendDocumentRequest{ChatID: 42, Filename: "export.txt", Data: []byte("document bytes"), Caption: "caption", ReplyMarkup: keyboard})
	if err != nil || doc.MessageID != 7 {
		t.Fatal(doc, err)
	}
	if _, err := client.SendPhoto(context.Background(), SendPhotoRequest{PNG: []byte("not png")}); err == nil {
		t.Fatal("accepted invalid PNG")
	}
}

func TestErrorsAreSafeAndBounded(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		api        bool
	}{
		{"rate limit", `{"ok":false,"error_code":429,"description":"secret-token https://private.invalid","parameters":{"retry_after":17}}`, 429, true},
		{"API error", `{"ok":false,"error_code":403,"description":"secret-token"}`, 200, true},
		{"bad gateway", `secret-token`, 502, true},
		{"malformed", `secret-token`, 200, false},
		{"null", `{"ok":true,"result":null}`, 200, false},
		{"wrong type", `{"ok":true,"result":"secret-token"}`, 200, false},
		{"too big", strings.Repeat("secret-token", 100), 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }, WithMaxResponseBytes(256))
			_, err := client.GetMe(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "https://") {
				t.Fatal(err)
			}
			var api *APIError
			if errors.As(err, &api) != test.api {
				t.Fatal(err)
			}
			if test.api && api.HTTPStatus != test.status {
				t.Fatal(api)
			}
			if test.status == 429 && (api.RetryAfter != 17 || api.ErrorCode != 429) {
				t.Fatal(api)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportRedactionAndCancellation(t *testing.T) {
	client, err := NewClient("123:dummy_secret", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("leaked %s private payload", r.URL)
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), "dummy_secret") || strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "payload") {
		t.Fatal(err)
	}
	client = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}, WithTimeout(20*time.Millisecond))
	_, err = client.GetMe(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.GetMe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRedirectRefused(t *testing.T) {
	followed := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer destination.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.GetMe(context.Background())
	if err == nil || followed {
		t.Fatal("redirect was followed", err)
	}
}
