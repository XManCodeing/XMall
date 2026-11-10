package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// ExecuteRecoveryOperation executes only an already claimed task-scoped operation.
// Recovery callers must supply narrow repository/routes/platform and execution fence;
// ordinary scheduling remains RunAccess and cannot select arbitrary operation IDs.
func (service *AccessService) ExecuteRecoveryOperation(ctx context.Context, operation *d.AccessOperation) error {
	if operation == nil || operation.ID == 0 || operation.State != "creating" {
		return c.ErrInvalid
	}
	return service.executeAccess(ctx, operation)
}

const accessLeaseDuration = 90 * time.Second

// RunAccess 在受管 runtime fence context 下分页执行 bot 的持久工作；limit 为 1..100。
// 只消费 pending/retryable，失租 creating 转 unknown；不扫描付款短时间窗口。
func (service *AccessService) RunAccess(ctx context.Context, bot uint, limit int) (int, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	completed := 0
	for completed < limit {
		token := make([]byte, 24)
		if _, err := rand.Read(token); err != nil {
			return completed, err
		}
		operation, err := service.Store.ClaimAccess(ctx, bot, hex.EncodeToString(token), service.accessNow(), accessLeaseDuration)
		if errors.Is(err, c.ErrNotFound) {
			return completed, nil
		}
		if err != nil {
			return completed, err
		}
		if err := service.executeAccess(ctx, operation); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// prepareAccess 在事务外复核路由、资格、当前绑定，并续租保存实际代次。
// 返回的路由只供紧接着的外部调用使用，不允许跨任务缓存。
func (service *AccessService) prepareAccess(ctx context.Context, operation *d.AccessOperation, snapshot *c.AccessEligibility) (c.AccessRoute, error) {
	if operation.JoinID != 0 {
		if _, err := service.Store.CurrentJoin(ctx, operation, service.accessNow()); err != nil {
			return c.AccessRoute{}, err
		}
	}
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
	if operation.Kind == "create" || operation.Kind == "approve" {
		identity := c.AccessIdentity{AccountID: operation.AccountID, TelegramID: operation.TelegramID, BotID: operation.BotID, Route: &route}
		if err := service.authorizeAccess(ctx, identity, operation.TargetID); err != nil {
			return route, err
		}
		eligible, err := service.Store.Eligibility(ctx, identity, operation.TargetID, service.accessNow())
		if err != nil {
			return route, err
		}
		if !eligible.Active || operation.BindingVersion == 0 || operation.BindingVersion != eligible.BindingVersion {
			return route, c.ErrUntrustedIdentity
		}
		invite, err := service.Store.Invite(ctx, operation.InviteID)
		if err != nil {
			return route, err
		}
		if invite.BindingVersion != operation.BindingVersion || invite.TelegramID != operation.TelegramID || !d.IsInviteUsableAt(*invite, service.accessNow()) || (operation.Kind == "approve" && invite.State != "active") {
			return route, c.ErrUntrustedIdentity
		}
		operation.EntitlementVersion = eligible.Version
		*snapshot = eligible
		if err := service.checkAccessMember(ctx, operation, route); err != nil {
			return route, err
		}
	}
	if operation.Kind == "revoke" && operation.CleanupCycleID != 0 {
		guard, ok := service.Store.(c.InviteCleanupRepository)
		if !ok {
			return route, c.ErrCapability
		}
		eligible, err := guard.CleanupEligibility(ctx, operation, service.accessNow())
		if err != nil {
			return route, err
		}
		*snapshot = eligible
	}
	operation.Generation = route.Generation
	if err := service.Store.RenewAccess(ctx, operation, service.accessNow(), accessLeaseDuration); err != nil {
		return route, err
	}
	// 授权/数据库读取可能耗时，最后再次读取路由能力并检查实际运行租约。
	latest, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return latest, err
	}
	if latest != route {
		return latest, c.ErrConflict
	}
	return route, checkAccessFence(ctx, route)
}

// executeAccess 执行单一已抢占副作用，外部调用限时且绝不处于数据库事务。
// 调用后失去路由/身份/权益/代次会保留 unknown，不把结果伪装成确定失败重放。
func (service *AccessService) executeAccess(ctx context.Context, operation *d.AccessOperation) error {
	var snapshot c.AccessEligibility
	route, err := service.prepareAccess(ctx, operation, &snapshot)
	if err == nil && (operation.Kind == "create" || operation.Kind == "approve") {
		err = service.verifyAccessSnapshot(ctx, operation, route, snapshot)
	}
	if err == nil && (operation.Kind == "approve" || operation.Kind == "decline") {
		err = service.checkJoinBeforeCall(ctx, operation, route, snapshot)
	}
	if err == nil && operation.Kind == "revoke" && operation.CleanupCycleID != 0 {
		err = service.verifyCleanupSnapshot(ctx, operation, snapshot)
	}
	if err != nil {
		outcome := c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "unavailable", RetryAfter: time.Minute}
		if errors.Is(err, c.ErrUntrustedIdentity) || errors.Is(err, c.ErrConflict) {
			outcome.Outcome, outcome.ErrorClass = c.AccessFailed, "identity"
		}
		return service.Store.FinishAccess(ctx, operation, outcome, "", "", service.accessNow())
	}
	slog.InfoContext(ctx, "membership_access_started", "operation_id", operation.ID, "kind", operation.Kind, "bot_id", operation.BotID)
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	outcome, cipher, hash := service.callAccess(callCtx, operation, route)
	cancel()
	if err := service.verifyAccessAfter(ctx, operation, route, snapshot); err != nil {
		// 明确的异常创建响应否定平台期限；后置校验失败不能抹掉这项证据。
		if operation.Kind != "create" || outcome.ErrorClass != "invalid_response" {
			outcome = c.AccessAPIResult{Outcome: c.AccessUnknown, ErrorClass: "entitlement_changed"}
		}
	}
	// 不输出 API 响应或邀请秘密；失租完成返回冲突，恢复扫描将其保留 unknown。
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	err = service.Store.FinishAccess(finishCtx, operation, outcome, cipher, hash, service.accessNow())
	slog.InfoContext(ctx, "membership_access_finished", "operation_id", operation.ID, "outcome", outcome.Outcome, "persisted", err == nil)
	return err
}

