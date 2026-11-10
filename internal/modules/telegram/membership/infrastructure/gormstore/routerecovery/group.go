package routerecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	base "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GroupRecovery executes the existing admission/lifecycle state machines while the
// target stays frozen. Authority must load real parent/scope and current RBAC each call.
// Actor resolves only the system continuation sponsor. Administrator resume always
// uses the explicit trusted Request.ActorID and never falls back to that sponsor.
type GroupRecovery struct {
	Store      *Store
	Cipher     c.AccessCipher
	Platforms  c.GroupRecoveryPlatformFactory
	Authority  c.GroupRecoveryAuthorization
	Actor      func(context.Context, uint) (uint, error)
	Authorizer c.AccessAuthorizer
}

// groupSession scopes all repository and platform calls to one immutable task/route.
// Embedded repositories reuse existing ledger, binding, invite and lifecycle algorithms.
type groupSession struct {
	*base.Store
	worker            *GroupRecovery
	request           Request
	actor             uint
	old               bool
	botRequest        *c.BotRouteRecoveryRequest
	botAuthority      c.MembershipRecoveryAuthorizer
	admissionObserved map[int64]bool
}

// RecoverRoute satisfies the existing grouprecovery bridge without unlocking business.
// Each pass creates/reconciles admission, observes destination, then cleans old members.
func (worker *GroupRecovery) RecoverRoute(ctx context.Context, input Request) (Result, error) {
	out := Result{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion}
	if input.ActorID == 0 {
		return out, c.ErrInvalid
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return out, err
	}
	if err := session.check(ctx); err != nil {
		return out, err
	}
	if err := worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		_, err := lockRun(tx, input)
		return err
	}); err != nil {
		return out, err
	}
	platform, lifecycle := worker.Platforms.MembershipGroupPlatform(input.TaskID, session.actor, session)
	if platform == nil || lifecycle == nil {
		return out, c.ErrInvalid
	}
	access, err := app.NewAccess(session, session, worker.Authorizer, worker.Cipher, platform)
	if err != nil {
		return out, err
	}
	scoped := runtime.WithExecutionFence(ctx, session)
	if err := session.retireOldInvites(scoped, platform); err != nil {
		return out, err
	}
	if err := session.admissions(scoped, access, platform); err != nil {
		return out, err
	}
	if err := session.cleanup(scoped, lifecycle); err != nil {
		return out, err
	}
	if err := session.check(ctx); err != nil {
		return out, err
	}
	return session.complete(ctx)
}

// complete persists one atomic full inventory and nonempty receipt after dispositions.
// Failed scans cannot leave a transient Verified receipt from a partial scan.
func (session *groupSession) complete(ctx context.Context) (Result, error) {
	input := session.request
	out := Result{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion}
	err := session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		run, err := lockRun(tx, input)
		if err != nil {
			return err
		}
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Update("state", "unknown").Error; err != nil {
			return err
		}
		scope := scanRequest{Request: input, OldBotID: input.BotID, NewBotID: input.BotID}
		for _, scan := range []func(*gorm.DB, scanRequest, time.Time) error{scanLedger, scanInvites, scanOperations, scanMembers, scanJoins, scanMigrations, scanReceipts} {
			if err := scan(tx, scope, time.Now().UTC()); err != nil {
				return err
			}
		}
		if err := scanActions(tx, scope, time.Now().UTC()); err != nil {
			return err
		}
		if err := session.bindOperations(tx, scope, time.Now().UTC()); err != nil {
			return err
		}
		if err := session.scanAdmission(tx, scope); err != nil {
			return err
		}
		var remaining, unknown int64
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", input.TaskID, input.TargetID, "remaining").Count(&remaining).Error; err != nil {
			return err
		}
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", input.TaskID, input.TargetID, "unknown").Count(&unknown).Error; err != nil {
			return err
		}
		out.Remaining, out.Unknown = uint64(remaining), uint64(unknown)
		out.Verified = remaining == 0 && unknown == 0
		receipt := ""
		if out.Verified {
			receipt = fmt.Sprintf("membership-group:%d:%d:%d", input.TaskID, input.TargetID, input.NewRouteVersion)
		}
		out.Checkpointed = unknown == 0
		updated := tx.Model(&Run{}).Where("task_id = ? AND target_id = ? AND version = ?", input.TaskID, input.TargetID, run.Version).Updates(map[string]any{"version": run.Version + 1, "verified": out.Verified, "remaining": remaining, "unknown": unknown, "completion_receipt": receipt, "scanned_at": time.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		out.Verified, out.Checkpointed = false, false
	}
	slog.InfoContext(ctx, "membership_group_recovery_checkpoint", "task_id", input.TaskID, "target_id", input.TargetID, "remaining", out.Remaining, "unknown", out.Unknown, "persisted", err == nil)
	return out, err
}

