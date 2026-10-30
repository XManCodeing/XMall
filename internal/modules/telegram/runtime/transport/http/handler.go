package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/XManCodeing/XMall/internal/i18n"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type Handler struct{ manager *app.Manager }

func New(m *app.Manager) *Handler { return &Handler{manager: m} }
func fail(ctx *gin.Context, err error) {
	status := http.StatusBadRequest
	key := "error.bad_request"
	if errors.Is(err, c.ErrConflict) {
		status = http.StatusConflict
		key = "error.telegram_bot_conflict"
	}
	if errors.Is(err, c.ErrDuplicate) {
		key = "error.telegram_bot_duplicate"
	}
	if errors.Is(err, c.ErrNotFound) {
		status = http.StatusNotFound
		key = "error.not_found"
	}
	// Never serialize upstream errors, tokens or raw Telegram replies.
	ctx.AbortWithStatusJSON(status, gin.H{"code": status, "message": i18n.T(i18n.ResolveLocale(ctx), key)})
}
func (h *Handler) List(ctx *gin.Context) {
	values, err := h.manager.Store.List(ctx.Request.Context())
	if err != nil {
		ginutil.RespondError(ctx, response.CodeInternal, "error.internal_error", nil)
		return
	}
	result := make([]app.InstanceView, 0, len(values))
	for _, v := range values {
		result = append(result, app.View(v))
	}
	response.Success(ctx, gin.H{"items": result})
}
func (h *Handler) Create(ctx *gin.Context) {
	var input struct {
		Token string `json:"token"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	value, err := h.manager.Create(ctx.Request.Context(), input.Token)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, app.View(*value))
}
func (h *Handler) Configure(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	var input struct {
		Version uint64  `json:"expected_version"`
		Enabled *bool   `json:"enabled"`
		Token   *string `json:"token"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	if err := h.manager.Configure(ctx.Request.Context(), id, input.Version, input.Enabled, input.Token); err != nil {
		fail(ctx, err)
		return
	}
	h.Status(ctx)
}
func (h *Handler) Test(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	identity, webhook, err := h.manager.Test(ctx.Request.Context(), id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"telegram_id": identity.ID, "username": identity.Username, "webhook_present": webhook, "ok": true})
}
func (h *Handler) Status(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	value, err := h.manager.Status(ctx.Request.Context(), id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, value)
}
func (h *Handler) Deliveries(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	values, total, err := h.manager.Store.ListDeliveries(ctx.Request.Context(), id, 0, page, size)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"items": values, "total": total, "page": page, "page_size": size})
}

func (h *Handler) GetProfile(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	value, err := h.manager.GetProfile(ctx.Request.Context(), id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, value)
}

func (h *Handler) UpdateProfile(ctx *gin.Context) {
	id, parseErr := ginutil.ParseParamUint(ctx, "id")
	if parseErr != nil {
		fail(ctx, parseErr)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expected_version"`
		app.BotProfileInput
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	value, err := h.manager.UpdateProfile(ctx.Request.Context(), id, input.ExpectedVersion, input.BotProfileInput)
	if err != nil {
		if errors.Is(err, c.ErrConflict) {
			fail(ctx, err)
			return
		}
		if value.InstanceID != 0 {
			ctx.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"code": http.StatusBadGateway, "message": i18n.T(i18n.ResolveLocale(ctx), "error.telegram_bot_profile_sync"), "data": value})
			return
		}
		key := "error.bad_request"
		switch {
		case strings.Contains(err.Error(), "profile video"):
			key = "error.telegram_bot_profile_video"
		case strings.Contains(err.Error(), "cover image"), strings.Contains(err.Error(), "upload root"), strings.Contains(err.Error(), "cover must"):
			key = "error.telegram_bot_profile_cover"
		case strings.Contains(err.Error(), "credential"), strings.Contains(err.Error(), "profile sync unavailable"), strings.Contains(err.Error(), "profile sync failed"), strings.Contains(err.Error(), "profile persist failed"):
			key = "error.telegram_bot_profile_sync"
		case strings.Contains(err.Error(), "display name"), strings.Contains(err.Error(), "description exceeds"):
			key = "error.bad_request"
		}
		ctx.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": i18n.T(i18n.ResolveLocale(ctx), key)})
		return
	}
	response.Success(ctx, value)
}
