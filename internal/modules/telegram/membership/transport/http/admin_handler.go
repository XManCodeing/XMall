package membershiphttp

import (
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
	"log/slog"
	"net/url"
	"strconv"
)

// AdminHandler exposes the recovery queue under the existing admin JWT/Casbin middleware.
type AdminHandler struct {
	access    c.AccessAdmin
	refunds   c.RefundAdmin
	lifecycle c.LifecycleAdmin
}

// WithDisposition binds existing refund evidence and lifecycle recovery ports; nil ports fail closed.
func (handler *AdminHandler) WithDisposition(refunds c.RefundAdmin, lifecycle c.LifecycleAdmin) *AdminHandler {
	handler.refunds, handler.lifecycle = refunds, lifecycle
	return handler
}

// NewAdminHandler binds the assembled service without opening sales or starting workers.
func NewAdminHandler(access c.AccessAdmin) *AdminHandler { return &AdminHandler{access: access} }

// RegisterAdminRoutes registers separate query and recovery permissions; callers supply authenticated admin scope.
func RegisterAdminRoutes(admin gin.IRoutes, handler *AdminHandler) {
	admin.GET("/telegram-bot/membership/access/tasks", handler.Tasks)
	admin.GET("/telegram-bot/membership/access/reviews", handler.Reviews)
	admin.POST("/telegram-bot/membership/access/tasks/:id/recover", handler.Recover)
	admin.GET("/telegram-bot/membership/refunds", handler.RefundCandidates)
	admin.POST("/telegram-bot/membership/refunds/dispose", handler.DisposeRefund)
	admin.GET("/telegram-bot/membership/lifecycle/tasks", handler.LifecycleTasks)
	admin.POST("/telegram-bot/membership/lifecycle/tasks/:id/recover", handler.RecoverLifecycle)
}

// accessAdminPage validates canonical bot/cursor and bounded limit before any repository call.
func accessAdminPage(ctx *gin.Context) (uint, uint, int, error) {
	bot, err := parseID(ctx.Query("bot_id"), false)
	if err != nil {
		return 0, 0, 0, c.ErrInvalid
	}
	cursor, err := parseID(ctx.DefaultQuery("cursor", "0"), true)
	limit, limitErr := strconv.Atoi(ctx.DefaultQuery("limit", "20"))
	if err != nil || limitErr != nil || limit < 1 || limit > 100 {
		return 0, 0, 0, c.ErrInvalid
	}
	return bot, cursor, limit, nil
}

// Tasks returns secret-free per-group operation history with a stable exclusive cursor.
func (handler *AdminHandler) Tasks(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	if handler.accessSummary(ctx, false) || handler.numberedPage(ctx, "tasks") {
		return
	}
	bot, cursor, limit, err := accessAdminPage(ctx)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	if handler.access == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	rows, err := handler.access.ListAccessTasks(ctx.Request.Context(), bot, cursor, limit)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	next := ""
	if len(rows) == limit {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	response.Success(ctx, gin.H{"items": rows, "next_cursor": next})
}

// Reviews returns unresolved applications independently of whether a trusted invitation could be matched.
func (handler *AdminHandler) Reviews(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	if handler.accessSummary(ctx, true) || handler.numberedPage(ctx, "reviews") {
		return
	}
	bot, cursor, limit, err := accessAdminPage(ctx)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	if handler.access == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	rows, err := handler.access.ListAccessReviews(ctx.Request.Context(), bot, cursor, limit)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	next := ""
	if len(rows) == limit {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	response.Success(ctx, gin.H{"items": rows, "next_cursor": next})
}

// Recover obtains actor solely from the authenticated admin context and rejects malformed versions/actions.
func (handler *AdminHandler) Recover(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return
	}
	bot, botErr := parseID(ctx.Query("bot_id"), false)
	id, idErr := parseID(ctx.Param("id"), false)
	var input c.AccessRecoveryInput
	if botErr != nil || idErr != nil || ctx.ShouldBindJSON(&input) != nil {
		adminAccessError(ctx, c.ErrInvalid)
		return
	}
	if handler.access == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	if err := handler.access.RecoverAccess(ctx.Request.Context(), bot, id, actor, input); err != nil {
		adminAccessError(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"accepted": true})
}

// RefundCandidates returns verified item choices for an authorized order; never implies a refund allocation.
func (handler *AdminHandler) RefundCandidates(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	id, err := parseID(ctx.Query("order_id"), false)
	if err != nil {
		adminAccessError(ctx, c.ErrInvalid)
		return
	}
	if handler.refunds == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	rows, err := handler.refunds.RefundCandidates(ctx.Request.Context(), id)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"items": rows})
}

