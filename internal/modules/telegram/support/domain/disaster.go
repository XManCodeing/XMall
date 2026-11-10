package domain

import "time"

// DisasterCheckpoint is an immutable-source local isolation plan with durable
// per-family high-water marks and CAS progress. It contains no transport payload,
// receipt, user identity or reachability. Register with the migration owner.
type DisasterCheckpoint struct {
	ID                                                                                 uint   `gorm:"primarykey"`
	Key                                                                                string `gorm:"size:64;uniqueIndex;not null"`
	SourceBotID                                                                        uint   `gorm:"uniqueIndex;not null"`
	DestinationBotID                                                                   uint
	SourceGeneration                                                                   uint64
	ActorID                                                                            uint
	Evidence                                                                           string `gorm:"size:128"`
	Revision                                                                           uint64
	Phase                                                                              string `gorm:"size:24"`
	Cursor                                                                             uint
	TopicBound, OperationBound, SessionBound, MessageBound, ReplyBound, ChallengeBound uint
	RetiredTopics, ClosedSessions, CancelledMessages                                   uint64
	RetainedMessages, RetainedReplies, UncertainTopics                                 uint64
	CreatedAt, UpdatedAt                                                               time.Time
}

// TableName returns the support-owned local recovery checkpoint table.
func (DisasterCheckpoint) TableName() string { return "telegram_support_disaster_checkpoints" }

// RestorationCheckpoint is a task-owned bounded historical topic rebuild. Route
// snapshots are immutable; no customer payload or Telegram receipts are copied.
type RestorationCheckpoint struct {
	TaskID                                                             uint `gorm:"primaryKey;autoIncrement:false"`
	ActorID                                                            uint
	SourceBotID, DestinationBotID                                      uint
	TargetID                                                           uint `gorm:"primaryKey;autoIncrement:false"`
	SourceVersion, SourceGeneration, SourceRouteVersion                uint64
	DestinationVersion, DestinationGeneration, DestinationRouteVersion uint64
	SourceChatID, DestinationChatID                                    int64
	Evidence                                                           string `gorm:"size:128"`
	Revision                                                           uint64
	Cursor, UpperBound                                                 uint
	DownstreamReceiptCursor                                            uint
	Scanned                                                            bool
	CreatedAt, UpdatedAt                                               time.Time
}

// TableName names the task checkpoint registered by the migration owner.
func (RestorationCheckpoint) TableName() string { return "telegram_support_restorations" }

// RestorationEntry keeps source ownership and links a newly created destination
// topic only after a proven result. Legacy counters never imply safe redelivery.
type RestorationEntry struct {
	ID                                     uint `gorm:"primaryKey"`
	TaskID                                 uint `gorm:"uniqueIndex:idx_support_restoration_session;not null"`
	TargetID                               uint `gorm:"uniqueIndex:idx_support_restoration_session;not null"`
	SourceSessionID                        uint `gorm:"uniqueIndex:idx_support_restoration_session;not null"`
	DestinationSessionID                   uint
	OperationID                            uint
	State                                  string `gorm:"size:32;index"`
	Legacy                                 uint64
	DownstreamTrackingVersion              uint64 `gorm:"not null;default:0"`
	DownstreamUpperBound, DownstreamCursor uint
	DownstreamScanned                      bool `gorm:"not null;default:false"`
	CreatedAt, UpdatedAt                   time.Time
}

// TableName names the per-session recovery evidence table.
func (RestorationEntry) TableName() string { return "telegram_support_restoration_entries" }

// RestorationDownstreamLiability records exactly one counted original reply.
// Version protects consumption; source fields never change after registration.
type RestorationDownstreamLiability struct {
	ID                                    uint `gorm:"primaryKey;index:idx_support_downstream_liability_page,priority:4"`
	TaskID                                uint `gorm:"not null;uniqueIndex:idx_support_downstream_liability_source,priority:1;index:idx_support_downstream_liability_page,priority:1"`
	TargetID                              uint `gorm:"not null;uniqueIndex:idx_support_downstream_liability_source,priority:2;index:idx_support_downstream_liability_page,priority:2"`
	EntryID, SourceBotID, SourceSessionID uint
	DownstreamID                          uint `gorm:"not null;uniqueIndex:idx_support_downstream_liability_source,priority:3"`
	SourceVersion                         uint64
	SourceState                           string `gorm:"size:24"`
	TopicID                               uint
	RouteVersion                          uint64
	AlbumID                               uint
	State                                 string `gorm:"size:24;not null;index:idx_support_downstream_liability_page,priority:3"`
	Version                               uint64
	DeliveryID                            uint
	ConsumedBusinessVersion               uint64
	ConsumedAt                            *time.Time
	CreatedAt, UpdatedAt                  time.Time
}

// TableName returns the task-owned counted downstream receipt ledger.
func (RestorationDownstreamLiability) TableName() string {
	return "telegram_support_restoration_downstreams"
}
