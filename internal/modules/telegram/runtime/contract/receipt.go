package contract

import (
	"context"
	"time"
)

// DeliveryReceipt contains ordered observations only, with no text or file IDs.
// Invalid/partial observations must be retained under unknown, never retried.
type DeliveryReceipt struct {
	MessageIDs []int64 `json:"message_ids"`
}

// DeliveryReceiptStore is mandatory before any Transfer call, including singles.
// Completion atomically persists the status and every observed ID under the claim.
// Repeating completion with the same claim/result must be safe after lost DB ack.
// Legacy FinishDelivery remains unchanged for existing shopping integrations.
type DeliveryReceiptStore interface {
	FinishDeliveryReceipt(ctx context.Context, id uint, claim, status string, receipt DeliveryReceipt, detail string, available time.Time) error
}
