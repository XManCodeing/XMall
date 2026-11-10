package routerecovery

import (
	"context"
	"fmt"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	base "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dispose performs old-invite revocations and known-member queries. It does
// not create invitations, approve users or remove old members before new admission.
func (worker *BotRecovery) dispose(ctx context.Context, input c.BotRouteRecoveryRequest, platform c.RecoveryAccessPlatform) error {
	var invites []d.AccessInvite
	query := worker.Store.db.WithContext(ctx).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", input.TargetID, input.OldBotID, input.OldChatID, input.OldRouteVersion)
	if err := query.Where("state = ? AND link_cipher <> ? AND (term = 'long_term' OR expiry_trusted = ? OR expires_at > ?)", "active", "", false, time.Now().UTC()).Order("id").Find(&invites).Error; err != nil {
		return err
	}
	blocked := map[string]bool{}
	var actions []Action
	if err := worker.Store.db.WithContext(ctx).Where("task_id = ? AND target_id = ? AND kind = ? AND state NOT IN ?", input.TaskID, input.TargetID, "revoke", []string{"pending", "retryable", "failed"}).Find(&actions).Error; err != nil {
		return err
	}
	for _, action := range actions {
		blocked[action.Reference] = true
	}
	attempts := 0
	for _, invite := range invites {
		if blocked[fmt.Sprint(invite.ID)] || (d.IsInviteExpiredAt(invite, time.Now())) {
			continue
		}
		if attempts >= 32 {
			break
		}
		attempts++
		if err := worker.revoke(ctx, input, platform, invite); err != nil {
			return err
		}
	}
	var members []d.AccessMember
	if err := worker.Store.db.WithContext(ctx).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", input.TargetID, input.OldBotID, input.OldChatID, input.OldRouteVersion).Order("telegram_id").Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		if err := worker.queryMember(ctx, input, platform, member); err != nil {
			return err
		}
	}
	return nil
}

// claimAction persists intent BEFORE HTTP. A crash leaves uncertain mutations blocked;
// only read-only queries and definite retryable failures can acquire another attempt.
func (store *Store) claimAction(ctx context.Context, input c.BotRouteRecoveryRequest, kind, reference string) (*Action, error) {
	var claimed *Action
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := botFence(tx, input); err != nil {
			return err
		}
		row := Action{TaskID: input.TaskID, TargetID: input.TargetID, Kind: kind, Reference: reference, State: "pending"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.First(&row, "task_id = ? AND target_id = ? AND kind = ? AND reference = ?", input.TaskID, input.TargetID, kind, reference).Error; err != nil {
			return err
		}
		if kind != "member_query" && row.State != "pending" && row.State != "retryable" && row.State != "failed" {
			return nil
		}
		if kind == "member_query" && row.State == "creating" && time.Since(row.AttemptedAt) < time.Minute {
			return nil
		}
		row.Version++
		row.State, row.AttemptedAt = "creating", time.Now().UTC()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		claimed = &row
		return nil
	})
	return claimed, err
}

// finishAction binds a result to its claimed attempt and rechecks all fences.
// apply is local persistence only; it cannot call any platform/service dependency.
func (store *Store) finishAction(ctx context.Context, input c.BotRouteRecoveryRequest, action *Action, result c.AccessAPIResult, apply func(*gorm.DB) error) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := botFence(tx, input); err != nil {
			return err
		}
		state := result.Outcome
		if state != c.AccessSucceeded && state != c.AccessFailed && state != c.AccessRetryable {
			state = c.AccessUnknown
		}
		updated := tx.Model(&Action{}).Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ? AND version = ? AND state = ?", action.TaskID, action.TargetID, action.Kind, action.Reference, action.Version, "creating").Updates(map[string]any{"state": state, "error_class": result.ErrorClass})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		if state == c.AccessSucceeded && apply != nil {
			return apply(tx)
		}
		return nil
	})
}

