package botapi

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteWebhookPreservesPendingUpdates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["drop_pending_updates"] == true {
			t.Error("pending updates would be dropped")
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	if err := adapter.DeleteWebhook(context.Background(), "dummy", true); err == nil {
		t.Fatal("destructive request accepted")
	}
	if calls != 0 {
		t.Fatal("rejected request reached network")
	}
	if err := adapter.DeleteWebhook(context.Background(), "dummy", false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestPollPreservesUnknownUpdateIDsAndPrivateContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["offset"] != float64(8) {
			t.Error("offset missing")
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":8,"message":{"from":{"id":12},"chat":{"id":12,"type":"private"},"text":"/start"}},{"update_id":9,"callback_query":{"id":"q","from":{"id":12},"message":{"chat":{"id":-100,"type":"supergroup"}},"data":"buy"}},{"update_id":10}]}`))
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	updates, err := adapter.Poll(context.Background(), "dummy", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 || !updates[0].Private || updates[1].Private || updates[2].ID != 10 {
		t.Fatalf("invalid normalization: %+v", updates)
	}
}
func TestSendDefinitiveAndUncertainFailures(t *testing.T) {
	for _, status := range []int{401, 403, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": status, "description": "credential or private payload"})
			}))
			defer server.Close()
			adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
			_, err := adapter.Send(context.Background(), "dummy", c.Message{ChatID: 12, Text: "secret"})
			var classified c.APIError
			if !errors.As(err, &classified) || classified.StatusCode() != status || classified.Uncertain() != (status >= 500) {
				t.Fatalf("classification %v", err)
			}
			if err.Error() != "telegram delivery failed" {
				t.Fatal("upstream detail leaked")
			}
		})
	}
}

func TestSendClassifiesRateLimitWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"private detail","parameters":{"retry_after":17}}`))
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	_, err := adapter.Send(context.Background(), "token", c.Message{ChatID: 1, Text: "hello"})
	var classified c.APIError
	if !errors.As(err, &classified) || classified.StatusCode() != 429 || classified.RetrySeconds() != 17 || classified.Uncertain() {
		t.Fatalf("bad classification: %v", err)
	}
}
