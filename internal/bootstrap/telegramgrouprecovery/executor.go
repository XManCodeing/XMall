package telegramgrouprecovery

import (
	"context"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	domain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// Executor assembles existing group execution ports without owning credentials or transactions.
// Authorize receives only fixed recovery action names and a trusted administrator ID.
type Executor struct {
	Store           recovery.Store
	Groups          app.GroupExecutionGroups
	Routes          groups.RecoveryStore
	Identity        app.GroupIdentity
	Membership      membership.Recoverer
	Support         recovery.GroupRecoverer
	ContinueSupport func(context.Context, recovery.GroupRecoveryRequest) error
	Authorize       func(context.Context, uint, string) error
}

// Available reports same-Bot replacements only when their own module is wired; read failures fail closed.
// detail is server-loaded; this method performs no mutations or platform calls.
func (executor *Executor) Available(ctx context.Context, detail *recovery.Detail) bool {
	if executor == nil || executor.Store == nil || executor.Groups == nil || executor.Routes == nil || executor.Identity == nil || executor.Authorize == nil || detail == nil || detail.Task.Kind != domain.GroupReplace {
		return false
	}
	snapshot := detail.Snapshot
	if snapshot.Old.TargetID == 0 || snapshot.Old.TargetID != snapshot.New.TargetID || snapshot.Old.BotID == 0 || snapshot.Old.BotID != snapshot.New.BotID || snapshot.Old.ChatID >= 0 || snapshot.New.ChatID >= 0 || snapshot.Old.ChatID == snapshot.New.ChatID {
		return false
	}
	target, err := executor.Groups.Detail(ctx, snapshot.Old.TargetID)
	return err == nil && target != nil && ((target.Purpose == "membership" && executor.Membership != nil) || (target.Purpose == "support" && executor.ContinueSupport != nil))
}

// prepare authenticates the exact action before checking supported purpose and assembling ports.
// No fence, scope or route can change when the adapter is absent or purpose is unsupported.
func (executor *Executor) prepare(ctx context.Context, id, actor uint, action string) (*app.GroupExecutor, *recovery.Detail, error) {
	if executor == nil || executor.Store == nil || executor.Authorize == nil || id == 0 || actor == 0 {
		return nil, nil, recovery.ErrInvalid
	}
	if err := executor.Authorize(ctx, actor, action); err != nil {
		return nil, nil, err
	}
	detail, err := executor.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !executor.Available(ctx, detail) {
		return nil, nil, recovery.ErrInvalid
	}
	authority := authorizer{check: executor.Authorize, action: action}
	group := app.NewGroupExecutor(executor.Store, executor.Groups, executor.Routes, executor.Identity, authority, executor)
	group.ContinueSupport = executor.ContinueSupport
	return group, detail, nil
}

// Confirm reuses immutable preflight validation and scope acquisition; input actor is trusted HTTP identity.
// Unsupported purposes are rejected before confirmation, not after route replacement.
func (executor *Executor) Confirm(ctx context.Context, id uint, input recovery.Confirmation) (*domain.Task, error) {
	group, _, err := executor.prepare(ctx, id, input.ActorID, "confirm")
	if err != nil {
		return nil, err
	}
	return group.ConfirmGroup(ctx, id, input)
}

// Resume validates the caller's persisted version before one bounded execution pass.
// Unknown and remaining liabilities remain owned by the original recovery fence.
func (executor *Executor) Resume(ctx context.Context, id uint, version uint64, actor uint) (*domain.Task, error) {
	group, detail, err := executor.prepare(ctx, id, actor, "resume")
	if err != nil {
		return nil, err
	}
	if version == 0 || version != detail.Task.Version {
		return nil, recovery.ErrStale
	}
	return group.ExecuteGroupVersion(ctx, id, version, actor)
}

// RecoverGroup dispatches only to the adapter for the server-derived purpose.
// request retains its actor, route and checkpoint evidence; absent ports fail closed.
func (executor *Executor) RecoverGroup(ctx context.Context, request recovery.GroupRecoveryRequest) (recovery.GroupRecoveryResult, error) {
	if executor == nil {
		return recovery.GroupRecoveryResult{}, app.ErrRecoveryIncomplete
	}
	if request.Purpose == "support" && executor.Support != nil {
		return executor.Support.RecoverGroup(ctx, request)
	}
	if executor.Membership == nil || request.Purpose != "membership" {
		return recovery.GroupRecoveryResult{}, app.ErrRecoveryIncomplete
	}
	if switcher, ok := executor.Membership.(membership.EntrySwitcher); ok {
		result, err := switcher.SwitchEntry(ctx, membership.Request(request))
		return recovery.GroupRecoveryResult(result), err
	}
	result, err := executor.Membership.RecoverRoute(ctx, membership.Request(request))
	return recovery.GroupRecoveryResult(result), err
}

// authorizer binds the application target check to one immutable action grant.
type authorizer struct {
	check  func(context.Context, uint, string) error
	action string
}

// AuthorizeGroupRecovery checks a real actor and server-derived target; returns the exact RBAC decision.
func (authority authorizer) AuthorizeGroupRecovery(ctx context.Context, actor, target uint) error {
	if target == 0 {
		return recovery.ErrInvalid
	}
	return authority.check(ctx, actor, authority.action)
}
