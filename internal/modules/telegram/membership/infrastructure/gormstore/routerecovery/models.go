// Package routerecovery persists fenced membership route evidence without platform calls.
package routerecovery

import "time"

// Run binds an idempotency key permanently to one physical replacement.
// A new scan refreshes evidence but never changes the immutable route identity.
type Run struct {
	TaskID                       uint `gorm:"primaryKey;autoIncrement:false"`
	TargetID                     uint `gorm:"primaryKey;autoIncrement:false"`
	OldBotID, NewBotID           uint
	OldVersion, NewVersion       uint64
	OldGeneration, NewGeneration uint64
	CompletionReceipt            string `gorm:"size:128"`
	BotID                        uint
	OldChatID                    int64
	NewChatID                    int64
	OldRouteVersion              uint64
	NewRouteVersion              uint64
	Generation                   uint64
	Version                      uint64
	Verified                     bool
	Remaining                    uint64
	Unknown                      uint64
	ScannedAt                    time.Time
}

// TableName returns the durable run table; it has no parameters or side effects.
func (Run) TableName() string { return "telegram_membership_route_recovery_target_runs" }

// Evidence retains each enumerated liability or immutable source identity, including
// resolved rows. Keys are local references only; no invite secret or platform payload is copied.
type Evidence struct {
	TaskID             uint   `gorm:"primaryKey;autoIncrement:false"`
	Kind               string `gorm:"primaryKey;size:24"`
	Reference          string `gorm:"primaryKey;size:128"`
	State              string `gorm:"size:32"`
	TargetID           uint   `gorm:"primaryKey;autoIncrement:false"`
	SourceID           uint
	OrderID            uint
	OrderItemID        uint
	TelegramID         int64
	EntitlementVersion uint64
	TermKind           string
	ExpiresAt          *time.Time
	ScannedAt          time.Time
}

// TableName returns the evidence table; it has no parameters or side effects.
func (Evidence) TableName() string { return "telegram_membership_route_recovery_target_evidence" }

// Models returns models for bootstrap registration; this function never migrates tables.
func Models() []any { return []any{&Run{}, &Evidence{}, &Action{}, &ModuleReceipt{}} }

// ModuleReceipt binds the complete current/history inventory to a durable module proof.
type ModuleReceipt struct {
	TaskID             uint   `gorm:"primaryKey;autoIncrement:false"`
	InventoryHash      string `gorm:"size:64;not null"`
	Receipt            string `gorm:"size:128"`
	Complete           bool
	Remaining, Unknown uint64
	CheckedAt          time.Time
}

// TableName returns the module receipt table, without side effects.
func (ModuleReceipt) TableName() string { return "telegram_membership_bot_recovery_receipts" }

// Action persists one task-owned platform attempt; uncertain mutations are never replayed.
// Query attempts may be repeated because they cannot create admission or membership.
type Action struct {
	TaskID      uint   `gorm:"primaryKey;autoIncrement:false"`
	TargetID    uint   `gorm:"primaryKey;autoIncrement:false"`
	Kind        string `gorm:"primaryKey;size:24"`
	Reference   string `gorm:"primaryKey;size:128"`
	State       string `gorm:"size:24"`
	Version     uint64
	AttemptedAt time.Time
	ErrorClass  string `gorm:"size:48"`
}

// TableName returns the recovery action table without performing migration.
func (Action) TableName() string { return "telegram_membership_route_recovery_actions" }
