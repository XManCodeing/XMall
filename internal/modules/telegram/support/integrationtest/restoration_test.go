package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	adapter "github.com/XManCodeing/XMall/internal/bootstrap/telegramsupportrecovery"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"gorm.io/gorm"
)

// receiptCipher is an identity cipher for isolated receipt fixtures only.
type receiptCipher struct{}

func (receiptCipher) Open(value string) (string, error) { return value, nil }
func (receiptCipher) Seal(value string) (string, error) { return value, nil }

// TestRestorationReceiptedLegacy verifies exact durable receipts drain counted
// history once, while missing and cross-route evidence never clears a liability.
func TestRestorationReceiptedLegacy(t *testing.T) {
	for _, cross := range []bool{false, true} {
		db, _, plan, now := restorationFixture(t, cross)
		a := adapter.Adapter{DB: db, Cipher: receiptCipher{}, Authorize: func(_ context.Context, _ *gorm.DB, actual c.SupportRestoration, _ time.Time) error {
			if actual != plan {
				return c.ErrDenied
			}
			return nil
		}}
		store := a.Store()
		ctx := context.Background()
		for index := 1; index <= 3; index++ {
			message := d.BufferedMessage{ID: uint(index), BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: int64(index), Sequence: uint64(index), State: "unknown", TargetChatID: -100, TargetThreadID: 88, RouteVersion: 1}
			mustDisaster(t, db.Create(&message).Error)
			if index == 3 {
				continue
			}
			envelope := r.Message{BusinessRef: c.ReadyBusinessRef(1, message.ID), TargetType: r.TargetSupportGroup, Purpose: "support_ready", Operation: "send", ChatID: -100, ThreadID: 88, RouteVersion: 1}
			if index == 2 {
				envelope.RouteVersion = 99
			}
			payload, err := json.Marshal(envelope)
			mustDisaster(t, err)
			mustDisaster(t, db.Create(&rd.Delivery{BotID: 1, Key: envelope.BusinessRef, Status: "sent", TargetType: string(r.TargetSupportGroup), Purpose: "support_ready", Operation: "send", ChatID: -100, TelegramUserID: 42, MessageID: int64(100 + index), PayloadCipher: string(payload)}).Error)
		}
		progress, err := store.BeginSupportRestoration(ctx, plan, now)
		mustDisaster(t, err)
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
		mustDisaster(t, err)
		if progress.Legacy != 2 {
			t.Fatalf("legacy = %d", progress.Legacy)
		}
		var message d.BufferedMessage
		mustDisaster(t, db.First(&message, 1).Error)
		if message.LegacyTaskID != 0 || message.State != "delivered" {
			t.Fatal("receipt not consumed")
		}
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
		mustDisaster(t, err)
		if progress.Legacy != 2 {
			t.Fatal("duplicate decrement or missing proof cleared")
		}
	}
}

// TestReceiptPageReachesPastInvalidHeads proves durable bounded traversal across
// restarts, rollback and wrap-around. Invalid evidence is never acknowledged.
func TestReceiptPageReachesPastInvalidHeads(t *testing.T) {
	for _, cross := range []bool{false, true} {
		db, _, plan, now := restorationFixture(t, cross)
		a := adapter.Adapter{DB: db, Cipher: receiptCipher{}, Authorize: func(_ context.Context, _ *gorm.DB, actual c.SupportRestoration, _ time.Time) error {
			if actual != plan {
				return c.ErrDenied
			}
			return nil
		}}
		ctx := context.Background()
		for index := 1; index <= 5; index++ {
			mustDisaster(t, db.Create(&d.BufferedMessage{ID: uint(index), BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: int64(index), Sequence: uint64(index), State: "unknown", TargetChatID: -100, TargetThreadID: 88, RouteVersion: 1}).Error)
			envelope := r.Message{BusinessRef: c.ReadyBusinessRef(1, uint(index)), TargetType: r.TargetSupportGroup, Purpose: "support_ready", Operation: "send", ChatID: -100, ThreadID: 88, RouteVersion: 1}
			if index < 5 {
				envelope.RouteVersion = 99
			}
			payload, err := json.Marshal(envelope)
			mustDisaster(t, err)
			mustDisaster(t, db.Create(&rd.Delivery{BotID: 1, Key: envelope.BusinessRef, Status: "sent", TargetType: string(r.TargetSupportGroup), Purpose: "support_ready", Operation: "send", ChatID: -100, TelegramUserID: 42, MessageID: int64(100 + index), PayloadCipher: string(payload)}).Error)
		}
		progress, err := a.Store().BeginSupportRestoration(ctx, plan, now)
		mustDisaster(t, err)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 2, now)
		mustDisaster(t, err)
		if progress.Legacy != 5 {
			t.Fatal("invalid head consumed")
		}
		// A failed page must not lose its cursor or skip the legitimate receipt.
		mustDisaster(t, db.Exec("CREATE TRIGGER lose_page BEFORE INSERT ON telegram_support_resolution_audits WHEN NEW.resource = 'legacy_receipt_page' BEGIN SELECT RAISE(ABORT, 'lost page'); END").Error)
		if _, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 2, now); err == nil {
			t.Fatal("page failure ignored")
		}
		mustDisaster(t, db.Exec("DROP TRIGGER lose_page").Error)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 2, now)
		mustDisaster(t, err)
		if progress.Legacy != 5 {
			t.Fatal("page exceeded bound")
		}
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 2, now)
		mustDisaster(t, err)
		if progress.Legacy != 4 {
			t.Fatalf("valid tail starved: %d", progress.Legacy)
		}
		// Corrected head evidence is discovered on the next bounded wrap.
		envelope := r.Message{BusinessRef: c.ReadyBusinessRef(1, 1), TargetType: r.TargetSupportGroup, Purpose: "support_ready", Operation: "send", ChatID: -100, ThreadID: 88, RouteVersion: 1}
		payload, err := json.Marshal(envelope)
		mustDisaster(t, err)
		mustDisaster(t, db.Model(&rd.Delivery{}).Where("bot_id = ? AND key = ?", 1, envelope.BusinessRef).Update("payload_cipher", string(payload)).Error)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 2, now)
		mustDisaster(t, err)
		if progress.Legacy != 3 {
			t.Fatalf("wrap did not revisit evidence: %d", progress.Legacy)
		}
	}
}

