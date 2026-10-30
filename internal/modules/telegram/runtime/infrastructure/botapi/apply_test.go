package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

func TestApplyRoutesExactlyOneOperation(t *testing.T) {
	for _, tc := range []struct {
		name, method, kind string
		message            c.Message
	}{
		{"panel text budget", "sendMessage", "text", c.Message{Purpose: "panel", Text: strings.Repeat("a", 4096)}},
		{"legacy", "sendMessage", "text", c.Message{Text: "hello"}},
		{"text", "editMessageText", "text", c.Message{Operation: "edit", Text: "<b>hello</b>", ParseMode: "HTML", TargetKind: "text"}},
		{"caption", "editMessageCaption", "photo", c.Message{Operation: "edit", Text: "caption", TargetKind: "photo"}},
		{"text to photo", "editMessageMedia", "photo", c.Message{Operation: "edit", Text: "caption", TargetKind: "text", FileID: "cached"}},
		{"photo to photo", "editMessageMedia", "photo", c.Message{Operation: "edit", Text: "caption", TargetKind: "photo", FileID: "cached"}},
		{"clear", "editMessageReplyMarkup", "photo", c.Message{Operation: "markup", TargetKind: "photo", Buttons: [][]c.Button{{{Text: "stale", Data: "stale"}}}}},
		{"delete", "deleteMessage", "text", c.Message{Operation: "delete", TargetKind: "text"}},
		{"photo URL", "sendPhoto", "photo", c.Message{PhotoURL: "https://unreachable.invalid/p.png", Text: "caption", ParseMode: "HTML"}},
		{"delivery file", "sendDocument", "document", c.Message{Purpose: "delivery", ForceDocument: true, Text: "short secret"}},
		{"legacy long", "sendDocument", "document", c.Message{Text: strings.Repeat("a", 4001)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/"+tc.method) {
					t.Error(r.URL.Path)
				}
				if tc.method == "sendDocument" {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					if r.FormValue("protect_content") != "true" {
						t.Error("delivery not protected")
					}
					f, h, err := r.FormFile("document")
					if err != nil {
						t.Fatal(err)
					}
					f.Close()
					if h.Filename != "order.txt" {
						t.Error(h.Filename)
					}
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(tc.method, "send") && body["protect_content"] != true {
						t.Error("send protection regressed")
					}
					if strings.HasPrefix(tc.method, "edit") || tc.method == "deleteMessage" {
						if body["message_id"] != float64(99) {
							t.Error(body)
						}
					}
					if tc.method == "editMessageReplyMarkup" {
						if len(body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)) != 0 {
							t.Error("markup not cleared")
						}
					}
					if tc.message.ParseMode != "" && tc.method != "editMessageMedia" && body["parse_mode"] != tc.message.ParseMode {
						t.Error("missing parse mode")
					}
				}
				if tc.method == "deleteMessage" {
					fmt.Fprint(w, `{"ok":true,"result":true}`)
				} else {
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"photo":[{"file_id":"returned","width":10,"height":10}]}}`)
				}
			}))
			defer server.Close()
			a := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
			m := tc.message
			m.ChatID = 1
			m.TargetMessageID = 99
			result, err := a.Apply(context.Background(), "dummy", m)
			if err != nil || result.MessageID != 99 || result.MessageKind != tc.kind || calls != 1 {
				t.Fatal(result, err, calls)
			}
			if tc.kind == "photo" && result.FileID != "returned" {
				t.Error(result)
			}
		})
	}
}

func TestApplyFailureNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		op, description, kind string
		status                int
		uncertain, success    bool
	}{
		{"edit", "Bad Request: message is not modified", "", 400, false, true},
		{"edit", "Bad Request: message to edit not found", "target_missing", 400, false, false},
		{"edit", "Bad Request: can't parse entities", "invalid_content", 400, false, false},
		{"edit", "Bad Gateway", "retryable", 502, false, false},
		{"send", "Bad Gateway", "unknown", 502, true, false},
	} {
		t.Run(tc.op+tc.description, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.status, "description": tc.description + " private token"})
			}))
			defer s.Close()
			a := Adapter{Options: []api.Option{api.WithBaseURL(s.URL)}}
			result, err := a.Apply(context.Background(), "dummy", c.Message{Operation: tc.op, TargetMessageID: 99, TargetKind: "text", ChatID: 1, Text: "secret"})
			if calls != 1 {
				t.Fatal("fallback request", calls)
			}
			if tc.success {
				if err != nil || result.MessageID != 99 {
					t.Fatal(result, err)
				}
				return
			}
			var e *sendError
			if !errors.As(err, &e) || e.Kind() != tc.kind || e.Uncertain() != tc.uncertain {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("leaked description")
			}
		})
	}
}

func TestPollMessageIdentityAndKinds(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"text":"input","chat":{"id":1,"type":"private"},"from":{"id":1}}},{"update_id":2,"callback_query":{"id":"cb","from":{"id":1},"message":{"message_id":11,"chat":{"id":1,"type":"private"},"photo":[{"file_id":"cached"}]}}}]}`)
	}))
	defer s.Close()
	a := Adapter{Options: []api.Option{api.WithBaseURL(s.URL)}}
	updates, err := a.Poll(context.Background(), "dummy", 0)
	if err != nil || len(updates) != 2 {
		t.Fatal(updates, err)
	}
	if updates[0].MessageID != 10 || updates[0].MessageKind != "text" || updates[1].MessageID != 11 || updates[1].MessageKind != "photo" {
		t.Fatal(updates)
	}
}

func TestApplyRejectsInvalidOperationWithoutRequest(t *testing.T) {
	a := Adapter{}
	for _, m := range []c.Message{{Operation: "edit", Text: "x"}, {Operation: "unknown", TargetMessageID: 1}, {Operation: "send", Photo: []byte("bad image")}} {
		_, err := a.Apply(context.Background(), "dummy", m)
		var e *sendError
		if !errors.As(err, &e) || e.Uncertain() {
			t.Fatal(err)
		}
	}
}

var _ c.ApplyingAPI = Adapter{}
