package application

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"time"

	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// IdentityBackend reuses membership's trusted precheck and transactionally checkpoints mappings.
// Commit must atomically persist mappings and the recovery commit receipt, without network I/O.
type IdentityBackend interface {
	m.IdentityMigrationReader
	Commit(context.Context, *c.Detail) error
	CancelConfirmed(context.Context, c.Cancellation) (*d.Task, error)
	Completed(context.Context, *c.Detail) (bool, error)
	Mappings(context.Context, *c.Detail) ([]c.IdentityMapping, error)
}

// IdentityExecutor binds administrator review to immutable versions and durable progress.
// Authorize must recheck the action grant at each mutation, not only at HTTP entry.
type IdentityExecutor struct {
	Store     c.Store
	Backend   IdentityBackend
	Authorize func(context.Context, uint, string) error
}

// IdentityPreflightInput is human-reviewed intent; actor and checked time are server-owned.
type IdentityPreflightInput struct {
	m.IdentityMigrationInput
	RequestKey       string `json:"request_key"`
	Reason           string `json:"reason"`
	VerificationNote string `json:"verification_note"`
	HumanVerified    bool   `json:"human_verified"`
}

// Preview checks input and current evidence, returning a five-minute immutable task.
// The note is an administrator attestation, never automatic proof of account ownership.
func (executor *IdentityExecutor) Preview(ctx context.Context, actor uint, input IdentityPreflightInput) (*d.Task, error) {
	if executor == nil || executor.Store == nil || executor.Backend == nil || executor.Authorize == nil || actor == 0 || !input.HumanVerified || !validRequestKey(input.RequestKey) || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 512 || strings.TrimSpace(input.VerificationNote) == "" || len(input.VerificationNote) > 1024 {
		return nil, c.ErrInvalid
	}
	if err := executor.Authorize(ctx, actor, "identity-preflight"); err != nil {
		return nil, err
	}
	preview, err := executor.Backend.PreviewIdentityMigration(ctx, input.IdentityMigrationInput, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	snapshot, err := identitySnapshot(preview)
	if err != nil {
		return nil, err
	}
	return executor.Store.CreatePreflight(ctx, c.PreflightInput{Kind: d.IdentityTransfer, RequestKey: input.RequestKey, Snapshot: snapshot, ActorID: actor, Reason: strings.TrimSpace(input.Reason), VerificationNote: strings.TrimSpace(input.VerificationNote), TTL: 5 * time.Minute})
}

// identitySnapshot converts trusted, bounded membership evidence to the recovery whitelist.
// It rejects blocked bindings and malformed resource identifiers instead of silently coercing them.
func identitySnapshot(preview *m.IdentityMigrationPreview) (d.Snapshot, error) {
	out := d.Snapshot{}
	if preview == nil || !preview.BindingMatchesNewID || len(preview.Blockers) > 0 || len(preview.Targets) == 0 || len(preview.Targets) > 100 {
		return out, c.ErrConflict
	}
	oldID, err := strconv.ParseInt(preview.OldTelegramID, 10, 64)
	if err != nil || oldID <= 0 {
		return out, c.ErrInvalid
	}
	newID, err := strconv.ParseInt(preview.NewTelegramID, 10, 64)
	if err != nil || newID <= 0 || newID == oldID {
		return out, c.ErrInvalid
	}
	binding, err := strconv.ParseUint(preview.BindingVersion, 10, 64)
	if err != nil || binding == 0 {
		return out, c.ErrInvalid
	}
	out.Old = d.Endpoint{AccountID: preview.AccountID, TelegramID: oldID}
	out.New = d.Endpoint{AccountID: preview.AccountID, TelegramID: newID}
	out.ExpectedOld.Binding = binding
	out.Scope = d.Scope{AccountIDs: []string{strconv.FormatUint(uint64(preview.AccountID), 10)}, TelegramIDs: []string{preview.OldTelegramID, preview.NewTelegramID}}
	for _, target := range preview.Targets {
		out.Scope.TargetIDs = append(out.Scope.TargetIDs, target.TargetID)
		out.Scope.BotIDs = append(out.Scope.BotIDs, target.BotID)
		out.Scope.ChatIDs = append(out.Scope.ChatIDs, target.ChatID)
		out.IdentityTargets = append(out.IdentityTargets, d.IdentityTarget{TargetID: target.TargetID, BotID: target.BotID, ChatID: target.ChatID, EntitlementVersion: target.EntitlementVersion, GroupVersion: target.GroupVersion, RouteVersion: target.RouteVersion})
	}
	return out, nil
}

// IdentityCommitInput reconstructs membership CAS input exclusively from stored evidence.
// detail must have been loaded and authorized by IdentityExecutor before use.
func IdentityCommitInput(detail *c.Detail) m.IdentityMigrationCommit {
	snapshot := detail.Snapshot
	input := m.IdentityMigrationCommit{IdentityMigrationInput: m.IdentityMigrationInput{AccountID: snapshot.Old.AccountID, OldTelegramID: strconv.FormatInt(snapshot.Old.TelegramID, 10), NewTelegramID: strconv.FormatInt(snapshot.New.TelegramID, 10), TargetIDs: append([]string(nil), snapshot.Scope.TargetIDs...), ExpectedVersions: map[string]string{}}, ActorAdminID: detail.Task.ActorID, HumanVerified: true, ExpectedBindingVersion: strconv.FormatUint(snapshot.ExpectedOld.Binding, 10), ExpectedRoutes: map[string]string{}, ExpectedGroups: map[string]string{}}
	for _, target := range snapshot.IdentityTargets {
		input.ExpectedVersions[target.TargetID] = target.EntitlementVersion
		input.ExpectedRoutes[target.TargetID] = target.RouteVersion
		input.ExpectedGroups[target.TargetID] = target.GroupVersion
	}
	return input
}

// load validates task kind, original actor and per-action authorization before mutations.
func (executor *IdentityExecutor) load(ctx context.Context, id, actor uint, action string) (*c.Detail, error) {
	if executor == nil || executor.Store == nil || executor.Backend == nil || executor.Authorize == nil || id == 0 || actor == 0 {
		return nil, c.ErrInvalid
	}
	if err := executor.Authorize(ctx, actor, action); err != nil {
		return nil, err
	}
	detail, err := executor.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail.Task.Kind != d.IdentityTransfer || detail.Task.ActorID != actor || len(detail.Snapshot.IdentityTargets) == 0 || detail.Task.VerificationNote == "" {
		return nil, c.ErrStale
	}
	return detail, nil
}

// Cancel authorizes input against the original reviewer and delegates unconfirmed tasks
// to the unchanged generic store. Confirmed cancellation requires database-only proof.
// It returns the persisted task; a replay must use the original cancellation version.
func (executor *IdentityExecutor) Cancel(ctx context.Context, input c.Cancellation) (*d.Task, error) {
	if input.ExpectedVersion == 0 {
		return nil, c.ErrInvalid
	}
	detail, err := executor.load(ctx, input.TaskID, input.ActorID, "identity-cancel")
	if err != nil {
		return nil, err
	}
	if detail.Task.ConfirmedAt == nil {
		return executor.Store.Cancel(ctx, input)
	}
	return executor.Backend.CancelConfirmed(ctx, input)
}

// Confirm revalidates every version before acquiring scope; replay cannot replace intent or actor.
func (executor *IdentityExecutor) Confirm(ctx context.Context, id uint, input c.Confirmation) (*d.Task, error) {
	detail, err := executor.load(ctx, id, input.ActorID, "identity-confirm")
	if err != nil {
		return nil, err
	}
	if detail.Task.PreflightRef != input.PreflightRef {
		return nil, c.ErrStale
	}
	if detail.Task.ConfirmedAt == nil {
		if detail.Task.Version != input.ExpectedVersion || detail.Task.State != d.AwaitingConfirmation {
			return nil, c.ErrStale
		}
		if !detail.Task.ExpiresAt.After(time.Now().UTC()) {
			return nil, c.ErrExpired
		}
		preview, err := executor.Backend.PreviewIdentityMigration(ctx, IdentityCommitInput(detail).IdentityMigrationInput, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		snapshot, err := identitySnapshot(preview)
		if err != nil {
			return nil, err
		}
		// Scope arrays are normalized by the task store; compare immutable target evidence directly.
		if snapshot.Old != detail.Snapshot.Old || snapshot.New != detail.Snapshot.New || snapshot.ExpectedOld != detail.Snapshot.ExpectedOld || !reflect.DeepEqual(snapshot.IdentityTargets, detail.Snapshot.IdentityTargets) {
			return nil, c.ErrStale
		}
	}
	return executor.Store.Confirm(ctx, input)
}
