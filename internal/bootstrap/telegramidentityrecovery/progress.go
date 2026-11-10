package telegramidentityrecovery

import (
	"context"
	"strconv"

	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	"gorm.io/gorm"
)

// Mappings reloads immutable task evidence and returns only its atomically committed mappings.
// A successful commit receipt is mandatory: an overlapping unconfirmed/cancelled preview must
// not expose another task's mappings. Parent state never supplies a mapping state.
func (backend *Backend) Mappings(ctx context.Context, detail *c.Detail) ([]c.IdentityMapping, error) {
	result := make([]c.IdentityMapping, 0)
	if detail == nil || detail.Task.ID == 0 || detail.Task.Kind != d.IdentityTransfer {
		return nil, c.ErrInvalid
	}
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := tasks.New(tx).Get(ctx, detail.Task.ID)
		if err != nil {
			return err
		}
		if current.Task.Kind != d.IdentityTransfer || current.Task.PreflightRef != detail.Task.PreflightRef {
			return c.ErrStale
		}
		var receipts int64
		if err := tx.Model(&d.Step{}).Where("task_id = ? AND business_key = ? AND operation = ? AND state = ?", current.Task.ID, "identity_mapping_committed", "identity_mapping_committed", d.StepSucceeded).Count(&receipts).Error; err != nil {
			return err
		}
		if receipts == 0 {
			return nil
		}
		if current.Task.ConfirmedAt == nil || len(current.Snapshot.IdentityTargets) == 0 || len(current.Snapshot.IdentityTargets) > 100 {
			return c.ErrStale
		}
		for _, target := range current.Snapshot.IdentityTargets {
			var rows []md.IdentityMigration
			err := tx.Select("id", "target_id", "state", "old_telegram_id", "new_telegram_id").Where("account_id = ? AND target_id = ? AND old_telegram_id = ? AND new_telegram_id = ? AND actor_admin_id = ? AND binding_version = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND group_version = ? AND entitlement_version = ? AND created_at >= ?",
				current.Snapshot.Old.AccountID, target.TargetID, current.Snapshot.Old.TelegramID, current.Snapshot.New.TelegramID, current.Task.ActorID, current.Snapshot.ExpectedOld.Binding, target.BotID, target.ChatID, target.RouteVersion, target.GroupVersion, target.EntitlementVersion, *current.Task.ConfirmedAt).Limit(2).Find(&rows).Error
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return c.ErrStale
			}
			row := rows[0]
			result = append(result, c.IdentityMapping{ID: strconv.FormatUint(uint64(row.ID), 10), TargetID: strconv.FormatUint(uint64(row.TargetID), 10), State: row.State, OldTelegramID: strconv.FormatInt(row.OldTelegramID, 10), NewTelegramID: strconv.FormatInt(row.NewTelegramID, 10)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
