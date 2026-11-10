package domain

import "time"

// LedgerSource 保存不可变购买贡献。业务唯一键不含受益身份，防止重放篡改归属。
// 没有软删除字段；撤权由独立墓碑表示，不删除原事实。
type LedgerSource struct {
	ID                    uint      `gorm:"primaryKey"`
	OrderItemID           uint      `gorm:"not null;uniqueIndex:uidx_membership_source,priority:1"`
	TargetID              uint      `gorm:"not null;uniqueIndex:uidx_membership_source,priority:2;index:idx_membership_source_scope,priority:2"`
	Operation             string    `gorm:"size:16;not null;uniqueIndex:uidx_membership_source,priority:3"`
	BeneficiaryTelegramID int64     `gorm:"not null;index:idx_membership_source_scope,priority:1"`
	AccountID             uint      `gorm:"not null"`
	OrderID               uint      `gorm:"not null;index"`
	PaymentOrderID        uint      `gorm:"not null"`
	PaymentID             uint      `gorm:"not null"`
	PaidAt                time.Time `gorm:"not null"`
	Kind                  string    `gorm:"size:16;not null"`
	Months                int64     `gorm:"not null"`
	SnapshotHash          string    `gorm:"size:64;not null"`
	CreatedAt             time.Time `gorm:"not null"`
}

// TableName 返回来源事实表名，无参数。
func (LedgerSource) TableName() string { return "telegram_membership_ledger_sources" }

// LedgerRevocation 永久保留明确撤权墓碑，允许早于来源到达；不承载退款金额政策。
type LedgerRevocation struct {
	OrderItemID           uint      `gorm:"primaryKey;autoIncrement:false"`
	TargetID              uint      `gorm:"primaryKey;autoIncrement:false;index:idx_membership_revocation_scope,priority:2"`
	BeneficiaryTelegramID int64     `gorm:"not null;index:idx_membership_revocation_scope,priority:1"`
	RevokedAt             time.Time `gorm:"not null"`
}

// TableName 返回撤权墓碑表名，无参数。
func (LedgerRevocation) TableName() string { return "telegram_membership_ledger_revocations" }

// LedgerProjection 是受益人/逻辑目标的串行化根及可重建资格，不代表实际已入群。
// Version 用于 CAS 和未来 B6 动作栅栏，永久有效时 ExpiresAt 必须为空。
type LedgerProjection struct {
	BeneficiaryTelegramID int64      `gorm:"primaryKey;autoIncrement:false"`
	TargetID              uint       `gorm:"primaryKey;autoIncrement:false"`
	Version               uint64     `gorm:"not null"`
	Active                bool       `gorm:"not null;default:false"`
	Kind                  string     `gorm:"size:16;not null"`
	ExpiresAt             *time.Time `gorm:"index"`
	LimitedExpiresAt      *time.Time
	CalculatedAt          time.Time `gorm:"not null"`
}

// TableName 返回资格投影表名，无参数。
func (LedgerProjection) TableName() string { return "telegram_membership_ledger_projections" }

// LedgerOutbox 持久恢复任务仅记录 pending，外部审批/入群结果须由 B6 实际执行后确认。
// Grant 使用订单项/目标/操作唯一键；撤权单独使用 revoke 操作，重放不重置任务状态。
type LedgerOutbox struct {
	ID                    uint      `gorm:"primaryKey"`
	OrderItemID           uint      `gorm:"not null;uniqueIndex:uidx_membership_outbox,priority:1"`
	TargetID              uint      `gorm:"not null;uniqueIndex:uidx_membership_outbox,priority:2"`
	Operation             string    `gorm:"size:16;not null;uniqueIndex:uidx_membership_outbox,priority:3"`
	BeneficiaryTelegramID int64     `gorm:"not null"`
	ProjectionVersion     uint64    `gorm:"not null"`
	State                 string    `gorm:"size:24;not null;index:idx_membership_outbox_pending,priority:1"`
	Attempts              uint      `gorm:"not null;default:0"`
	AvailableAt           time.Time `gorm:"not null;index:idx_membership_outbox_pending,priority:2"`
	CreatedAt             time.Time `gorm:"not null"`
	UpdatedAt             time.Time `gorm:"not null"`
}

// TableName 返回待恢复任务表名，无参数。
func (LedgerOutbox) TableName() string { return "telegram_membership_ledger_outbox" }
