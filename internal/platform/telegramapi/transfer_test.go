package telegramapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// transferTestClient uses only an isolated HTTP server, never a real Bot API.
func transferTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient("test", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestTransferEveryKindExplicitFields(t *testing.T) {
	for kind, method := range map[string]string{"text": "sendMessage", "photo": "sendPhoto", "video": "sendVideo", "voice": "sendVoice", "audio": "sendAudio", "document": "sendDocument", "animation": "sendAnimation", "sticker": "sendSticker"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if request.URL.Path != "/bottest/"+method || string(body["message_thread_id"]) != "9" || string(body["protect_content"]) != "true" || string(body["chat_id"]) != "-10" {
					t.Errorf("unexpected request %s %v", request.URL.Path, body)
				}
				if string(body["reply_parameters"]) != `{"allow_sending_without_reply":false,"message_id":20}` {
					t.Errorf("reply missing: %s", body["reply_parameters"])
				}
				if _, exists := body["parse_mode"]; exists {
					t.Error("implicit parse mode")
				}
				if kind == "text" {
					if string(body["text"]) != `"😀hi"` || len(body["entities"]) == 0 {
						t.Error("text/entities lost")
					}
				} else {
					if string(body[kind]) != `"file_ID-1"` {
						t.Error("file ID lost")
					}
					if kind != "sticker" && (string(body["caption"]) != `"😀hi"` || len(body["caption_entities"]) == 0) {
						t.Error("caption/entities lost")
					}
				}
				fmt.Fprint(writer, `{"ok":true,"result":{"message_id":30,"chat":{"id":-10},"message_thread_id":9}}`)
			})
			content := TransferContent{Kind: kind, FileID: "file_ID-1", Caption: "😀hi", CaptionEntities: []MessageEntity{{Type: "bold", Offset: 2, Length: 2}}}
			if kind == "text" {
				content = TransferContent{Kind: kind, Text: "😀hi", Entities: content.CaptionEntities}
			}
			if kind == "sticker" {
				content.Caption, content.CaptionEntities = "", nil
			}
			result, err := client.Transfer(context.Background(), TransferRequest{ChatID: -10, ThreadID: 9, ReplyMessageID: 20, ProtectContent: true, Contents: []TransferContent{content}})
			if err != nil || len(result) != 1 || result[0].MessageID != 30 || calls != 1 {
				t.Fatal(result, err, calls)
			}
		})
	}
}

func TestTransferRejectsInvalidWithoutHTTP(t *testing.T) {
	client := transferTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request reached HTTP") })
	contents := []TransferContent{
		{Kind: "unknown", Text: "must not fallback"}, {Kind: "photo", FileID: "https://example.invalid/a"},
		{Kind: "photo", FileID: "attach://file"}, {Kind: "photo", FileID: "/uploads/a"},
		{Kind: "photo", FileID: "photo", FileSize: 10<<20 + 1}, {Kind: "video", FileID: "video", FileSize: 50<<20 + 1},
		{Kind: "photo", FileID: "photo", HasProtectedContent: true}, {Kind: "text", Text: "protected", HasProtectedContent: true},
		{Kind: "sticker", FileID: "sticker", Caption: "lost"}, {Kind: "text", Text: strings.Repeat("x", 4097)},
		{Kind: "photo", FileID: "photo", Caption: strings.Repeat("x", 1025)}, {Kind: "text", Text: "😀", Entities: []MessageEntity{{Type: "bold", Offset: 1, Length: 1}}},
		{Kind: "text", Text: "x", Entities: []MessageEntity{{Type: "bold", Offset: 0, Length: 2}}},
	}
	for _, content := range contents {
		if _, err := client.Transfer(context.Background(), TransferRequest{ChatID: 10, Contents: []TransferContent{content}}); err == nil {
			t.Fatalf("accepted %+v", content)
		}
	}
	for _, kinds := range [][]string{{"voice", "voice"}, {"animation", "photo"}, {"audio", "photo"}, {"document", "video"}, {"sticker", "sticker"}} {
		var album []TransferContent
		for _, kind := range kinds {
			album = append(album, TransferContent{Kind: kind, FileID: "file"})
		}
		if _, err := client.Transfer(context.Background(), TransferRequest{ChatID: 10, Contents: album}); err == nil {
			t.Fatal("accepted incompatible album", kinds)
		}
	}
}