// session validates dependencies and binds the real authenticated recovery actor.
func (worker *GroupRecovery) session(ctx context.Context, input Request) (*groupSession, error) {
	if worker == nil || worker.Store == nil || worker.Cipher == nil || worker.Platforms == nil || worker.Authority == nil || worker.Actor == nil || worker.Authorizer == nil {
		return nil, c.ErrInvalid
	}
	if err := validate(input); err != nil {
		return nil, err
	}
	actor := input.ActorID
	if actor == 0 {
		var err error
		actor, err = worker.Actor(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
	}
	if actor == 0 {
		return nil, c.ErrInvalid
	}
	return &groupSession{Store: base.New(worker.Store.db), worker: worker, request: input, actor: actor}, nil
}

// route returns only the selected historical or destination route of this session.
func (session *groupSession) route() c.AccessRoute {
	if session.botRequest != nil {
		return recoveryRoute(*session.botRequest, !session.old)
	}
	input := session.request
	route := c.AccessRoute{TargetID: input.TargetID, BotID: input.BotID, ChatID: input.NewChatID, RouteVersion: input.NewRouteVersion, Generation: input.Generation}
	if session.old {
		route.ChatID, route.RouteVersion = input.OldChatID, input.OldRouteVersion
	}
	return route
}

// check revalidates parent/RBAC outside SQL, then locks exact running runtime/group/history.
func (session *groupSession) check(ctx context.Context) error {
	if session.botRequest != nil {
		if err := session.botAuthority.AuthorizeMembershipRecovery(ctx, *session.botRequest, session.route()); err != nil {
			return err
		}
		return session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return session.fence(tx) })
	}
	if err := session.worker.Authority.AuthorizeMembershipGroupRecovery(ctx, session.request.TaskID, session.actor, session.route()); err != nil {
		return err
	}
	return session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return session.fence(tx) })
}

// fence verifies durable scope slots, switched receipt, real parent and fresh group
// permissions inside the same transaction as each CAS. RBAC remains outside SQL.
func (session *groupSession) fence(tx *gorm.DB) error {
	if session.botRequest != nil {
		return session.botAdmissionFence(tx)
	}
	input := session.request
	if err := fence(tx, input); err != nil {
		return err
	}
	if err := matchedLock(tx.Table("telegram_recovery_tasks").Where("id = ? AND kind = ? AND confirmed_actor_id > 0 AND confirmed_at IS NOT NULL AND state IN ?", input.TaskID, "group_replace", []string{"recovering", "needs_review", "partial_failed"}), "version"); err != nil {
		return err
	}
	for _, key := range []string{fmt.Sprintf("target:%d", input.TargetID), fmt.Sprintf("bot:%d", input.BotID), fmt.Sprintf("chat:%d", input.OldChatID), fmt.Sprintf("chat:%d", input.NewChatID)} {
		var count int64
		if err := tx.Table("telegram_recovery_scope_slots").Where("task_id = ?", input.TaskID).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
	}
	var receipt struct{ ReplacedVersion uint64 }
	if err := tx.Table("telegram_group_recovery_target_fences").Where("task_id = ? AND target_id = ? AND old_bot_id = ? AND new_bot_id = ? AND old_chat_id = ? AND new_chat_id = ? AND initial_route_version = ? AND released_version = 0 AND replaced_version > 0", input.TaskID, input.TargetID, input.BotID, input.BotID, input.OldChatID, input.NewChatID, input.OldRouteVersion).Take(&receipt).Error; err != nil {
		return err
	}
	query := tx.Table("telegram_group_targets").Where("id = ? AND version = ? AND obs_generation = ? AND obs_route_version = ? AND obs_sequence = observation_seq AND obs_state = ? AND obs_error_class = ? AND obs_is_member = ? AND obs_can_invite_users = ? AND obs_can_restrict_members = ? AND obs_observed_at > ? AND obs_observed_at <= ?", input.TargetID, receipt.ReplacedVersion, input.Generation, input.NewRouteVersion, "ready", "", true, true, true, time.Now().UTC().Add(-time.Minute), time.Now().UTC())
	if err := matchedLock(query, "version"); err != nil {
		return err
	}
	if err := session.drained(tx); err != nil {
		return err
	}
	// A group replacement must never borrow a Bot-replacement runtime fence.
	return matchedLock(tx.Table("telegram_bot_instances").Where("id = ? AND recovery_task_id = 0", input.BotID), "generation")
}

