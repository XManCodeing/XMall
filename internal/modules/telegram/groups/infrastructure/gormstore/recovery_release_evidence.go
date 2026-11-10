package gormstore

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// ReadRecoveryRelease validates the entire historical receipt against immutable
// request identity and the original CAS chain. It reads no mutable target/runtime
// state: newer observations/generations/tasks cannot change a completed release.
// Missing evidence fails closed; an unreleased exact receipt returns nil, nil.
func (s *Store) ReadRecoveryRelease(ctx context.Context, input c.RecoveryReleaseEvidenceRequest) (*c.RecoveryReleaseEvidence, error) {
	if !validRecovery(input.RecoveryInput) {
		return nil, c.ErrInvalid
	}
	var receipt d.RecoveryFence
	if err := s.db.WithContext(ctx).Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Take(&receipt).Error; err != nil {
		return nil, result(err)
	}
	if !sameRecovery(receipt, input.RecoveryInput) || receipt.InitialVersion != input.ExpectedVersion || receipt.InitialRouteVersion != input.ExpectedRouteVersion || receipt.FrozenVersion != input.ExpectedVersion+1 || receipt.ReplacedVersion != input.ExpectedVersion+2 {
		return nil, c.ErrConflict
	}
	if receipt.ReleasedVersion == 0 {
		return nil, nil
	}
	if receipt.ReleasedVersion != input.ExpectedVersion+3 || receipt.ReleasedObservationSeq == 0 || receipt.ReleasedBotTelegramID <= 0 || receipt.ReleasedGeneration != input.Generation {
		return nil, c.ErrConflict
	}
	return &c.RecoveryReleaseEvidence{ReleasedVersion: receipt.ReleasedVersion, ObservationSeq: receipt.ReleasedObservationSeq, Generation: receipt.ReleasedGeneration, BotTelegramID: receipt.ReleasedBotTelegramID}, nil
}
