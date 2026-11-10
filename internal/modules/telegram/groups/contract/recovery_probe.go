package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"time"
)

// RecoveryProbeInput authorizes read-only platform checks under a drained, exact task fence.
// Generation may be zero for a never-started destination; no worker admission is performed.
type RecoveryProbeInput struct {
	TaskID, TargetID, BotID           uint
	Version, Generation, RouteVersion uint64
	TelegramID, ChatID                int64
}

// RecoveryProber never sends messages, creates topics, or temporarily releases runtime fences.
type RecoveryProber interface {
	ProbeRecovery(context.Context, RecoveryProbeInput) (d.Observation, error)
}

// RecoveryObservation binds real platform observations to the still-frozen route and owner.
type RecoveryObservation struct {
	RecoveryProbeInput
	ExpectedTargetVersion uint64
	Observation           d.Observation
	CheckedAt             time.Time
}
