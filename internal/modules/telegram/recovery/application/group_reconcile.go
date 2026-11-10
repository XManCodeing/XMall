package application

import (
	"context"
	"errors"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// groupIdentity checks that the trusted Bot identity still matches the confirmed generation.
// snapshot is immutable; a restarted/replaced Bot requires a new explicit recovery decision.
func (executor *GroupExecutor) groupIdentity(ctx context.Context, snapshot d.Snapshot) (groups.Identity, error) {
	identity, err := executor.Identity.Instance(ctx, snapshot.New.BotID)
	if err != nil {
		return identity, err
	}
	if !identity.Running || identity.TelegramID <= 0 || identity.Generation != snapshot.ExpectedNew.Generation {
		return identity, c.ErrStale
	}
	return identity, nil
}

// recoverGroup performs a real persisted capability check, module recovery and release.
// task/snapshot carry immutable ownership; actor is used by the ordinary group checker.
func (executor *GroupExecutor) recoverGroup(ctx context.Context, task d.Task, snapshot d.Snapshot, actor uint) error {
	if err := executor.runGroupStep(ctx, task.ID, "check_route", func(callCtx context.Context) error {
		if err := executor.Authorizer.AuthorizeGroupRecovery(callCtx, actor, snapshot.New.TargetID); err != nil {
			return err
		}
		if _, err := executor.groupIdentity(callCtx, snapshot); err != nil {
			return err
		}
		current, err := executor.Groups.Detail(callCtx, snapshot.New.TargetID)
		if err != nil {
			return err
		}
		if current == nil || current.RecoveryTaskID != task.ID || current.Version != snapshot.ExpectedOld.Config+2 || current.RouteVersion != snapshot.ExpectedOld.Route+1 || current.BotInstanceID != snapshot.New.BotID || current.TelegramChatID != snapshot.New.ChatID {
			return c.ErrStale
		}
		// The platform probe is read-only; BeginCheck/FinishCheck use an
		// observation sequence CAS to discard any old in-flight response.
		_, err = executor.Groups.Check(callCtx, snapshot.New.TargetID, snapshot.ExpectedOld.Route+1, actor, "recovery")
		if err != nil {
			return err
		}
		_, err = executor.checkedGroup(callCtx, task.ID, snapshot)
		return err
	}); err != nil {
		return err
	}
	if err := executor.runGroupStep(ctx, task.ID, "recover_module", func(callCtx context.Context) error {
		if err := executor.Authorizer.AuthorizeGroupRecovery(callCtx, actor, snapshot.New.TargetID); err != nil {
			return err
		}
		// A previous page may have waited for customer action longer than the
		// capability TTL. Refresh outside transactions before continuing work.
		if _, err := executor.checkedGroup(callCtx, task.ID, snapshot); err != nil {
			if !errors.Is(err, groups.ErrCapability) {
				return err
			}
			if _, err := executor.groupIdentity(callCtx, snapshot); err != nil {
				return err
			}
			if _, err := executor.Groups.Check(callCtx, snapshot.New.TargetID, snapshot.ExpectedOld.Route+1, actor, "recovery"); err != nil {
				return err
			}
		}
		return executor.recoverModule(callCtx, task.ID, snapshot, actor)
	}); err != nil {
		return err
	}
	return executor.runGroupStep(ctx, task.ID, "release", func(callCtx context.Context) error {
		if err := executor.Authorizer.AuthorizeGroupRecovery(callCtx, actor, snapshot.New.TargetID); err != nil {
			return err
		}
		// Historical evidence repairs this step only. Never call ReleaseRecovery
		// again when the original release is already durable, even after restart
		// or when a later task/route now owns the target.
		if reader, ok := executor.Routes.(groups.RecoveryReleaseReader); ok {
			evidence, err := reader.ReadRecoveryRelease(callCtx, groups.RecoveryReleaseEvidenceRequest{
				RecoveryInput: recoveryInput(task.ID, snapshot, false), Generation: snapshot.ExpectedNew.Generation,
			})
			if err != nil {
				return err
			}
			if evidence != nil {
				return nil
			}
		}
		current, err := executor.checkedGroup(callCtx, task.ID, snapshot)
		if err != nil {
			return err
		}
		identity, err := executor.groupIdentity(callCtx, snapshot)
		if err != nil {
			return err
		}
		_, err = executor.Routes.ReleaseRecovery(callCtx, groups.RecoveryRelease{TaskID: task.ID, TargetID: snapshot.New.TargetID,
			ExpectedVersion: snapshot.ExpectedOld.Config + 2, ExpectedRouteVersion: snapshot.ExpectedOld.Route + 1, ExpectedObservationSeq: current.ObservationSeq,
			BotID: snapshot.New.BotID, ChatID: snapshot.New.ChatID, Generation: identity.Generation, BotTelegramID: identity.TelegramID})
		return err
	})
}

// checkedGroup rejects stale capabilities, wrong ownership and substituted destinations.
// It returns the persisted detail, not an uncommitted platform response. Fresh is false
// while fenced by design, so freshness is validated from the observation timestamp below.
func (executor *GroupExecutor) checkedGroup(ctx context.Context, taskID uint, snapshot d.Snapshot) (*groups.Detail, error) {
	current, err := executor.Groups.Detail(ctx, snapshot.New.TargetID)
	if err != nil {
		return nil, err
	}
	identity, err := executor.groupIdentity(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ID != snapshot.New.TargetID || current.RecoveryTaskID != taskID || current.Version != snapshot.ExpectedOld.Config+2 || current.RouteVersion != snapshot.ExpectedOld.Route+1 || current.BotInstanceID != snapshot.New.BotID || current.TelegramChatID != snapshot.New.ChatID || (current.Purpose != "support" && current.Purpose != "membership") {
		return nil, c.ErrStale
	}
	observation := current.Observation
	if observation.State != "ready" || observation.ErrorClass != "" || !observation.IsMember || observation.RouteVersion != current.RouteVersion || observation.Sequence != current.ObservationSeq || observation.Generation != identity.Generation || observation.BotTelegramID != identity.TelegramID || observation.ObservedAt == nil || observation.ObservedAt.After(time.Now().UTC()) || current.FreshUntil == nil || !current.FreshUntil.After(time.Now().UTC()) || len(current.MissingCapabilities) != 0 {
		return nil, groups.ErrCapability
	}
	return current, nil
}

// recoverModule requires explicit route-bound completeness evidence from the selected
// module adapter. Nil/zero-value output cannot certify topics or membership liabilities.
func (executor *GroupExecutor) recoverModule(ctx context.Context, taskID uint, snapshot d.Snapshot, actor uint) error {
	if executor.Modules == nil {
		return ErrRecoveryIncomplete
	}
	current, err := executor.checkedGroup(ctx, taskID, snapshot)
	if err != nil {
		return err
	}
	result, err := executor.Modules.RecoverGroup(ctx, c.GroupRecoveryRequest{TaskID: taskID, ActorID: actor, TargetID: current.ID,
		BotID: current.BotInstanceID, OldChatID: snapshot.Old.ChatID, NewChatID: snapshot.New.ChatID,
		OldRouteVersion: snapshot.ExpectedOld.Route, NewRouteVersion: current.RouteVersion,
		Generation: snapshot.ExpectedNew.Generation, Purpose: current.Purpose})
	if err != nil {
		return err
	}
	if result.Unknown != 0 {
		return ErrRecoveryReview
	}
	if result.TaskID != taskID || result.TargetID != current.ID || result.RouteVersion != current.RouteVersion {
		return ErrRecoveryIncomplete
	}
	if current.Purpose == "membership" && result.EntryReady {
		return nil
	}
	if result.Checkpointed && result.Remaining > 0 {
		return ErrRecoveryPending
	}
	if !result.Verified || result.Remaining != 0 {
		return ErrRecoveryIncomplete
	}
	return nil
}