// DisposeRefund accepts an explicit purchase selection; success means queued, never removed from Telegram.
func (handler *AdminHandler) DisposeRefund(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return
	}
	var input c.RefundSelection
	if ctx.ShouldBindJSON(&input) != nil {
		adminAccessError(ctx, c.ErrInvalid)
		return
	}
	input.ActorAdminID = actor
	if handler.refunds == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	if err := handler.refunds.DisposeRefund(ctx.Request.Context(), input); err != nil {
		adminAccessError(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"accepted": true, "state": "queued"})
}

// LifecycleTasks exposes actual durable per-group phases including administrator and unknown-effect review.
func (handler *AdminHandler) LifecycleTasks(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	if _, ok := ginutil.GetAdminID(ctx); !ok {
		return
	}
	if handler.lifecycleSummary(ctx) || handler.numberedPage(ctx, "lifecycle") {
		return
	}
	bot, cursor, limit, err := accessAdminPage(ctx)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	if handler.lifecycle == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	rows, err := handler.lifecycle.Tasks(ctx.Request.Context(), bot, cursor, limit)
	if err != nil {
		adminAccessError(ctx, err)
		return
	}
	next := ""
	if len(rows) == limit {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	response.Success(ctx, gin.H{"items": rows, "next_cursor": next})
}

// RecoverLifecycle derives actor from authentication and forwards only versioned safe recovery commands.
func (handler *AdminHandler) RecoverLifecycle(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	actor, ok := ginutil.GetAdminID(ctx)
	if !ok {
		return
	}
	bot, botErr := parseID(ctx.Query("bot_id"), false)
	id, idErr := parseID(ctx.Param("id"), false)
	var input struct {
		ExpectedVersion uint64 `json:"expected_version,string"`
		Action          string `json:"action"`
	}
	if botErr != nil || idErr != nil || ctx.ShouldBindJSON(&input) != nil || input.ExpectedVersion == 0 {
		adminAccessError(ctx, c.ErrInvalid)
		return
	}
	if handler.lifecycle == nil {
		adminAccessError(ctx, c.ErrCapability)
		return
	}
	if err := handler.lifecycle.Recover(ctx.Request.Context(), c.LifecycleRecovery{BotID: bot, ID: id, ExpectedVersion: input.ExpectedVersion, ActorAdminID: actor, Action: input.Action}); err != nil {
		adminAccessError(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"accepted": true})
}

// numberedPage handles optional page/page_size on the existing authorized route.
// resource chooses its existing reader; absent pagination preserves the cursor contract.
func (handler *AdminHandler) numberedPage(ctx *gin.Context, resource string) bool {
	query, parseErr := url.ParseQuery(ctx.Request.URL.RawQuery)
	_, hasPage := query["page"]
	_, hasSize := query["page_size"]
	_, hasStatus := query["status"]
	_, hasOrder := query["order_no"]
	groupBy, hasGroup := query["group_by"]
	keywords, hasKeyword := query["keyword"]
	if !hasPage && !hasSize && !hasStatus && !hasOrder && !hasGroup && !hasKeyword {
		return false
	}
	if hasGroup && (resource != "lifecycle" || len(groupBy) != 1 || groupBy[0] != "telegram_id") {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	keyword, _, keywordErr := c.LifecycleKeyword(query.Get("keyword"))
	if hasKeyword && ((!hasGroup && resource != "reviews") || len(keywords) != 1 || keywordErr != nil) {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	_, orderErr := c.AccessOrderFilter(query["order_no"]...)
	if hasOrder && (resource != "tasks" || orderErr != nil || len(query["order_no"]) != 1) {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	_, statusErr := c.LifecycleFilterStates(query["status"]...)
	if resource == "reviews" {
		_, statusErr = c.AccessReviewFilter(query["status"]...)
	}
	if hasStatus && ((resource != "lifecycle" && resource != "reviews") || statusErr != nil || len(query["status"]) != 1) {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	bot, botErr := parseID(ctx.Query("bot_id"), false)
	page, pageErr := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	_, hasCursor := query["cursor"]
	_, hasLimit := query["limit"]
	if parseErr != nil || botErr != nil || pageErr != nil || sizeErr != nil || !c.ValidAdminPage(page, size) || hasCursor || hasLimit || len(query["page"]) > 1 || len(query["page_size"]) > 1 || len(query["bot_id"]) != 1 {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	var rows any
	var total int64
	var err error
	if resource == "lifecycle" && hasGroup {
		rows, total, err = handler.lifecycleUserPage(ctx, bot, page, size, query["status"], keyword)
	} else if resource == "lifecycle" {
		reader, ok := handler.lifecycle.(c.LifecyclePageReader)
		if !ok {
			adminAccessError(ctx, c.ErrCapability)
			return true
		}
		rows, total, err = reader.LifecycleTasksPage(ctx.Request.Context(), bot, page, size, query["status"]...)
	} else if resource == "reviews" && (keyword != "" || hasStatus) {
		reader, ok := handler.access.(c.AccessReviewSearchReader)
		if !ok {
			adminAccessError(ctx, c.ErrCapability)
			return true
		}
		rows, total, err = reader.SearchAccessReviewsPage(ctx.Request.Context(), bot, page, size, keyword, query["status"]...)
	} else {
		reader, ok := handler.access.(c.AccessPageReader)
		if !ok {
			adminAccessError(ctx, c.ErrCapability)
			return true
		}
		if resource == "reviews" {
			rows, total, err = reader.AccessReviewsPage(ctx.Request.Context(), bot, page, size)
		} else {
			rows, total, err = reader.AccessTasksPage(ctx.Request.Context(), bot, page, size, query["order_no"]...)
		}
	}
	if err != nil {
		slog.ErrorContext(ctx.Request.Context(), "membership_admin_page_failed", "resource", resource, "bot_id", bot)
		adminAccessError(ctx, err)
		return true
	}
	totalPage := total / int64(size)
	if total%int64(size) != 0 {
		totalPage++
	}
	response.Success(ctx, gin.H{"items": rows, "page": page, "page_size": size, "total": total, "total_page": totalPage})
	return true
}

// lifecycleUserPage dispatches a validated grouped request, preserving legacy empty-keyword readers.
// bot/page/size/status/keyword are already validated; missing search capability returns an explicit error.
func (handler *AdminHandler) lifecycleUserPage(ctx *gin.Context, bot uint, page, size int, status []string, keyword string) ([]c.LifecycleUserTasks, int64, error) {
	if keyword != "" {
		reader, ok := handler.lifecycle.(c.LifecycleUserSearchReader)
		if !ok {
			return nil, 0, c.ErrCapability
		}
		filter := ""
		if len(status) == 1 {
			filter = status[0]
		}
		return reader.SearchLifecycleUsersPage(ctx.Request.Context(), bot, page, size, filter, keyword)
	}
	reader, ok := handler.lifecycle.(c.LifecycleUserPageReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	return reader.LifecycleUsersPage(ctx.Request.Context(), bot, page, size, status...)
}

// adminAccessError maps stable errors to existing three-language messages, without database/API diagnostics.
func adminAccessError(ctx *gin.Context, err error) {
	code, key := response.CodeInternal, "error.membership_access_unavailable"
	switch {
	case errors.Is(err, c.ErrInvalid):
		code, key = response.CodeBadRequest, "error.membership_access_invalid"
	case errors.Is(err, c.ErrNotFound):
		code, key = response.CodeNotFound, "error.not_found"
	case errors.Is(err, c.ErrConflict), errors.Is(err, c.ErrCapability), errors.Is(err, c.ErrUntrustedIdentity):
		code, key = response.CodeBadRequest, "error.telegram_bot_conflict"
	}
	ginutil.RespondError(ctx, code, key, nil)
}
