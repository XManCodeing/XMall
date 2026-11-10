package domain

import "time"

// PaymentReceiptAnomaly 保存同一支付尝试收到不同交易流水的异常事实，不覆盖原收款或人工审计。
type PaymentReceiptAnomaly struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	PaymentID     uint       `gorm:"not null;uniqueIndex:idx_payment_receipt_anomaly" json:"payment_id"`
	ReferenceHash string     `gorm:"size:64;not null;uniqueIndex:idx_payment_receipt_anomaly" json:"-"`
	TransactionID string     `gorm:"size:160;not null" json:"transaction_id"`
	Amount        string     `gorm:"size:32;not null" json:"amount"`
	Currency      string     `gorm:"size:12;not null" json:"currency"`
	PaidAt        *time.Time `json:"paid_at"`
	ObservedAt    time.Time  `gorm:"not null" json:"observed_at"`
}

// TableName 返回迟到额外收款的持久异常表名。
func (PaymentReceiptAnomaly) TableName() string { return "payment_receipt_anomalies" }
