package routerecovery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	base "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BotRecovery reuses the existing evidence inventory, access cipher and restricted
// platform capabilities. DB transactions contain no platform callbacks or HTTP.
type BotRecovery struct {
	Store      *Store
	Cipher     c.AccessCipher
	Platforms  c.RecoveryPlatformFactory
	Admission  c.RecoveryAdmissionFactory
	Authority  c.MembershipRecoveryAuthorizer
	Authorizer c.AccessAuthorizer
	Sponsor    func(context.Context, uint) (uint, error)
}

// RecoverBotRoute validates dual fences, performs bounded dispositions and returns
// an explicit durable receipt only when a final complete inventory has no liabilities.
func (worker *BotRecovery) RecoverBotRoute(ctx context.Context, input c.BotRouteRecoveryRequest) (c.BotRouteRecoveryResult, error) {
	out := c.BotRouteRecoveryResult{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion}
	if worker == nil || worker.Store == nil || worker.Cipher == nil || worker.Platforms == nil {
		return out, c.ErrInvalid
	}
	if worker.Authority != nil {
		if err := worker.Authority.AuthorizeMembershipRecovery(ctx, input, recoveryRoute(input, true)); err != nil {
			return out, err
		}
	}
	if err := worker.Store.beginBotRecovery(ctx, input); err != nil {
		return out, err
	}
	var admission *groupSession
	if input.OldChatID != input.NewChatID && worker.Admission != nil {
		var err error
		admission, err = worker.botSession(ctx, input, false)
		if err != nil {
			return out, err
		}
		if err := worker.continueAdmission(ctx, admission); err != nil {
			return out, err
		}
	}
	platform := worker.Platforms.MembershipRecoveryPlatform(input)
	if platform == nil {
		return out, c.ErrInvalid
	}
	if admission == nil {
		if err := worker.dispose(ctx, input, platform); err != nil {
			return out, err
		}
	}
	if admission != nil {
		if err := admission.check(ctx); err != nil {
			return out, err
		}
	}
	return worker.completeBot(ctx, input, admission)
}

// completeBot persists only the full inventory under the same dual-fenced connection.
func (worker *BotRecovery) completeBot(ctx context.Context, input c.BotRouteRecoveryRequest, admission *groupSession) (c.BotRouteRecoveryResult, error) {
	out := c.BotRouteRecoveryResult{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion}
	err := worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := botFence(tx, input); err != nil {
			return err
		}
		scope := botScope(input)
		at := time.Now().UTC()
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Update("state", "unknown").Error; err != nil {
			return err
		}
		for _, scan := range []func(*gorm.DB, scanRequest, time.Time) error{scanLedger, scanInvites, scanOperations, scanMembers, scanJoins, scanMigrations, scanReceipts} {
			if err := scan(tx, scope, at); err != nil {
				return err
			}
		}
		if err := scanActions(tx, scope, at); err != nil {
			return err
		}
		if admission != nil {
			if err := tx.Where("task_id = ? AND target_id = ? AND kind = ?", input.TaskID, input.TargetID, "admission_executor").Delete(&Evidence{}).Error; err != nil {
				return err
			}
			if err := admission.fence(tx); err != nil {
				return err
			}
			if err := admission.scanAdmission(tx, scope); err != nil {
				return err
			}
			if err := admission.bindOperations(tx, scope, at); err != nil {
				return err
			}
		}
		if input.OldChatID != input.NewChatID && admission == nil {
			var sources int64
			if err := tx.Table("telegram_membership_ledger_sources").Where("target_id = ?", input.TargetID).Count(&sources).Error; err != nil {
				return err
			}
			if sources > 0 {
				if err := record(tx, scope, at, Evidence{Kind: "admission_executor", Reference: "unavailable", State: "remaining"}); err != nil {
					return err
				}
			}
		}
		var remaining, unknown int64
		query := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID)
		if err := query.Session(&gorm.Session{}).Where("state = ?", "remaining").Count(&remaining).Error; err != nil {
			return err
		}
		if err := query.Session(&gorm.Session{}).Where("state = ?", "unknown").Count(&unknown).Error; err != nil {
			return err
		}
		out.Remaining, out.Unknown = uint64(remaining), uint64(unknown)
		out.Complete = remaining == 0 && unknown == 0
		if out.Complete {
			out.CompletionReceipt = fmt.Sprintf("membership:%d:%d:%d", input.TaskID, input.TargetID, input.NewRouteVersion)
		}
		return tx.Model(&Run{}).Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Updates(map[string]any{"verified": out.Complete, "remaining": remaining, "unknown": unknown, "scanned_at": at, "completion_receipt": out.CompletionReceipt, "version": gorm.Expr("version + 1")}).Error
	})
	if err != nil {
		out.Complete, out.CompletionReceipt = false, ""
	}
	slog.InfoContext(ctx, "membership bot recovery disposition", "task", input.TaskID, "target", input.TargetID, "complete", out.Complete, "remaining", out.Remaining, "unknown", out.Unknown)
	return out, err
}

