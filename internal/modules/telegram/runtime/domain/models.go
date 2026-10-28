package domain

import "time"

// BotOwnership records immutable historical channel/bot relationships. TelegramID
// zero is an upgrade snapshot, never a claim that a channel has no history.
type BotOwnership struct {
	LockVersion    uint64
	AttestedAt     *time.Time
	RepairRevision uint64
	ID             uint   `gorm:"primaryKey"`
	ChannelID      uint   `gorm:"uniqueIndex:idx_bot_ownership"`
	TelegramID     int64  `gorm:"uniqueIndex:idx_bot_ownership"`
	State          string `gorm:"size:24"`
	SnapshotCipher string `gorm:"type:text" json:"-"`
}

func (BotOwnership) TableName() string { return "telegram_bot_ownership" }

// Instance is an explicitly managed shopping bot. Secrets never enter API DTOs.
type Instance struct {
	ID               uint       `gorm:"primarykey" json:"id"`
	TelegramID       int64      `gorm:"uniqueIndex;not null" json:"telegram_id"`
	Username         string     `gorm:"size:128" json:"username"`
	TokenCipher      string     `gorm:"type:text;not null" json:"-"`
	ChannelClientID  *uint      `gorm:"uniqueIndex" json:"channel_client_id,omitempty"`
	CutoverCompleted bool       `json:"-"`
	Enabled          bool       `json:"enabled"`
	Mode             string     `gorm:"size:24;not null;default:'internal'" json:"mode"`
	Version          uint64     `gorm:"not null;default:1" json:"version"`
	AppliedVersion   uint64     `json:"applied_version"`
	Generation       uint64     `gorm:"not null;default:0" json:"-"`
	Offset           int64      `json:"-"`
	State            string     `gorm:"size:32;not null;default:'stopped'" json:"state"`
	LastError        string     `gorm:"size:300" json:"last_error"`
	LastSeenAt       *time.Time `json:"last_seen_at"`
	LastReceivedAt   *time.Time `json:"last_received_at"`
	LastSentAt       *time.Time `json:"last_sent_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (Instance) TableName() string { return "telegram_bot_instances" }

type Contact struct {
	ID             uint   `gorm:"primarykey"`
	BotID          uint   `gorm:"uniqueIndex:idx_bot_contact;not null"`
	TelegramUserID int64  `gorm:"uniqueIndex:idx_bot_contact;not null"`
	ChatID         int64  `gorm:"not null"`
	UserID         uint   `gorm:"index"`
	Reachable      bool   `gorm:"not null"`
	Locale         string `gorm:"size:16"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Contact) TableName() string { return "telegram_bot_contacts" }

type Inbox struct {
	ID            uint   `gorm:"primarykey"`
	BotID         uint   `gorm:"uniqueIndex:idx_bot_update;not null;index"`
	UpdateID      int64  `gorm:"uniqueIndex:idx_bot_update;not null"`
	PayloadCipher string `gorm:"type:text;not null"`
	Status        string `gorm:"size:24;index;not null"`
	Attempts      int
	AvailableAt   time.Time `gorm:"index"`
	ClaimedUntil  *time.Time
	Claim         string `gorm:"size:64"`
	LastError     string `gorm:"size:300"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Inbox) TableName() string { return "telegram_bot_inbox" }

type Session struct {
	ID             uint   `gorm:"primarykey"`
	BotID          uint   `gorm:"uniqueIndex:idx_bot_session;not null"`
	TelegramUserID int64  `gorm:"uniqueIndex:idx_bot_session;not null"`
	ChatID         int64  `gorm:"uniqueIndex:idx_bot_session;not null"`
	Version        uint64 `gorm:"not null"`
	State          string `gorm:"size:64"`
	DataCipher     string `gorm:"type:text"`
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Session) TableName() string { return "telegram_bot_sessions" }

// Delivery stores a message request, not a second business ledger.
type Delivery struct {
	ID             uint       `gorm:"primarykey" json:"id"`
	BotID          uint       `gorm:"uniqueIndex:idx_bot_delivery_key;not null;index" json:"bot_id"`
	Key            string     `gorm:"size:191;uniqueIndex:idx_bot_delivery_key;not null" json:"key"`
	ChatID         int64      `gorm:"index" json:"chat_id"`
	TelegramUserID int64      `json:"-"`
	UserID         uint       `json:"-"`
	BroadcastID    uint       `gorm:"index" json:"broadcast_id,omitempty"`
	PayloadCipher  string     `gorm:"type:text" json:"-"`
	Priority       int        `gorm:"index" json:"priority"`
	Status         string     `gorm:"size:24;index" json:"status"`
	Attempts       int        `json:"attempts"`
	AvailableAt    time.Time  `gorm:"index" json:"available_at"`
	ClaimedUntil   *time.Time `json:"-"`
	Claim          string     `gorm:"size:64" json:"-"`
	MessageID      int64      `json:"message_id,omitempty"`
	LastError      string     `gorm:"size:300" json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (Delivery) TableName() string { return "telegram_bot_deliveries" }
