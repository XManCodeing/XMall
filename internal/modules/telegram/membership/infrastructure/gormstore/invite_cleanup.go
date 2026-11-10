package gormstore

import (
	"context"
	"errors"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// PrepareIdentityInvites retires reviewed old entries, returning true only once
// every durable cycle confirms cleanup. The caller must own the recovery task lock.
// It never changes entitlement or membership and never retries an unknown effect.
func (store *Store) PrepareIdentityInvites(ctx context.Context, input c.IdentityMigrationCommit, at time.Time) (bool, error) {
	writer, err := im.New(store.db)
	if err != nil {
		return false, err
	}
	ready := true
	err = writer.PrepareInvites(ctx, input, at, func(tx *gorm.DB, target uint, identity int64, version uint64) error {
		cycleID, err := enqueueInviteCleanupForVersion(tx, target, identity, version, at)
		if err != nil || cycleID == 0 {
			return err
		}
		var cycle d.InviteCleanupCycle
		if err := tx.First(&cycle, cycleID).Error; err != nil {
			return err
		}
		ready = ready && cycle.State == "succeeded"
		return nil
	})
	return ready && err == nil, err
}

// lifecycleScanMembers 分页合并成员和全历史邀请身份，包含已离群和仅邀请的身份。
// tx 必须是当前事务；cursor 为上次稳定物理身份游标，返回可信来源候选。
func lifecycleScanMembers(tx *gorm.DB, bot uint, cursor d.LifecycleCursor, limit int) ([]d.AccessMember, error) {
	type scope struct {
		TargetID   uint
		ChatID     int64
		TelegramID int64
	}
	var scopes []scope
	union := tx.Raw("SELECT target_id, chat_id, telegram_id FROM telegram_membership_access_members WHERE bot_id = ? AND source_id > 0 UNION SELECT target_id, chat_id, telegram_id FROM telegram_membership_access_invites WHERE bot_id = ? AND source_id > 0", bot, bot)
	err := tx.Table("(?) AS scopes", union).Where("target_id > ? OR (target_id = ? AND chat_id > ?) OR (target_id = ? AND chat_id = ? AND telegram_id > ?)", cursor.TargetID, cursor.TargetID, cursor.ChatID, cursor.TargetID, cursor.ChatID, cursor.TelegramID).Order("target_id, chat_id, telegram_id").Limit(limit).Scan(&scopes).Error
	if err != nil {
		return nil, err
	}
	members := make([]d.AccessMember, 0, len(scopes))
	for _, scope := range scopes {
		var member d.AccessMember
		err := tx.Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND bot_id = ?", scope.TargetID, scope.ChatID, scope.TelegramID, bot).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var invite d.AccessInvite
			if err := tx.Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND bot_id = ?", scope.TargetID, scope.ChatID, scope.TelegramID, bot).Order("id DESC").First(&invite).Error; err != nil {
				return nil, err
			}
			member = d.AccessMember{TargetID: invite.TargetID, ChatID: invite.ChatID, TelegramID: invite.TelegramID, BotID: invite.BotID, RouteVersion: invite.RouteVersion, SourceID: invite.SourceID}
		} else if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

// enqueueInviteCleanup 在账本事务内重算全部来源，只有最终失效才创建幂等清理周期。
// target/telegram 指向历史身份，不使用当前账号绑定；返回周期 ID，仍有效时返回零。
func enqueueInviteCleanup(tx *gorm.DB, target uint, telegram int64, at time.Time) (uint, error) {
	// An early refund can tombstone a purchase before its grant exists. There is no
	// invitation liability to calculate then, and absence of a source is not an error.
	var invitationCount int64
	if err := tx.Model(&d.AccessInvite{}).Where("target_id = ? AND telegram_id = ?", target, telegram).Count(&invitationCount).Error; err != nil {
		return 0, err
	}
	if invitationCount == 0 {
		return 0, nil
	}
	root, err := im.Scope(tx, telegram, target)
	if err != nil {
		return 0, err
	}
	if _, err := lockProjection(tx, d.EntitlementScope{TargetID: target, BeneficiaryTelegramID: root}); err != nil {
		return 0, err
	}
	eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: telegram}, target, at)
	if err != nil {
		return 0, err
	}
	if eligible.Active {
		return 0, nil
	}
	return enqueueInviteCleanupForVersion(tx, target, telegram, eligible.Version, at)
}