// beginBotRecovery permanently binds task+target to the dual identity snapshot.
func (store *Store) beginBotRecovery(ctx context.Context, input c.BotRouteRecoveryRequest) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := botFence(tx, input); err != nil {
			return err
		}
		row := Run{TaskID: input.TaskID, TargetID: input.TargetID, OldBotID: input.OldBotID, NewBotID: input.NewBotID, OldChatID: input.OldChatID, NewChatID: input.NewChatID, OldRouteVersion: input.OldRouteVersion, NewRouteVersion: input.NewRouteVersion, OldVersion: input.OldVersion, NewVersion: input.NewVersion, OldGeneration: input.OldGeneration, NewGeneration: input.NewGeneration, Version: 1}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var saved Run
		if err := tx.First(&saved, "task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Error; err != nil {
			return err
		}
		if saved.OldBotID != row.OldBotID || saved.NewBotID != row.NewBotID || saved.OldChatID != row.OldChatID || saved.NewChatID != row.NewChatID || saved.OldRouteVersion != row.OldRouteVersion || saved.NewRouteVersion != row.NewRouteVersion || saved.OldVersion != row.OldVersion || saved.NewVersion != row.NewVersion || saved.OldGeneration != row.OldGeneration || saved.NewGeneration != row.NewGeneration {
			return c.ErrConflict
		}
		return nil
	})
}

// botScope translates explicit identities for the shared inventory, never an alias BotID.
func botScope(input c.BotRouteRecoveryRequest) scanRequest {
	return scanRequest{Request: Request{TaskID: input.TaskID, TargetID: input.TargetID, OldChatID: input.OldChatID, NewChatID: input.NewChatID, OldRouteVersion: input.OldRouteVersion, NewRouteVersion: input.NewRouteVersion, Purpose: "membership"}, OldBotID: input.OldBotID, NewBotID: input.NewBotID}
}

// AuthorizeMembershipRecovery checks the requested physical route and both task fences.
// It is suitable for the restricted botapi adapter, never ordinary access workers.
func (store *Store) AuthorizeMembershipRecovery(ctx context.Context, input c.BotRouteRecoveryRequest, route c.AccessRoute) error {
	old := route.BotID == input.OldBotID && route.ChatID == input.OldChatID && route.RouteVersion == input.OldRouteVersion && route.Generation == input.OldGeneration
	current := route.BotID == input.NewBotID && route.ChatID == input.NewChatID && route.RouteVersion == input.NewRouteVersion && route.Generation == input.NewGeneration
	if route.TargetID != input.TargetID || (!old && !current) {
		return c.ErrConflict
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return botFence(tx, input) })
}

