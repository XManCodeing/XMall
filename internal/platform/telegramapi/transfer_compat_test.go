package telegramapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestTransferPrivateProtectionFalseAndHomogeneousAlbums(t *testing.T) {
	for _, kind := range []string{"audio", "document"} {
		t.Run(kind, func(t *testing.T) {
			client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if string(body["protect_content"]) != "false" || string(body["message_thread_id"]) != "0" {
					t.Error("explicit protection/thread lost", body)
				}
				fmt.Fprint(writer, `{"ok":true,"result":[{"message_id":1,"chat":{"id":10}},{"message_id":2,"chat":{"id":10}}]}`)
			})
			messages, err := client.Transfer(context.Background(), TransferRequest{ChatID: 10, Contents: []TransferContent{{Kind: kind, FileID: "first"}, {Kind: kind, FileID: "second"}}})
			if err != nil || len(messages) != 2 {
				t.Fatal(messages, err)
			}
		})
	}
}

func TestForumInvalidRequestsNeverReachHTTP(t *testing.T) {
	client := transferTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid topic request reached HTTP") })
	for _, request := range []ForumTopicRequest{{ChatID: 10, Name: "support"}, {ChatID: -10}, {ChatID: -10, Name: "support", ThreadID: 9}} {
		if _, err := client.CreateForumTopic(context.Background(), request); err == nil {
			t.Fatal("accepted invalid create", request)
		}
	}
	for _, request := range []ForumTopicRequest{{ChatID: 10, ThreadID: 9}, {ChatID: -10}, {ChatID: -10, ThreadID: 9, Name: "support"}} {
		if err := client.ReopenForumTopic(context.Background(), request); err == nil {
			t.Fatal("accepted invalid reopen", request)
		}
		if err := client.CloseForumTopic(context.Background(), request); err == nil {
			t.Fatal("accepted invalid close", request)
		}
	}
}
