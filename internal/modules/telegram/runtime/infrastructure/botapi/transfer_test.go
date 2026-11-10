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

// TestVerificationCardTransport validates the four-button identity-card shape.
// The transport rejects malformed callbacks; business authorization still binds the exact envelope.
func TestVerificationCardTransport(t *testing.T) {
	message := c.Message{TargetType: c.TargetSupportGroup, BusinessRef: "support:control:" + strings.Repeat("x", 24), RouteVersion: 1, ChatID: -100, ThreadID: 9, Purpose: "support_control", Operation: "send", TargetKind: "text", Text: "customer identity"}
	buttons := make([]c.Button, 4)
	for index := range buttons {
		buttons[index] = c.Button{Text: fmt.Sprintf("ban %d", index), Data: "support:op:" + strings.Repeat("y", 24)}
	}
	message.Buttons = [][]c.Button{buttons}
	if !validSupportControl(message) {
		t.Fatal("four-button identity card rejected")
	}
	message.Buttons[0][3].Data = "support:arbitrary"
	if validSupportControl(message) {
		t.Fatal("unchecked fourth callback accepted")
	}
	message.Buttons[0][3].Data = "support:op:" + strings.Repeat("y", 24)
	message.Buttons = append(message.Buttons, buttons)
	if validSupportControl(message) {
		t.Fatal("unbounded identity-card buttons accepted")
	}
}

func TestSupportTransferClassifiesFailuresAndKeepsPartialIDs(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		status      int
		body        string
		unknown     bool
		code, delay int
	}{
		{"partial", 200, `{"ok":true,"result":[{"message_id":7,"chat":{"id":-10}}]}`, true, 0, 0},
		{"empty", 200, `{"ok":true,"result":[]}`, true, 0, 0},
		{"malformed", 200, `broken`, true, 0, 0},
		{"server", 500, `{"ok":false,"error_code":500}`, true, 500, 0},
		{"rate", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":8}}`, false, 429, 8},
		{"blocked", 403, `{"ok":false,"error_code":403}`, false, 403, 0},
		{"file", 400, `{"ok":false,"error_code":400,"description":"wrong file identifier"}`, false, 400, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				writer.WriteHeader(scenario.status)
				fmt.Fprint(writer, scenario.body)
			}))
			defer server.Close()
			adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
			result, err := adapter.Transfer(context.Background(), "test", c.TransferRequest{TargetType: c.TargetSupportGroup, BusinessRef: "session", RouteVersion: 1, ChatID: -10, ThreadID: 9, Contents: []c.TransferContent{{Kind: "photo", FileID: "first"}, {Kind: "video", FileID: "second"}}})
			var classified c.APIError
			if !errors.As(err, &classified) || classified.Uncertain() != scenario.unknown || classified.StatusCode() != scenario.code || classified.RetrySeconds() != scenario.delay || calls != 1 {
				t.Fatal(result, err, calls)
			}
			if scenario.name == "partial" && (len(result.Messages) != 1 || result.Messages[0].MessageID != 7) {
				t.Fatal("partial ID lost", result)
			}
		})
	}
}

func TestSupportCreateTopicUnknownNeverRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		fmt.Fprint(writer, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	_, err := adapter.CreateForumTopic(context.Background(), "test", c.ForumTopicRequest{ChatID: -10, Name: "support"})
	var classified c.APIError
	if !errors.As(err, &classified) || !classified.Uncertain() || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestSupportTransferRejectsTargetAndPreservesLegacyBoundary(t *testing.T) {
	adapter := Adapter{Options: []api.Option{api.WithBaseURL("http://127.0.0.1:1")}}
	for _, request := range []c.TransferRequest{
		{TargetType: c.TargetShopPrivate, BusinessRef: "session", RouteVersion: 1, ChatID: 1},
		{TargetType: c.TargetSupportGroup, BusinessRef: "session", RouteVersion: 1, ChatID: -10},
		{TargetType: c.TargetSupportPrivate, BusinessRef: "session", RouteVersion: 1, ChatID: 10, ThreadID: 9},
	} {
		_, err := adapter.Transfer(context.Background(), "test", request)
		var classified c.APIError
		if !errors.As(err, &classified) || classified.Uncertain() || classified.StatusCode() != 400 {
			t.Fatal(err)
		}
	}
	_, err := adapter.Apply(context.Background(), "test", c.Message{TargetType: c.TargetSupportGroup, ChatID: -10, ThreadID: 9, TargetKind: "video"})
	var classified c.APIError
	if !errors.As(err, &classified) || classified.StatusCode() != 400 {
		t.Fatal("legacy API gained media/group authority", err)
	}
}

// TestSupportControlTransportPinsTopicAndDoesNotWidenReady verifies the actual wire path.
func TestSupportControlTransportPinsTopicAndDoesNotWidenReady(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var wire map[string]any
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if !strings.HasSuffix(request.URL.Path, "/sendMessage") || wire["chat_id"] != float64(-10) || wire["message_thread_id"] != float64(9) || wire["protect_content"] != true || wire["reply_markup"] == nil {
			t.Errorf("unsafe wire: %+v", wire)
		}
		fmt.Fprint(writer, `{"ok":true,"result":{"message_id":7,"chat":{"id":-10}}}`)
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	message := c.Message{TargetType: c.TargetSupportGroup, Purpose: "support_control", Operation: "send", TargetKind: "text", ChatID: -10, ThreadID: 9, RouteVersion: 1, BusinessRef: "support:control:" + strings.Repeat("x", 24), Text: "controls", Buttons: [][]c.Button{{{Text: "close", Data: "support:op:" + strings.Repeat("y", 24)}}}}
	result, err := adapter.Apply(context.Background(), "synthetic", message)
	if err != nil || result.MessageID != 7 || calls != 1 {
		t.Fatal(result, err, calls)
	}
	for _, mutate := range []func(*c.Message){
		func(message *c.Message) { message.Purpose = "support_ready" },
		func(message *c.Message) { message.ThreadID = 0 },
		func(message *c.Message) { message.Operation = "edit" },
		func(message *c.Message) { message.PhotoURL = "https://invalid.test/photo" },
		func(message *c.Message) { message.Buttons = [][]c.Button{{{Text: "unsafe", Data: "s:shopping"}}} },
	} {
		changed := message
		mutate(&changed)
		if _, err := adapter.Apply(context.Background(), "synthetic", changed); err == nil || calls != 1 {
			t.Fatal("unsafe control transport", err, calls)
		}
	}
}

// TestSupportGatePNGTransport preserves byte uploads without widening URL/file-id access.
func TestSupportGatePNGTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if !strings.HasSuffix(request.URL.Path, "/sendPhoto") {
			t.Error("not a photo upload")
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer request.MultipartForm.RemoveAll()
		file, header, err := request.FormFile("photo")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		if header.Header.Get("Content-Type") != "image/png" || request.FormValue("chat_id") != "42" || request.FormValue("protect_content") != "true" {
			t.Error("invalid PNG wire")
		}
		fmt.Fprint(writer, `{"ok":true,"result":{"message_id":8}}`)
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	message := c.Message{TargetType: c.TargetSupportPrivate, Purpose: "support_gate", BusinessRef: "support:gate:test", Operation: "send", ChatID: 42, TargetKind: "photo", Photo: []byte("\x89PNG\r\n\x1a\nsynthetic"), Text: "captcha"}
	result, err := adapter.Apply(context.Background(), "test", message)
	if err != nil || result.MessageID != 8 || calls != 1 {
		t.Fatal(result, err, calls)
	}
	message.PhotoURL = "https://example.invalid/image.png"
	if _, err := adapter.Apply(context.Background(), "test", message); err == nil || calls != 1 {
		t.Fatal("gate accepts remote media")
	}
}

// TestSupportGateCleanupDeletion checks the exact deletion endpoint and idempotent absence.
// t drives a local HTTP server; arbitrary content/capabilities must never reach transport.
func TestSupportGateCleanupDeletion(t *testing.T) {
	for _, body := range []string{`{"ok":true,"result":true}`, `{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls++
			if request.URL.Path != "/bottest/deleteMessage" {
				t.Error("unexpected deletion endpoint", request.URL.Path)
			}
			fmt.Fprint(writer, body)
		}))
		adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
		message := c.Message{TargetType: c.TargetSupportPrivate, BusinessRef: "support:gate-cleanup:3", ChatID: 42, Purpose: "support_gate_cleanup", Operation: "delete", TargetMessageID: 80}
		result, err := adapter.Apply(context.Background(), "test", message)
		if err != nil || result.MessageID != 80 || calls != 1 {
			t.Fatal(result, err, calls)
		}
		message.Text = "not allowed"
		if _, err = adapter.Apply(context.Background(), "test", message); err == nil || calls != 1 {
			t.Fatal("unexpected cleanup payload reached transport")
		}
		server.Close()
	}
}

func TestInboundProtectedContentIsRetained(t *testing.T) {
	update := mapUpdate(api.Update{UpdateID: 1, Message: &api.Message{MessageID: 2, HasProtectedContent: true, Chat: api.Chat{ID: 10, Type: "private"}, Photo: []api.PhotoSize{{FileID: "photo", Width: 100, Height: 100}}}})
	content, err := c.TransferContentFromUpdate(update)
	if err != nil || !update.HasProtectedContent || !content.HasProtectedContent {
		t.Fatal(update, content, err)
	}
}
