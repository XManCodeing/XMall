package http

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// Execution contains trusted application callbacks. A nil port leaves execution
// routes unregistered; neither callback may accept completion claims from HTTP.
// Confirm binds a task ID to its preflight reference; Resume performs a bounded
// recovery pass using the supplied version and authenticated administrator.
type Execution struct {
	Confirm func(context.Context, uint, c.Confirmation) (*d.Task, error)
	Resume  func(context.Context, uint, uint64, uint) (*d.Task, error)
	// Available evaluates server-loaded scope and supported purpose without mutation.
	Available func(context.Context, *c.Detail) bool
}

// WithExecution installs a complete executor before route registration and returns
// the same handler. Incomplete assembly deliberately exposes no mutation routes.
func (handler *Handler) WithExecution(execution Execution) *Handler {
	if execution.Confirm != nil && execution.Resume != nil {
		handler.execution = execution
	}
	return handler
}

// groupExecutionAvailable fails closed unless the assembled adapter explicitly supports the task.
// detail is server-loaded; unsupported support/cross-Bot tasks never advertise execution.
func (handler *Handler) groupExecutionAvailable(ctx context.Context, detail *c.Detail) bool {
	return handler.execution.Confirm != nil && handler.execution.Resume != nil && handler.execution.Available != nil && detail != nil && detail.Task.Kind == d.GroupReplace && handler.execution.Available(ctx, detail)
}

// Confirm accepts only administrator intent, task version and preflight reference.
// It returns persisted progress; Telegram success cannot be asserted by the client.
func (handler *Handler) Confirm(ctx *gin.Context) {
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
	if input.ExpectedVersion == 0 || input.PreflightRef == "" || len(input.PreflightRef) > 128 || handler.execution.Confirm == nil {
		fail(ctx, c.ErrInvalid)
		return
	}
	confirm := handler.execution.Confirm
	if handler.service.Bots != nil {
		detail, err := handler.service.Store.Get(ctx.Request.Context(), id)
		if err != nil {
			fail(ctx, err)
			return
		}
		if detail.Task.Kind == d.BotReplace {
			confirm = handler.service.Bots.Confirm
		}
	}
	task, err := confirm(ctx.Request.Context(), id, c.Confirmation{
		PreflightRef: input.PreflightRef, ExpectedVersion: input.ExpectedVersion, ActorID: actor,
	})
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, task)
}

// Resume requests one version-fenced recovery pass. The application decides which
// persisted steps are safe to retry and never blindly replays unknown outcomes.
func (handler *Handler) Resume(ctx *gin.Context) {
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
	if input.ExpectedVersion == 0 || handler.execution.Resume == nil {
		fail(ctx, c.ErrInvalid)
		return
	}
	resume := handler.execution.Resume
	if handler.service.Bots != nil {
		detail, err := handler.service.Store.Get(ctx.Request.Context(), id)
		if err != nil {
			fail(ctx, err)
			return
		}
		if detail.Task.Kind == d.BotReplace {
			resume = handler.service.Bots.Resume
		}
	}
	task, err := resume(ctx.Request.Context(), id, input.ExpectedVersion, actor)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, task)
}
