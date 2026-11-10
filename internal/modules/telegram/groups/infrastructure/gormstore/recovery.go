package gormstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ c.RecoveryStore = (*Store)(nil)

// validRecovery validates explicit old/new identities; changing only Bot is
// valid, but an identical route or an omitted identity must never acquire a fence.
func validRecovery(in c.RecoveryInput) bool {
	return in.TargetID != 0 && in.TaskID != 0 && in.ExpectedVersion != 0 &&
		in.ExpectedRouteVersion != 0 && in.OldBotID != 0 && in.NewBotID != 0 &&
		in.OldChatID < 0 && in.NewChatID < 0 &&
		(in.OldBotID != in.NewBotID || in.OldChatID != in.NewChatID)
}

// sameRecovery binds the receipt to immutable request identities, not just taskID.
func sameRecovery(receipt d.RecoveryFence, in c.RecoveryInput) bool {
	return receipt.TaskID == in.TaskID && receipt.TargetID == in.TargetID &&
		receipt.OldBotID == in.OldBotID && receipt.OldChatID == in.OldChatID &&
		receipt.NewBotID == in.NewBotID && receipt.NewChatID == in.NewChatID
}

// recoveryTarget locks the target with the transaction's connection only.
func recoveryTarget(tx *gorm.DB, id uint) (d.Target, error) {
	var target d.Target
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, id).Error
	return target, err
}

// recoveryCAS applies fields only to the exact version, route, fence and
// observation snapshot. A concurrent probe/invalidation cannot be overwritten.
func recoveryCAS(tx *gorm.DB, target d.Target, fields map[string]any) error {
	fields["version"] = target.Version + 1
	query := tx.Model(&d.Target{}).Where("id = ? AND version = ? AND route_version = ? AND recovery_task_id = ? AND observation_seq = ?",
		target.ID, target.Version, target.RouteVersion, target.RecoveryTaskID, target.ObservationSeq).Updates(fields)
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// FreezeTarget durably pauses all target users without changing Enabled intent.
// Identical interrupted requests return the current owned phase snapshot; mismatched
// versions, identities and another task's fence return ErrConflict.
func (s *Store) FreezeTarget(ctx context.Context, in c.RecoveryInput) (*d.Target, error) {
	if !validRecovery(in) {
		return nil, c.ErrInvalid
	}
	var target d.Target
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if target, err = recoveryTarget(tx, in.TargetID); err != nil {
			return err
		}
		var receipt d.RecoveryFence
		err = tx.First(&receipt, "task_id = ? AND target_id = ?", in.TaskID, in.TargetID).Error
		if err == nil {
			if !sameRecovery(receipt, in) || receipt.InitialVersion != in.ExpectedVersion || receipt.InitialRouteVersion != in.ExpectedRouteVersion ||
				target.RecoveryTaskID != in.TaskID || (target.Version != receipt.FrozenVersion && target.Version != receipt.ReplacedVersion) {
				return c.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if target.RecoveryTaskID != 0 || target.Version != in.ExpectedVersion || target.RouteVersion != in.ExpectedRouteVersion ||
			target.BotInstanceID != in.OldBotID || target.TelegramChatID != in.OldChatID {
			return c.ErrConflict
		}
		if err = recoveryCAS(tx, target, map[string]any{"recovery_task_id": in.TaskID}); err != nil {
			return err
		}
		receipt = d.RecoveryFence{TaskID: in.TaskID, TargetID: in.TargetID, OldBotID: in.OldBotID, OldChatID: in.OldChatID,
			NewBotID: in.NewBotID, NewChatID: in.NewChatID, InitialVersion: target.Version, InitialRouteVersion: target.RouteVersion, FrozenVersion: target.Version + 1}
		if err = tx.Create(&receipt).Error; err != nil {
			return err
		}
		target.RecoveryTaskID, target.Version = in.TaskID, target.Version+1
		return nil
	})
	if err != nil {
		return nil, result(err)
	}
	return &target, nil
}

// ReplaceRoute swaps only route identity under the owning task's fence. Purpose,
// Enabled and module configuration are preserved. All old observation evidence
// is erased; route history and the physical occupancy unique index are retained.
func (s *Store) ReplaceRoute(ctx context.Context, in c.RecoveryInput) (*c.RecoveryResult, error) {
	if !validRecovery(in) {
		return nil, c.ErrInvalid
	}
	var target d.Target
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if target, err = recoveryTarget(tx, in.TargetID); err != nil {
			return err
		}
		var receipt d.RecoveryFence
		if err = tx.First(&receipt, "task_id = ? AND target_id = ?", in.TaskID, in.TargetID).Error; err != nil {
			return err
		}
		if !sameRecovery(receipt, in) || target.RecoveryTaskID != in.TaskID || receipt.FrozenVersion != in.ExpectedVersion || receipt.InitialRouteVersion != in.ExpectedRouteVersion {
			return c.ErrConflict
		}
		if receipt.ReplacedVersion != 0 {
			if target.Version != receipt.ReplacedVersion || target.RouteVersion != in.ExpectedRouteVersion+1 || target.BotInstanceID != in.NewBotID || target.TelegramChatID != in.NewChatID {
				return c.ErrConflict
			}
			return nil
		}
		if target.Version != in.ExpectedVersion || target.RouteVersion != in.ExpectedRouteVersion || target.BotInstanceID != in.OldBotID || target.TelegramChatID != in.OldChatID {
			return c.ErrConflict
		}
		if err = replaceRecoveryRoute(tx, &target, in); err != nil {
			return err
		}
		return tx.Model(&receipt).Update("replaced_version", target.Version).Error
	})
	if err != nil {
		return nil, result(err)
	}
	return &c.RecoveryResult{Target: target, CrossBot: in.OldBotID != in.NewBotID, ModuleConfigurationMoved: false}, nil
}