// callAccess 按 operation 执行平台窄接口，成功创建必须为真实邀请且可以加密。
func (service *AccessService) callAccess(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute) (c.AccessAPIResult, string, string) {
	action := c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID}
	switch operation.Kind {
	case "create":
		invite, err := service.Store.Invite(ctx, operation.InviteID)
		if err != nil {
			return c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "unavailable"}, "", ""
		}
		created := service.Platform.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, Name: fmt.Sprintf("membership-%d", operation.ID), Term: invite.Term, ExpiresAt: invite.ExpiresAt})
		if created.Outcome != c.AccessSucceeded {
			return created.AccessAPIResult, "", ""
		}
		if !validAccessLink(created.Link) {
			return c.AccessAPIResult{Outcome: c.AccessUnknown, ErrorClass: "invalid_response"}, "", ""
		}
		cipher, err := service.Cipher.Seal(created.Link)
		if err != nil {
			return c.AccessAPIResult{Outcome: c.AccessUnknown, ErrorClass: "crypto"}, "", ""
		}
		return created.AccessAPIResult, cipher, accessHash(created.Link)
	case "revoke":
		invite, err := service.Store.Invite(ctx, operation.InviteID)
		if err != nil {
			return c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "unavailable"}, "", ""
		}
		link, err := service.Cipher.Open(invite.LinkCipher)
		if err != nil || !validAccessLink(link) || accessHash(link) != invite.LinkHash {
			return c.AccessAPIResult{Outcome: c.AccessFailed, ErrorClass: "crypto"}, "", ""
		}
		return service.Platform.RevokeMembershipInvite(ctx, route, link), "", ""
	case "approve":
		return service.Platform.ApproveMembershipJoin(ctx, action), "", ""
	case "decline":
		return service.Platform.DeclineMembershipJoin(ctx, action), "", ""
	default:
		return c.AccessAPIResult{Outcome: c.AccessFailed, ErrorClass: "invalid_response"}, "", ""
	}
}

