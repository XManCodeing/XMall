package domain

import "time"

// LifecycleCursor persists a fair bounded scan across known historical member identities.
// Cursor advancement and task creation commit together; no paid-order time window is used.
type LifecycleCursor struct {
	BotID      uint      `gorm:"primaryKey;autoIncrement:false"`
	TargetID   uint      `gorm:"not null"`
	ChatID     int64     `gorm:"not null"`
	TelegramID int64     `gorm:"not null"`
	Version    uint64    `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

// TableName returns the scan checkpoint table, without arguments or side effects.
func (LifecycleCursor) TableName() string { return "telegram_membership_lifecycle_cursors" }
