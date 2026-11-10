package domain

import "time"

// ProactiveDelivery is a derived grant responsibility, not an entitlement or an invitation.
// SourceID is globally unique; immutable ownership is copied from the ledger, never a contact.
type ProactiveDelivery struct {
	ID             uint   `gorm:"primaryKey"`
	SourceID       uint   `gorm:"not null;uniqueIndex"`
	OutboxID       uint   `gorm:"not null"`
	AccountID      uint   `gorm:"not null"`
	TelegramID     int64  `gorm:"not null"`
	TargetID       uint   `gorm:"not null"`
	BotID          uint   `gorm:"not null;index:idx_membership_proactive_due,priority:1"`
	ChatID         int64  `gorm:"not null"`
	RouteVersion   uint64 `gorm:"not null"`
	BindingVersion uint   `gorm:"not null"`
	Generation     uint64 `gorm:"not null"`
	InviteID       uint   `gorm:"not null"`
	InviteState    string `gorm:"size:24;not null"`
	State          string `gorm:"size:24;not null;index:idx_membership_proactive_due,priority:2"`
	Stage          string `gorm:"size:24;not null"`
	BusinessKey    string `gorm:"size:160;not null"`
	MessageID      int64  `gorm:"not null"`
	ClaimOwner     string `gorm:"size:64;not null"`
	LeaseUntil     *time.Time
	AvailableAt    time.Time `gorm:"not null;index:idx_membership_proactive_due,priority:3"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

// TableName returns the separate durable delivery responsibility table.
func (ProactiveDelivery) TableName() string { return "telegram_membership_proactive_deliveries" }

// ProactiveCursor separates live grant consumption from minute-paced historical repair.
// Failed sources advance the checkpoint and are retried on the next history sweep.
type ProactiveCursor struct {
	BotID        uint      `gorm:"primaryKey;autoIncrement:false"`
	FastAfter    uint      `gorm:"not null"`
	HistoryAfter uint      `gorm:"not null"`
	HistoryAt    time.Time `gorm:"not null"`
}

// TableName returns the dedicated checkpoint table without sharing lifecycle cursors.
func (ProactiveCursor) TableName() string { return "telegram_membership_proactive_cursors" }
