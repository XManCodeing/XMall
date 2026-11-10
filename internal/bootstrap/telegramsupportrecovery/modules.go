package telegramsupportrecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	routes "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	br "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	sd "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
)

// Run is the explicit support-only module entry point. Its proof never stands
// in for membership; callers must compose the independent membership adapter.
func (a *Adapter) Run(ctx context.Context, request br.ModuleRequest) (br.ModuleProof, error) {
	proof := br.ModuleProof{Module: "support"}
	if a.DB == nil || a.Cipher == nil || a.API == nil || a.Permission == nil || a.Now == nil || a.Authorize == nil || request.ActorID == 0 || request.TaskID == 0 {
		return proof, c.ErrDenied
	}
	if err := a.Permission(ctx, request.ActorID, "resume"); err != nil {
		return proof, err
	}
	detail, err := tasks.New(a.DB).Get(ctx, request.TaskID)
	if err != nil {
		return proof, err
	}
	p := permit{Request: request, Version: detail.Task.Version}
	ctx = context.WithValue(ctx, permitKey{}, p)
	if err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { _, e := checkParent(ctx, tx, p, a.Now().UTC()); return e }); err != nil {
		return proof, err
	}
	count := 0
	for _, route := range request.Snapshot.BotRoutes {
		if route.Purpose != "support" {
			continue
		}
		count++
		progress, e := a.runTarget(ctx, p, route)
		if e != nil {
			return proof, e
		}
		proof.Unknown += progress.Unknown
		proof.Remaining += progress.Pending + progress.AwaitingConsultation + progress.Legacy
		if !progress.Scanned {
			proof.Remaining++
		}
	}
	// Even with no active target, historical sources are liabilities. This check
	// also catches orphan sessions/operations not covered by an enumerated route.
	remaining, err := a.uncovered(ctx, p)
	if err != nil {
		return proof, err
	}
	proof.Remaining += remaining
	proof.LocalComplete = proof.Remaining == 0
	proof.Receipt = fmt.Sprintf("support:%d:%d:%d", request.TaskID, p.Version, count)
	return proof, nil
}

// ContinueBot implements the parent module port with exactly one support proof.
func (a *Adapter) ContinueBot(ctx context.Context, request br.ModuleRequest) ([]br.ModuleProof, error) {
	proof, err := a.Run(ctx, request)
	if err != nil {
		return nil, err
	}
	return []br.ModuleProof{proof}, nil
}

// runTarget advances one bounded page and one external intent. The step belongs
// to the real parent and its business key includes target and checkpoint revision.
func (a *Adapter) runTarget(ctx context.Context, p permit, route rd.BotRoute) (c.RestorationProgress, error) {
	store := a.Store()
	plan := supportPlan(p.Request, route)
	if p.GroupPlan != nil {
		plan = *p.GroupPlan
	}
	progress, err := store.BeginSupportRestoration(ctx, plan, a.Now().UTC())
	if err != nil {
		return progress, err
	}
	progress, err = store.AdvanceSupportRestoration(ctx, plan.TaskID, progress.Revision, 50, a.Now().UTC(), route.TargetID)
	if err != nil {
		return progress, err
	}
	if err = a.Permission(ctx, p.Request.ActorID, "resume"); err != nil {
		return progress, err
	}
	step, job, err := a.claimSupportAttempt(ctx, p, route.TargetID, progress.Revision)
	if err != nil {
		return progress, err
	}
	if !job.Claim.Execute {
		return store.SupportRestorationProgress(ctx, plan.TaskID, route.TargetID)
	}
	p.Step = *step
	callCtx := context.WithValue(ctx, permitKey{}, p)
	err = a.executeClaim(callCtx, p, job)
	// The step records that a bounded attempt is durably checkpointed, not that
	// the customer history is complete; module counters remain authoritative.
	if err != nil {
		return progress, err
	}
	_, err = tasks.New(a.DB).CompleteStep(ctx, rc.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: rd.StepSucceeded, ResultCode: "support_checkpointed"})
	if err != nil {
		return progress, err
	}
	return store.SupportRestorationProgress(ctx, plan.TaskID, route.TargetID)
}

// executeClaim joins HTTP and renewal before persisting its bounded result.
// Lease loss makes even a late successful HTTP reply unknown, never replayable.
func (a *Adapter) executeClaim(ctx context.Context, p permit, job c.RestorationTopic) error {
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	renewCtx, stopRenewal := context.WithCancel(callCtx)
	done := make(chan struct{})
	var renewErr error // Published by close(done); read only after joining.
	defer func() {
		stopRenewal()
		<-done
	}()
	go func() {
		defer close(done)
		defer func() {
			if recover() != nil {
				renewErr = errors.New("support recovery lease renewal panic")
				cancel()
				slog.ErrorContext(ctx, "support recovery lease renewal panic", "task_id", p.Request.TaskID)
			}
		}()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if e := a.renew(renewCtx, p); e != nil {
					if callCtx.Err() == nil && renewCtx.Err() != nil && errors.Is(e, context.Canceled) {
						return // Normal join canceled an in-flight renewal.
					}
					renewErr = e
					cancel()
					return
				}
			}
		}
	}()
	result, callErr := a.CreateRestorationTopic(callCtx, job)
	stopRenewal()
	<-done
	leaseErr := errors.Join(renewErr, context.Cause(callCtx))
	// Stop/join before accepting a receipt so an in-flight renewal panic cannot
	// race a success commit. The store still checks the live lease in its tx.
	if leaseErr != nil || (result.State != "succeeded" && result.State != "failed" && result.State != "unknown") {
		result = c.TopicResult{State: "unknown"}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	err := a.Store().FinishRestorationTopic(finishCtx, p.Request.TaskID, job.EntryID, job.Claim.Operation.Claim, result, a.Now().UTC())
	if err != nil || leaseErr != nil {
		return errors.Join(err, leaseErr)
	}
	if callErr != nil && result.State == "succeeded" {
		return callErr
	}
	return nil
}