// drained refuses pre-freeze ordinary leases or late unclassified HTTP. Recovery
// operations have separate durable action bindings; ordinary queues exclude this target.
func (session *groupSession) drained(tx *gorm.DB) error {
	var references []string
	if err := tx.Model(&Action{}).Where("task_id = ? AND target_id = ? AND kind = ?", session.request.TaskID, session.request.TargetID, "group_operation").Pluck("reference", &references).Error; err != nil {
		return err
	}
	ids := []uint{0}
	for _, reference := range references {
		id, err := strconv.ParseUint(reference, 10, 64)
		if err != nil || id == 0 {
			return c.ErrConflict
		}
		ids = append(ids, uint(id))
	}
	var count int64
	if err := tx.Model(&d.AccessOperation{}).Where("target_id = ? AND state IN ? AND id NOT IN ?", session.request.TargetID, []string{"creating", "unknown"}, ids).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return c.ErrConflict
	}
	return nil
}

// Check implements a task-local execution fence used solely by existing state machines.
func (session *groupSession) Check(ctx context.Context, bot uint, generation uint64) error {
	route := session.route()
	if bot != route.BotID || generation != route.Generation {
		return c.ErrConflict
	}
	return session.check(ctx)
}

// AuthorizeMembershipGroupRecovery accepts only the two physical routes of this task.
func (session *groupSession) AuthorizeMembershipGroupRecovery(ctx context.Context, task, actor uint, route c.AccessRoute) error {
	input := session.request
	valid := route.ChatID == input.OldChatID && route.RouteVersion == input.OldRouteVersion || route.ChatID == input.NewChatID && route.RouteVersion == input.NewRouteVersion
	if task != input.TaskID || actor != session.actor || route.TargetID != input.TargetID || route.BotID != input.BotID || route.Generation != input.Generation || !valid {
		return c.ErrConflict
	}
	return session.check(ctx)
}

// MembershipRoute supplies the narrow recovery route, never the ordinary route resolver.
func (session *groupSession) MembershipRoute(ctx context.Context, target uint) (c.AccessRoute, error) {
	if target != session.request.TargetID {
		return c.AccessRoute{}, c.ErrConflict
	}
	return session.route(), session.check(ctx)
}

// claim claims only an explicitly scoped operation; stale side effects become unknown.
func (session *groupSession) claim(ctx context.Context, id uint) (*d.AccessOperation, error) {
	if err := session.check(ctx); err != nil {
		return nil, err
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	var out *d.AccessOperation
	err := session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		var row d.AccessOperation
		if err := tx.First(&row, "id = ? AND target_id = ? AND bot_id = ?", id, session.request.TargetID, session.route().BotID).Error; err != nil {
			return err
		}
		route := session.route()
		if row.ChatID != route.ChatID || row.RouteVersion != route.RouteVersion {
			return c.ErrConflict
		}
		now := time.Now().UTC()
		if row.State == "creating" && !row.LeaseUntil.After(now) {
			return tx.Model(&row).Updates(map[string]any{"state": "unknown", "version": row.Version + 1, "error_class": "lease_lost"}).Error
		}
		if row.State != "pending" && row.State != "retryable" {
			return nil
		}
		if row.NextAttemptAt.After(now) {
			return nil
		}
		update := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ? AND state = ?", row.ID, row.Version, row.State).Updates(map[string]any{"state": "creating", "version": row.Version + 1, "attempt": row.Attempt + 1, "lease_token": hex.EncodeToString(token), "lease_until": now.Add(90 * time.Second), "last_attempt_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return c.ErrConflict
		}
		row.State, row.Version, row.Attempt, row.LeaseToken, row.LeaseUntil = "creating", row.Version+1, row.Attempt+1, hex.EncodeToString(token), now.Add(90*time.Second)
		out = &row
		return nil
	})
	return out, err
}

