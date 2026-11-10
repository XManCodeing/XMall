package gormstore

import (
	"github.com/XManCodeing/XMall/internal/crypto"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

// TestInboxCrashBudgetIsChargedAtClaim verifies crashes cannot bypass eight attempts.
func TestInboxCrashBudgetIsChargedAtClaim(t *testing.T) {
	store, db, ctx, bot := setup(t)
	must(t, store.Accept(ctx, bot.ID, 0, []d.Inbox{{UpdateID: 1}, {UpdateID: 2}}, 3))
	for attempt := 1; attempt <= 8; attempt++ {
		entry, err := store.ClaimInbox(ctx, bot.ID, "worker", time.Now().Add(time.Minute))
		must(t, err)
		if entry == nil || entry.UpdateID != 1 || entry.Attempts != attempt {
			t.Fatalf("attempt %d: %+v", attempt, entry)
		}
		must(t, db.Model(entry).Update("claimed_until", time.Now().Add(-time.Second).UTC()).Error)
	}
	entry, err := store.ClaimInbox(ctx, bot.ID, "next", time.Now().Add(time.Minute))
	must(t, err)
	if entry == nil || entry.UpdateID != 2 {
		t.Fatalf("poison update blocked next record: %+v", entry)
	}
	var poisoned d.Inbox
	must(t, db.Where("bot_id = ? AND update_id = ?", bot.ID, 1).First(&poisoned).Error)
	if poisoned.Status != "failed" || poisoned.Attempts != 8 {
		t.Fatalf("not isolated: %+v", poisoned)
	}
}

// TestInboxExecutionMarkerPreventsCrashReplay covers failed outcome persistence.
func TestInboxExecutionMarkerPreventsCrashReplay(t *testing.T) {
	store, db, ctx, bot := setup(t)
	must(t, store.Accept(ctx, bot.ID, 0, []d.Inbox{{UpdateID: 1}, {UpdateID: 2}}, 3))
	entry, err := store.ClaimInbox(ctx, bot.ID, "worker", time.Now().Add(time.Minute))
	must(t, err)
	must(t, store.MarkInboxExecution(ctx, entry.ID, entry.Claim))
	must(t, db.Model(entry).Update("claimed_until", time.Now().Add(-time.Second).UTC()).Error)
	next, err := store.ClaimInbox(ctx, bot.ID, "next", time.Now().Add(time.Minute))
	must(t, err)
	if next == nil || next.UpdateID != 2 {
		t.Fatalf("uncertain action replayed: %+v", next)
	}
	must(t, db.First(entry, entry.ID).Error)
	if entry.Status != "unknown" || entry.Attempts != 1 {
		t.Fatalf("unknown result lost: %+v", entry)
	}
}

// TestLateSupportReceiptRefreshesUpdatedAt rejects GORM SkipHooks leakage.
func TestLateSupportReceiptRefreshesUpdatedAt(t *testing.T) {
	store, db, ctx, bot := setup(t)
	old := time.Now().Add(-24 * time.Hour).UTC()
	row := d.Delivery{BotID: bot.ID, Key: "late", Purpose: "support_reply", Operation: "send", Status: "processing", Claim: "receipt", ClaimedUntil: &old, UpdatedAt: old, AvailableAt: old}
	must(t, db.Create(&row).Error)
	must(t, db.Model(&row).UpdateColumn("updated_at", old).Error)
	_, err := store.ClaimDelivery(ctx, bot.ID, "next", time.Now().Add(time.Minute))
	must(t, err)
	must(t, db.First(&row, row.ID).Error)
	if row.Status != "unknown" || row.ReceiptClaim != "receipt" || !row.UpdatedAt.After(old) {
		t.Fatalf("receipt transition lost timestamp: %+v", row)
	}
}

// TestStartedMutableDeliveryNeverReplaysAfterCrash protects unknown external actions.
func TestStartedMutableDeliveryNeverReplaysAfterCrash(t *testing.T) {
	store, db, ctx, bot := setup(t)
	row := d.Delivery{BotID: bot.ID, Key: "edit", Operation: "edit", TargetMessageID: 99}
	must(t, store.Enqueue(ctx, &row))
	entry, err := store.ClaimDelivery(ctx, bot.ID, "worker", time.Now().Add(time.Minute))
	must(t, err)
	must(t, store.MarkDeliveryExecution(ctx, entry.ID, entry.Claim))
	must(t, db.Model(entry).Update("claimed_until", time.Now().Add(-time.Second).UTC()).Error)
	next, err := store.ClaimDelivery(ctx, bot.ID, "next", time.Now().Add(time.Minute))
	must(t, err)
	if next != nil {
		t.Fatal("unknown external action replayed")
	}
	must(t, db.First(entry, entry.ID).Error)
	if entry.Status != "unknown" {
		t.Fatalf("status=%s", entry.Status)
	}
}

// TestCleanupUsesUTCForLiveSessions reproduces premature cleanup on UTC+8 SQLite hosts.
// t owns isolated storage; the local timezone is restored and no remote Bot is contacted.
func TestCleanupUsesUTCForLiveSessions(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	s, db, ctx, bot := setup(t)
	now := time.Now().UTC()
	for index, expiry := range []time.Time{now.Add(30 * time.Minute), now.Add(-time.Minute)} {
		session := &d.Session{BotID: bot.ID, TelegramUserID: int64(101 + index), ChatID: int64(101 + index), DataCipher: "synthetic-session", ExpiresAt: expiry, PanelEpoch: "panel", PanelRevision: 3}
		must(t, s.SaveSession(ctx, session, 0))
	}
	must(t, s.Cleanup(ctx, bot.ID))
	var sessions []d.Session
	must(t, db.Where("bot_id = ?", bot.ID).Order("chat_id").Find(&sessions).Error)
	if len(sessions) != 2 {
		t.Fatalf("expected two sessions, got %d", len(sessions))
	}
	if sessions[0].DataCipher != "synthetic-session" {
		t.Fatal("live UTC session cleared by local-time cleanup")
	}
	if sessions[1].DataCipher != "" {
		t.Fatal("expired UTC session was not cleared")
	}
	if sessions[0].PanelEpoch != "panel" || sessions[0].PanelRevision != 3 {
		t.Fatal("cleanup changed panel state")
	}
}

func TestCleanupSessionReplayDependencies(t *testing.T) {
	for _, status := range []string{"pending", "retry", "processing", "failed", "unknown", "future_replay_state", ""} {
		name := status
		if name == "" {
			name = "empty_status"
		}
		t.Run(name, func(t *testing.T) {
			s, db, ctx, bot := setup(t)
			now := time.Now().UTC()
			cipher, err := crypto.Encrypt([]byte("01234567890123456789012345678901"), `{"legacy_input_through":42,"last_update":42}`)
			must(t, err)
			inbox := d.Inbox{BotID: bot.ID, UpdateID: 42, Status: status, PayloadCipher: "encrypted-update", AvailableAt: now.Add(time.Hour)}
			must(t, db.Create(&inbox).Error)
			// Inbox has no queryable chat key: retain both sessions conservatively.
			for _, chat := range []int64{12, 13} {
				session := d.Session{BotID: bot.ID, TelegramUserID: chat, ChatID: chat, Version: 1, DataCipher: cipher, ExpiresAt: now.Add(-time.Hour)}
				must(t, db.Create(&session).Error)
			}
			other := d.Session{BotID: bot.ID + 1, TelegramUserID: 12, ChatID: 12, Version: 1, DataCipher: cipher, ExpiresAt: now.Add(-time.Hour)}
			must(t, db.Create(&other).Error)
			must(t, s.Cleanup(ctx, bot.ID))
			must(t, s.Cleanup(ctx, other.BotID))
			var sessions []d.Session
			must(t, db.Where("bot_id = ?", bot.ID).Find(&sessions).Error)
			for _, session := range sessions {
				if session.DataCipher != cipher {
					t.Fatal("replay fence removed before completion")
				}
			}
			must(t, db.First(&other, other.ID).Error)
			if other.DataCipher != "" {
				t.Fatal("unrelated bot blocked cleanup")
			}
			must(t, db.Model(&inbox).Update("status", "done").Error)
			must(t, s.Cleanup(ctx, bot.ID))
			must(t, db.Where("bot_id = ?", bot.ID).Find(&sessions).Error)
			for _, session := range sessions {
				if session.DataCipher != "" {
					t.Fatal("completed dependency prevented cleanup")
				}
			}
		})
	}
}

func TestCleanupPreservesFenceAcrossInboxRetry(t *testing.T) {
	s, db, ctx, bot := setup(t)
	now := time.Now().UTC()
	must(t, s.Accept(ctx, bot.ID, bot.Generation, []d.Inbox{{UpdateID: 42, PayloadCipher: "encrypted-update"}}, 43))
	entry, err := s.ClaimInbox(ctx, bot.ID, "first", now.Add(time.Minute))
	must(t, err)
	cipher, err := crypto.Encrypt([]byte("01234567890123456789012345678901"), `{"legacy_input_through":42,"last_update":42}`)
	must(t, err)
	session := d.Session{BotID: bot.ID, TelegramUserID: 12, ChatID: 12, DataCipher: cipher, ExpiresAt: now.Add(-time.Hour)}
	must(t, s.SaveSession(ctx, &session, 0))
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, s.FinishInbox(ctx, entry.ID, entry.Claim, "retry", "update processing failed", now.Add(-time.Second)))
	must(t, s.Cleanup(ctx, bot.ID))
	entry, err = s.ClaimInbox(ctx, bot.ID, "second", now.Add(time.Minute))
	must(t, err)
	if entry == nil || entry.Attempts != 2 {
		t.Fatal("retry was not reclaimed")
	}
	must(t, db.First(&session, session.ID).Error)
	if session.DataCipher != cipher {
		t.Fatal("retry isolation state was cleared")
	}
	must(t, s.FinishInbox(ctx, entry.ID, entry.Claim, "done", "", now))
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(&session, session.ID).Error)
	if session.DataCipher != "" {
		t.Fatal("completed replay retained expired state")
	}
}

