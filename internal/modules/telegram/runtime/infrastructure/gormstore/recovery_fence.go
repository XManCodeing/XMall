package gormstore

import (
	"context"
	"errors"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
)

var _ c.RecoveryFenceStore = (*Store)(nil)

// recoveryOwner validates input and compares the complete persisted ownership tuple.
func recoveryOwner(row d.Instance, fence c.RecoveryFence) bool {
	return fence.BotID != 0 && fence.TaskID != 0 && fence.Version != 0 &&
		row.ID == fence.BotID && row.RecoveryTaskID == fence.TaskID &&
		row.RecoveryVersion == fence.Version && row.RecoveryGeneration == fence.Generation &&
		row.Version == fence.Version && row.Generation == fence.Generation
}

// BlockRecovery persists the fence under the same parent lock as generation admission.
// It does not disable the bot or advance ordinary configuration versions.
func (s *Store) BlockRecovery(ctx context.Context, fence c.RecoveryFence) error {
	if fence.BotID == 0 || fence.TaskID == 0 || fence.Version == 0 {
		return c.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, fence.BotID); err != nil {
			return err
		}
		var row d.Instance
		if err := tx.First(&row, fence.BotID).Error; err != nil {
			return notFound(err)
		}
		if row.RecoveryTaskID != 0 {
			if recoveryOwner(row, fence) {
				return nil
			}
			return c.ErrConflict
		}
		if row.Version != fence.Version || row.Generation != fence.Generation {
			return c.ErrConflict
		}
		return tx.Model(&row).Updates(map[string]any{"recovery_task_id": fence.TaskID, "recovery_version": fence.Version, "recovery_generation": fence.Generation}).Error
	})
}

// checkDrainLocked requires explicit joins for ALL generations, including workers
// whose leases expired and were replaced. Missing legacy/crashed-worker evidence
// stays blocked indefinitely, rather than guessing based on elapsed time.
func checkDrainLocked(tx *gorm.DB, fence c.RecoveryFence) error {
	var row d.Instance
	if err := tx.First(&row, fence.BotID).Error; err != nil {
		return notFound(err)
	}
	if !recoveryOwner(row, fence) {
		return c.ErrConflict
	}
	var joined int64
	if err := tx.Model(&d.GenerationExit{}).Where("bot_id = ? AND generation > 0 AND generation <= ? AND joined_at IS NOT NULL", fence.BotID, fence.Generation).Count(&joined).Error; err != nil {
		return err
	}
	if uint64(joined) != fence.Generation {
		return c.ErrRecoveryBlocked
	}
	return nil
}

// RecoveryDrained returns nil only for an exact owner with durable join evidence.
func (s *Store) RecoveryDrained(ctx context.Context, fence c.RecoveryFence) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, fence.BotID); err != nil {
			return err
		}
		return checkDrainLocked(tx, fence)
	})
}

// ReleaseRecovery removes only the exact drained fence; configuration is unchanged.
func (s *Store) ReleaseRecovery(ctx context.Context, fence c.RecoveryFence) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, fence.BotID); err != nil {
			return err
		}
		if err := checkDrainLocked(tx, fence); err != nil {
			return err
		}
		return tx.Model(&d.Instance{}).Where("id = ?", fence.BotID).Updates(map[string]any{"recovery_task_id": 0, "recovery_version": 0, "recovery_generation": 0}).Error
	})
}

// CheckRecovery is a pre-operation revalidation, NOT a drain proof or HTTP lock.
func (s *Store) CheckRecovery(ctx context.Context, bot uint, version, generation uint64) error {
	row, err := s.Get(ctx, bot)
	if err != nil {
		return err
	}
	if row.RecoveryTaskID != 0 {
		return c.ErrRecoveryBlocked
	}
	if row.Version != version || row.Generation != generation {
		return c.ErrStale
	}
	return nil
}

// RecordGenerationExit records a trusted supervisor join, not lease expiration.
// It may acknowledge an older generation after a lease replacement. Identity is
// checked against its admission record, never against mutable current state.
func (s *Store) RecordGenerationExit(ctx context.Context, bot uint, version, generation uint64) error {
	if bot == 0 || version == 0 || generation == 0 {
		return c.ErrStale
	}
	tx := s.db.WithContext(ctx).Model(&d.GenerationExit{}).Where("bot_id = ? AND version = ? AND generation = ?", bot, version, generation).Update("joined_at", time.Now().UTC())
	return result(tx, c.ErrStale)
}

// GuardRecoveryWrite locks the runtime parent using this store's transaction-bound handle.
// Bootstrap uses it to serialize ordinary group/module creation with persistent bot fences.
func (s *Store) GuardRecoveryWrite(ctx context.Context, bot uint) error {
	if err := lockBot(s.db.WithContext(ctx), bot); err != nil {
		return err
	}
	return guardRecoveryLocked(s.db.WithContext(ctx), bot)
}

// guardRecoveryLocked is used only with the already locked transaction connection.
func guardRecoveryLocked(tx *gorm.DB, bot uint) error {
	var row d.Instance
	if err := tx.First(&row, bot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.ErrNotFound
		}
		return err
	}
	if row.RecoveryTaskID != 0 {
		return c.ErrRecoveryBlocked
	}
	return nil
}
