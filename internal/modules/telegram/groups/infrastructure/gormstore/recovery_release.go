package gormstore

import (
	"context"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
)

// ReleaseRecovery clears only the owning task's fence after a fresh complete
// observation of its replacement route. Enabled intent is never changed. A
// durable release receipt makes an interrupted response safely replayable.
func (s *Store) ReleaseRecovery(ctx context.Context, in c.RecoveryRelease) (*d.Target, error) {
	if in.TargetID == 0 || in.TaskID == 0 || in.ExpectedVersion == 0 || in.ExpectedRouteVersion == 0 ||
		in.ExpectedObservationSeq == 0 || in.BotID == 0 || in.ChatID >= 0 || in.BotTelegramID <= 0 {
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
		if in.Generation == 0 && (receipt.OldBotID == receipt.NewBotID || target.Observation.Source != "recovery") {
			return c.ErrInvalid
		}
		if receipt.TargetID != in.TargetID || receipt.NewBotID != in.BotID || receipt.NewChatID != in.ChatID ||
			receipt.ReplacedVersion != in.ExpectedVersion || receipt.InitialRouteVersion+1 != in.ExpectedRouteVersion ||
			target.BotInstanceID != in.BotID || target.TelegramChatID != in.ChatID || target.RouteVersion != in.ExpectedRouteVersion {
			return c.ErrConflict
		}
		if receipt.ReleasedVersion != 0 {
			if target.RecoveryTaskID != 0 || target.Version != receipt.ReleasedVersion || receipt.ReleasedObservationSeq != in.ExpectedObservationSeq ||
				receipt.ReleasedGeneration != in.Generation || receipt.ReleasedBotTelegramID != in.BotTelegramID {
				return c.ErrConflict
			}
			return nil
		}
		if target.RecoveryTaskID != in.TaskID || target.Version != in.ExpectedVersion || target.ObservationSeq != in.ExpectedObservationSeq {
			return c.ErrConflict
		}
		if !recoveryReady(target, in, time.Now().UTC()) {
			return c.ErrCapability
		}
		if err = recoveryCAS(tx, target, map[string]any{"recovery_task_id": 0}); err != nil {
			return err
		}
		target.RecoveryTaskID, target.Version = 0, target.Version+1
		return tx.Model(&receipt).Updates(map[string]any{"released_version": target.Version, "released_observation_seq": in.ExpectedObservationSeq,
			"released_generation": in.Generation, "released_bot_telegram_id": in.BotTelegramID}).Error
	})
	if err != nil {
		return nil, result(err)
	}
	return &target, nil
}

// recoveryReady validates stored evidence against the caller's trusted runtime
// identity; it performs no network call and cannot accept old-route permissions.
func recoveryReady(target d.Target, in c.RecoveryRelease, now time.Time) bool {
	observation := target.Observation
	return (target.Purpose == "support" || target.Purpose == "membership") &&
		observation.State == "ready" && observation.ErrorClass == "" && observation.IsMember &&
		observation.RouteVersion == target.RouteVersion && observation.Sequence == target.ObservationSeq &&
		observation.Generation == in.Generation && observation.BotTelegramID == in.BotTelegramID &&
		observation.ObservedAt != nil && !observation.ObservedAt.After(now) && now.Sub(*observation.ObservedAt) < 5*time.Minute &&
		len(d.MissingCapabilities(target)) == 0
}