// TestLegacyAlbumReceiptRequiresCompleteOwnership exercises original-bot receipt
// registration without copying media or invoking Telegram, for both task types.
func TestLegacyAlbumReceiptRequiresCompleteOwnership(t *testing.T) {
	for _, cross := range []bool{false, true} {
		db, _, plan, now := restorationFixture(t, cross)
		a := adapter.Adapter{DB: db, Cipher: receiptCipher{}, Authorize: func(_ context.Context, _ *gorm.DB, actual c.SupportRestoration, _ time.Time) error {
			if actual != plan {
				return c.ErrDenied
			}
			return nil
		}}
		mustDisaster(t, db.Create(&d.Album{ID: 1, BotID: 1, ChatID: 42, SessionID: 1, LeaderID: 1, Count: 2, State: "sending"}).Error)
		for index := 1; index <= 2; index++ {
			mustDisaster(t, db.Create(&d.BufferedMessage{ID: uint(index), BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: int64(index), Sequence: uint64(index), State: "unknown", TargetChatID: -100, TargetThreadID: 88, RouteVersion: 1}).Error)
			mustDisaster(t, db.Create(&d.AlbumItem{AlbumID: 1, BufferedMessageID: uint(index), SourceMessageID: int64(index)}).Error)
		}
		envelope := c.TransferEnvelope(r.TargetSupportGroup, c.ReadyBusinessRef(1, 1), "support_ready", -100, 88, 1, 0, []r.TransferContent{{Kind: "photo", FileID: "old-file-one"}, {Kind: "photo", FileID: "old-file-two"}})
		payload, err := json.Marshal(envelope)
		mustDisaster(t, err)
		proof := rd.Delivery{BotID: 1, Key: envelope.BusinessRef, Status: "sent", TargetType: string(r.TargetSupportGroup), Purpose: "support_ready", Operation: "send", ChatID: -100, TelegramUserID: 42, MessageID: 101, MessageIDs: []int64{101}, PayloadCipher: string(payload)}
		mustDisaster(t, db.Create(&proof).Error)
		ctx := context.Background()
		progress, err := a.Store().BeginSupportRestoration(ctx, plan, now)
		mustDisaster(t, err)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 2 {
			t.Fatal("partial album receipt consumed")
		}
		proof.MessageIDs = []int64{101, 102}
		mustDisaster(t, db.Save(&proof).Error)
		// A conflicting historical mapping must roll back earlier mapping writes.
		mustDisaster(t, db.Create(&d.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 102, BufferedMessageID: 99}).Error)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		var count int64
		mustDisaster(t, db.Model(&d.ReplyMapping{}).Where("message_id = ?", 101).Count(&count).Error)
		if progress.Legacy != 2 || count != 0 {
			t.Fatal("partial mapping escaped savepoint")
		}
		mustDisaster(t, db.Where("buffered_message_id = ?", 99).Delete(&d.ReplyMapping{}).Error)
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 0 {
			t.Fatalf("album stuck: %d", progress.Legacy)
		}
		mustDisaster(t, db.Model(&d.BufferedMessage{}).Where("bot_id = ? AND state = ? AND legacy_task_id = ?", 1, "delivered", 0).Count(&count).Error)
		if count != 2 {
			t.Fatal("album not atomically settled")
		}
		progress, err = a.Store().AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 0 {
			t.Fatal("album double counted")
		}
	}
}

