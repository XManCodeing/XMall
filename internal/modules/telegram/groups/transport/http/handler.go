package http

import (
	"errors"
	"github.com/XManCodeing/XMall/internal/i18n"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
	"strconv"
)

type Handler struct{ service *a.Service }

func New(s *a.Service) *Handler { return &Handler{service: s} }
func fail(ctx *gin.Context, err error) {
	status, key, kind := 500, "error.internal_error", "internal"
	switch {
	case errors.Is(err, c.ErrChatUnresolved):
		status, key, kind = 422, "error.telegram_group_capability", "chat_id_unresolved"
	case errors.Is(err, c.ErrChatAmbiguous):
		status, key, kind = 422, "error.bad_request", "chat_id_ambiguous"
	case errors.Is(err, c.ErrConflict):
		status, key, kind = 409, "error.telegram_bot_conflict", "conflict"
	case errors.Is(err, c.ErrNotFound):
		status, key, kind = 404, "error.not_found", "not_found"
	case errors.Is(err, c.ErrInvalid):
		status, key, kind = 400, "error.bad_request", "invalid"
	case errors.Is(err, c.ErrCapability):
		status, key, kind = 409, "error.telegram_group_capability", "capability_unavailable"
	case errors.Is(err, c.ErrBusy):
		status, key, kind = 429, "error.telegram_group_busy", "check_busy"
	}
	ctx.AbortWithStatusJSON(status, gin.H{"code": status, "message": i18n.T(i18n.ResolveLocale(ctx), key), "error_class": kind})
}
func id(ctx *gin.Context) (uint, bool) {
	v, e := ginutil.ParseParamUint(ctx, "id")
	if e != nil || v == 0 {
		fail(ctx, c.ErrInvalid)
		return 0, false
	}
	return v, true
}
func (h *Handler) List(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	bot, _ := strconv.ParseUint(ctx.Query("bot_instance_id"), 10, 64)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, total, err := h.service.List(ctx.Request.Context(), c.Filter{BotID: uint(bot), Purpose: ctx.Query("purpose"), Page: page, PageSize: size})
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
func (h *Handler) Get(ctx *gin.Context) {
	v, ok := id(ctx)
	if !ok {
		return
	}
	out, err := h.service.Detail(ctx.Request.Context(), v)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// Save writes a group after server-side enable revalidation.
func (h *Handler) Save(ctx *gin.Context) { h.save(ctx, false) }

// PreviewEnable checks an unsaved form without any persistent changes.
func (h *Handler) PreviewEnable(ctx *gin.Context) { h.save(ctx, true) }

// save binds the same group input for writes and explicitly read-only previews.
func (h *Handler) save(ctx *gin.Context, preview bool) {
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	var v uint
	if ctx.Param("id") != "" {
		var ok bool
		v, ok = id(ctx)
		if !ok {
			return
		}
	}
	var in c.Input
	if err := ctx.ShouldBindJSON(&in); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	// Dedicated preview routes prevent old servers from treating a preview as a write.
	if preview {
		out, err := h.service.PreviewEnable(ctx.Request.Context(), v, in)
		if err != nil {
			fail(ctx, err)
			return
		}
		response.Success(ctx, out)
		return
	}
	out, err := h.service.Save(ctx.Request.Context(), v, in)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}
func (h *Handler) Check(ctx *gin.Context) {
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return
	}
	v, ok := id(ctx)
	if !ok {
		return
	}
	var in struct {
		ExpectedRouteVersion uint64 `json:"expected_route_version"`
	}
	if err := ctx.ShouldBindJSON(&in); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	out, err := h.service.Check(ctx.Request.Context(), v, in.ExpectedRouteVersion, actor, "manual")
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, out)
}

// ReplacementPreview performs read-only checks for an authenticated administrator.
// The path target is authoritative; the response is not a route-switch authorization.
func (h *Handler) ReplacementPreview(ctx *gin.Context) {
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	targetID, ok := id(ctx)
	if !ok {
		return
	}
	var input c.ReplacementInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	if input.TargetID != 0 && input.TargetID != targetID {
		fail(ctx, c.ErrInvalid)
		return
	}
	input.TargetID = targetID
	preview, err := h.service.PreviewReplacement(ctx.Request.Context(), input)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, preview)
}

func (h *Handler) Modules(ctx *gin.Context) {
	v, ok := id(ctx)
	if !ok {
		return
	}
	out, err := h.service.Modules(ctx.Request.Context(), v)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"items": out})
}
func (h *Handler) SaveModule(ctx *gin.Context) {
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return
	}
	v, ok := id(ctx)
	if !ok {
		return
	}
	var in c.ModuleInput
	if err := ctx.ShouldBindJSON(&in); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	out, err := h.service.SaveModule(ctx.Request.Context(), v, actor, in)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"items": out})
}
