package http

import (
	"errors"
	"net/http"
	"strconv"

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
		Token           string `json:"token"`
		ChannelClientID *uint  `json:"channel_client_id"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	// Legacy migration must verify the exact source; creating an instance cannot attach it implicitly.
	if input.ChannelClientID != nil {
		fail(ctx, errors.New("explicit cutover required"))
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
