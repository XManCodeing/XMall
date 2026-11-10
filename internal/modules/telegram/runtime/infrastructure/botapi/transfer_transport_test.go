package botapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

func TestSupportLostResponseIsUnknownAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		// The request was received and may already have taken effect; response lost.
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	for _, operation := range []string{"send", "forward", "create"} {
		var err error
		before := calls.Load()
		if operation == "send" {
			_, err = adapter.Transfer(context.Background(), "test", c.TransferRequest{TargetType: c.TargetSupportPrivate, BusinessRef: "session", RouteVersion: 1, ChatID: 10, Contents: []c.TransferContent{{Kind: "text", Text: "hello"}}})
		} else if operation == "forward" {
			_, err = adapter.Transfer(context.Background(), "test", c.TransferRequest{TargetType: c.TargetSupportGroup, BusinessRef: "support:ready:1:1", RouteVersion: 1, ChatID: -10, ThreadID: 9, SourceChatID: 10, SourceMessageIDs: []int64{5}, Contents: []c.TransferContent{{Kind: "text", Text: "hello"}}})
		} else {
			_, err = adapter.CreateForumTopic(context.Background(), "test", c.ForumTopicRequest{ChatID: -10, Name: "support"})
		}
		var classified c.APIError
		if !errors.As(err, &classified) || !classified.Uncertain() || calls.Load()-before != 1 {
			t.Fatal(operation, err, calls.Load()-before)
		}
	}
}
