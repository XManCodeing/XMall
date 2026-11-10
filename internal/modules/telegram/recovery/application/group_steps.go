package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// ExecuteGroup resumes persisted successful steps and executes pending steps once.
// taskID/actor are trusted caller identity; unknown or failed steps never auto-retry.
// Returns the latest durable task even when execution stops with an error.
func (executor *GroupExecutor) ExecuteGroup(ctx context.Context, taskID, actor uint) (*d.Task, error) {
	return executor.executeGroupVersion(ctx, taskID, 0, actor)
}

// ExecuteGroupVersion binds administrator resume to the currently loaded parent CAS.
// expectedVersion is required; stale callers cannot adopt a concurrently resumed task.
func (executor *GroupExecutor) ExecuteGroupVersion(ctx context.Context, taskID uint, expectedVersion uint64, actor uint) (*d.Task, error) {
	if expectedVersion == 0 {
		return nil, c.ErrInvalid
	}
	return executor.executeGroupVersion(ctx, taskID, expectedVersion, actor)
}

// executeGroupVersion loads and authorizes once, then resumes only the exact version.
// Zero expectedVersion preserves internal callers that already own their checkpoint.
func (executor *GroupExecutor) executeGroupVersion(ctx context.Context, taskID uint, expectedVersion uint64, actor uint) (*d.Task, error) {
	detail, err := executor.loadGroup(ctx, taskID, actor)
	if err != nil {
		return nil, err
	}
	if expectedVersion != 0 && detail.Task.Version != expectedVersion {
		return &detail.Task, c.ErrStale
	}
	if detail.Task.State != d.Completed && detail.Task.ConfirmedAt != nil {
		detail, err = executor.resumeLocalGroup(ctx, detail, actor)
		if err != nil {
			return nil, err
		}
	}
	if detail.Task.State == d.Completed {
		return &detail.Task, nil
	}
	if detail.Task.State == d.NeedsReview || detail.Task.State == d.PartialFailed {
		return &detail.Task, ErrRecoveryReview
	}
	if detail.Task.ConfirmedAt == nil || detail.Task.State == d.AwaitingConfirmation || detail.Task.State == d.Cancelled {
		return &detail.Task, c.ErrStale
	}
	err = executor.executePhases(ctx, detail, actor)
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	latest, readErr := executor.Store.Get(readCtx, taskID)
	if readErr != nil {
		return nil, errors.Join(err, readErr)
	}
	return &latest.Task, err
}

// executePhases advances only after each durable phase succeeds; snapshot never changes.
// detail supplies the current parent CAS, actor supplies the audit identity.
func (executor *GroupExecutor) executePhases(ctx context.Context, detail *c.Detail, actor uint) error {
	task, snapshot := &detail.Task, detail.Snapshot
	if task.State == d.Fencing {
		if err := executor.runGroupStep(ctx, task.ID, "freeze", func(callCtx context.Context) error {
			if err := executor.Authorizer.AuthorizeGroupRecovery(callCtx, actor, snapshot.Old.TargetID); err != nil {
				return err
			}
			// Freeze has an immutable receipt. A previous successful freeze can be replayed
			// safely, but never bypass source preflight before the first mutation.
			current, err := executor.Groups.Detail(callCtx, snapshot.Old.TargetID)
			if err != nil {
				return err
			}
			if current.RecoveryTaskID == 0 {
				if err := executor.revalidateGroup(callCtx, snapshot); err != nil {
					return err
				}
			}
			_, err = executor.Routes.FreezeTarget(callCtx, recoveryInput(task.ID, snapshot, false))
			return err
		}); err != nil {
			return err
		}
		if err := executor.advanceGroup(ctx, task, d.Switching); err != nil {
			return err
		}
	}
	if task.State == d.Switching {
		if err := executor.runGroupStep(ctx, task.ID, "switch_route", func(callCtx context.Context) error {
			if err := executor.Authorizer.AuthorizeGroupRecovery(callCtx, actor, snapshot.New.TargetID); err != nil {
				return err
			}
			if _, err := executor.groupIdentity(callCtx, snapshot); err != nil {
				return err
			}
			_, err := executor.Routes.ReplaceRoute(callCtx, recoveryInput(task.ID, snapshot, true))
			return err
		}); err != nil {
			return err
		}
		if err := executor.advanceGroup(ctx, task, d.Recovering); err != nil {
			return err
		}
	}
	if task.State != d.Recovering {
		return c.ErrStale
	}
	if executor.ContinueSupport != nil {
		current, err := executor.Groups.Detail(ctx, snapshot.New.TargetID)
		if err != nil {
			return err
		}
		if current != nil && current.Purpose == "support" {
			// The support port owns stop/join, task-bound probes and atomic finalization.
			// Do not run the generic release or advance the already-completed parent.
			return executor.ContinueSupport(ctx, c.GroupRecoveryRequest{TaskID: task.ID, ActorID: actor,
				TargetID: snapshot.New.TargetID, BotID: snapshot.New.BotID, OldChatID: snapshot.Old.ChatID,
				NewChatID: snapshot.New.ChatID, OldRouteVersion: snapshot.ExpectedOld.Route,
				NewRouteVersion: snapshot.ExpectedOld.Route + 1, Generation: snapshot.ExpectedNew.Generation, Purpose: "support"})
		}
	}
	if err := executor.recoverGroup(ctx, *task, snapshot, actor); err != nil {
		return err
	}
	return executor.advanceGroup(ctx, task, d.Completed)
}

