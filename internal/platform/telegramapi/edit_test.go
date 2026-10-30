package telegramapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestEditAndDeleteJSON(t *testing.T) {
	methods := []string{}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods = append(methods, method)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != float64(42) || body["message_id"] != float64(9) {
			t.Errorf("target: %v", body)
		}
		switch method {
		case "editMessageText", "editMessageCaption":
			if body["parse_mode"] != "HTML" {
				t.Error("missing parse mode")
			}
		case "editMessageMedia":
			media := body["media"].(map[string]any)
			if media["media"] != "cached" || media["type"] != "photo" || media["parse_mode"] != "HTML" {
				t.Error(media)
			}
		case "editMessageReplyMarkup":
			rows := body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
			if len(rows) != 0 {
				t.Error("keyboard not cleared")
			}
		case "deleteMessage":
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		default:
			t.Error(method)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":9,"photo":[{"file_id":"large","width":800,"height":600},{"file_id":"small","width":100,"height":100}]}}`)
	})
	ctx := context.Background()
	if _, err := client.EditMessageText(ctx, EditMessageTextRequest{ChatID: 42, MessageID: 9, Text: "<b>hello</b>", ParseMode: "HTML"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.EditMessageCaption(ctx, EditMessageCaptionRequest{ChatID: 42, MessageID: 9, Caption: "", ParseMode: "HTML"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.EditMessageMedia(ctx, EditMessageMediaRequest{ChatID: 42, MessageID: 9, FileID: "cached", ParseMode: "HTML"})
	if err != nil || result.PhotoFileID() != "large" {
		t.Fatal(result, err)
	}
	if _, err := client.EditMessageReplyMarkup(ctx, EditMessageReplyMarkupRequest{ChatID: 42, MessageID: 9}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteMessage(ctx, DeleteMessageRequest{ChatID: 42, MessageID: 9}); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 5 {
		t.Fatal(methods)
	}
}

func TestPhotoSourcesAndMultipartEdits(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			f, h, err := r.FormFile("photo")
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			if h.Header.Get("Content-Type") != "image/jpeg" || h.Filename != "product.jpg" {
				t.Fatal(h)
			}
			if strings.HasSuffix(r.URL.Path, "editMessageMedia") {
				var media map[string]any
				if err := json.Unmarshal([]byte(r.FormValue("media")), &media); err != nil {
					t.Fatal(err)
				}
				if media["media"] != "attach://photo" || media["parse_mode"] != "HTML" || r.FormValue("message_id") != "9" {
					t.Fatal(media)
				}
			} else if r.FormValue("protect_content") != "true" {
				t.Error("unprotected photo")
			}
		} else {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["photo"] != "cached" && body["photo"] != "https://unreachable.invalid/photo.png" {
				t.Fatal(body)
			}
			if _, ok := body["text"]; ok {
				t.Error("photo includes text field")
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":9,"photo":[{"file_id":"cached","width":10,"height":10}]}}`)
	})
	ctx := context.Background()
	for _, req := range []SendPhotoRequest{{ChatID: 42, Data: []byte{255, 216, 255, 0}, Filename: "product.jpg", ProtectContent: true}, {ChatID: 42, FileID: "cached"}, {ChatID: 42, URL: "https://unreachable.invalid/photo.png"}} {
		result, err := client.SendPhoto(ctx, req)
		if err != nil || result.PhotoFileID() != "cached" {
			t.Fatal(result, err)
		}
	}
	if _, err := client.EditMessageMedia(ctx, EditMessageMediaRequest{ChatID: 42, MessageID: 9, Data: []byte{255, 216, 255, 0}, Filename: "product.jpg", ParseMode: "HTML"}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []SendPhotoRequest{{Data: []byte("GIF89a")}, {FileID: "cached", URL: "https://example.com/p"}, {URL: "http://example.com/p"}, {Data: []byte{255, 216, 255}, Filename: "../secret.jpg"}} {
		_, err := client.SendPhoto(ctx, req)
		var classified interface{ Kind() string }
		if !errors.As(err, &classified) || classified.Kind() != "invalid_media" {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("unexpected fetch or invalid upload: %d", calls)
	}
}

func TestFiniteErrorKindsAndSendUncertainty(t *testing.T) {
	for _, tc := range []struct {
		description, kind string
		code              int
	}{
		{"Bad Request: message is not modified", "not_modified", 400},
		{"Bad Request: message to edit not found", "target_missing", 400},
		{"Bad Request: message can't be edited", "unknown", 400},
		{"Bad Request: can't parse entities: private text", "invalid_content", 400},
		{"Bad Request: wrong file identifier/HTTP URL specified", "invalid_media", 400},
		{"Forbidden: private secret", "forbidden", 403},
		{"Too Many Requests", "retryable", 429},
		{"private secret", "unknown", 400},
	} {
		t.Run(tc.kind+tc.description, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": tc.description})
			})
			_, err := client.EditMessageText(context.Background(), EditMessageTextRequest{MessageID: 1, Text: "hello"})
			var e *APIError
			if !errors.As(err, &e) || e.Kind() != tc.kind || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
	for _, method := range []string{"sendMessage", "sendPhoto", "sendDocument", "editMessageText", "deleteMessage"} {
		want := "unknown"
		if strings.HasPrefix(method, "edit") || method == "deleteMessage" {
			want = "retryable"
		}
		if got := classifyFailure(method, 502, 502, "private"); got != want {
			t.Fatalf("%s: %s", method, got)
		}
	}
}

func TestContentBudgets(t *testing.T) {
	if err := validateContent(strings.Repeat("界", 4096), "", 4096, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateContent(strings.Repeat("界", 4097), "", 4096, nil); err == nil {
		t.Fatal("accepted overlong text")
	}
	for _, n := range []int{64, 65} {
		err := validateContent("", "", 1024, &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "x", CallbackData: strings.Repeat("x", n)}}}})
		if (err == nil) != (n == 64) {
			t.Fatalf("callback bytes %d: %v", n, err)
		}
	}
}
