package domain

import "time"

// ManualConfirmation 保存不可变的人工收款审计，不代表支付网关通知。一个支付尝试只允许一份成功审计。
type ManualConfirmation struct {
	ID                    uint      `gorm:"primarykey" json:"id"`
	PaymentID             uint      `gorm:"not null;uniqueIndex" json:"payment_id"`
	OrderID               uint      `gorm:"not null;index" json:"order_id"`
	AdminID               uint      `gorm:"not null;index" json:"admin_id"`
	RequestID             string    `gorm:"size:80;not null;uniqueIndex" json:"request_id"`
	RequestHash           string    `gorm:"size:64;not null" json:"-"`
	Amount                string    `gorm:"size:32;not null" json:"amount"`
	Currency              string    `gorm:"size:12;not null" json:"currency"`
	TransactionID         string    `gorm:"size:160" json:"transaction_id"`
	EvidenceReference     string    `gorm:"size:240" json:"evidence_reference"`
	Reason                string    `gorm:"size:1000;not null" json:"reason"`
	PreviousPaymentStatus string    `gorm:"size:32;not null" json:"previous_payment_status"`
	PreviousOrderStatus   string    `gorm:"size:32;not null" json:"previous_order_status"`
	PaymentStatus         string    `gorm:"size:32;not null" json:"payment_status"`
	OrderStatus           string    `gorm:"size:32;not null" json:"order_status"`
	PaidAt                time.Time `gorm:"not null" json:"paid_at"`
	CreatedAt             time.Time `gorm:"not null;index" json:"created_at"`
}

// TableName 返回持久人工收款审计表名。
func (ManualConfirmation) TableName() string { return "payment_manual_confirmations" }

// PaymentEvidenceClaim 将人工和网关交易凭据绑定到唯一支付尝试。ReferenceHash 不存原始敏感凭据。
type PaymentEvidenceClaim struct {
	ID            uint      `gorm:"primarykey"`
	ChannelID     uint      `gorm:"not null;uniqueIndex:idx_payment_evidence_scope"`
	ReferenceHash string    `gorm:"size:64;not null;uniqueIndex:idx_payment_evidence_scope"`
	PaymentID     uint      `gorm:"not null;index"`
	CreatedAt     time.Time `gorm:"not null"`
}

// TableName 返回跨方言非空唯一证据槽位表名。
func (PaymentEvidenceClaim) TableName() string { return "payment_evidence_claims" }