// downstreamLiabilityFixture creates immutable original-bot source and sent
// proof evidence. album=true binds two members but only the leader is counted.
func downstreamLiabilityFixture(t *testing.T, cross, album bool) (*gorm.DB, *ss.Store, c.SupportRestoration, time.Time, d.Downstream, rd.Delivery) {
	t.Helper()
	db, _, plan, now := restorationFixture(t, cross)
	a := adapter.Adapter{DB: db, Cipher: receiptCipher{}, Authorize: func(_ context.Context, _ *gorm.DB, actual c.SupportRestoration, _ time.Time) error {
		if actual != plan {
			return c.ErrDenied
		}
		return nil
	}}
	mustDisaster(t, db.Create(&d.BufferedMessage{ID: 20, BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: 20, State: "delivered", DeliveryID: 20, TargetChatID: -100, TargetThreadID: 88, RouteVersion: 1}).Error)
	mustDisaster(t, db.Create(&d.ReplyMapping{ID: 20, BotID: 1, ChatID: -100, MessageID: 200, ThreadID: 88, TopicID: 1, SessionID: 1, BufferedMessageID: 20, DeliveryID: 20, UserID: 42, RouteVersion: 1}).Error)
	row := d.Downstream{ID: 1, BotID: 1, UpdateID: 1, SessionID: 1, TopicID: 1, ReplyMappingID: 20, ActorID: 55, SourceChatID: -100, SourceMessageID: 201, ThreadID: 88, RouteVersion: 1, UserID: 42, ChatID: 42, Kind: "reply", State: "unknown", Version: 3}
	contents := []r.TransferContent{{Kind: "text", Text: "historical"}}
	ids := []int64{301}
	if album {
		row.AlbumID = 1
		contents = []r.TransferContent{{Kind: "photo", FileID: "original-one"}, {Kind: "photo", FileID: "original-two"}}
		ids = []int64{301, 302}
		mustDisaster(t, db.Create(&d.DownstreamAlbum{ID: 1, BotID: 1, SourceChatID: -100, MediaGroupID: "fixture", LeaderID: 1, SessionID: 1, TopicID: 1, ActorID: 55, RouteVersion: 1, Count: 2, State: "pending"}).Error)
		member := row
		member.ID, member.UpdateID, member.SourceMessageID, member.State = 2, 2, 202, "album_item"
		mustDisaster(t, db.Create(&member).Error)
	}
	envelope := c.TransferEnvelope(r.TargetSupportPrivate, c.DownstreamBusinessRef(1), "support_reply", 42, 0, 1, 0, contents)
	payload, err := json.Marshal(envelope)
	mustDisaster(t, err)
	row.EnvelopeCipher = string(payload)
	mustDisaster(t, db.Create(&row).Error)
	proof := rd.Delivery{BotID: 1, Key: envelope.BusinessRef, Status: "sent", TargetType: string(r.TargetSupportPrivate), Purpose: "support_reply", Operation: "send", ChatID: 42, TelegramUserID: 42, MessageID: 301, MessageIDs: ids, PayloadCipher: string(payload)}
	mustDisaster(t, db.Create(&proof).Error)
	return db, a.Store(), plan, now, row, proof
}

// TestDownstreamLiabilityReceipt consumes only a counted source, including a
// complete album, and preserves legacy-only entries without inventing a ledger.
func TestDownstreamLiabilityReceipt(t *testing.T) {
	for _, cross := range []bool{false, true} {
		for _, album := range []bool{false, true} {
			db, store, plan, now, _, _ := downstreamLiabilityFixture(t, cross, album)
			ctx := context.Background()
			progress, err := store.BeginSupportRestoration(ctx, plan, now)
			mustDisaster(t, err)
			progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
			mustDisaster(t, err)
			if progress.Legacy != 0 {
				t.Fatalf("receipt stuck: %+v", progress)
			}
			var liability d.RestorationDownstreamLiability
			mustDisaster(t, db.Take(&liability).Error)
			if liability.State != "consumed" || liability.SourceVersion != 3 || liability.ConsumedBusinessVersion != 4 {
				t.Fatal("receipt CAS lost")
			}
			var count int64
			mustDisaster(t, db.Model(&d.RestorationDownstreamLiability{}).Count(&count).Error)
			if count != 1 {
				t.Fatal("album counted by member")
			}
			progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
			mustDisaster(t, err)
			if progress.Legacy != 0 {
				t.Fatal("duplicate consumption")
			}
			var row d.Downstream
			mustDisaster(t, db.First(&row, 1).Error)
			if row.Version != 4 || row.State != "delivered" {
				t.Fatal("business result reapplied")
			}
		}
	}
}

