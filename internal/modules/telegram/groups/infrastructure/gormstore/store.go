package gormstore

import (
	"context"
	"errors"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"github.com/XManCodeing/XMall/internal/shared/dberror"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type Store struct {
	db               *gorm.DB
	botRecoveryGuard func(*gorm.DB, uint) error
}

// WithBotRecoveryGuard installs the runtime-owned parent lock in production composition.
// guard must use the provided transaction and must not perform HTTP or global DB reads.
func (s *Store) WithBotRecoveryGuard(guard func(*gorm.DB, uint) error) *Store {
	s.botRecoveryGuard = guard
	return s
}

// guardBotRecovery delegates to the production transaction-bound runtime fence adapter.
func (s *Store) guardBotRecovery(tx *gorm.DB, bot uint) error {
	if s.botRecoveryGuard == nil {
		return nil
	}
	return s.botRecoveryGuard(tx, bot)
}

func New(db *gorm.DB) *Store { return &Store{db: db} }
func result(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || dberror.IsUniqueViolation(err) {
		return c.ErrConflict
	}
	return err
}
func (s *Store) Get(ctx context.Context, id uint) (*d.Target, error) {
	var v d.Target
	err := s.db.WithContext(ctx).First(&v, id).Error
	return &v, result(err)
}
func (s *Store) List(ctx context.Context, f c.Filter) ([]d.Target, int64, error) {
	q := s.db.WithContext(ctx).Model(&d.Target{})
	if f.BotID != 0 {
		q = q.Where("bot_instance_id = ?", f.BotID)
	}
	if f.Purpose != "" {
		q = q.Where("purpose = ?", f.Purpose)
	}
	if f.ChatID != 0 {
		q = q.Where("telegram_chat_id = ?", f.ChatID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []d.Target
	err := q.Order("id DESC").Limit(f.PageSize).Offset((f.Page - 1) * f.PageSize).Find(&rows).Error
	return rows, total, err
}
func (s *Store) Save(ctx context.Context, v *d.Target, version, route uint64) error {
	if v.RecoveryTaskID != 0 {
		return c.ErrConflict
	}
	return result(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.guardBotRecovery(tx, v.BotInstanceID); err != nil {
			return err
		}
		now := time.Now().UTC()
		v.OccupancySlot = fmt.Sprintf("chat:%d", v.TelegramChatID)
		// A portable unique-index collision may not be translated by the configured driver.
		var count int64
		if err := tx.Model(&d.Target{}).Where("occupancy_slot = ? AND id <> ?", v.OccupancySlot, v.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return c.ErrConflict
		}
		if v.ID == 0 {
			v.Version = 1
			v.RouteVersion = 1
			v.NextCheckAt = now
			if err := tx.Create(v).Error; err != nil {
				return err
			}
		} else {
			var old d.Target
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, v.ID).Error; err != nil {
				return err
			}
			if err := s.guardBotRecovery(tx, old.BotInstanceID); err != nil {
				return err
			}
			if old.RecoveryTaskID != 0 || old.Version != version || old.RouteVersion != route {
				return c.ErrConflict
			}
			changed := old.BotInstanceID != v.BotInstanceID || old.TelegramChatID != v.TelegramChatID || old.Purpose != v.Purpose
			if old.Enabled && changed {
				return c.ErrConflict
			}
			if v.Enabled && (changed || old.Observation.State != "ready" || old.Observation.ErrorClass != "" || old.Observation.RouteVersion != old.RouteVersion || old.Observation.ObservedAt == nil || time.Since(*old.Observation.ObservedAt) > 5*time.Minute) {
				return c.ErrCapability
			}
			fields := map[string]any{"name": v.Name, "purpose": v.Purpose, "enabled": v.Enabled, "version": version + 1, "bot_instance_id": v.BotInstanceID, "telegram_chat_id": v.TelegramChatID, "occupancy_slot": v.OccupancySlot}
			v.RouteVersion = old.RouteVersion
			if changed {
				v.RouteVersion++
				fields["route_version"] = v.RouteVersion
				fields["obs_state"] = "unknown"
				fields["obs_error_class"] = "route_changed"
				fields["next_check_at"] = now
				fields["observation_seq"] = gorm.Expr("observation_seq + 1")
				if err := tx.Model(&d.Route{}).Where("target_id = ? AND route_version = ?", v.ID, old.RouteVersion).Update("retired_at", now).Error; err != nil {
					return err
				}
			}
			r := tx.Model(&d.Target{}).Where("id = ? AND version = ? AND route_version = ? AND observation_seq = ? AND recovery_task_id = 0", v.ID, version, route, old.ObservationSeq).Updates(fields)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return c.ErrConflict
			}
			v.Version = version + 1
			if !changed {
				return nil
			}
		}
		return tx.Create(&d.Route{TargetID: v.ID, RouteVersion: v.RouteVersion, BotInstanceID: v.BotInstanceID, TelegramChatID: v.TelegramChatID, EffectiveAt: now}).Error
	}))
}
func (s *Store) History(ctx context.Context, id uint) ([]d.Route, []d.Operation, error) {
	var routes []d.Route
	var ops []d.Operation
	err := s.db.WithContext(ctx).Where("target_id = ?", id).Order("route_version DESC").Find(&routes).Error
	if err == nil {
		err = s.db.WithContext(ctx).Where("target_id = ?", id).Order("id DESC").Limit(20).Find(&ops).Error
	}
	return routes, ops, err
}
func (s *Store) BeginCheck(ctx context.Context, id uint, route uint64, source string, actor uint, now time.Time) (*d.Target, *d.Operation, error) {
	var target d.Target
	var op d.Operation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&target, id).Error; err != nil {
			return err
		}
		if target.RouteVersion != route {
			return c.ErrConflict
		}
		if target.Observation.CheckedAt != nil && now.Before(target.NextCheckAt) && (target.Observation.State == "checking" || now.Sub(*target.Observation.CheckedAt) < 15*time.Second || target.Observation.ErrorClass == "rate_limited") {
			return c.ErrBusy
		}
		seq := target.ObservationSeq + 1
		r := tx.Model(&d.Target{}).Where("id = ? AND route_version = ? AND observation_seq = ?", id, route, target.ObservationSeq).Updates(map[string]any{"observation_seq": seq, "obs_state": "checking", "obs_checked_at": now, "next_check_at": now.Add(30 * time.Second)})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return c.ErrBusy
		}
		// An abandoned read-only check is not a success; restart safely issues a new read.
		if err := tx.Model(&d.Operation{}).Where("target_id = ? AND state = ?", id, "checking").Updates(map[string]any{"state": "superseded", "error_class": "interrupted", "finished_at": now}).Error; err != nil {
			return err
		}
		target.ObservationSeq = seq
		op = d.Operation{TargetID: id, RouteVersion: route, Sequence: seq, ActorAdminID: actor, Source: source, State: "checking", StartedAt: now}
		return tx.Create(&op).Error
	})
	return &target, &op, result(err)
}
func (s *Store) FinishCheck(ctx context.Context, op *d.Operation, obs d.Observation, next time.Time) (bool, error) {
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var target d.Target
		if err := tx.First(&target, op.TargetID).Error; err != nil {
			return err
		}
		if obs.LastSuccessAt == nil {
			obs.LastSuccessAt = target.Observation.LastSuccessAt
		}
		if obs.LastFailureAt == nil {
			obs.LastFailureAt = target.Observation.LastFailureAt
		}
		if target.RouteVersion == op.RouteVersion && target.ObservationSeq == op.Sequence {
			target.Observation = obs
			target.NextCheckAt = next
			r := tx.Model(&d.Target{}).Where("id = ? AND route_version = ? AND observation_seq = ?", op.TargetID, op.RouteVersion, op.Sequence).Select("obs_route_version", "obs_sequence", "obs_bot_telegram_id", "obs_generation", "obs_chat_type", "obs_title", "obs_is_forum", "obs_is_member", "obs_member_status", "obs_can_manage_topics", "obs_can_invite_users", "obs_can_restrict_members", "obs_state", "obs_error_class", "obs_source", "obs_checked_at", "obs_observed_at", "obs_last_success_at", "obs_last_failure_at", "obs_retry_after_seconds", "next_check_at").Updates(&target)
			if r.Error != nil {
				return r.Error
			}
			applied = r.RowsAffected == 1
		}
		state := obs.State
		if !applied {
			state = "superseded"
		}
		now := time.Now().UTC()
		return tx.Model(&d.Operation{}).Where("id = ?", op.ID).Updates(map[string]any{"state": state, "error_class": obs.ErrorClass, "finished_at": now}).Error
	})
	return applied, result(err)
}
func (s *Store) Due(ctx context.Context, bot uint, generation uint64, now time.Time, limit int) ([]d.Target, error) {
	var rows []d.Target
	err := s.db.WithContext(ctx).Where("bot_instance_id = ? AND (next_check_at <= ? OR (obs_generation <> ? AND obs_state <> ? AND obs_error_class = ?))", bot, now, generation, "checking", "").Order("next_check_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}
func (s *Store) Invalidate(ctx context.Context, bot uint, chat int64, now time.Time) error {
	q := s.db.WithContext(ctx).Model(&d.Target{}).Where("bot_instance_id = ?", bot)
	if chat != 0 {
		q = q.Where("telegram_chat_id = ?", chat)
	}
	return q.Updates(map[string]any{"observation_seq": gorm.Expr("observation_seq + 1"), "obs_state": "unknown", "obs_error_class": "event_pending", "next_check_at": now}).Error
}
func (s *Store) Modules(ctx context.Context, bot uint) ([]c.ModuleView, error) {
	var rows []c.ModuleView
	// Return an effective enable flag in the existing DTO. Preserve the desired
	// configuration and CAS version in storage, but never activate a stale target
	// reference after its purpose/instance changes. One SELECT uses one snapshot.
	err := s.db.WithContext(ctx).Table("telegram_bot_module_configs AS m").
		Select(`m.id, m.bot_instance_id, m.partition, m.version, m.target_id, m.updated_by, m.updated_at,
		 m.enabled AS configured_enabled,
		 CASE WHEN m.enabled = ? AND t.enabled = ? AND t.bot_instance_id = m.bot_instance_id
		 AND t.purpose = m.partition AND t.recovery_task_id = 0 AND t.obs_state = ? AND t.obs_error_class = ?
		 AND t.obs_route_version = t.route_version AND t.obs_observed_at > ?
		 THEN ? ELSE ? END AS enabled`, true, true, "ready", "", time.Now().UTC().Add(-5*time.Minute), true, false).
		Joins("LEFT JOIN telegram_group_targets AS t ON t.id = m.target_id").
		Where("m.bot_instance_id = ?", bot).Order("m.partition ASC").Scan(&rows).Error
	for i := range rows {
		rows[i].EffectiveEnabled = rows[i].Enabled
	}
	return rows, err
}
func (s *Store) SaveModule(ctx context.Context, v *d.ModuleConfig, version uint64) error {
	return result(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.guardBotRecovery(tx, v.BotInstanceID); err != nil {
			return err
		}
		if err := guardRecoveryModule(tx, v); err != nil {
			return err
		}
		if v.Enabled {
			var target d.Target
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, v.TargetID).Error; err != nil {
				return err
			}
			if target.BotInstanceID != v.BotInstanceID || target.Purpose != v.Partition || !target.Enabled || target.Observation.State != "ready" || target.Observation.RouteVersion != target.RouteVersion || target.Observation.ObservedAt == nil || time.Since(*target.Observation.ObservedAt) > 5*time.Minute {
				return c.ErrCapability
			}
		}
		v.Version = version + 1
		if version == 0 {
			r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(v)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return c.ErrConflict
			}
			return nil
		}
		r := tx.Model(&d.ModuleConfig{}).Where("bot_instance_id = ? AND partition = ? AND version = ?", v.BotInstanceID, v.Partition, version).Updates(map[string]any{"enabled": v.Enabled, "target_id": v.TargetID, "version": v.Version, "updated_by": v.UpdatedBy})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	}))
}
