package domain

// RecoveryFence is a durable idempotency receipt, not a recovery task or lease.
// (TaskID, TargetID) is the parent/target identity. Immutable route identities bind retries to the same
// request; phase versions prevent a replay from acquiring a later task's fence.
type RecoveryFence struct {
	TaskID                 uint `gorm:"primaryKey;autoIncrement:false"`
	TargetID               uint `gorm:"primaryKey;autoIncrement:false;not null;index"`
	OldBotID               uint
	OldChatID              int64
	NewBotID               uint
	NewChatID              int64
	InitialVersion         uint64
	InitialRouteVersion    uint64
	FrozenVersion          uint64
	ReplacedVersion        uint64
	ReleasedVersion        uint64
	ReleasedObservationSeq uint64
	ReleasedGeneration     uint64
	ReleasedBotTelegramID  int64
}

// TableName returns the private recovery receipt table; bootstrap owns migration.
func (RecoveryFence) TableName() string { return "telegram_group_recovery_target_fences" }
