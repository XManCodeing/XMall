package membershiphttp

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// RegisterCombinedRefundRoutes registers one atomic money-plus-intent operation, never a client-side two-call sequence.
// workflow independently verifies the two underlying permissions beyond route Casbin coverage.
func RegisterCombinedRefundRoutes(admin gin.IRoutes, workflow order.MembershipRefundAdmin) {
	admin.GET("/telegram-bot/membership/orders/:id/refund-and-terminate", func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "no-store")
		actor, ok := ginutil.GetAdminID(ctx)
		if !ok {
			return
		}
		id, err := parseID(ctx.Param("id"), false)
		if err != nil {
			ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
			return
		}
		if workflow == nil {
			ginutil.RespondError(ctx, response.CodeInternal, "error.membership_access_unavailable", nil)
			return
		}
		if ctx.Query("preview") == "1" {
			combinedRefundPreview(ctx, workflow, actor, id)
			return
		}
		rows, err := workflow.Requests(ctx.Request.Context(), actor, id)
		if err != nil {
			code, key := response.CodeInternal, "error.membership_access_unavailable"
			if errors.Is(err, order.ErrRefundRequestDenied) {
				code, key = response.CodeForbidden, "error.forbidden"
			} else {
				// Identifiers only: raw persistence errors may contain sensitive command values.
				slog.ErrorContext(ctx.Request.Context(), "membership_refund_preview_unavailable", "order_id", id, "admin_id", actor)
			}
			ginutil.RespondError(ctx, code, key, nil)
			return
		}
		response.Success(ctx, gin.H{"items": rows})
	})
	admin.POST("/telegram-bot/membership/orders/:id/refund-and-terminate", func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "no-store")
		actor, ok := ginutil.GetAdminID(ctx)
		if !ok {
			return
		}
		id, err := parseID(ctx.Param("id"), false)
		var payload struct {
			order.MembershipRefundInput
			ManualConfirmed bool `json:"manual_confirmed"`
		}
		if err != nil || ctx.ShouldBindJSON(&payload) != nil || payload.OrderID != 0 && payload.OrderID != id || payload.Method == "manual" && !payload.ManualConfirmed {
			ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
			return
		}
		input := payload.MembershipRefundInput
		input.OrderID, input.ActorAdminID = id, actor
		if workflow == nil {
			ginutil.RespondError(ctx, response.CodeInternal, "error.membership_access_unavailable", nil)
			return
		}
		result, err := workflow.Execute(ctx.Request.Context(), input)
		if err != nil {
			code, key := response.CodeBadRequest, "error.membership_access_invalid"
			if errors.Is(err, order.ErrRefundRequestDenied) {
				code, key = response.CodeForbidden, "error.forbidden"
			}
			if errors.Is(err, order.ErrRefundRequestConflict) {
				key = "error.telegram_bot_conflict"
			}
			ginutil.RespondError(ctx, code, key, nil)
			return
		}
		response.Success(ctx, result)
	})
}

// combinedRefundPreview validates bounded query IDs and delegates only to the authorized read workflow.
// Reuses the GET route and its existing RBAC policy; no monetary operation is called.
func combinedRefundPreview(ctx *gin.Context, workflow order.MembershipRefundAdmin, actor, orderID uint) {
	reader, ok := workflow.(interface {
		Preview(context.Context, uint, uint, string, []uint) (*membership.RefundPreview, error)
	})
	parts := strings.Split(ctx.Query("item_ids"), ",")
	if !ok || len(parts) > 100 {
		ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
		return
	}
	items := make([]uint, 0, len(parts))
	for _, part := range parts {
		item, err := strconv.ParseUint(part, 10, 32)
		if err != nil || item == 0 {
			ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
			return
		}
		items = append(items, uint(item))
	}
	preview, err := reader.Preview(ctx.Request.Context(), actor, orderID, ctx.Query("method"), items)
	if err != nil {
		code, key := response.CodeBadRequest, "error.membership_access_invalid"
		if errors.Is(err, order.ErrRefundRequestDenied) {
			code, key = response.CodeForbidden, "error.forbidden"
		}
		ginutil.RespondError(ctx, code, key, nil)
		return
	}
	response.Success(ctx, preview)
}
