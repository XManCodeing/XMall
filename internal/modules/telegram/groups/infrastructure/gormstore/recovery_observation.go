package gormstore

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
	"time"
)

// ObserveRecovery persists a read-only probe against exact owner/target/route CAS.
// Zero generation is valid only for this task-bound recovery API, never ordinary runtime admission.
func (s *Store) ObserveRecovery(ctx context.Context, in c.RecoveryObservation) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := recoveryTarget(tx, in.TargetID)
		if err != nil {
			return err
		}
		if in.TaskID == 0 || target.RecoveryTaskID != in.TaskID || target.Version != in.ExpectedTargetVersion || target.BotInstanceID != in.BotID || target.TelegramChatID != in.ChatID || target.RouteVersion != in.RouteVersion {
			return c.ErrConflict
		}
		obs := in.Observation
		now := time.Now().UTC()
		if obs.BotTelegramID != in.TelegramID || obs.Generation != in.Generation || obs.RouteVersion != in.RouteVersion || obs.State != "ready" || obs.ErrorClass != "" || obs.ObservedAt == nil || obs.ObservedAt.After(now) || now.Sub(*obs.ObservedAt) > time.Minute {
			return c.ErrCapability
		}
		target.Observation = obs
		if len(d.MissingCapabilities(target)) != 0 {
			return c.ErrCapability
		}
		target.ObservationSeq++
		target.Observation.Sequence = target.ObservationSeq
		result := tx.Model(&d.Target{}).Where("id = ? AND version = ? AND route_version = ? AND recovery_task_id = ?", target.ID, in.ExpectedTargetVersion, in.RouteVersion, in.TaskID).
			Select("observation_seq", "obs_route_version", "obs_sequence", "obs_bot_telegram_id", "obs_generation", "obs_chat_type", "obs_is_forum", "obs_is_member", "obs_member_status", "obs_can_manage_topics", "obs_can_invite_users", "obs_can_restrict_members", "obs_state", "obs_error_class", "obs_source", "obs_checked_at", "obs_observed_at", "obs_last_success_at").Updates(&target)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
}