// renew rechecks permission before opening the single-connection transaction,
// then extends only the exact live step. Expired/unknown claims cannot revive.
func (a *Adapter) renew(ctx context.Context, p permit) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.Permission(ctx, p.Request.ActorID, "resume"); err != nil {
		return err
	}
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := a.Now().UTC()
		if _, err := checkParent(ctx, tx, p, now); err != nil {
			return err
		}
		changed := tx.Model(&rd.Step{}).Where("id = ? AND version = ? AND lease_token = ? AND state = ? AND lease_until > ?", p.Step.ID, p.Step.Version, p.Step.LeaseToken, rd.StepRunning, now).Update("lease_until", now.Add(time.Minute))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrDenied
		}
		return nil
	})
}

// uncovered counts all source-owned historical business not represented by an
// actual restoration entry. Empty module configuration alone is never evidence.
func (a *Adapter) uncovered(ctx context.Context, p permit) (uint64, error) {
	var remaining uint64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := checkParent(ctx, tx, p, a.Now().UTC()); err != nil {
			return err
		}
		for _, side := range []struct {
			bot                 uint
			version, generation uint64
		}{{p.Request.Snapshot.Old.BotID, p.Request.Snapshot.ExpectedOld.Config, p.Request.Snapshot.ExpectedOld.Generation}, {p.Request.Snapshot.New.BotID, p.Request.Snapshot.ExpectedNew.Config, p.Request.Snapshot.ExpectedNew.Generation}} {
			if err := bots.New(tx).RecoveryDrained(ctx, runtime.RecoveryFence{TaskID: p.Request.TaskID, BotID: side.bot, Version: side.version, Generation: side.generation}); err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Model(&sd.Session{}).Where("bot_id = ?", p.Request.Snapshot.Old.BotID).Where("NOT EXISTS (SELECT 1 FROM telegram_support_restoration_entries e WHERE e.task_id = ? AND e.source_session_id = telegram_support_sessions.id)", p.Request.TaskID).Count(&count).Error; err != nil {
			return err
		}
		remaining += uint64(count)
		if err := tx.Model(&sd.TopicOperation{}).Where("bot_id = ?", p.Request.Snapshot.Old.BotID).Where("NOT EXISTS (SELECT 1 FROM telegram_support_restoration_entries e JOIN telegram_support_restorations p ON p.task_id = e.task_id AND p.target_id = e.target_id WHERE e.task_id = ? AND e.source_session_id = telegram_support_topic_operations.session_id AND e.target_id = telegram_support_topic_operations.target_id AND p.source_route_version >= telegram_support_topic_operations.route_version AND telegram_support_topic_operations.retired = true)", p.Request.TaskID).Count(&count).Error; err != nil {
			return err
		}
		remaining += uint64(count)
		for _, model := range []any{&sd.BufferedMessage{}, &sd.Downstream{}, &sd.Challenge{}, &sd.TopicMapping{}, &sd.ReplyMapping{}} {
			if err := tx.Model(model).Where("bot_id = ? AND session_id NOT IN (?)", p.Request.Snapshot.Old.BotID, tx.Model(&sd.Session{}).Select("id").Where("bot_id = ?", p.Request.Snapshot.Old.BotID)).Count(&count).Error; err != nil {
				return err
			}
			remaining += uint64(count)
		}
		if err := tx.Model(&sd.ReadyDispatch{}).Where("bot_id = ? AND message_id NOT IN (?)", p.Request.Snapshot.Old.BotID, tx.Model(&sd.BufferedMessage{}).Select("id").Where("bot_id = ?", p.Request.Snapshot.Old.BotID)).Count(&count).Error; err != nil {
			return err
		}
		remaining += uint64(count)
		return nil
	})
	return remaining, err
}

// ResolveTopic exposes the audited task-bound reconciliation port. The caller
// supplies authenticated actor in request; resolution cannot substitute another actor.
// No external retry occurs here: retry only authorizes the next bounded Run.
func (a *Adapter) ResolveTopic(ctx context.Context, request br.ModuleRequest, entry uint, resolution c.Resolution) error {
	if a.DB == nil || a.Permission == nil || a.Now == nil || request.ActorID == 0 || resolution.ActorID != request.ActorID {
		return c.ErrDenied
	}
	if err := a.Permission(ctx, request.ActorID, "resume"); err != nil {
		return err
	}
	detail, err := tasks.New(a.DB).Get(ctx, request.TaskID)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, permitKey{}, permit{Request: request, Version: detail.Task.Version})
	return a.Store().ResolveRestorationTopic(ctx, request.TaskID, entry, resolution, a.Now().UTC())
}