func TestTransferAlbumResultsNeverRetry(t *testing.T) {
	for _, scenario := range []struct {
		name, result string
		uncertain    bool
	}{
		{"complete", `[{"message_id":1,"chat":{"id":-10}},{"message_id":2,"chat":{"id":-10}}]`, false},
		{"partial", `[{"message_id":1,"chat":{"id":-10}}]`, true}, {"empty", `[]`, true},
		{"zero", `[{"message_id":0,"chat":{"id":-10}},{"message_id":2,"chat":{"id":-10}}]`, true},
		{"duplicate", `[{"message_id":1,"chat":{"id":-10}},{"message_id":1,"chat":{"id":-10}}]`, true},
		{"wrong-chat", `[{"message_id":1,"chat":{"id":-20}},{"message_id":2,"chat":{"id":-10}}]`, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				var body struct {
					Media []struct {
						Type, Media, Caption string `json:",omitempty"`
					}
					Protect bool `json:"protect_content"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if request.URL.Path != "/bottest/sendMediaGroup" || len(body.Media) != 2 || body.Media[0].Type != "photo" || body.Media[1].Type != "video" || body.Media[1].Caption != "second" {
					t.Error("album order lost")
				}
				fmt.Fprintf(writer, `{"ok":true,"result":%s}`, scenario.result)
			})
			result, err := client.Transfer(context.Background(), TransferRequest{ChatID: -10, ThreadID: 9, Contents: []TransferContent{{Kind: "photo", FileID: "first"}, {Kind: "video", FileID: "second", Caption: "second"}}})
			var uncertain *UncertainResultError
			if errors.As(err, &uncertain) != scenario.uncertain || calls != 1 {
				t.Fatal(result, err, calls)
			}
			if scenario.name == "partial" && (len(result) != 1 || result[0].MessageID != 1) {
				t.Fatal("lost observed ID")
			}
		})
	}
}

// TestNativeForwardTransport verifies ordered attribution transport and partial receipts.
func TestNativeForwardTransport(t *testing.T) {
	for _, scenario := range []struct {
		name, result string
		ids          []int64
		uncertain    bool
	}{
		{"single", `{"message_id":30,"chat":{"id":-10},"message_thread_id":9}`, []int64{4}, false},
		{"album", `[{"message_id":30},{"message_id":31}]`, []int64{4, 5}, false},
		{"partial", `[{"message_id":30}]`, []int64{4, 5}, true},
		{"empty", `[]`, []int64{4, 5}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				method := "forwardMessages"
				if len(scenario.ids) == 1 {
					method = "forwardMessage"
				}
				if request.URL.Path != "/bottest/"+method || string(body["from_chat_id"]) != "42" || string(body["message_thread_id"]) != "9" || string(body["protect_content"]) != "true" {
					t.Errorf("incorrect forward: %v", body)
				}
				if len(scenario.ids) == 2 && string(body["message_ids"]) != "[4,5]" {
					t.Error("order lost")
				}
				if len(scenario.ids) == 1 && string(body["message_id"]) != "4" {
					t.Error("source lost")
				}
				if len(body["text"]) != 0 || len(body["media"]) != 0 {
					t.Error("copied body instead of forward")
				}
				fmt.Fprintf(writer, `{"ok":true,"result":%s}`, scenario.result)
			})
			contents := make([]TransferContent, len(scenario.ids))
			for index := range contents {
				contents[index] = TransferContent{Kind: "photo", FileID: "image"}
			}
			messages, err := client.Transfer(context.Background(), TransferRequest{ChatID: -10, ThreadID: 9, SourceChatID: 42, SourceMessageIDs: scenario.ids, ProtectContent: true, Contents: contents})
			var uncertain *UncertainResultError
			if calls != 1 || errors.As(err, &uncertain) != scenario.uncertain {
				t.Fatal(messages, err, calls)
			}
			if scenario.name == "partial" && (len(messages) != 1 || messages[0].MessageID != 30) {
				t.Fatal("partial receipt lost")
			}
		})
	}
}

// TestNativeForwardRejectsUntrustedShape ensures invalid source/protection never reaches HTTP.
func TestNativeForwardRejectsUntrustedShape(t *testing.T) {
	client := transferTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid forward reached HTTP") })
	base := TransferRequest{ChatID: -10, ThreadID: 9, SourceChatID: 42, SourceMessageIDs: []int64{4}, Contents: []TransferContent{{Kind: "text", Text: "original"}}}
	for _, mutate := range []func(*TransferRequest){
		func(request *TransferRequest) { request.SourceChatID = 0 },
		func(request *TransferRequest) { request.SourceMessageIDs = nil },
		func(request *TransferRequest) { request.SourceMessageIDs = []int64{0} },
		func(request *TransferRequest) {
			request.SourceMessageIDs = []int64{5, 4}
			request.Contents = append(request.Contents, request.Contents[0])
		},
		func(request *TransferRequest) { request.ChatID, request.ThreadID = 42, 0 },
		func(request *TransferRequest) { request.ReplyMessageID = 1 },
		func(request *TransferRequest) {
			request.Contents = []TransferContent{{Kind: "text", Text: "original", HasProtectedContent: true}}
		},
	} {
		request := base
		mutate(&request)
		if _, err := client.Transfer(context.Background(), request); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}

// TestNativeForwardNeverFallsBack covers explicit protection errors and network ambiguity.
func TestNativeForwardNeverFallsBack(t *testing.T) {
	for _, status := range []int{400, 500} {
		calls := 0
		client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
			calls++
			if request.URL.Path != "/bottest/forwardMessage" {
				t.Error("fallback attempted")
			}
			writer.WriteHeader(status)
			fmt.Fprintf(writer, `{"ok":false,"error_code":%d,"description":"protected content"}`, status)
		})
		_, err := client.Transfer(context.Background(), TransferRequest{ChatID: -10, ThreadID: 9, SourceChatID: 42, SourceMessageIDs: []int64{4}, Contents: []TransferContent{{Kind: "text", Text: "original"}}})
		if err == nil || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}

func TestForumTopicMethodsAndUnknownResults(t *testing.T) {
	for _, method := range []string{"createForumTopic", "reopenForumTopic", "closeForumTopic"} {
		for _, valid := range []bool{true, false} {
			calls := 0
			client := transferTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/bottest/"+method {
					t.Error(request.URL.Path)
				}
				result := "true"
				if !valid {
					result = "false"
				}
				if method == "createForumTopic" {
					result = `{"message_thread_id":9}`
					if !valid {
						result = `{}`
					}
				}
				fmt.Fprintf(writer, `{"ok":true,"result":%s}`, result)
			})
			var err error
			request := ForumTopicRequest{ChatID: -10, ThreadID: 9}
			switch method {
			case "createForumTopic":
				_, err = client.CreateForumTopic(context.Background(), ForumTopicRequest{ChatID: -10, Name: "support"})
			case "reopenForumTopic":
				err = client.ReopenForumTopic(context.Background(), request)
			case "closeForumTopic":
				err = client.CloseForumTopic(context.Background(), request)
			}
			if (err == nil) != valid || calls != 1 {
				t.Fatal(method, err, calls)
			}
		}
	}
}
