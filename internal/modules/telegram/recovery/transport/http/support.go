package http

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// WithSupportManagement installs the fail-closed management port before registration.
func (h *Handler) WithSupportManagement(port c.SupportManagement) *Handler {
	h.support = port
	return h
}

// registerSupport adds only authenticated admin routes; nil assembly exposes none.
func (h *Handler) registerSupport(admin gin.IRoutes) {
	if h.support == nil {
		return
	}
	admin.GET("/telegram-bot/recovery/tasks/:id/support-entries", h.supportEntries)
	admin.GET("/telegram-bot/recovery/tasks/:id/support-entries/:entry/operations", h.supportOperations)
	admin.POST("/telegram-bot/recovery/tasks/:id/support-entries/:entry/resolve", h.resolveSupport)
}

// supportEntries returns a bounded projection without executing recovery.
func (h *Handler) supportEntries(ctx *gin.Context) {
	actor, ok := h.ready(ctx)
	if !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	after, limit, ok := pagination(ctx)
	if !ok {
		return
	}
	result, err := h.support.SupportEntries(ctx.Request.Context(), actor, id, after, limit)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, result)
}

// supportEntryID parses a positive entry identifier, never a caller-supplied route.
func supportEntryID(ctx *gin.Context) (uint, bool) {
	id, err := ginutil.ParseParamUint(ctx, "entry")
	if err != nil || id == 0 {
		fail(ctx, c.ErrInvalid)
		return 0, false
	}
	return id, true
}

// supportOperations exposes current and legacy topic CAS for one owned entry.
func (h *Handler) supportOperations(ctx *gin.Context) {
	actor, ok := h.ready(ctx)
	if !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	entry, ok := supportEntryID(ctx)
	if !ok {
		return
	}
	after, limit, ok := pagination(ctx)
	if !ok {
		return
	}
	result, err := h.support.SupportOperations(ctx.Request.Context(), actor, id, entry, after, limit)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, result)
}

var supportEvidence = regexp.MustCompile(`^(ticket|audit|case):[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// resolveSupport rejects unknown JSON keys, oversized input and client identity
// injection. Success means the audited CAS was saved, not that the parent completed.
func (h *Handler) resolveSupport(ctx *gin.Context) {
	actor, ok := h.ready(ctx)
	if !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	entry, ok := supportEntryID(ctx)
	if !ok {
		return
	}
	var input c.SupportResolution
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(ctx, c.ErrInvalid)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		fail(ctx, c.ErrInvalid)
		return
	}
	if input.TaskVersion == 0 || input.TaskVersion >= 1<<63-1 || input.Revision == 0 || input.Revision >= 1<<63-1 || input.OperationID == 0 || input.OperationVersion == 0 || input.OperationVersion >= 1<<63-1 || !supportEvidence.MatchString(input.Evidence) || (input.Decision != "retry" && input.Decision != "failed" && input.Decision != "succeeded") || (input.Decision == "succeeded" && input.ThreadID <= 0) || (input.Decision != "succeeded" && input.ThreadID != 0) {
		fail(ctx, c.ErrInvalid)
		return
	}
	if err := h.support.ResolveSupport(ctx.Request.Context(), actor, id, entry, input); err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"resolved": true})
}
