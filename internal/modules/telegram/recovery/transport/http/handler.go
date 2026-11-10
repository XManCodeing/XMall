package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/XManCodeing/XMall/internal/i18n"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	bot "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	support "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// Handler exposes persisted progress and optionally a fully assembled executor.
type Handler struct {
	service   *a.Service
	execution Execution
	support   c.SupportManagement
}

// New receives the assembled service and returns an HTTP adapter without side effects.
func New(service *a.Service) *Handler { return &Handler{service: service} }

// RegisterRoutes installs authenticated read/preflight routes and, when fully
// assembled, version-fenced execution routes beneath admin JWT/Casbin middleware.
func RegisterRoutes(admin gin.IRoutes, handler *Handler) {
	handler.registerIdentity(admin)
	handler.registerSupport(admin)
	if handler.service != nil && handler.service.Bots != nil {
		admin.POST("/telegram-bot/recovery/bot-preflight", handler.PreviewBot)
	}
	admin.GET("/telegram-bot/recovery/tasks", handler.List)
	admin.GET("/telegram-bot/recovery/tasks/:id", handler.Get)
	admin.GET("/telegram-bot/recovery/tasks/:id/steps", handler.Steps)
	admin.POST("/telegram-bot/recovery/group-preflight", handler.Preview)
	admin.POST("/telegram-bot/recovery/tasks/:id/cancel", handler.Cancel)
	if handler.execution.Confirm != nil && handler.execution.Resume != nil {
		admin.POST("/telegram-bot/recovery/tasks/:id/confirm", handler.Confirm)
		admin.POST("/telegram-bot/recovery/tasks/:id/resume", handler.Resume)
	}
}

// fail maps internal errors to localized responses without leaking SQL, secrets or platform text.
func fail(ctx *gin.Context, err error) {
	status, key, kind := 500, "error.internal_error", "internal"
	switch {
	case errors.Is(err, support.ErrDenied):
		status, key, kind = 403, "error.forbidden", "forbidden"
	case errors.Is(err, runtime.ErrRecoveryBlocked):
		status, key, kind = 409, "error.telegram_bot_conflict", "exit_proof_required"
	case errors.Is(err, bot.ErrContinuationRequired):
		status, key, kind = 409, "error.telegram_bot_conflict", "bot_continuation_required"
	case errors.Is(err, c.ErrInvalid), errors.Is(err, groups.ErrInvalid):
		status, key, kind = 400, "error.bad_request", "invalid"
	case errors.Is(err, c.ErrNotFound), errors.Is(err, groups.ErrNotFound):
		status, key, kind = 404, "error.not_found", "not_found"
	case errors.Is(err, a.ErrRecoveryReview):
		status, key, kind = 409, "error.telegram_bot_conflict", "needs_review"
	case errors.Is(err, a.ErrRecoveryPending):
		status, key, kind = 409, "error.telegram_bot_conflict", "recovery_pending"
	case errors.Is(err, a.ErrRecoveryIncomplete):
		status, key, kind = 409, "error.telegram_bot_conflict", "recovery_incomplete"
	case errors.Is(err, c.ErrExpired):
		status, key, kind = 409, "error.telegram_bot_conflict", "expired"
	case errors.Is(err, c.ErrConflict), errors.Is(err, c.ErrStale), errors.Is(err, c.ErrScopeBusy), errors.Is(err, groups.ErrConflict):
		status, key, kind = 409, "error.telegram_bot_conflict", "conflict"
	case errors.Is(err, groups.ErrCapability):
		status, key, kind = 409, "error.telegram_group_capability", "capability_unavailable"
	}
	ctx.AbortWithStatusJSON(status, gin.H{"code": status, "message": i18n.T(i18n.ResolveLocale(ctx), key), "error_class": kind})
}

// ready checks authentication and assembly for every handler; route middleware remains mandatory.
func (handler *Handler) ready(ctx *gin.Context) (uint, bool) {
	ctx.Header("Cache-Control", "no-store")
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return 0, false
	}
	if handler.service == nil || handler.service.Store == nil {
		fail(ctx, errors.New("recovery unavailable"))
		return 0, false
	}
	return actor, true
}

// taskID parses the positive path ID without silently accepting overflow or zero.
func taskID(ctx *gin.Context) (uint, bool) {
	id, err := ginutil.ParseParamUint(ctx, "id")
	if err != nil || id == 0 {
		fail(ctx, c.ErrInvalid)
		return 0, false
	}
	return id, true
}

// pagination validates bounded cursor query parameters and returns the cursor and limit.
func pagination(ctx *gin.Context) (uint, int, bool) {
	after, err := strconv.ParseUint(ctx.DefaultQuery("after_id", "0"), 10, 64)
	limit, limitErr := strconv.Atoi(ctx.DefaultQuery("limit", "20"))
	if err != nil || uint64(uint(after)) != after || limitErr != nil || limit < 1 || limit > 100 {
		fail(ctx, c.ErrInvalid)
		return 0, 0, false
	}
	return uint(after), limit, true
}

