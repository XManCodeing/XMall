package domain

import "time"

// Downstream is a persisted business reply or explicitly marked internal note.
// Runtime remains the only external-delivery queue; replay uses the stable ID.
type Downstream struct {
	ID                   uint  `gorm:"primarykey"`
	BotID                uint  `gorm:"not null;uniqueIndex:idx_support_downstream_source"`
	UpdateID             int64 `gorm:"not null;uniqueIndex:idx_support_downstream_source"`
	SessionID            uint  `gorm:"index;not null"`
	TopicID              uint
	ReplyMappingID       uint
	ActorID              int64
	SourceChatID         int64
	SourceMessageID      int64
	ThreadID             int64
	RouteVersion         uint64
	UserID               int64
	ChatID               int64
	Kind                 string `gorm:"size:24"`
	State                string `gorm:"size:24;index"`
	SourceCipher         string `gorm:"type:text"`
	EnvelopeCipher       string `gorm:"type:text"`
	DeliveryID           uint
	Version              uint64
	AlbumID              uint `gorm:"index"`
	SourceBytes          int64
	DestinationMessageID int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (Downstream) TableName() string { return "telegram_support_downstreams" }

// DownstreamAlbum is bounded source aggregation metadata, never a send queue.
// One immutable leader owns the existing runtime delivery key. Late arrivals
// cannot reopen or extend a closed window, including after process restart.
type DownstreamAlbum struct {
	ID           uint   `gorm:"primarykey"`
	BotID        uint   `gorm:"not null;uniqueIndex:idx_support_down_album_source"`
	SourceChatID int64  `gorm:"not null;uniqueIndex:idx_support_down_album_source"`
	MediaGroupID string `gorm:"size:128;not null;uniqueIndex:idx_support_down_album_source"`
	LeaderID     uint   `gorm:"uniqueIndex;not null"`
	SessionID    uint
	TopicID      uint
	ActorID      int64
	RouteVersion uint64
	Count        int
	Bytes        int64
	State        string `gorm:"size:24"`
	ClosesAt     time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (DownstreamAlbum) TableName() string { return "telegram_support_downstream_albums" }

// SupportBlock is scoped to this bot's consultation only. It never authorizes
// changes to shopping, identity, membership, payment or refund permissions.
type SupportBlock struct {
	ID        uint  `gorm:"primarykey"`
	BotID     uint  `gorm:"not null;uniqueIndex:idx_support_block_user"`
	UserID    int64 `gorm:"not null;uniqueIndex:idx_support_block_user"`
	Enabled   bool
	ExpiresAt *time.Time
	Version   uint64
	ActorID   int64
	Evidence  string `gorm:"size:512"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (SupportBlock) TableName() string { return "telegram_support_blocks" }
