package gormstore

import (
	"context"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"time"
)

// releasedEntrySwitch permits a new physical-route slot only after an explicit,
// completed same-Bot entry switch. tx is the caller's entitlement transaction;
// old remains immutable evidence, not a revoked/expired platform invitation.
func releasedEntrySwitch(tx *gorm.DB, old d.AccessInvite, route c.AccessRoute) (bool, error) {
	if old.TargetID != route.TargetID || old.BotID != route.BotID || old.ChatID == route.ChatID || old.RouteVersion >= route.RouteVersion {
		return false, nil
	}
	if old.State == "unknown" || old.State == "creating" || old.State == "revoked" || old.State == "expired" {
		return false, nil
	}
	// Serialize the proof against another route replacement using this transaction.
	if err := dblock.Row(tx.Table("telegram_group_targets").Where("id = ? AND recovery_task_id = 0 AND bot_instance_id = ? AND telegram_chat_id = ? AND route_version = ?", route.TargetID, route.BotID, route.ChatID, route.RouteVersion), "version", c.ErrConflict); err != nil {
		return false, err
	}
	var proof struct {
		TaskID            uint
		CompletionReceipt string
	}
	query := tx.Table("telegram_membership_route_recovery_target_runs AS run").
		Select("run.task_id, run.completion_receipt").
		Joins("JOIN telegram_group_recovery_target_fences AS fence ON fence.task_id = run.task_id AND fence.target_id = run.target_id").
		Joins("JOIN telegram_recovery_tasks AS task ON task.id = run.task_id").
		Joins("JOIN telegram_group_targets AS target ON target.id = run.target_id").
		Where("run.target_id = ? AND run.bot_id = ? AND run.old_chat_id = ? AND run.new_chat_id = ? AND run.old_route_version = ? AND run.new_route_version = ? AND run.unknown = 0", route.TargetID, route.BotID, old.ChatID, route.ChatID, old.RouteVersion, route.RouteVersion).
		Where("fence.old_bot_id = ? AND fence.new_bot_id = ? AND fence.old_chat_id = ? AND fence.new_chat_id = ? AND fence.initial_route_version = ? AND fence.released_version > 0", route.BotID, route.BotID, old.ChatID, route.ChatID, old.RouteVersion).
		Where("task.kind = ? AND task.state = ? AND task.confirmed_at IS NOT NULL", "group_replace", "completed").
		Where("target.recovery_task_id = 0 AND target.bot_instance_id = ? AND target.telegram_chat_id = ? AND target.route_version = ?", route.BotID, route.ChatID, route.RouteVersion).
		Limit(1).Find(&proof)
	if query.Error != nil {
		return false, query.Error
	}
	if query.RowsAffected != 1 || proof.CompletionReceipt != fmt.Sprintf("membership-entry:%d:%d:%d", proof.TaskID, route.TargetID, route.RouteVersion) {
		return false, nil
	}
	// Late unknown receipts must not inherit an earlier local switch certificate.
	operations := tx.Model(&d.AccessOperation{}).Where("target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", old.TargetID, old.BotID, old.ChatID, old.RouteVersion)
	var count int64
	if err := operations.Session(&gorm.Session{}).Where("state IN ? OR lease_token <> ? OR ban_outcome = ? OR unban_outcome = ?", []string{"creating", "unknown"}, "", "unknown", "unknown").Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	if err := tx.Model(&d.AccessResult{}).Where("operation_id IN (?) AND outcome = ?", operations.Select("id"), "unknown").Count(&count).Error; err != nil {
		return false, err
	}
	return count == 0, nil
}

// recoverInviteCleanup only retries definite failures with durable retirement authority.
// tx is the caller's admin transaction; unknown effects are never replayed.
func recoverInviteCleanup(tx *gorm.DB, operation *d.AccessOperation, action string, at time.Time) error {
	if action != "retry" || (operation.State != "failed" && operation.State != "retryable") {
		return c.ErrConflict
	}
	eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: operation.TelegramID}, operation.TargetID, at)
	if err != nil {
		return err
	}
	if eligible.Active {
		var authorized int64
		if err := tx.Model(&d.InviteCleanupItem{}).Where("cycle_id = ? AND invite_id = ? AND operation_id = ?", operation.CleanupCycleID, operation.InviteID, operation.ID).Count(&authorized).Error; err != nil {
			return err
		}
		if operation.CleanupCycleID == 0 || authorized != 1 {
			return c.ErrConflict
		}
	}
	var invite d.AccessInvite
	if err := tx.First(&invite, operation.InviteID).Error; err != nil {
		return err
	}
	if invite.LinkCipher == "" || len(invite.LinkHash) != 64 {
		return c.ErrConflict
	}
	return tx.Model(operation).Updates(map[string]any{"state": "pending", "next_attempt_at": at, "entitlement_version": eligible.Version, "error_class": ""}).Error
}