// enqueueInviteCleanupForVersion records explicit retirement at the already locked
// entitlement version. Callers must prove expiry or reviewed identity transfer first.
func enqueueInviteCleanupForVersion(tx *gorm.DB, target uint, telegram int64, version uint64, at time.Time) (uint, error) {
	var invites []d.AccessInvite
	if err := tx.Where("target_id = ? AND telegram_id = ?", target, telegram).Order("id").Find(&invites).Error; err != nil {
		return 0, err
	}
	if len(invites) == 0 {
		return 0, nil
	}
	cycle := d.InviteCleanupCycle{TargetID: target, TelegramID: telegram, EntitlementVersion: version, State: "pending", CreatedAt: at, UpdatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cycle).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("target_id = ? AND telegram_id = ? AND entitlement_version = ?", target, telegram, version).First(&cycle).Error; err != nil {
		return 0, err
	}
	for _, invite := range invites {
		if err := enqueueInviteCleanupItem(tx, cycle, invite, at); err != nil {
			return 0, err
		}
	}
	return cycle.ID, refreshCleanupCycles(tx, cycle.ID, 0, at)
}

// enqueueInviteCleanupItem 复用唯一撤销操作及结果证据，绝不重放未知副作用。
// 迟到创建回执中的密文可补充未知邀请，但缺失 URL 不能成功；有限期限仅凭可信截止结案。
func enqueueInviteCleanupItem(tx *gorm.DB, cycle d.InviteCleanupCycle, invite d.AccessInvite, at time.Time) error {
	item := d.InviteCleanupItem{CycleID: cycle.ID, InviteID: invite.ID, State: "pending", UpdatedAt: at}
	cancelled, err := cancelUndispatchedInvite(tx, invite, at)
	if err != nil {
		return err
	}
	if cancelled {
		item.State, item.Reason = "succeeded", "create_not_dispatched"
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
	}
	var creating int64
	if err := tx.Model(&d.AccessOperation{}).Where("invite_id = ? AND kind = ? AND state IN ?", invite.ID, "create", []string{"creating", "pending", "retryable"}).Count(&creating).Error; err != nil {
		return err
	}
	if creating > 0 {
		// Pending creation is cancelled only before dispatch; an already leased call remains an outstanding liability.
		if err := tx.Model(&d.AccessOperation{}).Where("invite_id = ? AND kind = ? AND state IN ?", invite.ID, "create", []string{"pending", "retryable"}).Updates(map[string]any{"state": "failed", "error_class": "expired", "version": gorm.Expr("version + 1"), "updated_at": at}).Error; err != nil {
			return err
		}
	}
	if err := cleanupRecoverLink(tx, &invite); err != nil {
		return err
	}
	knownURL := invite.LinkCipher != "" && len(invite.LinkHash) == 64
	boundedExpiry := false
	if !knownURL {
		var err error
		boundedExpiry, err = cleanupBoundedExpiry(tx, invite, at)
		if err != nil {
			return err
		}
	}
	if creating == 0 && (knownURL && (invite.State == "revoked" || d.IsInviteExpiredAt(invite, at)) || boundedExpiry) {
		item.State = "succeeded"
		if boundedExpiry {
			// 仅确认短效入口已自然失效，绝不把未知创建/撤销操作改成平台成功。
			item.Reason = "trusted_expiry"
		}
	} else {
		if !knownURL {
			item.State, item.Reason = "unknown", "invite_url_unknown"
		} else {
			operation := accessCreateOperation(invite, at)
			operation.Kind, operation.BusinessKey, operation.CleanupCycleID = "revoke", fmt.Sprintf("invite:%d:revoke", invite.ID), cycle.ID
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&operation).Error; err != nil {
				return err
			}
			if err := tx.Where("business_key = ?", operation.BusinessKey).First(&operation).Error; err != nil {
				return err
			}
			if operation.State == "failed" && operation.ErrorClass == "identity" && operation.CleanupCycleID != 0 {
				if err := tx.Model(&operation).Updates(map[string]any{"state": "pending", "next_attempt_at": at, "version": operation.Version + 1, "cleanup_cycle_id": cycle.ID}).Error; err != nil {
					return err
				}
				operation.State = "pending"
			}
			item.OperationID, item.State = operation.ID, operation.State
			if operation.CleanupCycleID == 0 {
				if err := tx.Model(&operation).Update("cleanup_cycle_id", cycle.ID).Error; err != nil {
					return err
				}
			}
		}
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "cycle_id"}, {Name: "invite_id"}}, DoUpdates: clause.AssignmentColumns([]string{"operation_id", "state", "reason", "updated_at"})}).Create(&item).Error
}