// botFence serializes exact task-owned runtimes and target, requiring genuine exit
// evidence for every admitted generation. A stopped state is intentionally ignored.
func botFence(tx *gorm.DB, input c.BotRouteRecoveryRequest) error {
	if input.TaskID == 0 || input.TargetID == 0 || input.OldBotID == 0 || input.NewBotID == 0 || input.OldBotID == input.NewBotID || input.OldVersion == 0 || input.NewVersion == 0 || input.OldChatID >= 0 || input.NewChatID >= 0 || input.OldRouteVersion == 0 || input.NewRouteVersion == 0 || input.NewRouteVersion != input.OldRouteVersion+1 {
		return c.ErrInvalid
	}
	if err := runtimeFences(tx, input); err != nil {
		return err
	}
	group := tx.Table("telegram_group_targets").Where("id = ? AND recovery_task_id = ? AND purpose = ? AND enabled = ? AND bot_instance_id = ? AND telegram_chat_id = ? AND route_version = ?", input.TargetID, input.TaskID, "membership", true, input.NewBotID, input.NewChatID, input.NewRouteVersion)
	if err := matchedLock(group, "version"); err != nil {
		return err
	}
	for _, route := range []struct {
		bot     uint
		chat    int64
		version uint64
		retired bool
	}{{input.OldBotID, input.OldChatID, input.OldRouteVersion, true}, {input.NewBotID, input.NewChatID, input.NewRouteVersion, false}} {
		query := tx.Table("telegram_group_routes").Where("target_id = ? AND bot_instance_id = ? AND telegram_chat_id = ? AND route_version = ?", input.TargetID, route.bot, route.chat, route.version)
		if route.retired {
			query = query.Where("retired_at IS NOT NULL")
		} else {
			query = query.Where("retired_at IS NULL")
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
	}
	return nil
}

var _ c.BotRouteRecoverer = (*BotRecovery)(nil)

// runtimeFences validates both exact owners and every joined generation using tx only.
func runtimeFences(tx *gorm.DB, input c.BotRouteRecoveryRequest) error {
	if input.TaskID == 0 || input.OldBotID == 0 || input.NewBotID == 0 || input.OldBotID == input.NewBotID || input.OldVersion == 0 || input.NewVersion == 0 {
		return c.ErrInvalid
	}
	identities := []struct {
		bot                 uint
		version, generation uint64
	}{{input.OldBotID, input.OldVersion, input.OldGeneration}, {input.NewBotID, input.NewVersion, input.NewGeneration}}
	if identities[0].bot > identities[1].bot {
		identities[0], identities[1] = identities[1], identities[0]
	}
	for _, identity := range identities {
		query := tx.Table("telegram_bot_instances").Where("id = ? AND mode = ? AND recovery_task_id = ? AND recovery_version = ? AND version = ? AND recovery_generation = ? AND generation = ?", identity.bot, "internal", input.TaskID, identity.version, identity.version, identity.generation, identity.generation)
		if err := matchedLock(query, "generation"); err != nil {
			return err
		}
		var count int64
		if err := tx.Table("telegram_bot_generation_exits").Where("bot_id = ? AND generation > 0 AND generation <= ? AND joined_at IS NOT NULL", identity.bot, identity.generation).Count(&count).Error; err != nil {
			return err
		}
		if uint64(count) != identity.generation {
			return c.ErrConflict
		}
	}
	return nil
}

// botSession adapts the shared admission/lifecycle state machines to two stopped
// runtimes. Admin execution never falls back to the website's current sponsor.
func (worker *BotRecovery) botSession(ctx context.Context, input c.BotRouteRecoveryRequest, system bool) (*groupSession, error) {
	if worker == nil || worker.Store == nil || worker.Admission == nil || worker.Authority == nil || worker.Authorizer == nil || worker.Cipher == nil || input.OldChatID == input.NewChatID {
		return nil, c.ErrInvalid
	}
	if system {
		if input.ActorID != 0 || worker.Sponsor == nil {
			return nil, c.ErrInvalid
		}
		actor, err := worker.Sponsor(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		input.ActorID = actor
	}
	if input.ActorID == 0 {
		return nil, c.ErrInvalid
	}
	shared := &GroupRecovery{Store: worker.Store, Cipher: worker.Cipher, Authorizer: worker.Authorizer}
	scope := botScope(input).Request
	scope.ActorID = input.ActorID
	scope.BotID = input.NewBotID
	scope.Generation = input.NewGeneration
	session := &groupSession{Store: base.New(worker.Store.db), worker: shared, request: scope, actor: input.ActorID, botRequest: &input, botAuthority: worker.Authority, admissionObserved: map[int64]bool{}}
	return session, session.check(ctx)
}

// botAdmissionFence keeps scope slots, dual exits and authoritative capability snapshots
// in the same connection as each operation CAS; no dependency callback runs in SQL.
func (session *groupSession) botAdmissionFence(tx *gorm.DB) error {
	input := *session.botRequest
	if err := botFence(tx, input); err != nil {
		return err
	}
	if err := matchedLock(tx.Table("telegram_recovery_tasks").Where("id = ? AND kind = ? AND confirmed_actor_id > 0 AND confirmed_at IS NOT NULL AND state IN ?", input.TaskID, "bot_replace", []string{"recovering", "needs_review", "partial_failed"}), "version"); err != nil {
		return err
	}
	for _, key := range []string{fmt.Sprintf("bot:%d", input.OldBotID), fmt.Sprintf("bot:%d", input.NewBotID), fmt.Sprintf("target:%d", input.TargetID), fmt.Sprintf("chat:%d", input.OldChatID), fmt.Sprintf("chat:%d", input.NewChatID)} {
		var count int64
		if err := tx.Table("telegram_recovery_scope_slots").Where("task_id = ?", input.TaskID).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
	}
	if err := matchedLock(tx.Table("telegram_group_targets").Where("id = ? AND obs_generation = ? AND obs_route_version = ? AND obs_sequence = observation_seq AND obs_state = ? AND obs_error_class = ? AND obs_is_member = ? AND obs_can_invite_users = ? AND obs_can_restrict_members = ? AND obs_observed_at > ? AND obs_observed_at <= ?", input.TargetID, input.NewGeneration, input.NewRouteVersion, "ready", "", true, true, true, time.Now().UTC().Add(-time.Minute), time.Now().UTC()), "version"); err != nil {
		return err
	}
	return session.drained(tx)
}

// AuthorizeMembershipRecovery supplies the exact request binding to the platform
// before/after credential access. Both generations remain durably stopped and fenced.
func (session *groupSession) AuthorizeMembershipRecovery(ctx context.Context, input c.BotRouteRecoveryRequest, route c.AccessRoute) error {
	if session.botRequest == nil || input != *session.botRequest {
		return c.ErrConflict
	}
	if route != recoveryRoute(input, false) && route != recoveryRoute(input, true) {
		return c.ErrConflict
	}
	return session.check(ctx)
}

// continueAdmission issues approval-only website invites and queries actual destination
// membership. With both runtimes stopped no new join update exists: no poll, synthetic
// inbox or blind approve is used. A real external admission may be observed on resume.
func (worker *BotRecovery) continueAdmission(ctx context.Context, session *groupSession) error {
	platform, lifecycle := worker.Admission.MembershipBotAdmission(*session.botRequest, session)
	if platform == nil || lifecycle == nil {
		return c.ErrCapability
	}
	access, err := app.NewAccess(session, session, worker.Authorizer, worker.Cipher, platform)
	if err != nil {
		return err
	}
	scoped := runtime.WithExecutionFence(ctx, session)
	if err := session.retireOldInvites(scoped, platform); err != nil {
		return err
	}
	if err := session.admissions(scoped, access, platform); err != nil {
		return err
	}
	if err := session.cleanup(scoped, lifecycle); err != nil {
		return err
	}
	return session.check(ctx)
}

// BotEntry is an authenticated website-only entry: system sponsor is independently
// reauthorized and identity/ledger are checked by the existing AccessService.
func (worker *BotRecovery) BotEntry(ctx context.Context, input c.BotRouteRecoveryRequest, identity c.AccessIdentity) (*c.AccessEntry, error) {
	session, err := worker.botSession(ctx, input, true)
	if err != nil {
		return nil, err
	}
	if identity.BotID != input.NewBotID {
		return nil, c.ErrUntrustedIdentity
	}
	platform, _ := worker.Admission.MembershipBotAdmission(*session.botRequest, session)
	access, err := app.NewAccess(session, session, worker.Authorizer, worker.Cipher, platform)
	if err != nil {
		return nil, err
	}
	entry, err := access.RequestMembershipInvite(runtime.WithExecutionFence(ctx, session), identity, input.TargetID)
	if err != nil {
		return nil, err
	}
	if err := session.check(ctx); err != nil {
		return nil, err
	}
	return entry, nil
}

// BotAvailable reads recovery eligibility without invitations or manufactured contact.
func (worker *BotRecovery) BotAvailable(ctx context.Context, input c.BotRouteRecoveryRequest, identity c.AccessIdentity) error {
	session, err := worker.botSession(ctx, input, true)
	if err != nil {
		return err
	}
	if identity.BotID != input.NewBotID {
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

var _ c.MembershipRecoveryAuthorizer = (*Store)(nil)