// ResolveLegacy is the cross-bot counterpart of the same audited no-effect
// resolution. It does not resolve messages or private reachability liabilities.
func (a *Adapter) ResolveLegacy(ctx context.Context, request br.ModuleRequest, entry, operation uint, resolution c.Resolution) error {
	if a == nil || a.DB == nil || a.Permission == nil || a.Now == nil || request.ActorID != resolution.ActorID {
		return c.ErrDenied
	}
	if err := a.Permission(ctx, request.ActorID, "resume"); err != nil {
		return err
	}
	detail, err := tasks.New(a.DB).Get(ctx, request.TaskID)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, permitKey{}, permit{Request: request, Version: detail.Task.Version})
	return a.Store().ReconcileRestorationLegacy(ctx, request.TaskID, entry, operation, resolution, a.Now().UTC())
}

// GroupDrainer is the existing supervisor stop-and-join port. A nil drainer
// cannot turn stopped state or lease expiry into proof of worker termination.
type GroupDrainer interface {
	BlockAndDrainRecovery(context.Context, runtime.RecoveryFence) error
}

// RecoverGroup performs a bounded same-bot restoration after the route switch.
// actor is authenticated transport identity, never inferred from ConfirmedActorID.
// request must match the persisted snapshot; returned counters never release fences.
func (a *Adapter) RecoverGroup(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, drain GroupDrainer) (rc.GroupRecoveryResult, error) {
	out := rc.GroupRecoveryResult{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion}
	p, err := a.prepareGroup(ctx, actor, request, drain)
	if err != nil {
		return out, err
	}
	ctx = context.WithValue(ctx, permitKey{}, p)
	progress, err := a.runTarget(ctx, p, rd.BotRoute{TargetID: request.TargetID})
	if err != nil {
		return out, err
	}
	out.Verified = progress.Scanned
	out.Remaining = progress.Pending + progress.AwaitingConsultation + progress.Legacy
	out.Unknown = progress.Unknown
	extra, err := a.groupUncovered(ctx, p)
	if err != nil {
		return out, err
	}
	out.Remaining += extra
	return out, nil
}

// ContinueGroup is the production support branch after the existing route-check
// phase. It preserves Recovering on partial results; complete results atomically
// release group/runtime fences and complete the parent without requiring a running bot.
func (a *Adapter) ContinueGroup(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, drain GroupDrainer) (rc.GroupRecoveryResult, error) {
	result := rc.GroupRecoveryResult{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion}
	if a == nil || a.DB == nil || a.Permission == nil || a.Now == nil || actor == 0 {
		return result, c.ErrDenied
	}
	if err := a.Permission(ctx, actor, "resume"); err != nil {
		return result, err
	}
	detail, err := tasks.New(a.DB).Get(ctx, request.TaskID)
	if err != nil {
		return result, err
	}
	if detail.Task.Kind == rd.GroupReplace && detail.Task.State == rd.Completed {
		snapshot := detail.Snapshot
		expected := rc.GroupRecoveryRequest{TaskID: detail.Task.ID, ActorID: actor, TargetID: snapshot.New.TargetID, BotID: snapshot.New.BotID, OldChatID: snapshot.Old.ChatID, NewChatID: snapshot.New.ChatID, OldRouteVersion: snapshot.ExpectedOld.Route, NewRouteVersion: snapshot.ExpectedOld.Route + 1, Generation: snapshot.ExpectedNew.Generation, Purpose: "support"}
		if expected != request {
			return result, c.ErrDenied
		}
		if err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return checkActor(tx, actor) }); err != nil {
			return result, err
		}
		result.Verified = true
		return result, nil
	}
	result, err = a.RecoverGroup(ctx, actor, request, drain)
	if err != nil || !result.Verified || result.Remaining != 0 || result.Unknown != 0 {
		return result, err
	}
	p, err := a.prepareGroup(ctx, actor, request, drain)
	if err != nil {
		return result, err
	}
	ctx = context.WithValue(ctx, permitKey{}, p)
	err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := a.Now().UTC()
		plan := *p.GroupPlan
		if err := (guard{tx: tx, authority: a.Authorize}).CheckSupportRestoration(ctx, plan, now); err != nil {
			return err
		}
		progress, err := a.boundStore(tx).SupportRestorationProgress(ctx, request.TaskID, request.TargetID)
		if err != nil {
			return err
		}
		if !progress.Scanned || progress.Pending+progress.Legacy+progress.AwaitingConsultation != 0 {
			return c.ErrConflict
		}
		bound := *a
		bound.DB = tx
		uncovered, err := bound.groupUncovered(ctx, p)
		if err != nil {
			return err
		}
		if uncovered != 0 {
			return c.ErrConflict
		}
		var active int64
		if err = tx.Model(&rd.Step{}).Where("task_id = ? AND state <> ?", request.TaskID, rd.StepSucceeded).Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return c.ErrConflict
		}
		var target gd.Target
		if err = tx.First(&target, request.TargetID).Error; err != nil {
			return err
		}
		instance, err := bots.New(tx).Get(ctx, request.BotID)
		if err != nil {
			return err
		}
		if _, err = routes.New(tx).ReleaseRecovery(ctx, groups.RecoveryRelease{TaskID: request.TaskID, TargetID: request.TargetID, ExpectedVersion: target.Version, ExpectedRouteVersion: target.RouteVersion, ExpectedObservationSeq: target.ObservationSeq, BotID: request.BotID, ChatID: request.NewChatID, Generation: request.Generation, BotTelegramID: instance.TelegramID}); err != nil {
			return err
		}
		if err = bots.New(tx).ReleaseRecovery(ctx, runtime.RecoveryFence{TaskID: request.TaskID, BotID: request.BotID, Version: instance.Version, Generation: instance.Generation}); err != nil {
			return err
		}
		_, err = tasks.New(tx).Transition(ctx, rc.Transition{TaskID: request.TaskID, ExpectedVersion: p.Version, State: rd.Completed})
		return err
	})
	return result, err
}

