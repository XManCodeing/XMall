package telegramhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"github.com/gin-gonic/gin"
)

type channelSettingsFake struct {
	status settingsmessaging.TelegramBotRuntimeStatusSetting
}

func (s *channelSettingsFake) GetTelegramBotConfig() (settingsmessaging.TelegramBotConfigSetting, error) {
	return settingsmessaging.TelegramBotConfigSetting{}, nil
}
func (s *channelSettingsFake) GetTelegramBotRuntimeStatus() (settingsmessaging.TelegramBotRuntimeStatusSetting, error) {
	return s.status, nil
}
func (s *channelSettingsFake) UpdateTelegramBotRuntimeStatus(status settingsmessaging.TelegramBotRuntimeStatusSetting) error {
	s.status = status
	return nil
}

type channelTokensFake struct{ id uint }

func (s *channelTokensFake) DecryptBotTokenByClientID(id uint) (string, error) {
	s.id = id
	return "123:test-only", nil
}

func TestChannelConfigAndHeartbeatWithoutCutoverDependency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &channelSettingsFake{}
	tokens := &channelTokensFake{}
	h := NewChannelBotHandler(settings, tokens)
	r := gin.New()
	// Production routes populate this only after signature authentication.
	r.Use(func(c *gin.Context) { c.Set("channel_client_id", uint(7)) })
	r.GET("/config", h.GetBotConfig)
	r.POST("/heartbeat", h.ReportHeartbeat)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/config", nil))
	if w.Code != http.StatusOK || tokens.id != 7 {
		t.Fatal(w.Code, tokens.id, w.Body.String())
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/heartbeat", strings.NewReader(`{"bot_version":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !settings.status.Connected || settings.status.BotVersion != "test" || settings.status.LastSeenAt == "" {
		t.Fatal(w.Code, settings.status, w.Body.String())
	}
}