// admissions reuses EnsureInvite and ExecuteRecoveryOperation, with current account
// binding validation. No contact/delivery is fabricated; website may read active entry.
func (session *groupSession) admissions(ctx context.Context, service *app.AccessService, platform c.AccessPlatform) error {
	var sources []d.LedgerSource
	if err := session.worker.Store.db.WithContext(ctx).Where("target_id = ?", session.request.TargetID).Order("id").Find(&sources).Error; err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, source := range sources {
		if seen[source.BeneficiaryTelegramID] {
			continue
		}
		seen[source.BeneficiaryTelegramID] = true
		identity := c.AccessIdentity{AccountID: source.AccountID, TelegramID: source.BeneficiaryTelegramID, BotID: session.request.BotID}
		eligible, err := session.Store.Eligibility(ctx, identity, session.request.TargetID, time.Now().UTC())
		if errors.Is(err, c.ErrUntrustedIdentity) {
			continue
		}
		if err != nil {
			return err
		}
		if !eligible.Active {
			continue
		}
		if err := service.Authorizer.AuthorizeMembership(ctx, identity, session.request.TargetID); err != nil {
			return err
		}
		member := platform.GetMembershipMember(ctx, c.AccessJoinAction{Route: session.route(), TelegramID: identity.TelegramID})
		if member.Outcome != c.AccessSucceeded {
			continue
		}
		if err := session.observe(ctx, identity.TelegramID, member); err != nil {
			return err
		}
		if member.IsMember || member.Status == "member" || member.Status == "administrator" || member.Status == "creator" {
			continue
		}
		if member.Status == "kicked" {
			continue
		}
		if _, err := service.RequestMembershipInvite(ctx, identity, session.request.TargetID); err != nil {
			return err
		}
	}
	kinds := []string{"create", "approve", "decline"}
	// Stopped cross-Bot runtimes cannot supply new trusted join events. Existing
	// approval work is retained as a liability, never inferred from a website click.
	if session.botRequest != nil {
		kinds = []string{"create"}
	}
	var operations []d.AccessOperation
	if err := session.worker.Store.db.WithContext(ctx).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND kind IN ? AND state IN ?", session.request.TargetID, session.request.BotID, session.request.NewChatID, session.request.NewRouteVersion, kinds, []string{"pending", "retryable", "creating"}).Order("id").Limit(100).Find(&operations).Error; err != nil {
		return err
	}
	for _, row := range operations {
		if err := session.operationBinding(ctx, row); err != nil {
			return err
		}
		operation, err := session.claim(ctx, row.ID)
		if err != nil {
			return err
		}
		if operation != nil {
			if err := service.ExecuteRecoveryOperation(ctx, operation); err != nil {
				return err
			}
		}
	}
	return nil
}

