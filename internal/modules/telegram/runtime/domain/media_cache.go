package domain

import "time"

// MediaCache is scoped to a bot: Telegram file IDs cannot be shared across bots.
type MediaCache struct {
	ID          uint   `gorm:"primarykey"`
	BotID       uint   `gorm:"uniqueIndex:idx_bot_media;not null"`
	PathHash    string `gorm:"size:64;uniqueIndex:idx_bot_media;not null"`
	ContentHash string `gorm:"size:64;uniqueIndex:idx_bot_media;not null"`
	FileID      string `gorm:"type:text;not null"`
	UpdatedAt   time.Time
}

func (MediaCache) TableName() string { return "telegram_bot_media_cache" }
