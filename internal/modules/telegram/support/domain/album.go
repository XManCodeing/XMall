package domain

import "time"

// Album owns one immutable source aggregation window; late arrivals are rejected
// rather than silently appended to an already scheduled or delivered album.
type Album struct {
	ID           uint   `gorm:"primarykey"`
	BotID        uint   `gorm:"not null;uniqueIndex:idx_support_album_source"`
	ChatID       int64  `gorm:"not null;uniqueIndex:idx_support_album_source"`
	MediaGroupID string `gorm:"size:128;not null;uniqueIndex:idx_support_album_source"`
	SessionID    uint
	LeaderID     uint
	Count        int
	Bytes        int64
	State        string `gorm:"size:24"`
	ClosesAt     time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Album) TableName() string { return "telegram_support_albums" }

// AlbumItem links each buffered source to its ordered eventual destination ID.
// Only a complete runtime receipt can advance DestinationMessageID to trusted.
type AlbumItem struct {
	ID                   uint  `gorm:"primarykey"`
	AlbumID              uint  `gorm:"index;not null;uniqueIndex:idx_support_album_message"`
	BufferedMessageID    uint  `gorm:"not null;uniqueIndex"`
	SourceMessageID      int64 `gorm:"not null;uniqueIndex:idx_support_album_message"`
	DestinationMessageID int64
	CreatedAt            time.Time
}

func (AlbumItem) TableName() string { return "telegram_support_album_items" }
