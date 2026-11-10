package contract

import (
	"context"
	"time"
)

// RefundPreviewReader reads a point-in-time hypothetical termination without writes or Telegram calls.
type RefundPreviewReader interface {
	PreviewRefund(context.Context, uint, []uint) (*RefundPreview, error)
}

// RefundPreview reports all selected purchases and server-calculated remaining rights.
// It is informational, not an execution authorization or a guarantee against concurrent renewal.
type RefundPreview struct {
	ObservedAt time.Time            `json:"observed_at"`
	Groups     []RefundPreviewGroup `json:"groups"`
}

// RefundPreviewGroup identifies a historical purchase target and remaining aggregate entitlement.
// Removal and invite cleanup remain unconfirmed until their independent lifecycle evidence exists.
type RefundPreviewGroup struct {
	ItemID                uint       `json:"item_id"`
	TargetID              uint       `json:"target_id"`
	ChatID                int64      `json:"chat_id,string"`
	BeneficiaryTelegramID int64      `json:"beneficiary_telegram_id,string"`
	Active                bool       `json:"active"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	RemovalState          string     `json:"removal_state"`
	CleanupState          string     `json:"cleanup_state"`
}