// replaceRecoveryRoute performs the mutation after ownership checks. Occupancy
// conflicts roll back history retirement and preserve the original fenced route.
func replaceRecoveryRoute(tx *gorm.DB, target *d.Target, in c.RecoveryInput) error {
	now := time.Now().UTC()
	slot := fmt.Sprintf("chat:%d", in.NewChatID)
	var count int64
	if err := tx.Model(&d.Target{}).Where("occupancy_slot = ? AND id <> ?", slot, target.ID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return c.ErrConflict
	}
	fields := resetObservationFields()
	fields["bot_instance_id"], fields["telegram_chat_id"], fields["occupancy_slot"] = in.NewBotID, in.NewChatID, slot
	fields["route_version"], fields["observation_seq"], fields["next_check_at"] = target.RouteVersion+1, target.ObservationSeq+1, now
	if err := recoveryCAS(tx, *target, fields); err != nil {
		return err
	}
	if err := tx.Model(&d.Route{}).Where("target_id = ? AND route_version = ? AND retired_at IS NULL", target.ID, target.RouteVersion).Update("retired_at", now).Error; err != nil {
		return err
	}
	if err := tx.Create(&d.Route{TargetID: target.ID, RouteVersion: target.RouteVersion + 1, BotInstanceID: in.NewBotID, TelegramChatID: in.NewChatID, EffectiveAt: now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&d.Operation{}).Where("target_id = ? AND state = ?", target.ID, "checking").Updates(map[string]any{"state": "superseded", "error_class": "route_changed", "finished_at": now}).Error; err != nil {
		return err
	}
	// Scan into a fresh value: SQL NULL must not leave prior pointer timestamps.
	id := target.ID
	*target = d.Target{}
	return tx.First(target, id).Error
}

// resetObservationFields explicitly includes zero/nil values; GORM struct updates
// would otherwise retain permissions, timestamps and identity from the old route.
func resetObservationFields() map[string]any {
	return map[string]any{
		"obs_route_version": 0, "obs_sequence": 0, "obs_bot_telegram_id": 0, "obs_generation": 0,
		"obs_chat_type": "", "obs_title": "", "obs_is_forum": false, "obs_is_member": false,
		"obs_member_status": "", "obs_can_manage_topics": false, "obs_can_invite_users": false,
		"obs_can_restrict_members": false, "obs_state": "unknown", "obs_error_class": "route_changed",
		"obs_source": "", "obs_checked_at": nil, "obs_observed_at": nil, "obs_last_success_at": nil,
		"obs_last_failure_at": nil, "obs_retry_after_seconds": 0,
	}
}
