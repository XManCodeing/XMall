package telegramhttp

import (
	"net/http"

	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"github.com/XManCodeing/XMall/internal/platform/http/channelresponse"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// BotSettings retains the assembly contract for retired external bot endpoints.
type BotSettings interface {
	GetTelegramBotConfig() (settingsmessaging.TelegramBotConfigSetting, error)
	GetTelegramBotRuntimeStatus() (settingsmessaging.TelegramBotRuntimeStatusSetting, error)
	UpdateTelegramBotRuntimeStatus(status settingsmessaging.TelegramBotRuntimeStatusSetting) error
}

// ChannelBotTokenProvider is retained for constructor compatibility only.
// Retired handlers must never call it or disclose stored credentials.
type ChannelBotTokenProvider interface {
	DecryptBotTokenByClientID(clientID uint) (string, error)
}

// ChannelBotHandler returns explicit retirement responses behind channel authentication.
// Ordinary channel APIs and existing stored credentials are not modified.
type ChannelBotHandler struct{}

// NewChannelBotHandler accepts existing assembly ports without retaining or calling them.
// It returns a handler that cannot decrypt tokens or mutate legacy runtime status.
func NewChannelBotHandler(_ BotSettings, _ ChannelBotTokenProvider) *ChannelBotHandler {
	return &ChannelBotHandler{}
}

// GetBotConfig responds to the authenticated request with HTTP 410 and no credentials.
func (h *ChannelBotHandler) GetBotConfig(c *gin.Context) {
	h.retired(c)
}

// ReportHeartbeat responds with HTTP 410 without parsing or persisting the supplied body.
func (h *ChannelBotHandler) ReportHeartbeat(c *gin.Context) {
	h.retired(c)
}

// retired logs only the route, then emits a localized stable channel error response.
// c is the authenticated request context; no settings or credential ports are accessed.
func (h *ChannelBotHandler) retired(c *gin.Context) {
	ginutil.RequestLog(c).Infow("legacy_telegram_channel_retired", "route", c.FullPath())
	channelresponse.Error(c, http.StatusGone, response.CodeBadRequest,
		"telegram_channel_retired", "error.telegram_channel_retired", nil)
}