// revoke uses the original bot's authority and existing invite cipher. Success must
// come from the restricted platform adapter's verified revoke receipt, never a scan.
func (worker *BotRecovery) revoke(ctx context.Context, input c.BotRouteRecoveryRequest, platform c.RecoveryAccessPlatform, invite d.AccessInvite) error {
	link, err := worker.Cipher.Open(invite.LinkCipher)
	if err != nil {
		return fmt.Errorf("membership recovery invite decrypt: %w", c.ErrInvalid)
	}
	action, err := worker.Store.claimAction(ctx, input, "revoke", fmt.Sprint(invite.ID))
	if err != nil || action == nil {
		return err
	}
	route := recoveryRoute(input, false)
	result := platform.RevokeMembershipInvite(ctx, route, link)
	return worker.Store.finishAction(ctx, input, action, result, func(tx *gorm.DB) error {
		return tx.Model(&d.AccessInvite{}).Where("id = ? AND bot_id = ? AND target_id = ? AND state = ?", invite.ID, input.OldBotID, input.TargetID, "active").Updates(map[string]any{"state": "revoked", "updated_at": time.Now().UTC()}).Error
	})
}

// queryMember reuses lifecycle's trusted member query. Same-chat bot replacement
// transfers only the current observation, never ledger ownership or expiry. Different
// chats retain present old members as explicit cleanup liabilities; no blind kicks.
func (worker *BotRecovery) queryMember(ctx context.Context, input c.BotRouteRecoveryRequest, platform c.RecoveryAccessPlatform, member d.AccessMember) error {
	if member.SourceID == 0 || member.ObservedAt.IsZero() || (member.ObservationSource != "query" && member.ObservationSource != "chat_member") {
		return nil
	}
	var source d.LedgerSource
	if err := worker.Store.db.WithContext(ctx).First(&source, "id = ? AND target_id = ? AND beneficiary_telegram_id = ?", member.SourceID, input.TargetID, member.TelegramID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	if source.Operation != "grant" || source.AccountID == 0 || source.OrderID == 0 || source.OrderItemID == 0 {
		return nil
	}
	action, err := worker.Store.claimAction(ctx, input, "member_query", fmt.Sprint(member.TelegramID))
	if err != nil || action == nil {
		return err
	}
	sameChat := input.OldChatID == input.NewChatID
	route := recoveryRoute(input, sameChat)
	result := platform.GetMembershipMember(ctx, c.AccessJoinAction{Route: route, TelegramID: member.TelegramID})
	return worker.Store.finishAction(ctx, input, action, result.AccessAPIResult, func(tx *gorm.DB) error {
		// Recheck trusted source association and observation CAS after external I/O.
		var count int64
		if err := tx.Model(&d.LedgerSource{}).Where("id = ? AND target_id = ? AND beneficiary_telegram_id = ?", member.SourceID, input.TargetID, member.TelegramID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
		values := map[string]any{"status": result.Status, "is_member": result.IsMember, "observation_source": "query", "observed_at": time.Now().UTC(), "sequence": 0}
		if sameChat {
			values["bot_id"], values["route_version"] = input.NewBotID, input.NewRouteVersion
		}
		updated := tx.Model(&d.AccessMember{}).Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND bot_id = ? AND route_version = ? AND observed_at = ?", member.TargetID, member.ChatID, member.TelegramID, member.BotID, member.RouteVersion, member.ObservedAt).Updates(values)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
}

// recoveryRoute returns only the exact persisted old/new physical route snapshot.
func recoveryRoute(input c.BotRouteRecoveryRequest, destination bool) c.AccessRoute {
	route := c.AccessRoute{TargetID: input.TargetID, BotID: input.OldBotID, ChatID: input.OldChatID, RouteVersion: input.OldRouteVersion, Generation: input.OldGeneration}
	if destination {
		route.BotID, route.ChatID, route.RouteVersion, route.Generation = input.NewBotID, input.NewChatID, input.NewRouteVersion, input.NewGeneration
	}
	return route
}

// recheckAbsentCleanup queues the existing read-only lifecycle recheck after a real
// left observation. It accepts only this task's definite failed ban or no-write
// administrator protection; unknown effects and ban ownership remain untouched.
func (session *groupSession) recheckAbsentCleanup(ctx context.Context, operation d.AccessOperation, member c.AccessMemberResult) error {
	if member.Outcome != c.AccessSucceeded || member.Status != "left" || member.IsMember || !safeCleanupRecheck(operation) {
		return nil
	}
	if err := session.destination(ctx, operation.TelegramID); err != nil {
		return err
	}
	return session.worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		route := session.route()
		var current d.AccessOperation
		if err := tx.First(&current, operation.ID).Error; err != nil {
			return err
		}
		key := fmt.Sprintf("route-recovery:%d:%d:%d", session.request.TaskID, route.TargetID, operation.TelegramID)
		if current.Version != operation.Version || current.BusinessKey != key || current.Kind != "lifecycle" || current.TargetID != route.TargetID || current.BotID != route.BotID || current.ChatID != route.ChatID || current.RouteVersion != route.RouteVersion || current.Generation != route.Generation || !safeCleanupRecheck(current) {
			return c.ErrConflict
		}
		var bound int64
		if err := tx.Model(&Action{}).Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ? AND state = ?", session.request.TaskID, route.TargetID, "group_operation", fmt.Sprint(current.ID), "succeeded").Count(&bound).Error; err != nil {
			return err
		}
		if bound != 1 {
			return c.ErrConflict
		}
		var source d.AccessMember
		if err := tx.Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND telegram_id = ?", route.TargetID, route.BotID, route.ChatID, route.RouteVersion, current.TelegramID).First(&source).Error; err != nil {
			return err
		}
		matched, err := im.SourceMatches(tx, source.SourceID, source.TargetID, source.TelegramID)
		if err != nil {
			return err
		}
		if !matched || source.Status != "left" || source.IsMember || source.ObservationSource != "query" || source.ObservedAt.Before(time.Now().UTC().Add(-time.Minute)) {
			return c.ErrUntrustedIdentity
		}
		var uncertain int64
		if err := tx.Model(&d.AccessResult{}).Where("operation_id = ? AND action IN ? AND outcome NOT IN ?", current.ID, []string{"ban", "unban"}, []string{"failed", "retryable", "needs_review"}).Count(&uncertain).Error; err != nil {
			return err
		}
		if uncertain != 0 {
			return nil
		}
		store, err := base.InTransaction(tx)
		if err != nil {
			return err
		}
		return store.RecoverLifecycle(ctx, c.LifecycleRecovery{BotID: current.BotID, ID: current.ID, ExpectedVersion: current.Version, ActorAdminID: session.actor, Action: "recheck"}, time.Now().UTC())
	})
}