// groupUncovered prevents orphan mappings/operations from disappearing behind
// an empty session scan. Another logical target remains isolated, not retired.
func (a *Adapter) groupUncovered(ctx context.Context, p permit) (uint64, error) {
	var remaining uint64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := checkParent(ctx, tx, p, a.Now().UTC()); err != nil {
			return err
		}
		plan := p.GroupPlan
		var count int64
		if err := tx.Model(&sd.TopicOperation{}).Where("bot_id = ? AND target_id = ? AND route_version <= ?", plan.Source.BotID, plan.Source.TargetID, plan.Source.RouteVersion).Where("NOT EXISTS (SELECT 1 FROM telegram_support_restoration_entries e WHERE e.task_id = ? AND e.target_id = telegram_support_topic_operations.target_id AND e.source_session_id = telegram_support_topic_operations.session_id)", plan.TaskID).Count(&count).Error; err != nil {
			return err
		}
		remaining += uint64(count)
		if err := tx.Model(&sd.TopicMapping{}).Where("bot_id = ? AND target_id = ? AND route_version <= ? AND state <> ?", plan.Source.BotID, plan.Source.TargetID, plan.Source.RouteVersion, "retired").Count(&count).Error; err != nil {
			return err
		}
		remaining += uint64(count)
		return nil
	})
	return remaining, err
}

// prepareGroup installs the runtime fence only after exact parent/scope/RBAC and
// switched target validation. Draining occurs outside the transaction and is rechecked by core.
func (a *Adapter) prepareGroup(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, drain GroupDrainer) (permit, error) {
	p := permit{}
	if a.DB == nil || a.Permission == nil || a.Cipher == nil || a.API == nil || a.Now == nil || drain == nil || actor == 0 || request.Purpose != "support" {
		return p, c.ErrDenied
	}
	if err := a.Permission(ctx, actor, "resume"); err != nil {
		return p, err
	}
	var fence runtime.RecoveryFence
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := tasks.New(tx).Get(ctx, request.TaskID)
		if err != nil {
			return err
		}
		if detail.Task.Kind != rd.GroupReplace || detail.Task.State != rd.Recovering || detail.Task.ConfirmedAt == nil {
			return c.ErrDenied
		}
		if err := dblock.Row(tx.Model(&rd.Task{}).Where("id = ? AND version = ?", detail.Task.ID, detail.Task.Version), "version", c.ErrDenied); err != nil {
			return err
		}
		if err = checkActor(tx, actor); err != nil {
			return err
		}
		if err = checkScopes(tx, detail); err != nil {
			return err
		}
		snapshot := detail.Snapshot
		expected := rc.GroupRecoveryRequest{TaskID: detail.Task.ID, ActorID: actor, TargetID: snapshot.New.TargetID, BotID: snapshot.New.BotID, OldChatID: snapshot.Old.ChatID, NewChatID: snapshot.New.ChatID, OldRouteVersion: snapshot.ExpectedOld.Route, NewRouteVersion: snapshot.ExpectedOld.Route + 1, Generation: snapshot.ExpectedNew.Generation, Purpose: "support"}
		if request != expected || snapshot.Old.BotID != snapshot.New.BotID || snapshot.Old.TargetID != snapshot.New.TargetID {
			return c.ErrDenied
		}
		instance, err := bots.New(tx).Get(ctx, request.BotID)
		if err != nil {
			return err
		}
		if instance.Mode != "internal" || instance.TelegramID <= 0 || instance.Generation != request.Generation {
			return c.ErrDenied
		}
		plan := groupPlan(detail, actor, instance.Version)
		if err = checkGroupTarget(tx, detail, plan, a.Now().UTC()); err != nil {
			return err
		}
		fence = runtime.RecoveryFence{TaskID: request.TaskID, BotID: request.BotID, Version: instance.Version, Generation: instance.Generation}
		if err = bots.New(tx).BlockRecovery(ctx, fence); err != nil {
			return err
		}
		p = permit{Request: br.ModuleRequest{TaskID: request.TaskID, ActorID: actor, Snapshot: snapshot}, Version: detail.Task.Version, GroupPlan: &plan}
		return nil
	})
	if err != nil {
		return p, err
	}
	if err = drain.BlockAndDrainRecovery(ctx, fence); err != nil {
		return p, err
	}
	if err = a.Permission(ctx, actor, "resume"); err != nil {
		return p, err
	}
	return p, nil
}