// TestDownstreamLiabilityBoundedAndLegacy verifies incomplete registration is
// visible, invalid heads do not starve receipts, and old entries never backfill.
func TestDownstreamLiabilityBoundedAndLegacy(t *testing.T) {
	db, store, plan, now, row, proof := downstreamLiabilityFixture(t, false, false)
	ctx := context.Background()
	proof.Status = "unknown"
	mustDisaster(t, db.Save(&proof).Error)
	for index := 2; index <= 3; index++ {
		extra := row
		extra.ID, extra.UpdateID = uint(index), int64(index)
		mustDisaster(t, db.Create(&extra).Error)
	}
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
	mustDisaster(t, err)
	var entry d.RestorationEntry
	mustDisaster(t, db.Take(&entry).Error)
	if entry.DownstreamScanned || entry.DownstreamCursor != 1 || progress.Pending < 2 || progress.Legacy != 1 {
		t.Fatal("premature completion")
	}
	// Freeze the ledger registration after one page to model an existing old entry.
	mustDisaster(t, db.Model(&entry).Updates(map[string]any{"downstream_tracking_version": 0, "legacy": 7}).Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
	mustDisaster(t, err)
	var count int64
	mustDisaster(t, db.Model(&d.RestorationDownstreamLiability{}).Count(&count).Error)
	if count != 1 || progress.Legacy != 7 {
		t.Fatal("old aggregate backfilled")
	}
}

// TestDownstreamReceiptPagesSkipInvalidHeads traverses persisted liability IDs,
// leaving invalid/missing receipts isolated while consuming a later valid row.
func TestDownstreamReceiptPagesSkipInvalidHeads(t *testing.T) {
	db, store, plan, now, row, proof := downstreamLiabilityFixture(t, false, false)
	ctx := context.Background()
	proof.PayloadCipher = "invalid"
	mustDisaster(t, db.Save(&proof).Error)
	for index := 2; index <= 3; index++ {
		extra := row
		extra.ID, extra.UpdateID = uint(index), int64(index)
		if index == 3 {
			envelope := c.TransferEnvelope(r.TargetSupportPrivate, c.DownstreamBusinessRef(3), "support_reply", 42, 0, 1, 0, []r.TransferContent{{Kind: "text", Text: "tail"}})
			payload, err := json.Marshal(envelope)
			mustDisaster(t, err)
			extra.EnvelopeCipher = string(payload)
			tail := proof
			tail.ID, tail.Key, tail.PayloadCipher = 0, envelope.BusinessRef, string(payload)
			mustDisaster(t, db.Create(&tail).Error)
		}
		mustDisaster(t, db.Create(&extra).Error)
	}
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	for pass := 0; pass < 6; pass++ {
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
		mustDisaster(t, err)
	}
	if progress.Legacy != 2 {
		t.Fatalf("tail starved: %+v", progress)
	}
	var tail d.Downstream
	mustDisaster(t, db.First(&tail, 3).Error)
	if tail.State != "delivered" {
		t.Fatal("tail receipt not persisted")
	}
	var entry d.RestorationEntry
	mustDisaster(t, db.Take(&entry).Error)
	if !entry.DownstreamScanned {
		t.Fatal("registration never finished")
	}
}

// TestDownstreamLiabilityOwnershipAndRollback rejects forged entry ownership,
// duplicate sources and partial album receipts without changing the aggregate.
func TestDownstreamLiabilityOwnershipAndRollback(t *testing.T) {
	db, store, plan, now, _, proof := downstreamLiabilityFixture(t, false, true)
	ctx := context.Background()
	proof.MessageIDs = []int64{301}
	mustDisaster(t, db.Save(&proof).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	mustDisaster(t, db.Exec("CREATE TRIGGER lose_source BEFORE INSERT ON telegram_support_restoration_downstreams BEGIN SELECT RAISE(ABORT, 'lost source'); END").Error)
	if _, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now); err == nil {
		t.Fatal("registration failure ignored")
	}
	mustDisaster(t, db.Exec("DROP TRIGGER lose_source").Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
	mustDisaster(t, err)
	if progress.Legacy != 1 {
		t.Fatal("partial receipt cleared")
	}
	var liability d.RestorationDownstreamLiability
	mustDisaster(t, db.Take(&liability).Error)
	duplicate := liability
	duplicate.ID = 0
	if err = db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate source accepted")
	}
	proof.MessageIDs = []int64{301, 302}
	mustDisaster(t, db.Save(&proof).Error)
	originalEntry := liability.EntryID
	mustDisaster(t, db.Model(&liability).Update("entry_id", 999).Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
	mustDisaster(t, err)
	if progress.Legacy != 1 {
		t.Fatal("wrong entry consumed")
	}
	mustDisaster(t, db.Model(&liability).Update("entry_id", originalEntry).Error)
	mustDisaster(t, db.Exec("CREATE TRIGGER lose_album_consume BEFORE UPDATE ON telegram_support_restoration_downstreams WHEN NEW.state = 'consumed' BEGIN SELECT RAISE(ABORT, 'lost consume'); END").Error)
	if _, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now); err == nil {
		t.Fatal("consume failure ignored")
	}
	var count int64
	mustDisaster(t, db.Model(&d.Downstream{}).Where("state = ?", "delivered").Count(&count).Error)
	if count != 0 {
		t.Fatal("album writes escaped rollback")
	}
	mustDisaster(t, db.Exec("DROP TRIGGER lose_album_consume").Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
	mustDisaster(t, err)
	if progress.Legacy != 0 {
		t.Fatal("exact album receipt remained stuck")
	}
}

// TestDownstreamReceiptAfterBusinessCommit accepts only exact version+1 sent
// results and does not increment business versions again after completion loss.
func TestDownstreamReceiptAfterBusinessCommit(t *testing.T) {
	for _, cross := range []bool{false, true} {
		db, store, plan, now, row, proof := downstreamLiabilityFixture(t, cross, false)
		ctx := context.Background()
		proof.Status = "unknown"
		mustDisaster(t, db.Save(&proof).Error)
		progress, err := store.BeginSupportRestoration(ctx, plan, now)
		mustDisaster(t, err)
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 1 {
			t.Fatal("unknown cleared")
		}
		proof.Status = "sent"
		mustDisaster(t, db.Save(&proof).Error)
		mustDisaster(t, db.Model(&row).Updates(map[string]any{"state": "delivered", "version": 5, "delivery_id": proof.ID, "destination_message_id": 301}).Error)
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 1 {
			t.Fatal("unrelated version accepted")
		}
		mustDisaster(t, db.Model(&row).Update("version", 4).Error)
		mustDisaster(t, db.Exec("CREATE TRIGGER lose_consume BEFORE UPDATE ON telegram_support_restoration_downstreams WHEN NEW.state = 'consumed' BEGIN SELECT RAISE(ABORT, 'lost'); END").Error)
		if _, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now); err == nil {
			t.Fatal("interruption ignored")
		}
		mustDisaster(t, db.Exec("DROP TRIGGER lose_consume").Error)
		progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 10, now)
		mustDisaster(t, err)
		if progress.Legacy != 0 {
			t.Fatal("durable receipt not consumed")
		}
		mustDisaster(t, db.First(&row, 1).Error)
		if row.Version != 4 {
			t.Fatal("business receipt applied twice")
		}
	}
}