// safeCleanupRecheck restricts automatic read rechecks to unowned definite failures
// or local administrator protection. It never schedules a ban or unban phase.
func safeCleanupRecheck(operation d.AccessOperation) bool {
	if operation.State != "needs_review" || operation.LeaseToken != "" || operation.BanConfirmed || operation.BanBlocked || operation.UnbanOutcome != "" {
		return false
	}
	return (operation.LifecyclePhase == "ban" || operation.LifecyclePhase == "verify") && operation.BanOutcome == c.AccessFailed ||
		(operation.LifecyclePhase == "check" || operation.LifecyclePhase == "ban" || operation.LifecyclePhase == "verify") && operation.BanOutcome == "" && operation.ReviewReason == "administrator_member"
}

// scanActions makes attempted external work part of release evidence. An unknown
// mutation cannot be erased by later expiry, a missing row or a successful scan.
func scanActions(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []Action
	if err := tx.Where("task_id = ? AND target_id = ?", request.TaskID, request.TargetID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "remaining"
		if row.State == "succeeded" {
			state = "resolved"
		}
		if row.State == "creating" || row.State == "unknown" {
			state = "unknown"
		}
		if err := record(tx, request, at, Evidence{Kind: "recovery_action", Reference: row.Kind + ":" + row.Reference, State: state}); err != nil {
			return err
		}
	}
	return nil
}