// cancelUndispatchedInvite 仅对从未租用且无回执/链接的创建做安全取消。
// tx 为当前账本事务，返回可证没有平台责任；已派发或未知始终保守。
func cancelUndispatchedInvite(tx *gorm.DB, invite d.AccessInvite, at time.Time) (bool, error) {
	if invite.LinkCipher != "" || invite.LinkHash != "" {
		return false, nil
	}
	var operation d.AccessOperation
	err := tx.Where("invite_id = ? AND kind = ?", invite.ID, "create").First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if operation.Attempt != 0 || operation.LastAttemptAt != nil || operation.LeaseToken != "" || (operation.State != "pending" && !(operation.State == "failed" && operation.ErrorClass == "expired")) {
		return false, nil
	}
	var receipts int64
	if err := tx.Model(&d.AccessResult{}).Where("operation_id = ?", operation.ID).Count(&receipts).Error; err != nil || receipts != 0 {
		return false, err
	}
	if err := tx.Model(&operation).Updates(map[string]any{"state": "failed", "error_class": "expired", "version": operation.Version + 1, "updated_at": at}).Error; err != nil {
		return false, err
	}
	// expired 表示安全终止本地未创建入口，不冒充 Telegram revoke 成功。
	err = tx.Model(&d.AccessInvite{}).Where("id = ?", invite.ID).Updates(map[string]any{"state": "expired", "updated_at": at}).Error
	return err == nil, err
}

// cleanupBoundedExpiry 对无 URL 责任沿用 rebuildAccess 的可信短效恢复边界。
// tx 为当前事务，invite 为历史邀请，at 为可信时钟；返回自然到期证据是否充分及查询错误。
func cleanupBoundedExpiry(tx *gorm.DB, invite d.AccessInvite, at time.Time) (bool, error) {
	if !d.IsInviteExpiredAt(invite, at) || !invite.ExpiresAt.After(invite.CreatedAt) || invite.ExpiresAt.Sub(invite.CreatedAt) > 24*time.Hour || at.Before(invite.ExpiresAt.Add(5*time.Minute)) {
		return false, nil
	}
	var creation d.AccessOperation
	if err := tx.Where("invite_id = ? AND kind = ?", invite.ID, "create").First(&creation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if creation.State == "pending" || creation.State == "retryable" || creation.State == "creating" {
		return false, nil
	}
	if creation.LastAttemptAt != nil && (creation.LastAttemptAt.After(invite.ExpiresAt) || at.Before(creation.LastAttemptAt.Add(5*time.Minute))) {
		return false, nil
	}
	var inFlight int64
	if err := tx.Model(&d.AccessOperation{}).Where("invite_id = ? AND state = ?", invite.ID, "creating").Count(&inFlight).Error; err != nil {
		return false, err
	}
	return inFlight == 0, nil
}

// cleanupRecoverLink 读取同邀请创建证据补全秘密；只用独立持久回执，不推断未知创建失败。
// tx 为当前事务，invite 会被补全；多个不同 URL 证据必须人工核对，不能任选一个结案。
func cleanupRecoverLink(tx *gorm.DB, invite *d.AccessInvite) error {
	var receipts []d.AccessResult
	ids := tx.Model(&d.AccessOperation{}).Select("id").Where("invite_id = ? AND kind = ?", invite.ID, "create")
	if err := tx.Where("operation_id IN (?) AND link_cipher <> '' AND link_hash <> ''", ids).Order("received_at DESC").Find(&receipts).Error; err != nil {
		return err
	}
	for _, receipt := range receipts {
		if invite.LinkHash != "" && invite.LinkHash != receipt.LinkHash {
			invite.LinkCipher, invite.LinkHash = "", ""
			return nil
		}
		invite.LinkCipher, invite.LinkHash = receipt.LinkCipher, receipt.LinkHash
	}
	if invite.LinkCipher == "" {
		return nil
	}
	return tx.Model(&d.AccessInvite{}).Where("id = ?", invite.ID).Updates(map[string]any{"link_cipher": invite.LinkCipher, "link_hash": invite.LinkHash}).Error
}