// operationBinding durably associates existing access work with exactly one recovery
// owner before execution; no new access schema or global ordinary worker bypass exists.
func (session *groupSession) operationBinding(ctx context.Context, operation d.AccessOperation) error {
	return session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		row := Action{TaskID: session.request.TaskID, TargetID: session.request.TargetID, Kind: "group_operation", Reference: fmt.Sprint(operation.ID), State: "succeeded", Version: 1, AttemptedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

// bindOperations records actual task-owned queue outcomes, not a synthetic API success.
func (session *groupSession) bindOperations(tx *gorm.DB, scope scanRequest, at time.Time) error {
	var actions []Action
	if err := tx.Where("task_id = ? AND target_id = ? AND kind = ?", scope.TaskID, scope.TargetID, "group_operation").Find(&actions).Error; err != nil {
		return err
	}
	for _, action := range actions {
		var operation d.AccessOperation
		if err := tx.Where("id = ? AND target_id = ?", action.Reference, scope.TargetID).First(&operation).Error; err != nil {
			return err
		}
		state := "remaining"
		switch operation.State {
		case "succeeded", "failed":
			state = "resolved"
		case "creating", "unknown", "needs_review":
			state = "unknown"
		}
		if err := record(tx, scope, at, Evidence{Kind: "recovery_action", Reference: "group_operation:" + action.Reference, State: state, TelegramID: operation.TelegramID}); err != nil {
			return err
		}
	}
	return nil
}

// observe persists real query observations through the original source/watermark code.
func (session *groupSession) observe(ctx context.Context, identity int64, member c.AccessMemberResult) error {
	if err := session.check(ctx); err != nil {
		return err
	}
	route := session.route()
	if session.botRequest != nil && !session.old && session.admissionObserved != nil {
		session.admissionObserved[identity] = true
	}
	_, err := session.Store.ObserveAccess(ctx, c.AccessObservation{TargetID: route.TargetID, BotID: route.BotID, ChatID: route.ChatID, RouteVersion: route.RouteVersion, TelegramID: identity, Status: member.Status, IsMember: member.IsMember, ObservedAt: time.Now().UTC(), Source: "query"})
	return err
}

// cleanup schedules only known old members after real destination observation. The
// existing lifecycle machine handles admin protection, own-ban attribution and verify.
func (session *groupSession) cleanup(ctx context.Context, platform c.LifecyclePlatform) error {
	old := *session
	old.old = true
	service, err := app.NewLifecycle(&old, &old, &groupCleanupPlatform{LifecyclePlatform: platform, session: &old})
	if err != nil {
		return err
	}
	var members []d.AccessMember
	if err := session.worker.Store.db.WithContext(ctx).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", session.request.TargetID, old.route().BotID, session.request.OldChatID, session.request.OldRouteVersion).Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		if member.SourceID == 0 {
			continue
		}
		observed := platform.GetMembershipMember(ctx, c.AccessJoinAction{Route: old.route(), TelegramID: member.TelegramID})
		if observed.Outcome != c.AccessSucceeded {
			continue
		}
		if err := old.observe(ctx, member.TelegramID, observed); err != nil {
			return err
		}
		if err := old.destination(ctx, member.TelegramID); err != nil {
			if errors.Is(err, c.ErrConflict) {
				continue
			}
			return err
		}
		row := d.AccessOperation{BusinessKey: fmt.Sprintf("route-recovery:%d:%d:%d", session.request.TaskID, member.TargetID, member.TelegramID), Kind: "lifecycle", LifecyclePhase: "check", TargetID: member.TargetID, BotID: member.BotID, ChatID: member.ChatID, RouteVersion: member.RouteVersion, Generation: old.route().Generation, TelegramID: member.TelegramID, State: "pending", Version: 1, NextAttemptAt: time.Now().UTC()}
		if err := session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := old.fence(tx); err != nil {
				return err
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
		}); err != nil {
			return err
		}
		if err := session.worker.Store.db.WithContext(ctx).Where("business_key = ?", row.BusinessKey).First(&row).Error; err != nil {
			return err
		}
		if err := old.recheckAbsentCleanup(ctx, row, observed); err != nil {
			return err
		}
		if err := old.operationBinding(ctx, row); err != nil {
			return err
		}
		operation, err := old.claim(ctx, row.ID)
		if err != nil {
			return err
		}
		if operation != nil {
			if err := service.ExecuteRecoveryOperation(runtime.WithExecutionFence(ctx, &old), operation); err != nil {
				return err
			}
		}
	}
	return nil
}

// destination rechecks that the same trusted member is actually in the new chat,
// with current entitlement and a recent real query; approval alone is never proof.
func (session *groupSession) destination(ctx context.Context, identity int64) error {
	if err := session.check(ctx); err != nil {
		return err
	}
	eligible, err := session.Store.Eligibility(ctx, c.AccessIdentity{TelegramID: identity}, session.request.TargetID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !eligible.Active {
		return c.ErrConflict
	}
	var count int64
	err = session.worker.Store.db.WithContext(ctx).Model(&d.AccessMember{}).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND telegram_id = ? AND source_id > 0 AND observation_source = ? AND observed_at > ? AND (status IN ? OR (status = ? AND is_member = ?))", session.request.TargetID, session.request.BotID, session.request.NewChatID, session.request.NewRouteVersion, identity, "query", time.Now().UTC().Add(-time.Minute), []string{"member", "administrator", "creator"}, "restricted", true).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return c.ErrConflict
	}
	return nil
}

