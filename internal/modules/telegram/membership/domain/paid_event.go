package domain

import "time"

// PaidEvent 是独立于 Telegram 通知收件人的持久权益 outbox。
// OrderID 指向可信付款父单（或独立单），消费者展开真实子订单项快照。
// PaidAt 一经写入不可改变，必须取可信 Payment.PaidAt；不使用消费时间。
type PaidEvent struct {
	ID          uint       `gorm:"primaryKey"`
	OrderID     uint       `gorm:"not null;uniqueIndex:uidx_membership_paid_order"`
	PaymentID   uint       `gorm:"not null;uniqueIndex:uidx_membership_paid_payment"`
	PaidAt      time.Time  `gorm:"not null"`
	AvailableAt time.Time  `gorm:"not null;index:idx_membership_paid_due,priority:2"`
	CompletedAt *time.Time `gorm:"index:idx_membership_paid_due,priority:1"`
	ClaimOwner  string     `gorm:"size:64;not null;default:''"`
	ClaimUntil  *time.Time
	Attempts    uint64 `gorm:"not null;default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回权益付款事件表名；无参数，无外部副作用。
func (PaidEvent) TableName() string { return "telegram_membership_paid_events" }
