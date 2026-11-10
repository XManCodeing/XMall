package telegramapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestLifecycleWire verifies exact integer identities and mandatory safe-unban JSON on a local server.
func TestLifecycleWire(t *testing.T) {
	calls := 0
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var body struct {
			ChatID       int64 `json:"chat_id"`
			UserID       int64 `json:"user_id"`
			OnlyIfBanned *bool `json:"only_if_banned"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.ChatID != -10042 || body.UserID != 9007199254740993 {
			t.Error("identity changed on wire")
		}
		if strings.HasSuffix(request.URL.Path, "/unbanChatMember") {
			if body.OnlyIfBanned == nil || !*body.OnlyIfBanned {
				t.Error("unban missing mandatory only_if_banned")
			}
		} else if !strings.HasSuffix(request.URL.Path, "/banChatMember") {
			t.Error("unexpected lifecycle method")
		}
		fmt.Fprint(writer, `{"ok":true,"result":true}`)
	})
	for _, action := range []func(context.Context, int64, int64) error{client.BanChatMember, client.UnbanChatMember} {
		if err := action(context.Background(), -10042, 9007199254740993); err != nil {
			t.Fatal(err)
		}
		if err := action(context.Background(), 0, 1); err == nil {
			t.Error("invalid physical route accepted")
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected retries or invalid requests: %d", calls)
	}
}
