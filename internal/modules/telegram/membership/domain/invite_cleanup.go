package domain

import "time"

// InviteCleanupCycle 是最终权益失效后的持久清理周期，不复用申请协调父操作字段。
// EntitlementVersion 区分续费后的新周期；所有历史路由邀请都归入相同身份范围。
type InviteCleanupCycle struct {
	ID                 uint      `gorm:"primaryKey"`
	TargetID           uint      `gorm:"not null;uniqueIndex:uidx_membership_cleanup_scope,priority:1"`
	TelegramID         int64     `gorm:"not null;uniqueIndex:uidx_membership_cleanup_scope,priority:2"`
	EntitlementVersion uint64    `gorm:"not null;uniqueIndex:uidx_membership_cleanup_scope,priority:3"`
	State              string    `gorm:"size:24;not null"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// TableName 返回清理周期表名，无参数。
func (InviteCleanupCycle) TableName() string { return "telegram_membership_invite_cleanup_cycles" }

// InviteCleanupItem 将周期逐项关联到邀请及既有撤销操作；未知 URL 留下不可消失的责任。
type InviteCleanupItem struct {
	CycleID     uint      `gorm:"primaryKey;autoIncrement:false"`
	InviteID    uint      `gorm:"primaryKey;autoIncrement:false"`
	OperationID uint      `gorm:"not null;index"`
	State       string    `gorm:"size:24;not null"`
	Reason      string    `gorm:"size:48;not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

// TableName 返回清理逐邀请关联表名，无参数。
func (InviteCleanupItem) TableName() string { return "telegram_membership_invite_cleanup_items" }
