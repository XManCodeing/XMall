package domain

import "time"

// GenerationExit records a generation admitted by this runtime protocol. An
// absent or unfinished record is never evidence that external requests drained.
// Records must not be pruned while a recovery fence may need their proof.
type GenerationExit struct {
	BotID      uint   `gorm:"primaryKey;autoIncrement:false"`
	Generation uint64 `gorm:"primaryKey;autoIncrement:false"`
	Version    uint64 `gorm:"not null"`
	JoinedAt   *time.Time
}

// TableName returns the durable runtime exit ledger table.
func (GenerationExit) TableName() string { return "telegram_bot_generation_exits" }