// LifecycleEligibility reuses ledger/lease validation, then marks ONLY the retired
// physical route inactive after new membership proof. Ledger projections are untouched.
func (session *groupSession) LifecycleEligibility(ctx context.Context, operation *d.AccessOperation, at time.Time) (c.AccessEligibility, error) {
	if err := session.destination(ctx, operation.TelegramID); err != nil {
		return c.AccessEligibility{}, err
	}
	eligible, err := session.Store.LifecycleEligibility(ctx, operation, at)
	if err != nil {
		return eligible, err
	}
	eligible.Active = false
	return eligible, nil
}

// RenewAccess checks task and destination proof immediately before every cleanup write.
func (session *groupSession) RenewAccess(ctx context.Context, operation *d.AccessOperation, at time.Time, ttl time.Duration) error {
	if err := session.check(ctx); err != nil {
		return err
	}
	if operation.Kind == "lifecycle" {
		if err := session.destination(ctx, operation.TelegramID); err != nil {
			return err
		}
	}
	return session.Store.RenewAccess(ctx, operation, at, ttl)
}

// FinishLifecycle delegates real receipt/CAS persistence; no fabricated cleanup outcome.
func (session *groupSession) FinishLifecycle(ctx context.Context, operation *d.AccessOperation, result c.LifecycleResult, at time.Time) error {
	if err := session.check(ctx); err != nil {
		return err
	}
	return session.Store.FinishLifecycle(ctx, operation, result, at)
}

