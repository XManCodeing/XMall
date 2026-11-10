package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	"github.com/gin-gonic/gin"
)

// TestReplacementPreviewRejectsUntrustedInput verifies authentication and path/body consistency.
// Invalid inputs must never reach dependencies or perform a platform request.
func TestReplacementPreviewRejectsUntrustedInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name  string
		actor bool
		body  string
	}{
		{name: "missing_admin", body: `{}`},
		{name: "mismatched_target", actor: true, body: `{"target_id":2}`},
		{name: "bad_json", actor: true, body: `{`},
		{name: "numeric_chat_loses_precision", actor: true, body: `{"new_telegram_chat_id":-100}`},
		{name: "invalid_destination", actor: true, body: `{"expected_version":1,"expected_route_version":1,"new_bot_instance_id":2,"new_telegram_chat_id":"100"}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			router := gin.New()
			if scenario.actor {
				router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			}
			RegisterRoutes(router, New(a.New(nil, nil)))
			request := httptest.NewRequest(http.MethodPost, "/telegram-bot/groups/1/replacement-preview", strings.NewReader(scenario.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			status := response.Code
			if status == http.StatusOK {
				var envelope struct {
					StatusCode int `json:"status_code"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				status = envelope.StatusCode
			}
			if status < 400 || status >= 500 {
				t.Fatalf("unexpected status=%d body=%s", status, response.Body.String())
			}
		})
	}
}