// List returns an authenticated, bounded page of persisted tasks; it never runs work.
func (handler *Handler) List(ctx *gin.Context) {
	if _, ok := handler.ready(ctx); !ok {
		return
	}
	input, numbered, valid := numberedPagination(ctx)
	if !valid {
		return
	}
	filter := c.ListFilter{Kind: d.Kind(ctx.Query("kind")), State: d.State(ctx.Query("state"))}
	var page c.TaskPage
	var err error
	if numbered {
		page, err = handler.service.Store.ListPage(ctx.Request.Context(), filter, input)
	} else {
		after, limit, ok := pagination(ctx)
		if !ok {
			return
		}
		filter.AfterID, filter.Limit = after, limit
		page, err = handler.service.Store.List(ctx.Request.Context(), filter)
	}
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, page)
}

// Get returns the typed safe snapshot for a persisted task ID.
func (handler *Handler) Get(ctx *gin.Context) {
	if _, ok := handler.ready(ctx); !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	detail, err := handler.service.Store.Get(ctx.Request.Context(), id)
	if err != nil {
		fail(ctx, err)
		return
	}
	if detail.Task.Kind == "identity_transfer" && handler.service.Identity != nil {
		handler.identityDetail(ctx, detail)
		return
	}
	response.Success(ctx, struct {
		*c.Detail
		ExecutionAvailable   bool   `json:"execution_available"`
		PreparationAvailable bool   `json:"preparation_available"`
		Blocker              string `json:"blocker,omitempty"`
	}{Detail: detail, ExecutionAvailable: handler.groupExecutionAvailable(ctx.Request.Context(), detail) || (detail.Task.Kind == d.BotReplace && handler.botExecutionBlocker() == "") || (handler.service.Identity != nil && detail.Task.Kind == "identity_transfer"), PreparationAvailable: handler.botPreparationAvailable() && detail.Task.Kind == d.BotReplace, Blocker: handler.botBlocker(detail.Task.Kind)})
}

// botPreparationAvailable describes constructor capability only, never actor permission
// or a completed preflight. Each mutation still performs its exact authorization.
func (handler *Handler) botPreparationAvailable() bool {
	if handler.service == nil || handler.service.Bots == nil {
		return false
	}
	service := handler.service.Bots
	return service.Store != nil && service.Backend != nil && service.Authorize != nil
}

// botExecutionBlocker reports the first missing production dependency without
// claiming drain, platform capability or module completion has been verified.
func (handler *Handler) botExecutionBlocker() string {
	if !handler.botPreparationAvailable() || handler.execution.Confirm == nil || handler.execution.Resume == nil {
		return "execution_unavailable"
	}
	return handler.service.Bots.ExecutionBlocker()
}

// botBlocker only describes assembly for Bot replacement tasks; runtime evidence
// remains in durable task/step progress, not inferred from these capability flags.
func (handler *Handler) botBlocker(kind d.Kind) string {
	if kind == d.BotReplace {
		return handler.botExecutionBlocker()
	}
	return ""
}

// Steps returns a separate bounded page instead of expanding every checkpoint in task detail.
func (handler *Handler) Steps(ctx *gin.Context) {
	if _, ok := handler.ready(ctx); !ok {
		return
	}
	id, ok := taskID(ctx)
	if !ok {
		return
	}
	input, numbered, valid := numberedPagination(ctx)
	if !valid {
		return
	}
	var page c.StepPage
	var err error
	if numbered {
		page, err = handler.service.Store.ListStepsPage(ctx.Request.Context(), id, input)
	} else {
		after, limit, ok := pagination(ctx)
		if !ok {
			return
		}
		page, err = handler.service.Store.ListSteps(ctx.Request.Context(), id, after, limit)
	}
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, page)
}

// Preview stores an idempotent, read-only same-Bot group replacement preflight.
func (handler *Handler) Preview(ctx *gin.Context) {
	actor, ok := handler.ready(ctx)
	if !ok {
		return
	}
	var input a.GroupPreflightInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(ctx, err)
		return
	}
	task, err := handler.service.PreviewGroup(ctx.Request.Context(), actor, input)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, task)
}

// PreviewBot persists the safety-stage inventory; HTTP cannot supply route scope or completion proof.
func (handler *Handler) PreviewBot(ctx *gin.Context) {
	actor, ok := handler.ready(ctx)
	if !ok {
		return
	}
	var input bot.Input
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(ctx, c.ErrInvalid)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		fail(ctx, c.ErrInvalid)
		return
	}
	task, err := handler.service.Bots.Preview(ctx.Request.Context(), actor, input)
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, task)
}

// Cancel uses the authenticated actor and exact input version; identity tasks additionally
// require the narrow untouched-fencing proof, while other kinds retain preflight-only cancellation.
func (handler *Handler) Cancel(ctx *gin.Context) {
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
	detail, err := handler.service.Store.Get(ctx.Request.Context(), id)
	if err != nil {
		fail(ctx, err)
		return
	}
	cancellation := c.Cancellation{TaskID: id, ExpectedVersion: input.ExpectedVersion, ActorID: actor}
	var task *d.Task
	if detail.Task.Kind == d.IdentityTransfer {
		if handler.service.Identity == nil {
			fail(ctx, c.ErrInvalid)
			return
		}
		task, err = handler.service.Identity.Cancel(ctx.Request.Context(), cancellation)
		if err != nil {
			identityFailure(ctx, err)
			return
		}
	} else {
		task, err = handler.service.Store.Cancel(ctx.Request.Context(), cancellation)
	}
	if err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, task)
}
