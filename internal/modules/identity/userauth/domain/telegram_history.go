package userauthdomain

import "time"

const (
	TelegramHistoryActive  = "active"
	TelegramHistoryRevoked = "revoked"
)

// TelegramHistoryGrant permits read-only access to a placeholder account's
// history. It never transfers ownership, balances, or permission to transact.
// IdentityID anchors the grant to one binding lifetime; revoked rows are never
// reactivated, even if the same Telegram user later binds the same account.
type TelegramHistoryGrant struct {
	ID             uint   `gorm:"primarykey"`
	BotID          uint   `gorm:"not null;uniqueIndex:idx_telegram_history_binding"`
	TelegramUserID string `gorm:"type:varchar(255);not null;index"`
	PreviousUserID uint   `gorm:"not null;uniqueIndex:idx_telegram_history_binding"`
	CurrentUserID  uint   `gorm:"not null;index"`
	IdentityID     uint   `gorm:"not null;uniqueIndex:idx_telegram_history_binding"`
	Status         string `gorm:"type:varchar(16);not null;index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RevokedAt      *time.Time
}

func (TelegramHistoryGrant) TableName() string { return "telegram_history_grants" }
