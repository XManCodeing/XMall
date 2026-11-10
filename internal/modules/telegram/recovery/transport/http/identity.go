package http

import (
	"errors"
	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	support "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// identityDetail returns real persisted mapping rows at the response data root.
// detail comes from authenticated server lookup; no query/body filter can widen its scope.
func (handler *Handler) identityDetail(ctx *gin.Context, detail *c.Detail) {
	if handler.service.Identity.Backend == nil {
		fail(ctx, c.ErrInvalid)
		return
	}
	mappings, err := handler.service.Identity.Backend.Mappings(ctx.Request.Context(), detail)
	if err != nil {
		identityFailure(ctx, err)
		return
	}
	if mappings == nil {
		mappings = []c.IdentityMapping{}
	}
	response.Success(ctx, struct {
		*c.Detail
		ExecutionAvailable bool                `json:"execution_available"`
		IdentityMappings   []c.IdentityMapping `json:"identity_mappings"`
	}{Detail: detail, ExecutionAvailable: true, IdentityMappings: mappings})
}

// registerIdentity exposes mutation routes only when the production executor is assembled.
func (handler *Handler) registerIdentity(admin gin.IRoutes) {
	if handler.service == nil || handler.service.Identity == nil {
		return
	}
	admin.POST("/telegram-bot/recovery/identity-preflight", handler.IdentityPreview)
	admin.POST("/telegram-bot/recovery/tasks/:id/identity-confirm", handler.IdentityConfirm)
	admin.POST("/telegram-bot/recovery/tasks/:id/identity-resume", handler.IdentityResume)
}

// identityFailure converts membership failures to fixed recovery classes without SQL disclosure.
func identityFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, support.ErrDenied):
		ctx.AbortWithStatusJSON(403, gin.H{"code": 403, "error_class": "forbidden"})
		return
	case errors.Is(err, m.ErrInvalid):
		err = c.ErrInvalid
	case errors.Is(err, m.ErrNotFound):
		err = c.ErrNotFound
	case errors.Is(err, m.ErrConflict), errors.Is(err, m.ErrUntrustedIdentity), errors.Is(err, m.ErrCapability):
		err = c.ErrConflict
	}
	fail(ctx, err)
}

// IdentityPreview binds the authenticated admin to human verification and immutable versions.
func (handler *Handler) IdentityPreview(ctx *gin.Context) {
	actor, ok := handler.ready(ctx)
	if !ok {
		return
	}
	var input a.IdentityPreflightInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	task, err := handler.service.Identity.Preview(ctx.Request.Context(), actor, input)
	if err != nil {
		identityFailure(ctx, err)
		return
	}
	response.Success(ctx, task)
}

// IdentityConfirm accepts only opaque preflight reference and expected task version.
// Untrusted identity/actor fields have no effect; all execution input is server-owned.
func (handler *Handler) IdentityConfirm(ctx *gin.Context) {
	actor, ok := handler.ready(ctx)
	if !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	var input struct {
		PreflightRef    string `json:"preflight_ref"`
		ExpectedVersion uint64 `json:"expected_version,string"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	if input.ExpectedVersion == 0 || len(input.PreflightRef) != 64 {
		fail(ctx, c.ErrInvalid)
		return
	}
	task, err := handler.service.Identity.Confirm(ctx.Request.Context(), id, c.Confirmation{PreflightRef: input.PreflightRef, ExpectedVersion: input.ExpectedVersion, ActorID: actor})
	if err != nil {
		identityFailure(ctx, err)
		return
	}
	response.Success(ctx, task)
}

// IdentityResume commits or reconciles a task using exact CAS; it does not call Telegram.
func (handler *Handler) IdentityResume(ctx *gin.Context) {
	actor, ok := handler.ready(ctx)
	if !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expected_version,string"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	if input.ExpectedVersion == 0 {
		fail(ctx, c.ErrInvalid)
		return
	}
	task, err := handler.service.Identity.Resume(ctx.Request.Context(), id, input.ExpectedVersion, actor)
	if err != nil {
		identityFailure(ctx, err)
		return
	}
	response.Success(ctx, task)
}