// RefreshGroup obtains fresh forum evidence while the same-bot runtime is
// stopped/fenced. It reuses the existing task-bound prober, never temporary enablement.
func (a *Adapter) RefreshGroup(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, probe groups.RecoveryProber) error {
	if a == nil || a.DB == nil || a.Permission == nil || a.Now == nil || probe == nil {
		return c.ErrDenied
	}
	if err := a.Permission(ctx, actor, "resume"); err != nil {
		return err
	}
	detail, err := tasks.New(a.DB).Get(ctx, request.TaskID)
	if err != nil {
		return err
	}
	instance, err := bots.New(a.DB).Get(ctx, request.BotID)
	if err != nil {
		return err
	}
	plan := groupPlan(detail, actor, instance.Version)
	expected := rc.GroupRecoveryRequest{TaskID: detail.Task.ID, ActorID: actor, TargetID: plan.Source.TargetID, BotID: plan.Source.BotID, OldChatID: plan.Source.ChatID, NewChatID: plan.Destination.ChatID, OldRouteVersion: plan.Source.RouteVersion, NewRouteVersion: plan.Destination.RouteVersion, Generation: plan.Source.Generation, Purpose: "support"}
	if expected != request {
		return c.ErrDenied
	}
	p := permit{Request: br.ModuleRequest{TaskID: request.TaskID, ActorID: actor, Snapshot: detail.Snapshot}, Version: detail.Task.Version, GroupPlan: &plan}
	ctx = context.WithValue(ctx, permitKey{}, p)
	check := func(tx *gorm.DB) error { _, err := checkParent(ctx, tx, p, a.Now().UTC()); return err }
	if err = a.DB.WithContext(ctx).Transaction(check); err != nil {
		return err
	}
	input := groups.RecoveryProbeInput{TaskID: request.TaskID, TargetID: request.TargetID, BotID: request.BotID, Version: instance.Version, Generation: instance.Generation, TelegramID: instance.TelegramID, ChatID: request.NewChatID, RouteVersion: request.NewRouteVersion}
	observation, err := probe.ProbeRecovery(ctx, input)
	if err != nil {
		return err
	}
	if err = a.Permission(ctx, actor, "resume"); err != nil {
		return err
	}
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := check(tx); err != nil {
			return err
		}
		return routes.New(tx).ObserveRecovery(ctx, groups.RecoveryObservation{RecoveryProbeInput: input, ExpectedTargetVersion: detail.Snapshot.ExpectedOld.Config + 2, Observation: observation})
	})
}

// ResolveGroupTopic uses the same authenticated parent/fence contract for an
// explicit failed retry or unknown reconciliation; it never performs HTTP.
func (a *Adapter) ResolveGroupTopic(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, drain GroupDrainer, entry uint, resolution c.Resolution) error {
	if actor != resolution.ActorID {
		return c.ErrDenied
	}
	p, err := a.prepareGroup(ctx, actor, request, drain)
	if err != nil {
		return err
	}
	return a.Store().ResolveRestorationTopic(context.WithValue(ctx, permitKey{}, p), request.TaskID, entry, resolution, a.Now().UTC())
}

// ResolveGroupLegacy accepts audited no-effect evidence for one retired unknown
// topic. Delivery uncertainty remains untouched and cannot be acknowledged away.
func (a *Adapter) ResolveGroupLegacy(ctx context.Context, actor uint, request rc.GroupRecoveryRequest, drain GroupDrainer, entry, operation uint, resolution c.Resolution) error {
	if actor != resolution.ActorID {
		return c.ErrDenied
	}
	p, err := a.prepareGroup(ctx, actor, request, drain)
	if err != nil {
		return err
	}
	return a.Store().ReconcileRestorationLegacy(context.WithValue(ctx, permitKey{}, p), request.TaskID, entry, operation, resolution, a.Now().UTC())
}

var _ br.Modules = (*Adapter)(nil)

const supportEntriesPath = "/admin/telegram-bot/recovery/tasks/:id/support-entries"
const supportOperationsPath = supportEntriesPath + "/:entry/operations"
const supportResolvePath = supportEntriesPath + "/:entry/resolve"

// managementRead verifies a real actor and exact persisted route grant on one DB
// transaction. The existing outer JWT/Casbin middleware is still mandatory.
func (a *Adapter) managementRead(ctx context.Context, actor, task uint, path string, fn func(*gorm.DB, *rc.Detail) error) error {
	if a == nil || a.DB == nil || actor == 0 || task == 0 {
		return rc.ErrInvalid
	}
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkActorPermission(tx, actor, path, "GET"); err != nil {
			return err
		}
		detail, err := tasks.New(tx).Get(ctx, task)
		if err != nil {
			return err
		}
		if detail.Task.Kind != rd.BotReplace && detail.Task.Kind != rd.GroupReplace {
			return rc.ErrInvalid
		}
		return fn(tx, detail)
	})
}

// SupportEntries returns at most limit safe entries and a durable checkpoint CAS.
// actor and task are authenticated/path inputs; the query never retires data.
func (a *Adapter) SupportEntries(ctx context.Context, actor, task, after uint, limit int) (out rc.SupportEntryPage, err error) {
	if limit < 1 || limit > 100 {
		return out, rc.ErrInvalid
	}
	out.Items = []rc.SupportEntry{}
	err = a.managementRead(ctx, actor, task, supportEntriesPath, func(tx *gorm.DB, detail *rc.Detail) error {
		out.TaskVersion = detail.Task.Version
		var entries []sd.RestorationEntry
		if err := tx.Where("task_id = ? AND id > ?", task, after).Order("id ASC").Limit(limit + 1).Find(&entries).Error; err != nil {
			return err
		}
		if len(entries) > limit {
			entries = entries[:limit]
			out.NextAfterID = entries[len(entries)-1].ID
		}
		for _, entry := range entries {
			var plan sd.RestorationCheckpoint
			if err := tx.Where("task_id = ? AND target_id = ?", task, entry.TargetID).Take(&plan).Error; err != nil {
				return err
			}
			out.Items = append(out.Items, rc.SupportEntry{ID: entry.ID, TargetID: entry.TargetID, Revision: plan.Revision, State: entry.State, Legacy: entry.Legacy, OperationID: entry.OperationID})
		}
		return nil
	})
	return
}