// TestRestorationLegacyPaginationSkipsAttemptedHeads prevents permanently
// uncertain early entries/messages from starving later never-attempted sources.
func TestRestorationLegacyPaginationSkipsAttemptedHeads(t *testing.T) {
	db, store, plan, now := restorationFixture(t, false)
	ctx := context.Background()
	mustDisaster(t, db.Create(&d.Session{ID: 2, BotID: 1, UserID: 43, ChatID: 43, State: "open"}).Error)
	mustDisaster(t, db.Create(&d.TopicOperation{ID: 2, BotID: 1, SessionID: 2, TargetID: 7, ChatID: -100, RouteVersion: 1, State: "succeeded", Action: "create", Version: 1}).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	// Model durable liabilities after an interrupted bounded pass. First entry
	// has unknown delivery; second has 100 attempted heads followed by safe work.
	for index := 1; index <= 102; index++ {
		session := uint(2)
		if index == 1 {
			session = 1
		}
		message := d.BufferedMessage{ID: uint(index), BotID: 1, SessionID: session, UserID: int64(41 + session), ChatID: int64(41 + session), UpdateID: int64(index), Sequence: uint64(index), State: "ready", LegacyTaskID: 9, PayloadCipher: "unchanged-source", RetainUntil: now.Add(time.Hour)}
		mustDisaster(t, db.Create(&message).Error)
		if index <= 101 {
			mustDisaster(t, db.Create(&rd.Delivery{BotID: 1, Key: c.ReadyBusinessRef(session, uint(index)), Status: "unknown"}).Error)
		}
	}
	mustDisaster(t, db.Model(&d.RestorationEntry{}).Where("task_id = ? AND source_session_id = ?", 9, 1).Update("legacy", 1).Error)
	mustDisaster(t, db.Model(&d.RestorationEntry{}).Where("task_id = ? AND source_session_id = ?", 9, 2).Update("legacy", 101).Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
	mustDisaster(t, err)
	if progress.Legacy != 101 {
		t.Fatalf("eligible source starved: %+v", progress)
	}
	var safe d.BufferedMessage
	mustDisaster(t, db.First(&safe, 102).Error)
	if safe.LegacyTaskID != 0 || safe.State != "ready" || safe.PayloadCipher != "unchanged-source" || !safe.RetainUntil.Equal(now.Add(time.Hour)) {
		t.Fatal("source mutated or not restored")
	}
	var blocked d.BufferedMessage
	mustDisaster(t, db.First(&blocked, 1).Error)
	if blocked.LegacyTaskID != 9 {
		t.Fatal("unknown liability removed")
	}
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
	mustDisaster(t, err)
	if progress.Legacy != 101 {
		t.Fatal("duplicate decrement")
	}
}

// restorationFixture uses actual drain evidence on a single SQLite connection.
// The injected authority models the recovery-owned route/task lock only.
func restorationFixture(t *testing.T, cross bool) (*gorm.DB, *ss.Store, c.SupportRestoration, time.Time) {
	t.Helper()
	db, _ := disasterDB(t)
	now := time.Now().UTC()
	mustDisaster(t, db.AutoMigrate(&d.RestorationDownstreamLiability{}, &d.DownstreamAlbum{}, &d.ResolutionAudit{}, &d.RestorationCheckpoint{}, &d.RestorationEntry{}, &d.SupportBlock{}, &d.Album{}, &d.AlbumItem{}))
	plan := c.SupportRestoration{TaskID: 9, ActorID: 5, Evidence: "case:B7", Source: c.RestorationRoute{BotID: 1, TargetID: 7, Version: 1, Generation: 3, RouteVersion: 1, ChatID: -100}, Destination: c.RestorationRoute{BotID: 1, TargetID: 7, Version: 1, Generation: 3, RouteVersion: 2, ChatID: -200}}
	if cross {
		plan.Destination.BotID = 2
		plan.Destination.Generation = 0
		mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 2).Updates(map[string]any{"version": 1, "generation": 0, "recovery_task_id": 9, "recovery_version": 1, "recovery_generation": 0}).Error)
	}
	a := adapter.Adapter{DB: db, Authorize: func(_ context.Context, tx *gorm.DB, in c.SupportRestoration, _ time.Time) error {
		if in != plan {
			return c.ErrConflict
		}
		var count int64
		if err := tx.Model(&rd.Instance{}).Where("id = ? AND recovery_task_id = ?", in.Destination.BotID, in.TaskID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrDenied
		}
		return nil
	}}
	mustDisaster(t, db.Create(&d.Session{ID: 1, BotID: 1, UserID: 42, ChatID: 42, State: "open", Version: 1}).Error)
	mustDisaster(t, db.Create(&d.TopicOperation{ID: 1, BotID: 1, SessionID: 1, TargetID: 7, ChatID: -100, RouteVersion: 1, State: "succeeded", Action: "create", Version: 1}).Error)
	mustDisaster(t, db.Create(&d.TopicMapping{ID: 1, OperationID: 1, BotID: 1, SessionID: 1, TargetID: 7, ChatID: -100, ThreadID: 88, RouteVersion: 1, State: "open", Version: 1}).Error)
	return db, a.Store(), plan, now
}

