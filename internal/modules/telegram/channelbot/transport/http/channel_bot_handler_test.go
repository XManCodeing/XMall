package telegramhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"github.com/gin-gonic/gin"
)

// retiredSettingsFake fails immediately if retired routes access legacy state.
type retiredSettingsFake struct{}

// GetTelegramBotConfig must never be called by a retired endpoint.
func (retiredSettingsFake) GetTelegramBotConfig() (settingsmessaging.TelegramBotConfigSetting, error) {
	panic("retired endpoint read legacy config")
}

// GetTelegramBotRuntimeStatus must never be called by a retired endpoint.
func (retiredSettingsFake) GetTelegramBotRuntimeStatus() (settingsmessaging.TelegramBotRuntimeStatusSetting, error) {
	panic("retired endpoint read legacy runtime")
}

// UpdateTelegramBotRuntimeStatus must never mutate the supplied status after retirement.
func (retiredSettingsFake) UpdateTelegramBotRuntimeStatus(settingsmessaging.TelegramBotRuntimeStatusSetting) error {
	panic("retired endpoint changed legacy runtime")
}

// retiredTokensFake catches accidental credential access, including suppressed errors.
type retiredTokensFake struct{}

// DecryptBotTokenByClientID must never decrypt credentials for any supplied client ID.
func (retiredTokensFake) DecryptBotTokenByClientID(uint) (string, error) {
	panic("retired endpoint decrypted credentials")
}

// TestChannelBotRetirement verifies both endpoints return 410 without touching dependencies.
func TestChannelBotRetirement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewChannelBotHandler(retiredSettingsFake{}, retiredTokensFake{})
	router := gin.New()
	// Production still requires channel signature authentication before these handlers.
	router.Use(func(ctx *gin.Context) { ctx.Set("channel_client_id", uint(7)) })
	router.GET("/config", handler.GetBotConfig)
	router.POST("/heartbeat", handler.ReportHeartbeat)
	for _, scenario := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/config", ""},
		{http.MethodPost, "/heartbeat", `{"bot_version":"test"}`},
		{http.MethodPost, "/heartbeat", "invalid json"},
	} {
		t.Run(scenario.path+scenario.body, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(scenario.method, scenario.path, strings.NewReader(scenario.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			body := recorder.Body.String()
			if recorder.Code != http.StatusGone || !strings.Contains(body, "telegram_channel_retired") {
				t.Fatalf("retirement response: status=%d body=%s", recorder.Code, body)
			}
			if strings.Contains(body, "bot_token") || strings.Contains(body, "config_version") {
				t.Fatalf("retired response exposed legacy config: %s", body)
			}
		})
	}
}
