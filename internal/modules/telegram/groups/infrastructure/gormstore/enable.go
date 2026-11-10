package gormstore

import (
	"context"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SaveChecked saves a freshly probed target atomically. Source version, route and
// observation sequence are checked under lock; recovery/occupancy use existing Save.
// No Telegram calls occur in this transaction. Failed checks commit disabled.
func (s *Store) SaveChecked(ctx context.Context, target *d.Target, version, route, sequence uint64) error {
	return result(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.guardBotRecovery(tx, target.BotInstanceID); err != nil {
			return err
		}
		if target.ID != 0 {
			var old d.Target
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, target.ID).Error; err != nil {
				return err
			}
			if old.Version != version || old.RouteVersion != route || old.ObservationSeq != sequence || old.RecoveryTaskID != 0 {
				return c.ErrConflict
			}
		}
		now := time.Now().UTC()
		observation := target.Observation
		if observation.CheckedAt == nil || observation.CheckedAt.After(now) || now.Sub(*observation.CheckedAt) > time.Minute {
			return c.ErrCapability
		}
		enabled := target.Enabled
		if enabled && (observation.State != "ready" || observation.ErrorClass != "" || !observation.IsMember || len(d.MissingCapabilities(*target)) != 0) {
			return c.ErrCapability
		}
		// Reuse ordinary route and occupancy transitions, but keep the intermediate
		// disabled state invisible until this enclosing transaction commits.
		target.Enabled = false
		inner := &Store{db: tx, botRecoveryGuard: s.botRecoveryGuard}
		if err := inner.Save(ctx, target, version, route); err != nil {
			return err
		}
		var saved d.Target
		if err := tx.First(&saved, target.ID).Error; err != nil {
			return err
		}
		saved.Enabled = enabled
		saved.ObservationSeq++
		observation.RouteVersion = saved.RouteVersion
		observation.Sequence = saved.ObservationSeq
		saved.Observation = observation
		saved.NextCheckAt = now.Add(2 * time.Minute)
		if observation.RetryAfterSeconds > 0 {
			saved.NextCheckAt = now.Add(time.Duration(observation.RetryAfterSeconds) * time.Second)
		}
		if err := tx.Model(&d.Target{}).Where("id = ?", saved.ID).Select("enabled", "observation_seq", "obs_route_version", "obs_sequence", "obs_bot_telegram_id", "obs_generation", "obs_chat_type", "obs_title", "obs_is_forum", "obs_is_member", "obs_member_status", "obs_can_manage_topics", "obs_can_invite_users", "obs_can_restrict_members", "obs_state", "obs_error_class", "obs_source", "obs_checked_at", "obs_observed_at", "obs_last_success_at", "obs_last_failure_at", "obs_retry_after_seconds", "next_check_at").Updates(&saved).Error; err != nil {
			return err
		}
		if err := tx.Create(&d.Operation{TargetID: saved.ID, RouteVersion: saved.RouteVersion, Sequence: saved.ObservationSeq, Source: "enable", State: observation.State, ErrorClass: observation.ErrorClass, StartedAt: *observation.CheckedAt, FinishedAt: &now}).Error; err != nil {
			return err
		}
		*target = saved
		return nil
	}))
}