// managementOperations scopes current and legacy operations by durable entry
// ownership; a raw operation ID never grants access to another task/session.
func managementOperations(tx *gorm.DB, entry sd.RestorationEntry, plan sd.RestorationCheckpoint) *gorm.DB {
	return tx.Model(&sd.TopicOperation{}).Where("(id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND route_version = ? AND chat_id = ?) OR (bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ? AND retired = ?)", entry.OperationID, plan.DestinationBotID, entry.DestinationSessionID, plan.TargetID, plan.DestinationRouteVersion, plan.DestinationChatID, plan.SourceBotID, entry.SourceSessionID, plan.TargetID, plan.SourceRouteVersion, true)
}

// SupportOperations lists only version/state/retired projection, not token,
// claim, messages, private identities, or a mutable route supplied by the client.
func (a *Adapter) SupportOperations(ctx context.Context, actor, task, entryID, after uint, limit int) (out rc.SupportOperationPage, err error) {
	if entryID == 0 || limit < 1 || limit > 100 {
		return out, rc.ErrInvalid
	}
	out.Items = []rc.SupportOperation{}
	err = a.managementRead(ctx, actor, task, supportOperationsPath, func(tx *gorm.DB, detail *rc.Detail) error {
		out.TaskVersion = detail.Task.Version
		entry, plan, err := managementEntry(tx, task, entryID)
		if err != nil {
			return err
		}
		var operations []sd.TopicOperation
		if err = managementOperations(tx, entry, plan).Where("id > ?", after).Order("id ASC").Limit(limit + 1).Find(&operations).Error; err != nil {
			return err
		}
		if len(operations) > limit {
			operations = operations[:limit]
			out.NextAfterID = operations[len(operations)-1].ID
		}
		for _, operation := range operations {
			out.Items = append(out.Items, rc.SupportOperation{ID: operation.ID, Version: operation.Version, State: operation.State, Retired: operation.Retired, TargetID: operation.TargetID, RouteVersion: operation.RouteVersion, ChatID: operation.ChatID, Action: operation.Action})
		}
		return nil
	})
	return
}

// managementEntry loads the composite plan only through an entry owned by task.
func managementEntry(tx *gorm.DB, task, entryID uint) (entry sd.RestorationEntry, plan sd.RestorationCheckpoint, err error) {
	if err = tx.Where("task_id = ? AND id = ?", task, entryID).Take(&entry).Error; err != nil {
		return entry, plan, rc.ErrNotFound
	}
	err = tx.Where("task_id = ? AND target_id = ?", task, entry.TargetID).Take(&plan).Error
	return
}

// ResolveSupport reconciles one operation under exact task/checkpoint/operation
// CAS. Review states permit this DB-only correction, never API execution or an
// arbitrary parent-success transition. Existing unknown recovery Steps remain.
func (a *Adapter) ResolveSupport(ctx context.Context, actor, task, entryID uint, in rc.SupportResolution) error {
	if a == nil || a.DB == nil || a.Permission == nil || a.Now == nil || actor == 0 || task == 0 || entryID == 0 || in.TaskVersion == 0 || in.TaskVersion >= 1<<63-1 || in.Revision == 0 || in.Revision >= 1<<63-1 || in.OperationID == 0 || in.OperationVersion == 0 || in.OperationVersion >= 1<<63-1 {
		return rc.ErrInvalid
	}
	if err := a.Permission(ctx, actor, "resume"); err != nil {
		return err
	}
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkActorPermission(tx, actor, supportResolvePath, "POST"); err != nil {
			return err
		}
		if err := dblock.Row(tx.Model(&rd.Task{}).Where("id = ? AND version = ?", task, in.TaskVersion), "version", rc.ErrStale); err != nil {
			return err
		}
		detail, err := tasks.New(tx).Get(ctx, task)
		if err != nil {
			return err
		}
		entry, plan, err := managementEntry(tx, task, entryID)
		if err != nil {
			return err
		}
		if plan.Revision != in.Revision {
			return rc.ErrStale
		}
		// Existing restoration authority binds the checkpoint to its original actor.
		// Do not impersonate that actor when another administrator submits a decision.
		if plan.ActorID != actor {
			return c.ErrDenied
		}
		var operation sd.TopicOperation
		if err = managementOperations(tx, entry, plan).Where("id = ? AND version = ?", in.OperationID, in.OperationVersion).Take(&operation).Error; err != nil {
			return rc.ErrStale
		}
		var running int64
		if err = tx.Model(&rd.Step{}).Where("task_id = ? AND state = ? AND lease_until > ?", task, rd.StepRunning, a.Now().UTC()).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return fmt.Errorf("support live attempts: %w", rc.ErrConflict)
		}
		p := permit{Request: br.ModuleRequest{TaskID: task, ActorID: actor, Snapshot: detail.Snapshot}, Version: in.TaskVersion, Review: true}
		if detail.Task.Kind == rd.GroupReplace {
			group := groupPlan(detail, actor, plan.SourceVersion)
			p.GroupPlan = &group
		}
		boundCtx := context.WithValue(ctx, permitKey{}, p)
		resolution := c.Resolution{ActorID: actor, ExpectedVersion: in.OperationVersion, Decision: in.Decision, Evidence: in.Evidence, ThreadID: in.ThreadID}
		store := a.boundStore(tx)
		if !operation.Retired && (operation.State == "succeeded" || operation.State == "failed") && in.Decision == operation.State {
			err = a.verifySupportReceipt(boundCtx, tx, plan, entry, operation, in)
		} else if operation.Retired {
			err = store.ReconcileRestorationLegacy(boundCtx, task, entryID, operation.ID, resolution, a.Now().UTC())
		} else {
			err = store.ResolveRestorationTopic(boundCtx, task, entryID, resolution, a.Now().UTC())
		}
		if err != nil {
			return fmt.Errorf("support resolution: %w", managementError(err))
		}
		changed := tx.Model(&sd.RestorationCheckpoint{}).Where("task_id = ? AND target_id = ? AND revision = ?", task, plan.TargetID, in.Revision).Updates(map[string]any{"revision": in.Revision + 1, "updated_at": a.Now().UTC()})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return rc.ErrStale
		}
		return a.reconcileSupportCheckpoint(boundCtx, tx, detail, entry, operation, in)
	})
}

