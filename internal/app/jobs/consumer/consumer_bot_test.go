package consumer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	channel "github.com/XManCodeing/XMall/internal/modules/channelclient/application"
	"github.com/XManCodeing/XMall/internal/queue"
	"github.com/hibiken/asynq"
)

type legacyRouterFake struct {
	endpoint *channel.ActiveEndpoint
	allowed  bool
	err      error
}

func (r legacyRouterFake) Resolve(context.Context, queue.BotNotifyPayload, uint) (*channel.ActiveEndpoint, error) {
	return r.endpoint, r.err
}
func (r legacyRouterFake) LegacyAllowed(context.Context, uint) (bool, error) { return r.allowed, nil }
func TestLegacyQueueFailsClosed(t *testing.T) {
	task := asynq.NewTask("test", []byte(`{"order_id":1,"telegram_user_id":"11","channel_id":2}`))
	for _, router := range []LegacyNotifyRouter{nil, legacyRouterFake{err: ErrLegacyNotifyUnmapped}, legacyRouterFake{endpoint: &channel.ActiveEndpoint{ClientID: 3, CallbackURL: "http://localhost"}, allowed: true}, legacyRouterFake{endpoint: &channel.ActiveEndpoint{ClientID: 2, CallbackURL: "http://localhost"}}} {
		if err := handleRoutedBotNotify(context.Background(), task, router); !errors.Is(err, asynq.SkipRetry) {
			t.Fatal(err)
		}
	}
}
func TestLegacyUnknownOutcomeDoesNotAutoResend(t *testing.T) {
	for _, status := range []int{500, 302, 429, 403, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
			}))
			defer server.Close()
			router := legacyRouterFake{endpoint: &channel.ActiveEndpoint{ClientID: 2, CallbackURL: server.URL}, allowed: true}
			task := asynq.NewTask("test", []byte(`{"order_id":1,"telegram_user_id":"11","channel_id":2}`))
			err := handleRoutedBotNotify(context.Background(), task, router)
			if calls != 1 {
				t.Fatal("redirect followed", calls)
			}
			if status == 200 && err != nil {
				t.Fatal(err)
			}
			if status == 429 && (err == nil || errors.Is(err, asynq.SkipRetry)) {
				t.Fatal("rate limit not retried", err)
			}
			if status != 200 && status != 429 && !errors.Is(err, asynq.SkipRetry) {
				t.Fatal("unsafe automatic retry", err)
			}
		})
	}
}
func TestCallbackURLDoesNotLeakCredentials(t *testing.T) {
	for _, raw := range []string{"https://user:secret@example.com", "secret", "ftp://example.com", "https://%secret"} {
		_, err := buildBotNotifyRequestURL(raw, "/event")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe URL", err)
		}
	}
}
