package gormstore

import (
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

func TestCleanupRetainsDeduplicationAndPendingPayload(t *testing.T) {
	s, db, ctx, bot := setup(t)
	done := d.Inbox{BotID: bot.ID, UpdateID: 1, Status: "done", PayloadCipher: "sensitive", AvailableAt: time.Now()}
	pending := d.Inbox{BotID: bot.ID, UpdateID: 2, Status: "pending", PayloadCipher: "needed", AvailableAt: time.Now()}
	must(t, db.Create(&done).Error)
	must(t, db.Create(&pending).Error)
	session := d.Session{BotID: bot.ID, TelegramUserID: 12, ChatID: 12, Version: 1, DataCipher: "expired", ExpiresAt: time.Now().Add(-time.Minute)}
	must(t, db.Create(&session).Error)
	must(t, s.Cleanup(ctx, bot.ID))
	var finished, waiting d.Inbox
	must(t, db.First(&finished, done.ID).Error)
	must(t, db.First(&waiting, pending.ID).Error)
	if finished.PayloadCipher != "" || waiting.PayloadCipher != "needed" {
		t.Fatal("incorrect inbox retention")
	}
	must(t, db.First(&session, session.ID).Error)
	if session.DataCipher != "" {
		t.Fatal("expired session retained secrets")
	}
	var count int64
	must(t, db.Model(&d.Inbox{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("dedup tombstones removed")
	}
}
