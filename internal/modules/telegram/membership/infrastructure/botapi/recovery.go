package botapi

import (
	"context"
	"strings"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// RecoveryFactory derives a restricted platform from existing access/lifecycle
// infrastructure. No runtime enable, ordinary context fence or fake generation is used.
type RecoveryFactory struct {
	Access     *Access
	Authorizer c.MembershipRecoveryAuthorizer
}

// recoveryPlatform intentionally exposes only revoke/query; it cannot be asserted
// to AccessPlatform or LifecyclePlatform to obtain create/approve/ban capabilities.
type recoveryPlatform struct{ access *Access }

// MembershipRecoveryPlatform binds input immutably to every platform authorization.
// Nil dependencies return nil so callers retain all fences instead of proceeding.
func (factory *RecoveryFactory) MembershipRecoveryPlatform(input c.BotRouteRecoveryRequest) c.RecoveryAccessPlatform {
	if factory == nil || factory.Access == nil || factory.Authorizer == nil {
		return nil
	}
	copy := *factory.Access
	copy.recovery, copy.recoveryAuthorizer = &input, factory.Authorizer
	return &recoveryPlatform{access: &copy}
}

// RevokeMembershipInvite reuses existing URL, creator and revocation receipt validation.
func (platform *recoveryPlatform) RevokeMembershipInvite(ctx context.Context, route c.AccessRoute, link string) c.AccessAPIResult {
	return platform.access.RevokeMembershipInvite(ctx, route, link)
}

// GetMembershipMember reuses existing exact-user and member-status response checks.
func (platform *recoveryPlatform) GetMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessMemberResult {
	return platform.access.GetMembershipMember(ctx, input)
}

// prepareRecovery checks task ownership before and immediately after decrypting the
// selected identity's credential. It never requires/creates a running worker, contact,
// or private chat, and does not interpret Enabled/stopped as a drain receipt.
func (access *Access) prepareRecovery(ctx context.Context, route c.AccessRoute) (string, int64, c.AccessAPIResult) {
	deny := func() (string, int64, c.AccessAPIResult) {
		return "", 0, accessResult(c.AccessRetryable, "recovery_fence")
	}
	if access.Store == nil || access.Cipher == nil || access.recoveryAuthorizer == nil {
		return deny()
	}
	if err := access.recoveryAuthorizer.AuthorizeMembershipRecovery(ctx, *access.recovery, route); err != nil {
		return deny()
	}
	instance, err := access.Store.Get(ctx, route.BotID)
	if err != nil || instance == nil || instance.TelegramID <= 0 || instance.RecoveryTaskID != access.recovery.TaskID || instance.Generation != route.Generation || instance.Mode != "internal" {
		return deny()
	}
	token, err := access.Cipher.Open(instance.TokenCipher)
	if err != nil || strings.TrimSpace(token) == "" {
		return "", 0, accessResult(c.AccessRetryable, "credential_unavailable")
	}
	if err := access.recoveryAuthorizer.AuthorizeMembershipRecovery(ctx, *access.recovery, route); err != nil {
		return deny()
	}
	if ctx.Err() != nil {
		return deny()
	}
	return token, instance.TelegramID, accessResult(c.AccessSucceeded, "")
}

// MembershipGroupPlatform copies existing validated access/lifecycle adapters with a
// task-bound authorization path. It does not modify the shared ordinary Access object.
func (factory *RecoveryFactory) MembershipGroupPlatform(task, actor uint, authorizer c.GroupRecoveryAuthorization) (c.AccessPlatform, c.LifecyclePlatform) {
	if factory == nil || factory.Access == nil || authorizer == nil || task == 0 || actor == 0 {
		return nil, nil
	}
	copy := *factory.Access
	copy.groupTask, copy.groupActor, copy.groupAuthorizer = task, actor, authorizer
	return &copy, &copy
}

// prepareGroupRecovery checks real parent authorization twice around credential access.
// Same-Bot replacement keeps the real runtime running; authorization remains target/task
// specific and never installs an ordinary worker bypass into a shared context.
func (access *Access) prepareGroupRecovery(ctx context.Context, route c.AccessRoute) (string, int64, c.AccessAPIResult) {
	deny := func() (string, int64, c.AccessAPIResult) {
		return "", 0, accessResult(c.AccessRetryable, "recovery_fence")
	}
	if access.Store == nil || access.Cipher == nil {
		return deny()
	}
	if err := access.groupAuthorizer.AuthorizeMembershipGroupRecovery(ctx, access.groupTask, access.groupActor, route); err != nil {
		return deny()
	}
	instance, err := access.Store.Get(ctx, route.BotID)
	if err != nil || instance == nil || instance.ID != route.BotID || instance.TelegramID <= 0 || instance.Generation != route.Generation || !instance.Enabled || instance.State != "running" || instance.Mode != "internal" || instance.RecoveryTaskID != 0 {
		return deny()
	}
	token, err := access.Cipher.Open(instance.TokenCipher)
	if err != nil || strings.TrimSpace(token) == "" {
		return deny()
	}
	if err := access.groupAuthorizer.AuthorizeMembershipGroupRecovery(ctx, access.groupTask, access.groupActor, route); err != nil {
		return deny()
	}
	if ctx.Err() != nil {
		return deny()
	}
	return token, instance.TelegramID, accessResult(c.AccessSucceeded, "")
}

// MembershipBotAdmission exposes existing state machines only with an immutable
// dual-fenced authorizer. No runtime is started and no ordinary authorization changes.
func (factory *RecoveryFactory) MembershipBotAdmission(input c.BotRouteRecoveryRequest, authority c.MembershipRecoveryAuthorizer) (c.AccessPlatform, c.LifecyclePlatform) {
	if factory == nil || factory.Access == nil || authority == nil {
		return nil, nil
	}
	copy := *factory.Access
	copy.recovery = &input
	copy.recoveryAuthorizer = authority
	platform := &botAdmissionPlatform{Access: &copy, input: input}
	return platform, platform
}

// botAdmissionPlatform denies approval without a live trusted source and confines
// removal to a distinct old chat. Ordinary shared Access remains unchanged.
type botAdmissionPlatform struct {
	*Access
	input c.BotRouteRecoveryRequest
}

// ApproveMembershipJoin cannot infer a Telegram application from a website request.
func (platform *botAdmissionPlatform) ApproveMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	return accessResult(c.AccessFailed, "recovery_join_source_unavailable")
}

