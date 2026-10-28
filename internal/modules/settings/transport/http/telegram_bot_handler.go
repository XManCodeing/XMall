package settingshttp

import (
	"encoding/json"
	"errors"
	"github.com/XManCodeing/XMall/internal/i18n"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	ginutil "github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"net/http"

	"github.com/gin-gonic/gin"
)

// TelegramBotAdminService 是后台 Telegram Bot 设置端口。
type TelegramBotAdminService interface {
	GetTelegramBotConfig() (settingsmessaging.TelegramBotConfigSetting, error)
	PatchTelegramBotConfigSection(section string, expected int, raw json.RawMessage) (settingsmessaging.TelegramBotConfigSetting, error)
	GetTelegramBotRuntimeStatus() (settingsmessaging.TelegramBotRuntimeStatusSetting, error)
}

// TelegramBotHandler 处理后台 Telegram Bot 设置请求。
type TelegramBotHandler struct {
	bot TelegramBotAdminService
}

func NewTelegramBotHandler(bot TelegramBotAdminService) *TelegramBotHandler {
	if bot == nil {
		panic("settings telegram-bot handler: bot is nil")
	}
	return &TelegramBotHandler{bot: bot}
}

// GetTelegramBotConfig 获取 Telegram Bot 配置。
func (h *TelegramBotHandler) GetTelegramBotConfig(c *gin.Context) {
	setting, err := h.bot.GetTelegramBotConfig()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}
	response.Success(c, settingsmessaging.MaskTelegramBotConfigForAdmin(setting))
}

// UpdateTelegramBotConfig deliberately rejects legacy unversioned whole-document writes.
func (h *TelegramBotHandler) UpdateTelegramBotConfig(c *gin.Context) {
	response.ErrorWithHTTPStatus(c, http.StatusMethodNotAllowed, response.CodeBadRequest, i18n.T(i18n.ResolveLocale(c), "error.bad_request"))
}

func (h *TelegramBotHandler) PatchTelegramBotConfigSection(c *gin.Context) {
	var req struct {
		ExpectedVersion *int            `json:"expected_version" binding:"required"`
		Data            json.RawMessage `json:"data" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	setting, err := h.bot.PatchTelegramBotConfigSection(c.Param("section"), *req.ExpectedVersion, req.Data)
	if errors.Is(err, settingsapp.ErrTelegramBotConfigConflict) {
		response.ErrorWithHTTPStatus(c, http.StatusConflict, 409, i18n.T(i18n.ResolveLocale(c), "error.settings_save_failed"))
		return
	}
	if errors.Is(err, settingsapp.ErrTelegramBotMenuUnsupported) {
		response.ErrorWithHTTPStatus(c, http.StatusBadRequest, response.CodeBadRequest, err.Error())
		return
	}
	if errors.Is(err, settingsapp.ErrTelegramBotSection) {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_save_failed", err)
		return
	}
	response.Success(c, settingsmessaging.MaskTelegramBotConfigForAdmin(setting))
}

// GetTelegramBotRuntimeStatus 获取 Telegram Bot 运行时状态。
func (h *TelegramBotHandler) GetTelegramBotRuntimeStatus(c *gin.Context) {
	status, err := h.bot.GetTelegramBotRuntimeStatus()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}
	response.Success(c, status)
}