// TestRestorationSameBotUnknownAndFreshInbound verifies real new topic mapping,
// preserved legacy payload and a fresh queue unblocked without replaying history.
func TestRestorationSameBotUnknownAndFreshInbound(t *testing.T) {
	db, store, plan, now := restorationFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mustDisaster(t, db.Create(&d.BufferedMessage{ID: 1, BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: 1, Sequence: 1, State: "unknown", PayloadCipher: "old-file-id", RetainUntil: now.Add(time.Hour)}).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 1, now)
	mustDisaster(t, err)
	if !progress.Scanned || progress.Pending != 1 || progress.Legacy != 1 {
		t.Fatalf("progress %+v", progress)
	}
	if _, err = store.AdvanceSupportRestoration(ctx, 9, 1, 1, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("CAS %v", err)
	}
	job, err := store.ClaimRestorationTopic(ctx, 9, "new-topic", now)
	mustDisaster(t, err)
	if !job.Claim.Execute || job.Claim.Operation.ChatID != -200 {
		t.Fatal("missing new mapping claim")
	}
	mustDisaster(t, store.FinishRestorationTopic(ctx, 9, job.EntryID, "new-topic", c.TopicResult{State: "succeeded", ThreadID: 99}, now))
	if err = store.FinishRestorationTopic(ctx, 9, job.EntryID, "old-claim", c.TopicResult{State: "succeeded", ThreadID: 88}, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("old receipt %v", err)
	}
	var old d.BufferedMessage
	mustDisaster(t, db.First(&old, 1).Error)
	if old.State != "unknown" || old.PayloadCipher != "old-file-id" || old.LegacyTaskID != 9 {
		t.Fatal("legacy mutated")
	}
	var oldTopic d.TopicMapping
	mustDisaster(t, db.First(&oldTopic, 1).Error)
	if oldTopic.State != "retired" || oldTopic.ThreadID != 88 {
		t.Fatal("old mapping lost")
	}
	// Resume normal operation only in the fixture, after task completion; recovery
	// itself never toggles runtime enabled/fences or manufactures a live lease.
	mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 1).Updates(map[string]any{"enabled": true, "state": "running", "recovery_task_id": 0}).Error)
	outcome, err := store.Accept(ctx, 1, r.Update{ID: 2, Private: true, UserID: 42, ChatID: 42, MessageID: 2}, "fresh", 5, "fresh-ref", c.DefaultPolicy(), now)
	mustDisaster(t, err)
	if outcome.Challenge == nil || outcome.Session.State != "waiting_verification" {
		t.Fatal("restoration bypassed daily verification")
	}
	pending, err := store.ClaimReady(ctx, 1, 1, 3, "before-verification", now, time.Minute)
	mustDisaster(t, err)
	if pending != nil {
		t.Fatal("restored consultation released without verification")
	}
	verified, err := store.Verify(ctx, 1, r.Update{ID: 3, UserID: 42, ChatID: 42}, c.VerificationAnswer(*outcome.Challenge, outcome.Challenge.AnswerIndex), c.DefaultPolicy(), now)
	mustDisaster(t, err)
	if verified.Reason != "passed" {
		t.Fatal("fresh consultation verification failed", verified.Reason)
	}
	fresh, err := store.ClaimReady(ctx, 1, 1, 3, "fresh-ready", now, time.Minute)
	mustDisaster(t, err)
	if fresh == nil || fresh.UpdateID != 2 {
		t.Fatal("unknown historical row blocks new inbound")
	}
}

