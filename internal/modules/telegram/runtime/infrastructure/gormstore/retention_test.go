package gormstore

import (
	"fmt"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

func TestClaimExcludedExpiredLeaseRecoveryAndTargetNormalization(t *testing.T) {
	s, db, ctx, bot := setup(t)
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	for i, row := range []d.Delivery{
		{ChatID: -100, Status: "processing", Operation: "send", ClaimedUntil: &expired, Attempts: 1},
		{ChatID: -100, Status: "processing", Operation: "edit", ClaimedUntil: &expired, Attempts: 1, TargetMessageID: 5},
		{ChatID: 11, TargetType: "future_target"},
		{ChatID: 12},
	} {
		row.BotID, row.Key, row.AvailableAt, row.PayloadCipher = bot.ID, fmt.Sprint(i), expired, "cipher"
		must(t, s.Enqueue(ctx, &row))
		if row.ChatID == 12 {
			must(t, db.Model(&row).Update("target_type", "").Error)
		}
	}
	row, err := s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation, now, []int64{-100})
	must(t, err)
	if row == nil || row.ChatID != 11 || row.TargetType != "future_target" {
		t.Fatalf("unknown target reclassified: %+v", row)
	}
	row, err = s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation, now, []int64{-100})
	must(t, err)
	if row == nil || row.ChatID != 12 || row.TargetType != "shop_private" {
		t.Fatalf("legacy target not normalized: %+v", row)
	}
	var recovered []d.Delivery
	must(t, db.Where("chat_id = ?", -100).Order("id").Find(&recovered).Error)
	if len(recovered) != 2 || recovered[0].Status != "unknown" || recovered[1].Status != "retry" {
		t.Fatalf("excluded expiry recovery: %+v", recovered)
	}
	row, err = s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation, now, nil)
	must(t, err)
	if row == nil || row.Operation != "edit" || row.Attempts != 2 {
		t.Fatalf("retry budget lost: %+v", row)
	}
}

func TestPayloadCleanupRetentionMatrix(t *testing.T) {
	s, db, ctx, bot := setup(t)
	past, future := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	for _, status := range []string{"done", "failed", "pending", "retry", "processing"} {
		for _, hold := range []bool{false, true} {
			for _, deadline := range []*time.Time{nil, &past, &future} {
				row := d.Inbox{BotID: bot.ID, UpdateID: time.Now().UnixNano(), Status: status, PayloadCipher: "cipher", PayloadHeld: hold, PayloadRetainUntil: deadline, UpdatedAt: old}
				must(t, db.Create(&row).Error)
				must(t, s.Cleanup(ctx, bot.ID))
				must(t, db.First(&row, row.ID).Error)
				clean := (status == "done" || status == "failed") && !hold && deadline != &future
				if (row.PayloadCipher == "") != clean {
					t.Fatalf("inbox %s held=%v deadline=%v", status, hold, deadline)
				}
			}
		}
	}
	for _, status := range []string{"sent", "failed", "superseded", "unknown", "pending", "processing", "retry"} {
		for _, hold := range []bool{false, true} {
			for i, deadline := range []*time.Time{nil, &past, &future} {
				row := d.Delivery{BotID: bot.ID, Key: fmt.Sprintf("%s-%v-%d", status, hold, i), Status: status, PayloadCipher: "cipher", PayloadHeld: hold, PayloadRetainUntil: deadline, UpdatedAt: old}
				must(t, db.Create(&row).Error)
				must(t, s.Cleanup(ctx, bot.ID))
				must(t, db.First(&row, row.ID).Error)
				clean := (status == "sent" || status == "failed" || status == "superseded") && !hold && deadline != &future
				if (row.PayloadCipher == "") != clean {
					t.Fatalf("delivery %s held=%v deadline=%v", status, hold, deadline)
				}
			}
		}
	}
}

func TestInboxHoldBeforeCompletionAndFencedRelease(t *testing.T) {
	s, db, ctx, bot := setup(t)
	must(t, s.Accept(ctx, bot.ID, bot.Generation, []d.Inbox{{UpdateID: 1, PayloadCipher: "cipher"}}, 2))
	row, err := s.ClaimInbox(ctx, bot.ID, "claim", time.Now().UTC().Add(time.Minute))
	must(t, err)
	until := time.Now().UTC().Add(time.Hour)
	wantErr(t, s.HoldInboxPayload(ctx, bot.ID, bot.Generation+1, row.ID, row.Claim, &until), c.ErrStale)
	wantErr(t, s.HoldInboxPayload(ctx, bot.ID, bot.Generation, row.ID, "wrong", &until), c.ErrStale)
	must(t, s.HoldInboxPayload(ctx, bot.ID, bot.Generation, row.ID, row.Claim, &until))
	shorter := until.Add(-time.Minute)
	must(t, s.HoldInboxPayload(ctx, bot.ID, bot.Generation, row.ID, row.Claim, &shorter))
	must(t, s.FinishInbox(ctx, row.ID, row.Claim, "done", "", time.Now()))
	wantErr(t, s.HoldInboxPayload(ctx, bot.ID, bot.Generation, row.ID, row.Claim, nil), c.ErrStale)
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(row, row.ID).Error)
	if row.PayloadCipher != "cipher" || !row.PayloadHeld || !row.PayloadRetainUntil.Equal(until) {
		t.Fatal("hold lost")
	}
	wantErr(t, s.ReleaseInboxPayload(ctx, bot.ID, bot.Generation+1, row.ID), c.ErrStale)
	must(t, s.ReleaseInboxPayload(ctx, bot.ID, bot.Generation, row.ID))
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(row, row.ID).Error)
	if row.PayloadCipher != "cipher" || row.PayloadHeld {
		t.Fatal("release shortened deadline")
	}
	must(t, db.Model(row).Update("payload_retain_until", time.Now().UTC().Add(-time.Minute)).Error)
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(row, row.ID).Error)
	if row.PayloadCipher != "" {
		t.Fatal("released expired payload retained")
	}
}

