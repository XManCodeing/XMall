package domain

import "time"

// BotPolicy persists only verification/buffer policy, never group binding or enablement.
// A row is created by CAS version zero; updates do not modify existing challenge/message snapshots.
type BotPolicy struct {
	BotID               uint   `gorm:"primaryKey;autoIncrement:false"`
	Version             uint64 `gorm:"not null"`
	MaxMessages         int    `gorm:"not null"`
	MaxBytes            int64  `gorm:"not null"`
	MaxAttempts         int    `gorm:"not null"`
	RetentionSeconds    int64  `gorm:"not null"`
	ChallengeTTLSeconds int64  `gorm:"not null"`
	CooldownSeconds     int64  `gorm:"not null"`
	UpdatedBy           uint   `gorm:"not null"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TableName returns the per-bot policy table registered by the migration owner.
func (BotPolicy) TableName() string { return "telegram_support_bot_policies" }

// Session owns a permanent bot/customer slot; lifecycle never implies delivery.
type Session struct {
	ID                  uint   `gorm:"primarykey"`
	BotID               uint   `gorm:"uniqueIndex:idx_support_customer;not null"`
	UserID              int64  `gorm:"uniqueIndex:idx_support_customer;not null"`
	ChatID              int64  `gorm:"not null"`
	DisplayName         string `gorm:"size:256"`
	Username            string `gorm:"size:64"`
	State               string `gorm:"size:32;not null"`
	Version             uint64 `gorm:"not null"`
	ChallengeID         uint
	Sequence            uint64
	ConsumerGeneration  uint64
	CooldownUntil       time.Time
	Attempts            int
	AttemptWindow       time.Time
	LastAttemptUpdateID int64  // legacy high-water mark; no longer used for deduplication
	Locale              string `gorm:"size:16"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (Session) TableName() string { return "telegram_support_sessions" }

type Challenge struct {
	ID            uint   `gorm:"primarykey"`
	Ref           string `gorm:"size:32;uniqueIndex;not null"`
	SessionID     uint   `gorm:"index;not null"`
	BotID         uint   `gorm:"index;not null"`
	UserID        int64  `gorm:"not null"`
	ChatID        int64  `gorm:"not null"`
	State         string `gorm:"size:24;not null"`
	Version       uint64 `gorm:"not null"`
	Attempts      int
	Question      string `gorm:"size:64"`
	Choices       string `gorm:"size:128"`
	AnswerIndex   int
	MaxAttempts   int
	ExpiresAt     time.Time `gorm:"index"`
	ConsumedAt    *time.Time
	NoticePending bool   `gorm:"index;not null;default:true"`
	Locale        string `gorm:"size:16"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Challenge) TableName() string { return "telegram_support_challenges" }

// BufferedMessage is a business recovery record, not a second transport inbox.
// Payload is an independent encrypted snapshot so inbox cleanup cannot lose it.
type BufferedMessage struct {
	LegacyTaskID    uint   `gorm:"index;not null;default:0"`
	ID              uint   `gorm:"primarykey"`
	BotID           uint   `gorm:"uniqueIndex:idx_support_source;not null"`
	UpdateID        int64  `gorm:"uniqueIndex:idx_support_source;not null"`
	SessionID       uint   `gorm:"index:idx_support_queue;not null"`
	Sequence        uint64 `gorm:"index:idx_support_queue;not null"`
	PayloadCipher   string `gorm:"type:text"`
	Bytes           int64
	State           string    `gorm:"size:24;index;not null"`
	Reason          string    `gorm:"size:32"`
	RetainUntil     time.Time `gorm:"index"`
	ReleasedAt      *time.Time
	UserID          int64
	ChatID          int64
	Locale          string `gorm:"size:16"`
	FeedbackPending bool   `gorm:"index;not null;default:false"`
	Claim           string `gorm:"size:64"`
	Generation      uint64
	LeaseUntil      *time.Time
	DeliveryID      uint
	TargetChatID    int64
	TargetThreadID  int64
	RouteVersion    uint64
	ErrorClass      string `gorm:"size:32"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (BufferedMessage) TableName() string { return "telegram_support_messages" }
