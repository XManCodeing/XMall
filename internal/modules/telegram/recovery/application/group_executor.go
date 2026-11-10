package application

import (
	"context"
	"errors"
	"strconv"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

var (
	// ErrRecoveryReview means execution stopped with durable evidence requiring review.
	ErrRecoveryReview = errors.New("recovery: group execution requires review")
	// ErrRecoveryIncomplete means a required module adapter or completion proof is missing.
	ErrRecoveryIncomplete = errors.New("recovery: module recovery is incomplete")
	// ErrRecoveryPending means a durable bounded attempt has more known work.
	// Unlike incomplete/unknown evidence it allows an explicitly requested resume.
	ErrRecoveryPending = errors.New("recovery: module recovery checkpointed with remaining work")
)

// GroupExecutionGroups reuses the existing read-only preflight and persisted group check.
// Check must perform platform I/O outside transactions and persist the observed route.
type GroupExecutionGroups interface {
	GroupPreviewer
	Detail(context.Context, uint) (*groups.Detail, error)
	Check(context.Context, uint, uint64, uint, string) (*groups.Detail, error)
}

// GroupIdentity exposes only the trusted running identity, never credentials.
type GroupIdentity interface {
	Instance(context.Context, uint) (groups.Identity, error)
}

// GroupAuthorizer must check the exact recovery action grant for the authenticated actor.
// target is server-derived; this check runs on confirmation and every execution attempt.
type GroupAuthorizer interface {
	AuthorizeGroupRecovery(context.Context, uint, uint) error
}

// GroupExecutor owns same-Bot replacement only. Optional Modules must not be replaced
// with a no-op: absent reconciliation deliberately retains the fence and scope slots.
type GroupExecutor struct {
	Store      c.Store
	Groups     GroupExecutionGroups
	Routes     groups.RecoveryStore
	Identity   GroupIdentity
	Authorizer GroupAuthorizer
	Modules    c.GroupRecoverer
	// ContinueSupport owns stopped-runtime continuation and atomic finalization.
	// Nil keeps the generic path available for tests, not production support.
	ContinueSupport func(context.Context, c.GroupRecoveryRequest) error
}

// NewGroupExecutor wires narrow ports; store/groups/routes/identity/authorizer are required.
// modules may be nil, in which case route recovery stops before release with explicit review.
func NewGroupExecutor(store c.Store, groupService GroupExecutionGroups, routes groups.RecoveryStore, identity GroupIdentity, authorizer GroupAuthorizer, modules c.GroupRecoverer) *GroupExecutor {
	return &GroupExecutor{Store: store, Groups: groupService, Routes: routes, Identity: identity, Authorizer: authorizer, Modules: modules}
}

// loadGroup validates taskID and actor, then authorizes the server-derived logical target.
// It returns only same-Bot, distinct-chat snapshots; no caller-supplied route is trusted.
func (executor *GroupExecutor) loadGroup(ctx context.Context, taskID, actor uint) (*c.Detail, error) {
	if executor == nil || executor.Store == nil || executor.Groups == nil || executor.Routes == nil || executor.Identity == nil || executor.Authorizer == nil || taskID == 0 || actor == 0 {
		return nil, c.ErrInvalid
	}
	detail, err := executor.Store.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	snapshot := detail.Snapshot
	if detail.Task.Kind != d.GroupReplace || snapshot.Old.TargetID == 0 || snapshot.New.TargetID != snapshot.Old.TargetID || snapshot.Old.BotID == 0 || snapshot.New.BotID != snapshot.Old.BotID || snapshot.Old.ChatID >= 0 || snapshot.New.ChatID >= 0 || snapshot.Old.ChatID == snapshot.New.ChatID || snapshot.ExpectedOld.Config == 0 || snapshot.ExpectedOld.Route == 0 || snapshot.ExpectedNew.Generation == 0 {
		return nil, c.ErrInvalid
	}
	if err := executor.Authorizer.AuthorizeGroupRecovery(ctx, actor, snapshot.Old.TargetID); err != nil {
		return nil, err
	}
	return detail, nil
}

// ConfirmGroup rechecks source versions, destination permissions and generation before
// acquiring scope. taskID binds the reference to a server-loaded snapshot; identical
// confirmations replay the existing task only after a fresh authorization decision.
func (executor *GroupExecutor) ConfirmGroup(ctx context.Context, taskID uint, input c.Confirmation) (*d.Task, error) {
	detail, err := executor.loadGroup(ctx, taskID, input.ActorID)
	if err != nil {
		return nil, err
	}
	if detail.Task.PreflightRef != input.PreflightRef {
		return nil, c.ErrStale
	}
	if detail.Task.ConfirmedAt == nil {
		if detail.Task.State != d.AwaitingConfirmation || detail.Task.Version != input.ExpectedVersion {
			return nil, c.ErrStale
		}
		if err := executor.revalidateGroup(ctx, detail.Snapshot); err != nil {
			return nil, err
		}
	}
	return executor.Store.Confirm(ctx, input)
}

// revalidateGroup repeats the actual group preflight with the immutable old CAS values.
// Returns a conflict on identity or generation drift, without opening a transaction.
func (executor *GroupExecutor) revalidateGroup(ctx context.Context, snapshot d.Snapshot) error {
	preview, err := executor.Groups.PreviewReplacement(ctx, groups.ReplacementInput{TargetID: snapshot.Old.TargetID,
		ExpectedVersion: snapshot.ExpectedOld.Config, ExpectedRouteVersion: snapshot.ExpectedOld.Route,
		NewBotInstanceID: snapshot.New.BotID, NewTelegramChatID: strconv.FormatInt(snapshot.New.ChatID, 10)})
	if err != nil {
		return err
	}
	if preview == nil || preview.Source.ID != snapshot.Old.TargetID || preview.Source.BotInstanceID != snapshot.Old.BotID || preview.Source.TelegramChatID != snapshot.Old.ChatID || preview.Source.Version != snapshot.ExpectedOld.Config || preview.Source.RouteVersion != snapshot.ExpectedOld.Route || preview.Destination.Generation != snapshot.ExpectedNew.Generation {
		return c.ErrStale
	}
	return nil
}
