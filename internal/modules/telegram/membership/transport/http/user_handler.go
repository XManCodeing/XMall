package membershiphttp

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// ErrAccessDenied hides whether a target belongs to a different account.
var ErrAccessDenied = errors.New("membership access denied")

// GroupItem exposes only the signed-in account's membership facts, never physical routes or invitation secrets.
type GroupItem struct {
	TargetID              string     `json:"target_id"`
	Title                 string     `json:"title"`
	BeneficiaryTelegramID string     `json:"beneficiary_telegram_id"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	State                 string     `json:"state"`
	MemberStatus          string     `json:"member_status,omitempty"`
	CanRequest            bool       `json:"can_request"`
}

// GroupPage carries a bounded, stable cursor rather than loading all historical purchases.
type GroupPage struct {
	Items      []GroupItem `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// InvitationResult discloses a secret only after an explicit, authorized request.
type InvitationResult struct {
	InviteID  uint       `json:"-"` // Internal Bot correlation; never exposed by public reads.
	State     string     `json:"state"`
	InviteURL string     `json:"invite_url,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// UserAccess is the account-scoped application adapter; account IDs originate exclusively in JWT middleware.
// List is read-only. Request durably schedules work and never performs Telegram HTTP on this request.
type UserAccess interface {
	List(context.Context, uint, uint, int) (GroupPage, error)
	Request(context.Context, uint, uint) (InvitationResult, error)
}

// UserHandler serves existing members independently of the new-sales gate and Bot private reachability.
type UserHandler struct{ access UserAccess }

// NewUserHandler binds an access port. A missing dependency fails closed without issuing invitations.
func NewUserHandler(access UserAccess) *UserHandler { return &UserHandler{access: access} }

// RegisterUserRoutes mounts authenticated website routes; the caller supplies existing JWT and rate-limit middleware.
func RegisterUserRoutes(user *gin.RouterGroup, handler *UserHandler) {
	user.GET("/user/membership-groups", handler.List)
	user.POST("/user/membership-groups/:target_id/invitation", handler.Request)
}

// List validates cursor/limit, reads only the authenticated account, and prevents cache storage.
func (handler *UserHandler) List(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	account, ok := ginutil.GetUserID(ctx)
	if !ok {
		return
	}
	cursor, err := parseID(ctx.DefaultQuery("cursor", "0"), true)
	limit, limitErr := strconv.Atoi(ctx.DefaultQuery("limit", "20"))
	if err != nil || limitErr != nil || limit < 1 || limit > 100 {
		ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
		return
	}
	if handler.access == nil {
		handler.respondError(ctx, errors.New("membership access unavailable"))
		return
	}
	page, err := handler.access.List(ctx.Request.Context(), account, cursor, limit)
	if err != nil {
		handler.respondError(ctx, err)
		return
	}
	if page.Items == nil {
		page.Items = []GroupItem{}
	}
	response.Success(ctx, page)
}

// Request uses only the authenticated account and validated target; client-supplied identity and route fields have no authority.
func (handler *UserHandler) Request(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	ctx.Header("Referrer-Policy", "no-referrer")
	account, ok := ginutil.GetUserID(ctx)
	if !ok {
		return
	}
	target, err := parseID(ctx.Param("target_id"), false)
	if err != nil {
		ginutil.RespondError(ctx, response.CodeBadRequest, "error.membership_access_invalid", nil)
		return
	}
	if handler.access == nil {
		handler.respondError(ctx, errors.New("membership access unavailable"))
		return
	}
	result, err := handler.access.Request(ctx.Request.Context(), account, target)
	if err != nil {
		handler.respondError(ctx, err)
		return
	}
	response.Success(ctx, result)
}

// respondError maps errors to stable localized messages without logging credentials or raw platform diagnostics.
func (handler *UserHandler) respondError(ctx *gin.Context, err error) {
	if errors.Is(err, ErrAccessDenied) {
		ginutil.RespondError(ctx, response.CodeForbidden, "error.forbidden", nil)
		return
	}
	ginutil.RespondError(ctx, response.CodeInternal, "error.membership_access_unavailable", nil)
}

// parseID accepts canonical unsigned decimal IDs; allowZero applies only to the first-page cursor.
func parseID(raw string, allowZero bool) (uint, error) {
	value, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || (!allowZero && value == 0) || strconv.FormatUint(value, 10) != raw {
		return 0, errors.New("invalid membership identifier")
	}
	return uint(value), nil
}