// managementError maps support CAS errors to the recovery HTTP vocabulary.
func managementError(err error) error {
	if errors.Is(err, c.ErrConflict) {
		return rc.ErrConflict
	}
	return err
}

// claimSupportAttempt commits step claim, business claim and binding together.
// No external call can occur before commit; an interrupted transaction leaves no
// claimed step. A no-op is completed in that same transaction, not inferred later.
func (a *Adapter) claimSupportAttempt(ctx context.Context, p permit, target uint, revision uint64) (step *rd.Step, job c.RestorationTopic, err error) {
	raw := make([]byte, 24)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := checkParent(ctx, tx, p, a.Now().UTC()); e != nil {
			return e
		}
		var e error
		step, e = tasks.New(tx).EnsureStep(ctx, rc.StepInput{TaskID: p.Request.TaskID, BusinessKey: fmt.Sprintf("support.%d.%d", target, revision), Operation: "support.topic"})
		if e != nil {
			return e
		}
		step, e = tasks.New(tx).ClaimStep(ctx, rc.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
		if e != nil {
			return e
		}
		p.Step = *step
		boundCtx := context.WithValue(ctx, permitKey{}, p)
		job, e = a.boundStore(tx).ClaimRestorationTopic(boundCtx, p.Request.TaskID, hex.EncodeToString(raw), a.Now().UTC(), target)
		if e != nil {
			return e
		}
		if !job.Claim.Execute {
			_, e = tasks.New(tx).CompleteStep(boundCtx, rc.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: rd.StepSucceeded, ResultCode: "support_checkpointed"})
			return e
		}
		bound := *a
		bound.DB = tx
		return bound.bindSupportStep(boundCtx, p, job)
	})
	return
}

// bindSupportStep persists exact entry/operation/version linkage before HTTP.
// It runs inside claimSupportAttempt's transaction; no unbound running claim
// can be committed by the production execution path.
func (a *Adapter) bindSupportStep(ctx context.Context, p permit, job c.RestorationTopic) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := checkParent(ctx, tx, p, a.Now().UTC()); err != nil {
			return err
		}
		key := fmt.Sprintf("%s.%d.%d.%d", p.Step.BusinessKey, job.EntryID, job.Claim.Operation.ID, job.Claim.Operation.Version)
		changed := tx.Model(&rd.Step{}).Where("id = ? AND task_id = ? AND version = ? AND state = ? AND lease_token = ? AND business_key = ?", p.Step.ID, p.Request.TaskID, p.Step.Version, rd.StepRunning, p.Step.LeaseToken, p.Step.BusinessKey).UpdateColumn("business_key", key)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return rc.ErrStale
		}
		return nil
	})
}