func TestCleanupRetainsDeduplicationAndPendingPayload(t *testing.T) {
	s, db, ctx, bot := setup(t)
	done := d.Inbox{BotID: bot.ID, UpdateID: 1, Status: "done", PayloadCipher: "sensitive", AvailableAt: time.Now()}
	pending := d.Inbox{BotID: bot.ID, UpdateID: 2, Status: "pending", PayloadCipher: "needed", AvailableAt: time.Now()}
	must(t, db.Create(&done).Error)
	must(t, db.Create(&pending).Error)
	session := d.Session{BotID: bot.ID, TelegramUserID: 12, ChatID: 12, Version: 1, DataCipher: "expired", ExpiresAt: time.Now().UTC().Add(-time.Minute)}
	must(t, db.Create(&session).Error)
	must(t, s.Cleanup(ctx, bot.ID))
	var finished, waiting d.Inbox
	must(t, db.First(&finished, done.ID).Error)
	must(t, db.First(&waiting, pending.ID).Error)
	if finished.PayloadCipher != "" || waiting.PayloadCipher != "needed" {
		t.Fatal("incorrect inbox retention")
	}
	must(t, db.First(&session, session.ID).Error)
	if session.DataCipher != "expired" {
		t.Fatal("pending inbox lost expired session replay fence")
	}
	must(t, db.Model(&pending).Update("status", "done").Error)
	must(t, s.Cleanup(ctx, bot.ID))
	must(t, db.First(&session, session.ID).Error)
	if session.DataCipher != "" {
		t.Fatal("completed inbox retained expired session")
	}
	var count int64
	must(t, db.Model(&d.Inbox{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("dedup tombstones removed")
	}
}
