package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// RunAccessReconcile runs a separate bounded read queue under the real runtime fence.
// bot and limit scope the scan; unknown approvals are never sent again, even after lease loss.
func (service *AccessService) RunAccessReconcile(ctx context.Context, bot uint, limit int) (int, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	for completed := 0; completed < limit; completed++ {
		token := make([]byte, 24)
		if _, err := rand.Read(token); err != nil {
			return completed, err
		}
		operation, err := service.Store.ClaimAccessReconcile(ctx, bot, hex.EncodeToString(token), service.accessNow(), accessLeaseDuration)
		if errors.Is(err, c.ErrNotFound) {
			return completed, nil
		}
		if err != nil {
			return completed, err
		}
		if err := service.executeAccessReconcile(ctx, operation); err != nil {
			return completed, err
		}
	}
	return limit, nil
}

// prepareAccessReconcile checks immutable provenance and the exact current physical route, not current binding.
// Returns a renewed route snapshot suitable only for the immediately following read request.
func (service *AccessService) prepareAccessReconcile(ctx context.Context, operation *d.AccessOperation) (c.AccessRoute, error) {
	route, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return route, err
	}
	if route.TargetID != operation.TargetID || !sameAccessRoute(route, operation.BotID, operation.ChatID, operation.RouteVersion) {
		return route, c.ErrConflict
	}
	if err := checkAccessFence(ctx, route); err != nil {
		return route, err
	}
	if _, err := service.Store.ReconcileAccessEligibility(ctx, operation, service.accessNow()); err != nil {
		return route, err
	}
	operation.Generation = route.Generation
	if err := service.Store.RenewAccess(ctx, operation, service.accessNow(), accessLeaseDuration); err != nil {
		return route, err
	}
	return route, service.verifyReconcileRoute(ctx, route)
}

// verifyReconcileRoute re-reads the current route and checks the live execution fence.
// route is the pre-call snapshot; changed routing cannot receive an old physical member observation.
func (service *AccessService) verifyReconcileRoute(ctx context.Context, route c.AccessRoute) error {
	latest, err := service.Routes.MembershipRoute(ctx, route.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return c.ErrConflict
	}
	return checkAccessFence(ctx, route)
}

// executeAccessReconcile performs GetMember outside transactions and atomically commits under the read lease.
// It never invokes approve/decline/ban; network failures and left are only current observation results.
func (service *AccessService) executeAccessReconcile(ctx context.Context, operation *d.AccessOperation) error {
	observedAt := service.accessNow()
	route, err := service.prepareAccessReconcile(ctx, operation)
	result := c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "unavailable"}}
	if err == nil {
		slog.InfoContext(ctx, "membership_reconcile_started", "operation_id", operation.ID, "parent_operation_id", operation.ParentOperationID)
		observedAt = service.accessNow()
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result = service.Platform.GetMembershipMember(callCtx, c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID})
		cancel()
		err = service.verifyReconcileRoute(ctx, route)
	}
	if err != nil {
		result = c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "unavailable"}}
		if errors.Is(err, c.ErrConflict) {
			result.ErrorClass = "route_changed"
		} else if errors.Is(err, c.ErrUntrustedIdentity) {
			result.ErrorClass = "identity"
		}
	}
	if result.Outcome == c.AccessSucceeded {
		switch result.Status {
		case "member", "administrator", "creator", "restricted", "left", "kicked":
		default:
			result = c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "invalid_response"}}
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = service.Store.FinishAccessReconcile(finishCtx, operation, result, observedAt, service.accessNow())
	slog.InfoContext(ctx, "membership_reconcile_finished", "operation_id", operation.ID, "outcome", result.Outcome, "persisted", err == nil)
	return err
}

// RevokeMembershipInvite 为 identity 拥有的已知入口 id 请求撤销；不等同权益撤权。
// 路由已变化时停止，旧 Bot 的邀请管理权限不转授给新 Bot。
func (service *AccessService) RevokeMembershipInvite(ctx context.Context, identity c.AccessIdentity, id uint) error {
	invite, err := service.Store.Invite(ctx, id)
	if err != nil {
		return err
	}
	if err := service.authorizeAccess(ctx, identity, invite.TargetID); err != nil {
		return err
	}
	route, err := service.Routes.MembershipRoute(ctx, invite.TargetID)
	if err != nil {
		return err
	}
	if !sameAccessRoute(route, invite.BotID, invite.ChatID, invite.RouteVersion) {
		return c.ErrConflict
	}
	_, err = service.Store.EnqueueRevoke(ctx, identity, id, service.accessNow())
	return err
}

// QueryMembershipMember 只定点核实有历史可信来源的 identity，不枚举或导入存量成员。
// 查询可以记录真实状态，但不将未知邀请创建/拒绝/撤销标为成功。
func (service *AccessService) QueryMembershipMember(ctx context.Context, identity c.AccessIdentity, target uint, sequence int64) (bool, error) {
	if err := service.authorizeAccess(ctx, identity, target); err != nil {
		return false, err
	}
	if _, err := service.Store.Eligibility(ctx, identity, target, service.accessNow()); err != nil {
		return false, err
	}
	route, err := service.Routes.MembershipRoute(ctx, target)
	if err != nil {
		return false, err
	}
	if route.BotID != identity.BotID {
		return false, c.ErrCapability
	}
	if err := checkAccessFence(ctx, route); err != nil {
		return false, err
	}
	// 以开始查询时刻排序，慢响应不能覆盖查询期间更新的 chat_member 事实。
	observedAt := service.accessNow()
	result := service.Platform.GetMembershipMember(ctx, c.AccessJoinAction{Route: route, TelegramID: identity.TelegramID})
	if result.Outcome != c.AccessSucceeded {
		return false, c.ErrCapability
	}
	if err := checkAccessFence(ctx, route); err != nil {
		return false, err
	}
	return service.ObserveMembership(ctx, c.AccessObservation{TargetID: target, BotID: route.BotID, ChatID: route.ChatID, RouteVersion: route.RouteVersion, TelegramID: identity.TelegramID, Status: result.Status, IsMember: result.IsMember, ObservedAt: observedAt, Sequence: sequence, Source: "query"})
}
