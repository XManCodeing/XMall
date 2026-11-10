package gormstore

import (
	"reflect"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

func TestTransferReceiptOrderedPersistenceReplayAndRetention(t *testing.T) {
	store, db, ctx, bot := setup(t)
	row := d.Delivery{BotID: bot.ID, Key: "support:ready:1:2", TargetType: "support_group", Purpose: "support_ready", Operation: "send", ChatID: -100, PayloadCipher: "encrypted"}
	must(t, store.Enqueue(ctx, &row))
	if !row.PayloadHeld {
		t.Fatal("support proof not held at enqueue")
	}
	claimed, err := store.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	if claimed == nil {
		t.Fatal("missing claim")
	}
	receipt := c.DeliveryReceipt{MessageIDs: []int64{9, 3}}
	must(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "sent", receipt, "", time.Now()))
	must(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "sent", receipt, "", time.Now()))
	wantErr(t, store.FinishDeliveryReceipt(ctx, row.ID, "other", "sent", receipt, "", time.Now()), c.ErrStale)
	wantErr(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "sent", c.DeliveryReceipt{MessageIDs: []int64{3, 9}}, "", time.Now()), c.ErrStale)
	must(t, db.First(&row, row.ID).Error)
	if row.MessageID != 9 || !reflect.DeepEqual(row.MessageIDs, receipt.MessageIDs) || row.ReceiptClaim != "claim" {
		t.Fatal(row)
	}
	must(t, db.Model(&row).Update("updated_at", time.Now().Add(-8*24*time.Hour)).Error)
	must(t, store.Cleanup(ctx, bot.ID))
	must(t, db.First(&row, row.ID).Error)
	if row.PayloadCipher != "encrypted" {
		t.Fatal("held proof cleared")
	}
	must(t, store.ReleaseDeliveryPayload(ctx, bot.ID, bot.Generation, row.ID))
	must(t, db.Model(&row).Update("updated_at", time.Now().Add(-8*24*time.Hour)).Error)
	must(t, store.Cleanup(ctx, bot.ID))
	row = d.Delivery{}
	must(t, db.First(&row, claimed.ID).Error)
	if row.PayloadCipher != "" || !reflect.DeepEqual(row.MessageIDs, receipt.MessageIDs) {
		t.Fatal("release cleared observations or retained payload", row)
	}
}

// TestReplyRequestHoldsProofUntilBusinessAck covers the real queue request path.
// Cleanup must preserve terminal proof until support commits its acknowledgment.
func TestReplyRequestHoldsProofUntilBusinessAck(t *testing.T) {
	for _, status := range []string{"sent", "failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			store, db, ctx, bot := setup(t)
			request := c.DeliveryRequest{BotID: bot.ID, Key: "support:reply:1", TargetType: "support_private", Purpose: "support_reply", Operation: "send", ChatID: 99, PayloadCipher: "synthetic-proof"}
			must(t, store.EnqueueRequest(ctx, &request))
			var row d.Delivery
			must(t, db.Where("bot_id = ? AND key = ?", bot.ID, request.Key).First(&row).Error)
			if !row.PayloadHeld {
				t.Fatal("downstream proof was not held by queue request")
			}
			must(t, db.Model(&row).Updates(map[string]any{"status": status, "updated_at": time.Now().Add(-8 * 24 * time.Hour)}).Error)
			must(t, store.Cleanup(ctx, bot.ID))
			must(t, db.First(&row, row.ID).Error)
			if row.PayloadCipher != "synthetic-proof" {
				t.Fatal("cleanup removed unacknowledged downstream proof")
			}
			if status != "sent" {
				return
			}
			must(t, store.ReleaseDeliveryPayload(ctx, bot.ID, bot.Generation, row.ID))
			must(t, db.Model(&row).Update("updated_at", time.Now().Add(-8*24*time.Hour)).Error)
			must(t, store.Cleanup(ctx, bot.ID))
			saved := d.Delivery{}
			must(t, db.First(&saved, row.ID).Error)
			if saved.PayloadCipher != "" {
				t.Fatal("acknowledged proof did not expire")
			}
		})
	}
}

func TestTransferPartialReceiptCannotBeRetriedOrMarkedSentInvalid(t *testing.T) {
	store, db, ctx, bot := setup(t)
	row := d.Delivery{BotID: bot.ID, Key: "partial", Status: "processing", Claim: "claim", TargetType: "support_group"}
	must(t, db.Create(&row).Error)
	receipt := c.DeliveryReceipt{MessageIDs: []int64{7, 0, 7}}
	wantErr(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "sent", receipt, "", time.Now()), c.ErrConflict)
	wantErr(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "retry", receipt, "", time.Now()), c.ErrConflict)
	must(t, store.FinishDeliveryReceipt(ctx, row.ID, "claim", "unknown", receipt, "delivery outcome unknown", time.Now()))
	must(t, db.First(&row, row.ID).Error)
	if row.Status != "unknown" || !reflect.DeepEqual(row.MessageIDs, receipt.MessageIDs) {
		t.Fatal(row)
	}
	next, err := store.ClaimDelivery(ctx, bot.ID, "next", time.Now().Add(time.Minute))
	must(t, err)
	if next != nil {
		t.Fatal("unknown observations retried")
	}
}