func TestPanelMergeRespectsRetention(t *testing.T) {
	s, db, ctx, bot := setup(t)
	until := time.Now().UTC().Add(time.Hour)
	for i, input := range []d.Delivery{{PayloadHeld: true}, {PayloadRetainUntil: &until}, {}} {
		input.BotID, input.Key, input.ChatID, input.TelegramUserID = bot.ID, fmt.Sprint(i), 42, 42
		input.Purpose, input.PanelEpoch, input.PanelRevision, input.PayloadCipher = "panel", "epoch", uint64(i+1), "cipher"
		must(t, s.Enqueue(ctx, &input))
	}
	latest := d.Delivery{BotID: bot.ID, Key: "last", ChatID: 42, TelegramUserID: 42, Purpose: "panel", PanelEpoch: "epoch", PanelRevision: 4}
	must(t, s.Enqueue(ctx, &latest))
	var rows []d.Delivery
	must(t, db.Order("id").Find(&rows).Error)
	for i := 0; i < 3; i++ {
		if rows[i].Status != "superseded" || (rows[i].PayloadCipher == "") != (i == 2) {
			t.Fatalf("row %d: %+v", i, rows[i])
		}
	}
}

func TestClaimDeliveryExcludingKeepsPriorityLeaseAndGeneration(t *testing.T) {
	s, db, ctx, bot := setup(t)
	now := time.Now().UTC()
	for i, row := range []d.Delivery{
		{ChatID: -100, Priority: 100}, {ChatID: 22, Priority: 2}, {ChatID: 33, Priority: 10}, {ChatID: 44, Priority: 10},
	} {
		row.BotID, row.Key, row.AvailableAt, row.PayloadCipher = bot.ID, fmt.Sprint(i), now.Add(-time.Second), "cipher"
		must(t, s.Enqueue(ctx, &row))
	}
	wantErrRow, err := s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation+1, now, nil)
	wantErr(t, err, c.ErrStale)
	if wantErrRow != nil {
		t.Fatal("stale claimant received row")
	}
	for _, chat := range []int64{33, 44, 22} {
		row, err := s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation, now, []int64{-100})
		must(t, err)
		if row == nil || row.ChatID != chat || row.Claim == "" || row.Attempts != 1 || !row.ClaimedUntil.Equal(now.Add(time.Minute)) || row.TargetType != "shop_private" {
			t.Fatalf("bad claim: %+v", row)
		}
		wantErr(t, s.FinishDelivery(ctx, row.ID, "wrong", "sent", 1, "", now), c.ErrStale)
		must(t, s.FinishDelivery(ctx, row.ID, row.Claim, "sent", 1, "", now))
	}
	row, err := s.ClaimDeliveryExcluding(ctx, bot.ID, bot.Generation, now, []int64{-100})
	must(t, err)
	if row != nil {
		t.Fatal("excluded chat claimed")
	}
	row, err = s.ClaimDelivery(ctx, bot.ID, "legacy", now.Add(time.Minute))
	must(t, err)
	if row == nil || row.ChatID != -100 {
		t.Fatal("legacy claim did not share queue")
	}
	must(t, s.HoldDeliveryPayload(ctx, bot.ID, bot.Generation, row.ID, row.Claim, nil))
	must(t, s.FinishDelivery(ctx, row.ID, row.Claim, "unknown", 0, "", now))
	wantErr(t, s.HoldDeliveryPayload(ctx, bot.ID, bot.Generation, row.ID, row.Claim, nil), c.ErrStale)
	wantErr(t, s.ReleaseDeliveryPayload(ctx, bot.ID, bot.Generation+1, row.ID), c.ErrStale)
	must(t, s.ReleaseDeliveryPayload(ctx, bot.ID, bot.Generation, row.ID))
	must(t, db.Model(row).Update("updated_at", now.Add(-30*24*time.Hour)).Error)
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(row, row.ID).Error)
	if row.PayloadCipher != "cipher" {
		t.Fatal("unknown outcome lost payload after release")
	}
}
