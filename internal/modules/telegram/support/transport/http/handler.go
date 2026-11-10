package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/XManCodeing/XMall/internal/i18n"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// Handler exposes only backend-authenticated support queries.
type Handler struct{ service *a.AdminService }

// New binds a read service; it does not enable the Telegram production consumer.
func New(service *a.AdminService) *Handler { return &Handler{service: service} }

// RegisterRoutes must be called on the JWT/RBAC-protected admin group.
// Each handler sets no-store before validating the backend actor.
func RegisterRoutes(admin gin.IRoutes, handler *Handler) {
	admin.POST("/telegram-bot/support/upstreams/:id/reconcile", handler.deliveryRecovery("upstream", "reconcile"))
	admin.POST("/telegram-bot/support/upstreams/:id/retry", handler.deliveryRecovery("upstream", "retry"))
	admin.POST("/telegram-bot/support/downstreams/:id/reconcile", handler.deliveryRecovery("downstream", "reconcile"))
	admin.POST("/telegram-bot/support/downstreams/:id/retry", handler.deliveryRecovery("downstream", "retry"))
	admin.GET("/telegram-bot/support/settings/:bot_id", handler.Settings)
	admin.PUT("/telegram-bot/support/settings/:bot_id", handler.UpdateSettings)
	admin.GET("/telegram-bot/support/topic-operations", handler.TopicOperations)
	admin.GET("/telegram-bot/support/topic-operations/:id", handler.TopicOperation)
	admin.POST("/telegram-bot/support/topic-operations/:id/confirm-deleted", handler.ConfirmDeletedTopic)
	admin.POST("/telegram-bot/support/topic-operations/:id/resolve", handler.ResolveTopicOperation)
	admin.GET("/telegram-bot/support/upstreams", handler.Upstreams)
	admin.GET("/telegram-bot/support/upstreams/:id", handler.Upstream)
	admin.POST("/telegram-bot/support/upstreams/:id/resolve", handler.ResolveUpstream)
	admin.GET("/telegram-bot/support/downstreams", handler.Downstreams)
	admin.GET("/telegram-bot/support/downstreams/:id", handler.Downstream)
	admin.POST("/telegram-bot/support/downstreams/:id/resolve", handler.ResolveDownstream)
	admin.GET("/telegram-bot/support/sessions", handler.Sessions)
	admin.GET("/telegram-bot/support/sessions/:id", handler.Session)
	admin.GET("/telegram-bot/support/sessions/:id/messages", handler.Messages)
	admin.GET("/telegram-bot/support/sessions/:id/messages/:message_id/content", handler.Content)
}