// retireOldInvites revokes known old links with persistent attempt CAS; unknown work
// cannot be replaced or silently expired. No HTTP occurs inside the transaction.
func (session *groupSession) retireOldInvites(ctx context.Context, platform c.AccessPlatform) error {
	var invites []d.AccessInvite
	oldRoute := *session
	oldRoute.old = true
	if err := session.worker.Store.db.WithContext(ctx).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND state = ?", session.request.TargetID, oldRoute.route().BotID, session.request.OldChatID, session.request.OldRouteVersion, "active").Find(&invites).Error; err != nil {
		return err
	}
	for _, invite := range invites {
		if d.IsInviteExpiredAt(invite, time.Now()) {
			continue
		}
		if err := session.check(ctx); err != nil {
			return err
		}
		var action Action
		claimed := false
		err := session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := session.fence(tx); err != nil {
				return err
			}
			action = Action{TaskID: session.request.TaskID, TargetID: session.request.TargetID, Kind: "group_revoke", Reference: strconv.FormatUint(uint64(invite.ID), 10), State: "pending"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&action).Error; err != nil {
				return err
			}
			if err := tx.Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ?", action.TaskID, action.TargetID, action.Kind, action.Reference).First(&action).Error; err != nil {
				return err
			}
			if action.State != "pending" && action.State != "retryable" && action.State != "failed" {
				return nil
			}
			action.State, action.Version, action.AttemptedAt = "creating", action.Version+1, time.Now().UTC()
			claimed = true
			return tx.Save(&action).Error
		})
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		link, err := session.worker.Cipher.Open(invite.LinkCipher)
		if err != nil {
			return err
		}
		old := *session
		old.old = true
		result := platform.RevokeMembershipInvite(ctx, old.route(), link)
		if err := session.check(ctx); err != nil {
			return err
		}
		err = session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := session.fence(tx); err != nil {
				return err
			}
			state := result.Outcome
			if state != "succeeded" && state != "failed" && state != "retryable" {
				state = "unknown"
			}
			update := tx.Model(&Action{}).Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ? AND version = ? AND state = ?", action.TaskID, action.TargetID, action.Kind, action.Reference, action.Version, "creating").Updates(map[string]any{"state": state, "error_class": result.ErrorClass})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return c.ErrConflict
			}
			if state == "succeeded" {
				return tx.Model(&d.AccessInvite{}).Where("id = ? AND state = ?", invite.ID, "active").Update("state", "revoked").Error
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Entry exposes a task-bound user request through original account/binding validation.
// Callers must pass authenticated identity; this method does not deliver a bot message.
func (worker *GroupRecovery) Entry(ctx context.Context, input Request, identity c.AccessIdentity) (*c.AccessEntry, error) {
	session, err := worker.session(ctx, input)
	if err != nil {
		return nil, err
	}
	platform, _ := worker.Platforms.MembershipGroupPlatform(input.TaskID, session.actor, session)
	service, err := app.NewAccess(session, session, worker.Authorizer, worker.Cipher, platform)
	if err != nil {
		return nil, err
	}
	entry, err := service.RequestMembershipInvite(runtime.WithExecutionFence(ctx, session), identity, input.TargetID)
	if err != nil {
		return nil, err
	}
	// Recheck after decryption too; revoked sponsor authority cannot release a secret.
	if err := session.check(ctx); err != nil {
		return nil, err
	}
	return entry, nil
}

// ReceiveJoin consumes only trusted persisted runtime inbox input and reuses same-user
// invitation checks and join watermarks while the ordinary group route stays frozen.
func (worker *GroupRecovery) ReceiveJoin(ctx context.Context, input Request, join c.AccessJoinRequest) error {
	if join.BotID != input.BotID || join.ChatID != input.NewChatID {
		return c.ErrConflict
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return err
	}
	platform, _ := worker.Platforms.MembershipGroupPlatform(input.TaskID, session.actor, session)
	service, err := app.NewAccess(session, session, worker.Authorizer, worker.Cipher, platform)
	if err != nil {
		return err
	}
	return service.ReceiveMembershipJoin(runtime.WithExecutionFence(ctx, session), join)
}

// scanAdmission turns active immutable sources into explicit destination admission
// liabilities. Created/approved invitations alone never satisfy actual membership.
func (session *groupSession) scanAdmission(tx *gorm.DB, scope scanRequest) error {
	var sources []d.LedgerSource
	if err := tx.Where("target_id = ?", scope.TargetID).Find(&sources).Error; err != nil {
		return err
	}
	bound, err := base.InTransaction(tx)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, source := range sources {
		identity := source.BeneficiaryTelegramID
		if seen[identity] {
			continue
		}
		seen[identity] = true
		eligible, err := bound.Eligibility(tx.Statement.Context, c.AccessIdentity{TelegramID: identity}, scope.TargetID, time.Now().UTC())
		if err != nil {
			return err
		}
		state := "resolved"
		if eligible.Active {
			var count int64
			if err := tx.Model(&d.AccessMember{}).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND telegram_id = ? AND source_id > 0 AND observation_source IN ? AND (status IN ? OR (status = ? AND is_member = ?))", scope.TargetID, scope.NewBotID, scope.NewChatID, scope.NewRouteVersion, identity, []string{"query", "chat_member"}, []string{"member", "administrator", "creator"}, "restricted", true).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 || session.botRequest != nil && !session.admissionObserved[identity] {
				state = "remaining"
			}
		}
		if err := record(tx, scope, time.Now().UTC(), Evidence{Kind: "admission", Reference: fmt.Sprint(identity), State: state, SourceID: source.ID, TelegramID: identity}); err != nil {
			return err
		}
	}
	return nil
}

// groupCleanupPlatform adds a fresh destination query immediately before ban/unban,
// rather than treating an earlier approved invitation or stale local row as admission.
type groupCleanupPlatform struct {
	c.LifecyclePlatform
	session *groupSession
}

// ready requires real new-chat membership and current ledger/parent proof before effects.
func (platform *groupCleanupPlatform) ready(ctx context.Context, input c.AccessJoinAction) error {
	current := *platform.session
	current.old = false
	member := platform.LifecyclePlatform.GetMembershipMember(ctx, c.AccessJoinAction{Route: current.route(), TelegramID: input.TelegramID})
	if member.Outcome != c.AccessSucceeded || !(member.Status == "member" || member.Status == "administrator" || member.Status == "creator" || member.Status == "restricted" && member.IsMember) {
		return c.ErrConflict
	}
	if err := current.observe(ctx, input.TelegramID, member); err != nil {
		return err
	}
	return platform.session.destination(ctx, input.TelegramID)
}

// BanMembershipMember retains lifecycle's own-ban and administrator safeguards.
func (platform *groupCleanupPlatform) BanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	if err := platform.ready(ctx, input); err != nil {
		return c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "destination_unconfirmed"}
	}
	return platform.LifecyclePlatform.BanMembershipMember(ctx, input)
}