// DeclineMembershipJoin likewise requires an actual trusted event, unavailable while stopped.
func (platform *botAdmissionPlatform) DeclineMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	return accessResult(c.AccessFailed, "recovery_join_source_unavailable")
}

// CreateMembershipInvite confines creation to the exact new route of a different-chat task.
func (platform *botAdmissionPlatform) CreateMembershipInvite(ctx context.Context, input c.AccessCreateInvite) c.AccessInviteResult {
	if platform.input.OldChatID == platform.input.NewChatID || input.Route.BotID != platform.input.NewBotID || input.Route.ChatID != platform.input.NewChatID {
		return c.AccessInviteResult{AccessAPIResult: accessResult(c.AccessFailed, "recovery_route")}
	}
	return platform.Access.CreateMembershipInvite(ctx, input)
}

// BanMembershipMember can remove only the retired different-chat member, never a same-chat handoff.
func (platform *botAdmissionPlatform) BanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	if platform.input.OldChatID == platform.input.NewChatID || input.Route.BotID != platform.input.OldBotID || input.Route.ChatID != platform.input.OldChatID {
		return accessResult(c.AccessFailed, "recovery_route")
	}
	return platform.Access.BanMembershipMember(ctx, input)
}

// UnbanMembershipMember shares the old physical-route constraint; lifecycle proves own-ban ownership.
func (platform *botAdmissionPlatform) UnbanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	if platform.input.OldChatID == platform.input.NewChatID || input.Route.BotID != platform.input.OldBotID || input.Route.ChatID != platform.input.OldChatID {
		return accessResult(c.AccessFailed, "recovery_route")
	}
	return platform.Access.UnbanMembershipMember(ctx, input)
}

var _ c.RecoveryPlatformFactory = (*RecoveryFactory)(nil)