// CleanupEligibility verifies the leased historical route and durable retirement item.
// ctx and at bound the lease; renewal never revives retired invitation entry points.
func (store *Store) CleanupEligibility(ctx context.Context, operation *d.AccessOperation, at time.Time) (out c.AccessEligibility, err error) {
	if operation == nil || operation.Kind != "revoke" || operation.CleanupCycleID == 0 {
		return out, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := accessLeaseQuery(tx, operation, at).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
		var invite d.AccessInvite
		if err := tx.First(&invite, operation.InviteID).Error; err != nil {
			return err
		}
		if invite.TargetID != operation.TargetID || invite.TelegramID != operation.TelegramID || invite.BotID != operation.BotID || invite.ChatID != operation.ChatID || invite.RouteVersion != operation.RouteVersion {
			return c.ErrUntrustedIdentity
		}
		matches, err := im.SourceMatches(tx, invite.SourceID, invite.TargetID, invite.TelegramID)
		if err != nil {
			return err
		}
		if !matches {
			return c.ErrUntrustedIdentity
		}
		if err := tx.Model(&d.InviteCleanupItem{}).Where("cycle_id = ? AND invite_id = ? AND operation_id = ?", operation.CleanupCycleID, invite.ID, operation.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrUntrustedIdentity
		}
		return nil
	})
	return out, err
}

// projectInviteRetirement 从任一历史清理项派生不可逆退休标记，不依赖清理是否成功。
// tx 必须为调用方事务，invite 为内部记录；查询失败关闭准入。
func projectInviteRetirement(tx *gorm.DB, invite *d.AccessInvite) error {
	var count int64
	if err := tx.Model(&d.InviteCleanupItem{}).Where("invite_id = ?", invite.ID).Count(&count).Error; err != nil {
		return err
	}
	invite.Retired = count > 0
	return nil
}

// refreshCleanupCycles 将逐邀请回收结果持久汇总；未知责任优先，禁止成员移出覆盖清理失败。
// tx 为当前事务，invite 指定变化邀请；零值用于本次扫描周期的全部项。
func refreshCleanupCycles(tx *gorm.DB, cycleID, inviteID uint, at time.Time) error {
	query := tx.Model(&d.InviteCleanupItem{}).Distinct("cycle_id")
	if cycleID != 0 {
		query = query.Where("cycle_id = ?", cycleID)
	}
	if inviteID != 0 {
		query = query.Where("invite_id = ?", inviteID)
	}
	var cycles []uint
	if err := query.Pluck("cycle_id", &cycles).Error; err != nil {
		return err
	}
	for _, cycle := range cycles {
		var items []d.InviteCleanupItem
		if err := tx.Where("cycle_id = ?", cycle).Find(&items).Error; err != nil {
			return err
		}
		state := "succeeded"
		for _, item := range items {
			current := item.State
			if item.OperationID != 0 && current != "succeeded" {
				var operation d.AccessOperation
				if err := tx.First(&operation, item.OperationID).Error; err != nil {
					return err
				}
				current = operation.State
				if err := tx.Model(&d.InviteCleanupItem{}).Where("cycle_id = ? AND invite_id = ?", cycle, item.InviteID).Updates(map[string]any{"state": current, "updated_at": at}).Error; err != nil {
					return err
				}
			}
			switch {
			case current == "unknown":
				state = "unknown"
			case state != "unknown" && (current == "failed" || current == "needs_review"):
				state = "failed"
			case state == "succeeded" && current != "succeeded":
				state = "pending"
			}
		}
		if err := tx.Model(&d.InviteCleanupCycle{}).Where("id = ?", cycle).Updates(map[string]any{"state": state, "updated_at": at}).Error; err != nil {
			return err
		}
	}
	return nil
}
