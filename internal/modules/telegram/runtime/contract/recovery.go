package contract

import (
	"context"
	"errors"
	"time"
)

// RecoveryFence identifies one immutable recovery owner and the captured old
// configuration/generation. TaskID is the recovery task primary key, not a token.
type RecoveryFence struct {
	BotID, TaskID       uint
	Version, Generation uint64
}

// RecoveryFenceStore is an optional fail-closed recovery capability. BlockRecovery
// persists ownership without changing Enabled, Version or Generation. Same-owner
// retries are idempotent; different owners or snapshots conflict. RecoveryDrained
// returns ErrRecoveryBlocked unless every admitted generation has explicitly
// joined. No lease expiry, heartbeat age or timeout constitutes drain proof.
// ReleaseRecovery requires the exact owner and proven drain; it does not enable
// a disabled bot. RecordGenerationExit is runtime-only, AFTER all children/HTTP
// calls join; callers must never manufacture it from an observed stopped state.
type RecoveryFenceStore interface {
	BlockRecovery(context.Context, RecoveryFence) error
	RecoveryDrained(context.Context, RecoveryFence) error
	ReleaseRecovery(context.Context, RecoveryFence) error
	CheckRecovery(context.Context, uint, uint64, uint64) error
	RecordGenerationExit(context.Context, uint, uint64, uint64) error
}

// ErrRecoveryBlocked means recovery cannot yet prove external work is drained.
var ErrRecoveryBlocked = errors.New("telegram recovery blocked: drain not proven")

// DeliveryRetry identifies an already proven failed immutable support delivery.
// All values come from trusted business storage, never from an HTTP receipt DTO.
// Callers must atomically commit business authorization and audit with the retry.
type DeliveryRetry struct {
	BotID, DeliveryID                       uint
	Generation                              uint64
	Key, TargetType, Purpose, PayloadCipher string
	ChatID, TelegramUserID                  int64
	// ReplacementPayloadCipher is set only after proven topic deletion; it
	// preserves the original consultation and changes its physical thread only.
	ReplacementPayloadCipher string
	Now                      time.Time
}

// DeliveryRecoveryStore requeues the SAME terminal failed row. Unknown, partial
// observations and active claims are rejected. A proven topic_missing rejection
// permits a transaction-authorized replacement envelope; all other retries retain it.
// No external API is called. Transaction-bound implementations use that connection.
type DeliveryRecoveryStore interface {
	RetryFailedDelivery(context.Context, DeliveryRetry) error
}
