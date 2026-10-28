package domain

import (
	"time"

	"github.com/homles698698-lab/XMall/internal/shared/money"
)

// Item 是一条对账差异。
type Item struct {
	ID                      uint         `gorm:"primarykey" json:"id"`
	JobID                   uint         `gorm:"index;not null" json:"job_id"`
	ProcurementOrderID      uint         `gorm:"index" json:"procurement_order_id"`
	LocalOrderNo            string       `gorm:"type:varchar(64)" json:"local_order_no"`
	UpstreamOrderExternalID string       `gorm:"type:varchar(255)" json:"upstream_order_external_id,omitempty"`
	UpstreamOrderNo         string       `gorm:"type:varchar(64)" json:"upstream_order_no"`
	LocalStatus             string       `gorm:"type:varchar(20)" json:"local_status"`
	UpstreamStatus          string       `gorm:"type:varchar(20)" json:"upstream_status"`
	LocalAmount             money.Amount `gorm:"type:decimal(20,2);not null;default:0" json:"local_amount"`
	UpstreamAmount          money.Amount `gorm:"type:decimal(20,2);not null;default:0" json:"upstream_amount"`
	LocalCurrency           string       `gorm:"type:varchar(10)" json:"local_currency,omitempty"`
	UpstreamCurrency        string       `gorm:"type:varchar(10)" json:"upstream_currency,omitempty"`
	Diagnostic              string       `gorm:"type:varchar(64)" json:"diagnostic,omitempty"`
	MismatchType            string       `gorm:"type:varchar(40)" json:"mismatch_type,omitempty"`
	Resolved                bool         `gorm:"not null;default:false" json:"resolved"`
	ResolvedBy              *uint        `json:"resolved_by,omitempty"`
	ResolvedAt              *time.Time   `json:"resolved_at,omitempty"`
	Remark                  string       `gorm:"type:text" json:"remark,omitempty"`
	CreatedAt               time.Time    `gorm:"index" json:"created_at"`
}

func (Item) TableName() string { return "reconciliation_items" }
