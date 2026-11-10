package domain

import "time"

// TopicOperation owns one immutable bot/session/route creation slot. A timed-out
// creating operation becomes unknown; only an audited CAS may resolve it.
type TopicOperation struct {
	// Retired permanently fences disaster-isolated slots, preserving original outcomes.
	Retired      bool   `gorm:"not null;default:false"`
	ID           uint   `gorm:"primarykey"`
	BotID        uint   `gorm:"not null;uniqueIndex:idx_support_topic_slot"`
	SessionID    uint   `gorm:"not null;uniqueIndex:idx_support_topic_slot"`
	TargetID     uint   `gorm:"not null;uniqueIndex:idx_support_topic_slot"`
	RouteVersion uint64 `gorm:"not null;uniqueIndex:idx_support_topic_slot"`
	SlotVersion  uint64 `gorm:"not null;default:0;uniqueIndex:idx_support_topic_slot"`
	// RetryNotBefore survives restarts and fences all topic calls for this bot.
	RetryNotBefore    *time.Time
	RetryAfterSeconds int64 `gorm:"not null;default:0"`
	ChatID            int64
	State             string `gorm:"size:24;index"`
	Action            string `gorm:"size:24"`
	Version           uint64
	Generation        uint64
	Claim             string `gorm:"size:64"`
	LeaseUntil        time.Time
	CreatedAt         time.Time
	// UpdatedAt is the trusted operation clock, also the service-event barrier.
	// Disable GORM's wall-clock replacement so delayed receipts cannot erase it.
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
}

func (TopicOperation) TableName() string { return "telegram_support_topic_operations" }

// TopicMapping permanently owns a physical topic, including after deletion or
// route retirement. No later consultation may inherit an old native reply.
type TopicMapping struct {
	// LastEventUpdateID is the durable per-topic service-event ordering watermark.
	LastEventUpdateID int64 `gorm:"not null;default:0"`
	ID                uint  `gorm:"primarykey"`
	OperationID       uint  `gorm:"not null;uniqueIndex"`
	BotID             uint  `gorm:"not null;uniqueIndex:idx_support_physical_topic"`
	ChatID            int64 `gorm:"not null;uniqueIndex:idx_support_physical_topic"`
	ThreadID          int64 `gorm:"not null;uniqueIndex:idx_support_physical_topic"`
	SessionID         uint  `gorm:"index;not null"`
	TargetID          uint
	RouteVersion      uint64
	State             string `gorm:"size:24"`
	Version           uint64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (TopicMapping) TableName() string { return "telegram_support_topic_mappings" }

// ReadyDispatch is business preparation metadata, NOT another delivery queue.
// Runtime owns the only attempts, retries and transport statuses. Its stable key
// is ReadyBusinessRef; the encrypted exact envelope prevents body substitution.
type ReadyDispatch struct {
	ID            uint `gorm:"primarykey"`
	MessageID     uint `gorm:"not null;uniqueIndex"`
	BotID         uint `gorm:"index;not null"`
	TopicID       uint
	PayloadCipher string `gorm:"type:text"`
	Version       uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (ReadyDispatch) TableName() string { return "telegram_support_ready_dispatches" }

// ReplyMapping is registered only from a verified sent runtime delivery. It is
// retained with the historical route and never inferred from message text.
type ReplyMapping struct {
	ID                uint  `gorm:"primarykey"`
	BotID             uint  `gorm:"not null;uniqueIndex:idx_support_reply_source"`
	ChatID            int64 `gorm:"not null;uniqueIndex:idx_support_reply_source"`
	MessageID         int64 `gorm:"not null;uniqueIndex:idx_support_reply_source"`
	ThreadID          int64
	TopicID           uint
	SessionID         uint
	BufferedMessageID uint `gorm:"not null;uniqueIndex"`
	DeliveryID        uint `gorm:"not null;index"`
	UserID            int64
	RouteVersion      uint64
	CreatedAt         time.Time
}

func (ReplyMapping) TableName() string { return "telegram_support_reply_mappings" }

// ResolutionAudit records who resolved which CAS revision and why, never raw
// message contents. Actor authorization belongs to the trusted application port.
type ResolutionAudit struct {
	ID         uint   `gorm:"primarykey"`
	BotID      uint   `gorm:"index"`
	Resource   string `gorm:"size:24"`
	ResourceID uint
	Version    uint64
	ActorID    uint
	Decision   string `gorm:"size:32"`
	Evidence   string `gorm:"size:512"`
	CreatedAt  time.Time
}

func (ResolutionAudit) TableName() string { return "telegram_support_resolution_audits" }
