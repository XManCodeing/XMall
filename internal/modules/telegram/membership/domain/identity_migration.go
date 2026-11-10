package domain

import "time"

// IdentityMigration records an administrator-reviewed, explicit per-target beneficiary mapping.
// Source identity and paid terms stay immutable; state advances only after actual membership.
// Both identities reserve this target permanently; chaining and merging require separate review.
type IdentityMigration struct {
	ID                 uint      `gorm:"primaryKey"`
	AccountID          uint      `gorm:"not null;index"`
	TargetID           uint      `gorm:"not null;uniqueIndex:uidx_membership_migration_old,priority:1;uniqueIndex:uidx_membership_migration_new,priority:1"`
	OldTelegramID      int64     `gorm:"not null;uniqueIndex:uidx_membership_migration_old,priority:2"`
	NewTelegramID      int64     `gorm:"not null;uniqueIndex:uidx_membership_migration_new,priority:2"`
	ActorAdminID       uint      `gorm:"not null"`
	BindingVersion     uint      `gorm:"not null"`
	BotID              uint      `gorm:"not null"`
	ChatID             int64     `gorm:"not null"`
	RouteVersion       uint64    `gorm:"not null"`
	GroupVersion       uint64    `gorm:"not null"`
	EntitlementVersion uint64    `gorm:"not null"`
	Version            uint64    `gorm:"not null"`
	State              string    `gorm:"size:32;not null"`
	CreatedAt          time.Time `gorm:"not null"`
	AdmittedAt         *time.Time
	CompletedAt        *time.Time
}

// TableName returns the explicit mapping table, without side effects.
func (IdentityMigration) TableName() string { return "telegram_membership_identity_migrations" }