// checkJoinBeforeCall 对 operation 重新判断申请决策，最后读取水位及 fence。
// decline 只有当前仍明确拒绝才能执行；故障返回错误重试，合法申请不沿用旧拒绝。
// Telegram 不支持申请 ID 条件调用，最终读取到 HTTP 之间的新申请仍无法原子排除。
func (service *AccessService) checkJoinBeforeCall(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute, snapshot c.AccessEligibility) error {
	join, err := service.Store.CurrentJoin(ctx, operation, service.accessNow())
	if err != nil {
		return err
	}
	invite, err := service.Store.Invite(ctx, join.InviteID)
	if err != nil {
		return err
	}
	input := c.AccessJoinRequest{BotID: join.BotID, UpdateID: join.UpdateID, ChatID: join.ChatID, TelegramID: join.TelegramID, ReceivedAt: join.ReceivedAt}
	if operation.Kind == "approve" {
		input.Authorization = &snapshot
	}
	validation := service.validateJoin(ctx, &input, invite)
	if operation.Kind == "approve" && validation != nil {
		return validation
	}
	if operation.Kind == "decline" {
		if validation == nil {
			return c.ErrConflict
		}
		if !errors.Is(validation, c.ErrUntrustedIdentity) {
			return validation
		}
	}
	latest, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return c.ErrConflict
	}
	if _, err := service.Store.CurrentJoin(ctx, operation, service.accessNow()); err != nil {
		return err
	}
	return checkAccessFence(ctx, route)
}

// verifyAccessAfter 在平台结果到达后重验栅栏与资格版本，不跨事务声称恰好一次。
func (service *AccessService) verifyAccessAfter(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute, snapshot c.AccessEligibility) error {
	if err := checkAccessFence(ctx, route); err != nil {
		return err
	}
	latest, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return c.ErrConflict
	}
	if operation.JoinID != 0 {
		if _, err := service.Store.CurrentJoin(ctx, operation, service.accessNow()); err != nil {
			return err
		}
	}
	if operation.Kind == "revoke" && operation.CleanupCycleID != 0 {
		guard, ok := service.Store.(c.InviteCleanupRepository)
		if !ok {
			return c.ErrCapability
		}
		_, err := guard.CleanupEligibility(ctx, operation, service.accessNow())
		return err
	}
	if operation.Kind != "create" && operation.Kind != "approve" {
		return nil
	}
	if err := service.checkAccessMember(ctx, operation, route); err != nil {
		return err
	}
	return service.verifyAccessSnapshot(ctx, operation, route, snapshot)
}

// verifyAccessSnapshot 在同事务重读账号、绑定、权益与本地封禁，核对调用前快照。
// operation 提供历史身份，route 提供当前群；任一版本变化返回冲突。
func (service *AccessService) verifyAccessSnapshot(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute, snapshot c.AccessEligibility) error {
	invite, err := service.Store.Invite(ctx, operation.InviteID)
	if err != nil {
		return err
	}
	if invite.Retired {
		return c.ErrUntrustedIdentity
	}
	identity := c.AccessIdentity{AccountID: operation.AccountID, TelegramID: operation.TelegramID, BotID: operation.BotID, Route: &route, Authorization: &snapshot}
	if err := service.authorizeAccess(ctx, identity, operation.TargetID); err != nil {
		return err
	}
	eligible, err := service.Store.Eligibility(ctx, identity, operation.TargetID, service.accessNow())
	if err != nil {
		return err
	}
	if !eligible.Active || eligible.Version != snapshot.Version || eligible.BindingVersion != snapshot.BindingVersion {
		return c.ErrConflict
	}
	return nil
}

// checkAccessMember 在事务外定点只读查询当前群；失败保守关闭，不批准或自动解除封禁。
// route 必须受当前执行 fence 保护；left 与 kicked 明确区分。
func (service *AccessService) checkAccessMember(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute) error {
	if err := checkAccessFence(ctx, route); err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	observedAt := service.accessNow()
	member := service.Platform.GetMembershipMember(queryCtx, c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID})
	if member.Outcome != c.AccessSucceeded {
		return c.ErrCapability
	}
	latest, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return c.ErrConflict
	}
	if err := checkAccessFence(ctx, route); err != nil {
		return err
	}
	switch member.Status {
	case "kicked":
		_, err := service.Store.ObserveAccess(ctx, c.AccessObservation{TargetID: operation.TargetID, BotID: route.BotID, ChatID: route.ChatID, RouteVersion: route.RouteVersion, TelegramID: operation.TelegramID, Status: "kicked", ObservedAt: observedAt, Source: "query"})
		if err != nil {
			return err
		}
		return c.ErrUntrustedIdentity
	case "member", "administrator", "creator", "restricted", "left":
		return checkAccessFence(ctx, route)
	default:
		return c.ErrCapability
	}
}
