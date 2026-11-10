package domain

import (
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
)

// Reminder records one target/cycle/version/tier notification, never an entitlement mutation.
// The stable key excludes delivery attempts, so restart and unknown outcomes cannot duplicate it.
type Reminder struct {
	ID                 uint      `gorm:"primaryKey"`
	BusinessKey        string    `gorm:"size:191;not null;uniqueIndex"`
	BotID              uint      `gorm:"not null;index:idx_membership_reminder_due,priority:1"`
	TargetID           uint      `gorm:"not null"`
	AccountID          uint      `gorm:"not null"`
	TelegramID         int64     `gorm:"not null"`
	ChatID             int64     `gorm:"not null"`
	BindingVersion     uint      `gorm:"not null"`
	EntitlementVersion uint64    `gorm:"not null"`
	RouteVersion       uint64    `gorm:"not null"`
	ExpiresAt          time.Time `gorm:"not null"`
	Days               int       `gorm:"not null"`
	State              string    `gorm:"size:24;not null;index:idx_membership_reminder_due,priority:2"`
	NextAttemptAt      time.Time `gorm:"not null;index:idx_membership_reminder_due,priority:3"`
	MessageID          int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName returns the durable reminder table.
func (Reminder) TableName() string { return "telegram_membership_reminders" }

// ReminderCursor bounds projection scans independently of member removal scans.
type ReminderCursor struct {
	BotID      uint `gorm:"primaryKey;autoIncrement:false"`
	TargetID   uint
	TelegramID int64
	Version    uint64 `gorm:"not null"`
}

// TableName returns the fair per-bot scan checkpoint table.
func (ReminderCursor) TableName() string { return "telegram_membership_reminder_cursors" }

// ReminderAvailable shifts Shanghai quiet hours to the next 08:00 without changing expiry.
// Asia/Shanghai has UTC+08:00 for all supported contemporary membership cycles.
func ReminderAvailable(at time.Time) time.Time {
	local := at.In(constants.DisplayLocation())
	if local.Hour() >= 22 {
		local = local.AddDate(0, 0, 1)
		return time.Date(local.Year(), local.Month(), local.Day(), 8, 0, 0, 0, local.Location()).UTC()
	}
	if local.Hour() < 8 {
		return time.Date(local.Year(), local.Month(), local.Day(), 8, 0, 0, 0, local.Location()).UTC()
	}
	return at.UTC()
}