// UnbanMembershipMember is reached only for a confirmed lifecycle-owned ban.
func (platform *groupCleanupPlatform) UnbanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	if err := platform.ready(ctx, input); err != nil {
		return c.AccessAPIResult{Outcome: c.AccessRetryable, ErrorClass: "destination_unconfirmed"}
	}
	return platform.LifecyclePlatform.UnbanMembershipMember(ctx, input)
}

// Available checks system sponsor authorization and current binding/ledger without
// creating an invite. ActorID must be zero for website/inbox, never an admin impersonation.
func (worker *GroupRecovery) Available(ctx context.Context, input Request, identity c.AccessIdentity) error {
	if input.ActorID != 0 {
		return c.ErrInvalid
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return err
	}
	if err := session.check(ctx); err != nil {
		return err
	}
	if identity.BotID != input.BotID {
		return c.ErrUntrustedIdentity
	}
	if err := worker.Authorizer.AuthorizeMembership(ctx, identity, input.TargetID); err != nil {
		return err
	}
	route := session.route()
	identity.Route = &route
	eligible, err := session.Store.Eligibility(ctx, identity, input.TargetID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !eligible.Active {
		return c.ErrUntrustedIdentity
	}
	return session.check(ctx)
}

// ReceiveUpdate verifies the exact decrypted processing inbox payload and live runtime
// lease before accepting a recovery join/member event; user JSON cannot supply provenance.
func (worker *GroupRecovery) ReceiveUpdate(ctx context.Context, input Request, update runtime.Update) error {
	if input.ActorID != 0 || update.ID <= 0 || update.ChatID != input.NewChatID || update.ChatType != "supergroup" {
		return c.ErrInvalid
	}
	if err := runtime.CheckExecutionFence(ctx, input.BotID, input.Generation); err != nil {
		return err
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return err
	}
	if err := session.check(ctx); err != nil {
		return err
	}
	var inbox struct{ PayloadCipher string }
	err = worker.Store.db.WithContext(ctx).Table("telegram_bot_inbox").Where("bot_id = ? AND update_id = ? AND status = ? AND claim <> ? AND claimed_until > ?", input.BotID, update.ID, "processing", "", time.Now().UTC()).Take(&inbox).Error
	if err != nil {
		return c.ErrUntrustedIdentity
	}
	raw, err := worker.Cipher.Open(inbox.PayloadCipher)
	if err != nil {
		return c.ErrUntrustedIdentity
	}
	var saved runtime.Update
	if json.Unmarshal([]byte(raw), &saved) != nil || !reflect.DeepEqual(saved, update) {
		return c.ErrUntrustedIdentity
	}
	if update.Type == "chat_join_request" {
		event := update.JoinRequest
		if event == nil || event.User.ID <= 0 || event.User.IsBot || event.Date <= 0 {
			return c.ErrInvalid
		}
		link := ""
		if event.InviteLink != nil && event.InviteLink.CreatesJoinRequest && !event.InviteLink.IsRevoked {
			link = event.InviteLink.InviteLink
		}
		return worker.ReceiveJoin(ctx, input, c.AccessJoinRequest{BotID: input.BotID, UpdateID: update.ID, ChatID: update.ChatID, TelegramID: event.User.ID, InviteLink: link, ReceivedAt: time.Unix(event.Date, 0).UTC()})
	}
	if update.Type != "chat_member" || update.Member == nil {
		return c.ErrInvalid
	}
	event := update.Member
	if event.Subject.ID <= 0 || event.Subject.IsBot || event.New.User.ID != event.Subject.ID || event.Date <= 0 {
		return c.ErrInvalid
	}
	// Recovery query is authoritative for cleanup. Inbox events still preserve external
	// ban attribution by conservatively not labeling their operator as our managed Bot.
	_, err = session.Store.ObserveAccess(ctx, c.AccessObservation{TargetID: input.TargetID, BotID: input.BotID, ChatID: input.NewChatID, RouteVersion: input.NewRouteVersion, TelegramID: event.Subject.ID, Status: event.New.Status, IsMember: event.New.IsMember, ObservedAt: time.Unix(event.Date, 0).UTC(), Sequence: update.ID, Source: "chat_member"})
	return err
}

var _ Recoverer = (*GroupRecovery)(nil)