// reconcileSupportCheckpoint closes only the exact support attempt backed by
// a persisted terminal receipt or an unknown resolution audited in this transaction.
// It certifies durable checkpointing, never remote success. Generic failures,
// unbound historical attempts and other scopes remain untouched.
func (a *Adapter) reconcileSupportCheckpoint(ctx context.Context, tx *gorm.DB, detail *rc.Detail, entry sd.RestorationEntry, operation sd.TopicOperation, in rc.SupportResolution) error {
	if operation.Retired || in.Decision == "retry" {
		return nil
	}
	if operation.State == "unknown" {
		var auditCount int64
		if err := tx.Model(&sd.ResolutionAudit{}).Where("bot_id = ? AND resource = ? AND resource_id = ? AND version = ? AND actor_id = ? AND decision = ? AND evidence = ?", operation.BotID, "restoration_topic", entry.ID, in.OperationVersion, detailActor(ctx), in.Decision, in.Evidence).Count(&auditCount).Error; err != nil {
			return err
		}
		if auditCount != 1 {
			return c.ErrDenied
		}
	} else if operation.State != "succeeded" && operation.State != "failed" {
		return c.ErrDenied
	}
	var steps []rd.Step
	suffix := fmt.Sprintf(".%d.%d.%d", entry.ID, operation.ID, operation.Version-1)
	if err := tx.Where("task_id = ? AND operation = ? AND business_key LIKE ? AND state IN ?", detail.Task.ID, "support.topic", "support.%"+suffix, []rd.StepState{rd.StepUnknown, rd.StepRunning}).Limit(2).Find(&steps).Error; err != nil {
		return err
	}
	if len(steps) == 0 {
		if operation.State != "unknown" {
			return c.ErrConflict
		}
		return nil
	}
	if len(steps) != 1 {
		return c.ErrDenied
	}
	step := steps[0]
	var target, entryID, operationID uint
	var revision, version uint64
	if _, err := fmt.Sscanf(step.BusinessKey, "support.%d.%d.%d.%d.%d", &target, &revision, &entryID, &operationID, &version); err != nil {
		return c.ErrDenied
	}
	expected := fmt.Sprintf("support.%d.%d.%d.%d.%d", entry.TargetID, revision, entry.ID, operation.ID, operation.Version-1)
	if step.BusinessKey != expected || revision == 0 || revision > in.Revision || target != entry.TargetID || entryID != entry.ID || operationID != operation.ID || version+1 != operation.Version {
		return c.ErrDenied
	}
	now := a.Now().UTC()
	if step.State == rd.StepRunning && (step.LeaseUntil == nil || step.LeaseUntil.After(now)) {
		return rc.ErrConflict
	}
	if step.State == rd.StepUnknown && step.ResultCode != "lease_expired" {
		return c.ErrDenied
	}
	changed := tx.Model(&rd.Step{}).Where("id = ? AND task_id = ? AND version = ? AND state = ? AND business_key = ?", step.ID, detail.Task.ID, step.Version, step.State, step.BusinessKey).Updates(map[string]any{"state": rd.StepSucceeded, "version": step.Version + 1, "lease_token": "", "lease_until": nil, "result_code": "support_reconciled_checkpoint", "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return rc.ErrStale
	}
	var blocked int64
	if err := tx.Model(&rd.Step{}).Where("task_id = ? AND state <> ?", detail.Task.ID, rd.StepSucceeded).Count(&blocked).Error; err != nil {
		return err
	}
	if blocked == 0 && detail.Task.State == rd.NeedsReview {
		changed = tx.Model(&rd.Task{}).Where("id = ? AND version = ? AND state = ?", detail.Task.ID, in.TaskVersion, rd.NeedsReview).Updates(map[string]any{"state": rd.Recovering, "version": in.TaskVersion + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return rc.ErrStale
		}
	}
	slog.InfoContext(ctx, "support checkpoint reconciled", "task_id", detail.Task.ID, "step_id", step.ID, "entry_id", entry.ID)
	return nil
}

// verifySupportReceipt accepts only an already persisted business result, never
// a user assertion of success. Inputs are exact server-loaded rows plus CAS;
// it returns an error on route, mapping, actor, fence or receipt disagreement.
func (a *Adapter) verifySupportReceipt(ctx context.Context, tx *gorm.DB, plan sd.RestorationCheckpoint, entry sd.RestorationEntry, operation sd.TopicOperation, in rc.SupportResolution) error {
	progress, err := a.boundStore(tx).BeginSupportRestoration(ctx, c.SupportRestoration{TaskID: plan.TaskID, ActorID: plan.ActorID, Evidence: plan.Evidence,
		Source:      c.RestorationRoute{BotID: plan.SourceBotID, TargetID: plan.TargetID, Version: plan.SourceVersion, Generation: plan.SourceGeneration, RouteVersion: plan.SourceRouteVersion, ChatID: plan.SourceChatID},
		Destination: c.RestorationRoute{BotID: plan.DestinationBotID, TargetID: plan.TargetID, Version: plan.DestinationVersion, Generation: plan.DestinationGeneration, RouteVersion: plan.DestinationRouteVersion, ChatID: plan.DestinationChatID}}, a.Now().UTC())
	if err != nil {
		return err
	}
	if progress.Revision != in.Revision || operation.Version < 2 || operation.Action != "create" || operation.Claim == "" || operation.BotID != plan.DestinationBotID || operation.SessionID != entry.DestinationSessionID || operation.TargetID != plan.TargetID || operation.ChatID != plan.DestinationChatID || operation.RouteVersion != plan.DestinationRouteVersion || operation.Generation != plan.DestinationGeneration || entry.OperationID != operation.ID || in.Decision != operation.State {
		return c.ErrDenied
	}
	if operation.State == "succeeded" {
		var count int64
		if err = tx.Model(&sd.TopicMapping{}).Where("operation_id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND route_version = ? AND thread_id = ? AND state = ?", operation.ID, operation.BotID, operation.SessionID, operation.TargetID, operation.ChatID, operation.RouteVersion, in.ThreadID, "open").Count(&count).Error; err != nil {
			return err
		}
		if count != 1 || in.ThreadID <= 0 || entry.State != "restored" {
			return c.ErrDenied
		}
	} else {
		if entry.State != "failed" || in.ThreadID != 0 {
			return c.ErrDenied
		}
		var mappings int64
		if err = tx.Model(&sd.TopicMapping{}).Where("operation_id = ?", operation.ID).Count(&mappings).Error; err != nil {
			return err
		}
		if mappings != 0 {
			return c.ErrDenied
		}
	}
	if !c.ValidRecoveryEvidence(in.Evidence) {
		return c.ErrDenied
	}
	return tx.Create(&sd.ResolutionAudit{BotID: operation.BotID, Resource: "support_checkpoint", ResourceID: entry.ID, Version: operation.Version, ActorID: detailActor(ctx), Decision: operation.State, Evidence: in.Evidence, CreatedAt: a.Now().UTC()}).Error
}

// detailActor reads only the server-built management permit, never request JSON.
func detailActor(ctx context.Context) uint {
	p, ok := ctx.Value(permitKey{}).(permit)
	if !ok {
		return 0
	}
	return p.Request.ActorID
}

var _ rc.SupportManagement = (*Adapter)(nil)
