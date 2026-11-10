package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
)

// canCompleteExpiredClaim accepts only the original in-process runtime receipt
// after the claim sweeper marked an unfinished send unknown. The saved claim is
// server-only; an unknown completed receipt or partial result cannot be upgraded.
// It never queues a retry: even a proven failure requires explicit business CAS.
func canCompleteExpiredClaim(row d.Delivery, claim, status string, receipt c.DeliveryReceipt) bool {
	return row.Status == "unknown" && row.ReceiptClaim == claim && row.Claim == "" && row.ClaimedUntil == nil &&
		row.MessageID == 0 && len(row.MessageIDs) == 0 && row.LastError == "support receipt pending" &&
		(row.TargetType == string(c.TargetSupportGroup) || row.TargetType == string(c.TargetSupportPrivate)) &&
		(row.Purpose == "support_ready" || row.Purpose == "support_reply") && row.Operation == "send" &&
		(status == "sent" || (status == "failed" && len(receipt.MessageIDs) == 0))
}

var _ c.DeliveryReceiptStore = (*Store)(nil)

// FinishDeliveryReceipt commits ordered observations and completion atomically.
// ReceiptClaim permits exact replay of a successful completion whose DB ack was
// lost, but a different claim or result cannot overwrite a previously sent row.
func (store *Store) FinishDeliveryReceipt(ctx context.Context, id uint, claim, status string, receipt c.DeliveryReceipt, detail string, available time.Time) error {
	if claim == "" {
		return c.ErrStale
	}
	switch status {
	case "sent", "failed", "unknown", "retry":
	default:
		return c.ErrConflict
	}
	if status == "sent" {
		seen := map[int64]bool{}
		if len(receipt.MessageIDs) == 0 || len(receipt.MessageIDs) > 10 {
			return c.ErrConflict
		}
		for _, messageID := range receipt.MessageIDs {
			if messageID <= 0 || seen[messageID] {
				return c.ErrConflict
			}
			seen[messageID] = true
		}
	}
	if len(receipt.MessageIDs) > 0 && status != "sent" && status != "unknown" {
		return c.ErrConflict
	}
	encoded, err := json.Marshal(receipt.MessageIDs)
	if err != nil {
		return err
	}
	first := int64(0)
	if len(receipt.MessageIDs) > 0 {
		first = receipt.MessageIDs[0]
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&d.Delivery{}).Where("id = ? AND status = ? AND claim = ?", id, "processing", claim).
			Updates(map[string]any{"status": status, "message_id": first, "message_ids": string(encoded), "receipt_claim": claim, "claim": "", "claimed_until": nil, "last_error": safeError(detail), "available_at": readyAt(available)})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 1 {
			return nil
		}
		var row d.Delivery
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.ErrStale
			}
			return err
		}
		if canCompleteExpiredClaim(row, claim, status, receipt) {
			return result(tx.Model(&d.Delivery{}).Where("id = ? AND status = ? AND receipt_claim = ? AND claim = ?", id, "unknown", claim, "").
				Updates(map[string]any{"status": status, "message_id": first, "message_ids": string(encoded), "last_error": safeError(detail), "available_at": readyAt(available)}), c.ErrStale)
		}
		if row.ReceiptClaim == claim && row.Status == status && row.Claim == "" && row.MessageID == first && slices.Equal(row.MessageIDs, receipt.MessageIDs) && row.LastError == safeError(detail) {
			return nil
		}
		return c.ErrStale
	})
}
