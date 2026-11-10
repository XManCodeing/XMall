package domain

import "time"

// OperationReference is a short, actor-bound capability, not authorization by itself.
// Each execution must recheck live membership and its persisted session revision.
type OperationReference struct {
	ID             uint   `gorm:"primarykey"`
	Ref            string `gorm:"size:32;not null;uniqueIndex"`
	BotID          uint   `gorm:"index;not null"`
	SessionID      uint
	TopicID        uint
	ActorID        int64
	ChatID         int64
	ThreadID       int64
	RouteVersion   uint64
	Generation     uint64
	SessionVersion uint64
	Action         string `gorm:"size:24"`
	// ControlRef is set only for actor-independent verification cards.
	ControlRef string `gorm:"size:32;index"`
	BlockDays  int
	NoteCipher string `gorm:"type:text"`
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// TableName returns the durable operation reference table.
func (OperationReference) TableName() string { return "telegram_support_operation_references" }

// OperationControl is a durable actor-bound menu; its encrypted envelope is immutable.
// UpdateID deduplicates inbox retries without replacing the original callbacks.
type OperationControl struct {
	ID             uint   `gorm:"primarykey"`
	BotID          uint   `gorm:"uniqueIndex:idx_support_control_update;not null"`
	UpdateID       int64  `gorm:"uniqueIndex:idx_support_control_update;not null"`
	Ref            string `gorm:"size:32;uniqueIndex;not null"`
	ActorID        int64
	ChatID         int64
	ThreadID       int64
	TopicID        uint
	SessionID      uint
	SessionVersion uint64
	RouteVersion   uint64
	Generation     uint64
	PayloadCipher  string `gorm:"type:text"`
	// DailyKey is nil for manual menus, otherwise bot/session/Beijing-day unique.
	DailyKey   *string `gorm:"size:80;uniqueIndex"`
	ConsumedAt *time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// TableName returns the durable staff control table.
func (OperationControl) TableName() string { return "telegram_support_operation_controls" }

// OperationAudit is immutable Telegram-staff evidence, separate from admin identity.
// Notes are encrypted and are never included in delivery queues or ordinary logs.
type OperationAudit struct {
	ID             uint `gorm:"primarykey"`
	ReferenceID    uint `gorm:"not null;uniqueIndex"`
	BotID          uint `gorm:"index"`
	SessionID      uint `gorm:"index"`
	TopicID        uint
	ActorID        int64
	ChatID         int64
	ThreadID       int64
	RouteVersion   uint64
	Generation     uint64
	SessionVersion uint64
	Action         string `gorm:"size:24"`
	NoteCipher     string `gorm:"type:text"`
	CreatedAt      time.Time
}

// TableName returns the immutable staff operation journal table.
func (OperationAudit) TableName() string { return "telegram_support_operation_audits" }