// TestRestorationCrossBotDoesNotInheritContactOrInventSession documents that a
// new bot without independent consultation cannot proactively restore history.
func TestRestorationCrossBotDoesNotInheritContactOrInventSession(t *testing.T) {
	db, store, plan, now := restorationFixture(t, true)
	ctx := context.Background()
	mustDisaster(t, db.Create(&rd.Contact{BotID: 1, TelegramUserID: 42, ChatID: 42, Reachable: true}).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	if progress.AwaitingConsultation != 1 || progress.Restored != 0 {
		t.Fatalf("progress %+v", progress)
	}
	job, err := store.ClaimRestorationTopic(ctx, 9, "no-user-start", now)
	mustDisaster(t, err)
	if job.Claim.Execute {
		t.Fatal("unstarted user restored")
	}
	for _, model := range []any{&d.Session{}, &rd.Contact{}, &d.BufferedMessage{}, &rd.Delivery{}, &d.TopicMapping{}} {
		var count int64
		mustDisaster(t, db.Model(model).Where("bot_id = ?", 2).Count(&count).Error)
		if count != 0 {
			t.Fatal("inherited target data")
		}
	}
	progress, err = store.SupportRestorationProgress(ctx, 9)
	mustDisaster(t, err)
	// Independent new-bot ingress/verification is simulated, not inherited.
	mustDisaster(t, db.Create(&d.Session{ID: 2, BotID: 2, UserID: 42, ChatID: 42, State: "open"}).Error)
	mustDisaster(t, db.Create(&d.Challenge{BotID: 2, SessionID: 2, Ref: "new-bot-passed", State: "passed", ConsumedAt: &now}).Error)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	job, err = store.ClaimRestorationTopic(ctx, 9, "independent-start", now)
	mustDisaster(t, err)
	if !job.Claim.Execute || job.Claim.Operation.BotID != 2 {
		t.Fatal("verified new-bot history not reconstructable")
	}
	mustDisaster(t, store.FinishRestorationTopic(ctx, 9, job.EntryID, "independent-start", c.TopicResult{State: "succeeded", ThreadID: 123}, now))
}

// TestRestorationNeverPreparedReusesDeliveryQueue restores a same-bot source
// only when no dispatch/runtime attempt exists; no new source IDs are copied.
func TestRestorationNeverPreparedReusesDeliveryQueue(t *testing.T) {
	db, store, plan, now := restorationFixture(t, false)
	ctx := context.Background()
	for index, state := range []string{"ready", "claimed"} {
		mustDisaster(t, db.Create(&d.BufferedMessage{BotID: 1, SessionID: 1, UserID: 42, ChatID: 42, UpdateID: int64(index + 1), Sequence: uint64(index + 1), State: state, PayloadCipher: "original-source", RetainUntil: now.Add(time.Hour)}).Error)
	}
	mustDisaster(t, db.Create(&rd.Delivery{BotID: 1, Key: c.ReadyBusinessRef(1, 2), Status: "unknown"}).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	progress, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	if progress.Legacy != 1 {
		t.Fatalf("legacy %+v", progress)
	}
	var first, second d.BufferedMessage
	mustDisaster(t, db.First(&first, 1).Error)
	mustDisaster(t, db.First(&second, 2).Error)
	if first.LegacyTaskID != 0 || first.State != "ready" || first.PayloadCipher != "original-source" || second.LegacyTaskID != 9 || second.State != "claimed" {
		t.Fatal("unsafe attempt recovery")
	}
}

// TestRestorationAuditedRetryAndUnknownResolution forbids unknown retries and
// only reuses failed creation after a separate actor/version/evidence decision.
func TestRestorationAuditedRetryAndUnknownResolution(t *testing.T) {
	db, store, plan, now := restorationFixture(t, false)
	ctx := context.Background()
	mustDisaster(t, db.AutoMigrate(&d.ResolutionAudit{}))
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	_, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	job, err := store.ClaimRestorationTopic(ctx, 9, "unknown-first", now)
	mustDisaster(t, err)
	mustDisaster(t, store.FinishRestorationTopic(ctx, 9, job.EntryID, "unknown-first", c.TopicResult{State: "unknown"}, now))
	var operation d.TopicOperation
	mustDisaster(t, db.First(&operation, job.Claim.Operation.ID).Error)
	in := c.Resolution{ActorID: 5, ExpectedVersion: operation.Version, Decision: "retry", Evidence: "case:verified"}
	if err = store.ResolveRestorationTopic(ctx, 9, job.EntryID, in, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("unknown retried %v", err)
	}
	in.Decision = "failed"
	mustDisaster(t, store.ResolveRestorationTopic(ctx, 9, job.EntryID, in, now))
	repeat, err := store.ClaimRestorationTopic(ctx, 9, "not-yet-authorized", now)
	mustDisaster(t, err)
	if repeat.Claim.Execute {
		t.Fatal("failure auto retried")
	}
	mustDisaster(t, db.First(&operation, operation.ID).Error)
	in.ExpectedVersion = operation.Version
	in.Decision = "retry"
	mustDisaster(t, store.ResolveRestorationTopic(ctx, 9, job.EntryID, in, now))
	retry, err := store.ClaimRestorationTopic(ctx, 9, "explicit-retry", now)
	mustDisaster(t, err)
	if !retry.Claim.Execute || retry.Claim.Operation.ID != operation.ID {
		t.Fatal("failed slot not reused")
	}
	if err = store.FinishRestorationTopic(ctx, 9, job.EntryID, "unknown-first", c.TopicResult{State: "succeeded", ThreadID: 7}, now); !errors.Is(err, c.ErrConflict) {
		t.Fatal("old claim receipt accepted")
	}
	mustDisaster(t, store.FinishRestorationTopic(ctx, 9, job.EntryID, "explicit-retry", c.TopicResult{State: "succeeded", ThreadID: 8}, now))
}

// TestRestorationDrainAndInterruptedClaim rejects missing proof/dependencies and
// treats a lost create response as unknown without granting another API attempt.
func TestRestorationDrainAndInterruptedClaim(t *testing.T) {
	db, store, plan, now := restorationFixture(t, false)
	ctx := context.Background()
	if _, err := ss.New(db).BeginSupportRestoration(ctx, plan, now); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("missing guard %v", err)
	}
	mustDisaster(t, db.Model(&rd.GenerationExit{}).Where("bot_id = ? AND generation = ?", 1, 2).Update("joined_at", nil).Error)
	if _, err := store.BeginSupportRestoration(ctx, plan, now); !errors.Is(err, r.ErrRecoveryBlocked) {
		t.Fatalf("missing exit %v", err)
	}
	mustDisaster(t, db.Model(&rd.GenerationExit{}).Where("bot_id = ? AND generation = ?", 1, 2).Update("joined_at", now).Error)
	progress, err := store.BeginSupportRestoration(ctx, plan, now)
	mustDisaster(t, err)
	_, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, now)
	mustDisaster(t, err)
	first, err := store.ClaimRestorationTopic(ctx, 9, "interrupted", now)
	mustDisaster(t, err)
	if !first.Claim.Execute {
		t.Fatal("first claim denied")
	}
	repeat, err := store.ClaimRestorationTopic(ctx, 9, "retry", now.Add(2*time.Minute))
	mustDisaster(t, err)
	if repeat.Claim.Execute {
		t.Fatal("unknown recreated")
	}
	progress, err = store.SupportRestorationProgress(ctx, 9)
	mustDisaster(t, err)
	if progress.Restored != 0 || progress.Pending != 1 {
		t.Fatal("unknown marked restored")
	}
	mustDisaster(t, store.FinishRestorationTopic(ctx, 9, first.EntryID, "interrupted", c.TopicResult{State: "succeeded", ThreadID: 101}, now.Add(2*time.Minute)))
	progress, err = store.SupportRestorationProgress(ctx, 9)
	mustDisaster(t, err)
	if progress.Restored != 1 {
		t.Fatal("proven late result not recorded")
	}
}