// fail emits localized finite errors and never includes database or decrypted error text.
func fail(ctx *gin.Context, err error) {
	status, key, kind := 500, "error.internal_error", "internal"
	switch {
	case errors.Is(err, c.ErrDenied):
		status, key, kind = 403, "error.forbidden", "forbidden"
	case errors.Is(err, c.ErrPolicyInvalid):
		status, key, kind = 400, "error.support_policy_invalid", "invalid_policy"
	case errors.Is(err, c.ErrConflict):
		status, key, kind = 409, "error.support_resolution_conflict", "conflict"
	case errors.Is(err, c.ErrAdminInvalid):
		status, key, kind = 400, "error.support_admin_invalid", "invalid"
	case errors.Is(err, c.ErrAdminNotFound):
		status, key, kind = 404, "error.not_found", "not_found"
	default:
		slog.Error("support_admin_query_failed", "route", ctx.FullPath())
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.AbortWithStatusJSON(status, gin.H{"code": status, "message": i18n.T(i18n.ResolveLocale(ctx), key), "error_class": kind})
}

// actor accepts only the server middleware's uint admin ID, never a header/body or user ID.
func actor(ctx *gin.Context) (uint, bool) {
	ctx.Header("Cache-Control", "no-store")
	value, exists := ctx.Get("admin_id")
	id, valid := value.(uint)
	if !exists || !valid || id == 0 {
		fail(ctx, c.ErrDenied)
		return 0, false
	}
	return id, true
}

// param validates positive local identifiers without truncating overflow.
func param(ctx *gin.Context, name string) (uint, bool) {
	value, err := strconv.ParseUint(ctx.Param(name), 10, strconv.IntSize)
	if err != nil || value == 0 {
		fail(ctx, c.ErrAdminInvalid)
		return 0, false
	}
	return uint(value), true
}

// filter validates query syntax instead of silently defaulting malformed pagination.
func filter(ctx *gin.Context) (c.AdminFilter, bool) {
	page, err := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	bot, botErr := strconv.ParseUint(ctx.DefaultQuery("bot_id", "0"), 10, strconv.IntSize)
	if err != nil || sizeErr != nil || botErr != nil {
		fail(ctx, c.ErrAdminInvalid)
		return c.AdminFilter{}, false
	}
	return c.AdminFilter{Page: page, PageSize: size, BotID: uint(bot), State: ctx.Query("state")}, true
}

// Settings returns per-bot policy/defaults with no-store and independent settings authorization.
func (h *Handler) Settings(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	bot, ok := param(ctx, "bot_id")
	if !ok {
		return
	}
	out, err := h.service.Settings(ctx.Request.Context(), admin, bot)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// UpdateSettings accepts only the version and bounded policy fields; enable/target/gate fields are rejected.
func (h *Handler) UpdateSettings(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	bot, ok := param(ctx, "bot_id")
	if !ok {
		return
	}
	var input c.AdminSettingsInput
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(ctx, c.ErrPolicyInvalid)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(ctx, c.ErrPolicyInvalid)
		return
	}
	out, err := h.service.UpdateSettings(ctx.Request.Context(), admin, bot, input)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Downstreams lists controlled task metadata, including unknown tasks awaiting review.
func (h *Handler) Downstreams(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	query, ok := filter(ctx)
	if !ok {
		return
	}
	out, err := h.service.Downstreams(ctx.Request.Context(), admin, query)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Downstream returns the version and local delivery reference needed for an explicit decision.
func (h *Handler) Downstream(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	out, err := h.service.Downstream(ctx.Request.Context(), admin, id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// ResolveDownstream accepts a bounded strict JSON contract, never actor/generation/receipt fields.
func (h *Handler) ResolveDownstream(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	var input c.AdminResolutionInput
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(ctx, c.ErrAdminInvalid)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(ctx, c.ErrAdminInvalid)
		return
	}
	if err := h.service.ResolveDownstream(ctx.Request.Context(), admin, id, input); err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"id": id, "resolved": true})
}

// TopicOperations lists metadata in the topic-operation namespace.
func (h *Handler) TopicOperations(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	query, ok := filter(ctx)
	if !ok {
		return
	}
	out, err := h.service.TopicOperations(ctx.Request.Context(), admin, query)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// TopicOperation reads one operation, never a mapping or downstream ID.
func (h *Handler) TopicOperation(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	out, err := h.service.TopicOperation(ctx.Request.Context(), admin, id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Upstreams lists only metadata for one bot's buffered-message tasks.
func (h *Handler) Upstreams(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	query, ok := filter(ctx)
	if !ok {
		return
	}
	out, err := h.service.Upstreams(ctx.Request.Context(), admin, query)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Upstream reads the buffered-message identity and its dispatch CAS revision.
func (h *Handler) Upstream(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	out, err := h.service.Upstream(ctx.Request.Context(), admin, id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// strictResolution rejects caller-supplied actor/generation/receipt IDs and trailing JSON.
func strictResolution(ctx *gin.Context, input any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		fail(ctx, c.ErrAdminInvalid)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(ctx, c.ErrAdminInvalid)
		return false
	}
	return true
}

// ResolveTopicOperation accepts explicit observed success or proven no-effect failure only.
func (h *Handler) ResolveTopicOperation(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	var input c.AdminTopicResolutionInput
	if !strictResolution(ctx, &input) {
		return
	}
	if err := h.service.ResolveTopicOperation(ctx.Request.Context(), admin, id, input); err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"id": id, "resolved": true})
}

// ResolveUpstream delegates real runtime proof validation; it never sends a message.
func (h *Handler) ResolveUpstream(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	var input c.AdminResolutionInput
	if !strictResolution(ctx, &input) {
		return
	}
	if err := h.service.ResolveUpstream(ctx.Request.Context(), admin, id, input); err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"id": id, "resolved": true})
}

// Sessions returns paginated session metadata for the authenticated actor.
func (h *Handler) Sessions(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	query, ok := filter(ctx)
	if !ok {
		return
	}
	query.Failure = ctx.Query("failure")
	for name, destination := range map[string]**bool{"unreplied": &query.Unreplied, "blocked": &query.Blocked, "has_topic": &query.HasTopic} {
		if values, exists := ctx.Request.URL.Query()[name]; exists {
			if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
				fail(ctx, c.ErrAdminInvalid)
				return
			}
			parsed := values[0] == "true"
			*destination = &parsed
		}
	}
	out, err := h.service.Sessions(ctx.Request.Context(), admin, query)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Session returns one metadata-only session for the authenticated actor.
func (h *Handler) Session(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	out, err := h.service.Session(ctx.Request.Context(), admin, id)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Messages returns a metadata page scoped to the selected session.
func (h *Handler) Messages(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	query, ok := filter(ctx)
	if !ok {
		return
	}
	out, err := h.service.Messages(ctx.Request.Context(), admin, id, query)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Content delegates exact permission, retention and durable audit to the service.
func (h *Handler) Content(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	message, ok := param(ctx, "message_id")
	if !ok {
		return
	}
	out, err := h.service.Content(ctx.Request.Context(), admin, id, message)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}