// recoveryInput derives exact group CAS values from the confirmed snapshot and phase.
// Freeze increments configuration once; route switch increments both config and route.
func recoveryInput(taskID uint, snapshot d.Snapshot, switched bool) groups.RecoveryInput {
	version := snapshot.ExpectedOld.Config
	if switched {
		version++
	}
	return groups.RecoveryInput{TaskID: taskID, TargetID: snapshot.Old.TargetID,
		ExpectedVersion: version, ExpectedRouteVersion: snapshot.ExpectedOld.Route,
		OldBotID: snapshot.Old.BotID, OldChatID: snapshot.Old.ChatID, NewBotID: snapshot.New.BotID, NewChatID: snapshot.New.ChatID}
}

// advanceGroup CAS-transitions task to state and updates the caller's local version.
// Returns persistence errors without attempting further business actions.
func (executor *GroupExecutor) advanceGroup(ctx context.Context, task *d.Task, state d.State) error {
	updated, err := executor.Store.Transition(ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: state})
	if err != nil {
		return err
	}
	*task = *updated
	return nil
}

// runGroupStep writes intent and obtains a lease before action runs outside any store
// transaction. Successful receipts are skipped; every ambiguous outcome is quarantined.
func (executor *GroupExecutor) runGroupStep(ctx context.Context, taskID uint, operation string, action func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	step, err := executor.Store.EnsureStep(ctx, c.StepInput{TaskID: taskID, BusinessKey: "group." + operation, Operation: operation})
	if err != nil {
		return err
	}
	if step.State == d.StepSucceeded {
		return nil
	}
	if step.State == d.StepFailed || step.State == d.StepUnknown {
		return ErrRecoveryReview
	}
	step, err = executor.Store.ClaimStep(ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: 2 * time.Minute})
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Minute)
	actionErr := action(callCtx)
	cancel()
	outcome, code := d.StepSucceeded, "verified"
	if actionErr != nil {
		outcome, code = d.StepUnknown, "execution_unknown"
		if errors.Is(actionErr, ErrRecoveryIncomplete) {
			outcome, code = d.StepFailed, "module_incomplete"
		}
		if operation == "recover_module" && errors.Is(actionErr, ErrRecoveryPending) {
			outcome, code = d.StepPending, "module_checkpointed"
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	_, persistErr := executor.Store.CompleteStep(finishCtx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version,
		LeaseToken: step.LeaseToken, Outcome: outcome, ResultCode: code})
	slog.InfoContext(finishCtx, "group recovery phase finished", "task_id", taskID, "operation", operation, "outcome", outcome, "persisted", persistErr == nil)
	return errors.Join(actionErr, persistErr)
}
