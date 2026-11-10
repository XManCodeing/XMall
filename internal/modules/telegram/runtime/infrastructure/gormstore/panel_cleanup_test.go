package gormstore

import (
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

// TestPanelCleanupRequiresConfirmedReceipts excludes unknown sends and arbitrary old buttons.
func TestPanelCleanupRequiresConfirmedReceipts(t *testing.T) {
	store, db, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelMessageID: 80, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, db.Create(session).Error)
	previous := &d.Delivery{BotID: bot.ID, Key: "old", ChatID: 9, TelegramUserID: 9, Purpose: "panel", Status: "sent", MessageID: 70}
	source := &d.Delivery{BotID: bot.ID, Key: "new", ChatID: 9, TelegramUserID: 9, Purpose: "panel", Status: "sent", MessageID: 80, PreviousMessageID: 70}
	must(t, db.Create(previous).Error)
	must(t, db.Create(source).Error)
	proof := c.ConfirmedPanelCleanup{SourceDeliveryID: source.ID}
	must(t, store.AuthorizePanelCleanup(ctx, bot.ID, 9, 70, proof))
	for _, target := range []int64{0, 69, 80} {
		if store.AuthorizePanelCleanup(ctx, bot.ID, 9, target, proof) == nil {
			t.Fatal("unconfirmed target accepted", target)
		}
	}
	if store.AuthorizePanelCleanup(ctx, bot.ID, 10, 70, proof) == nil {
		t.Fatal("cross recipient cleanup accepted")
	}
	must(t, db.Model(previous).Update("status", "unknown").Error)
	if store.AuthorizePanelCleanup(ctx, bot.ID, 9, 70, proof) == nil {
		t.Fatal("unknown previous send accepted")
	}
	must(t, db.Model(previous).Update("status", "sent").Error)
	must(t, db.Model(source).Update("status", "unknown").Error)
	if store.AuthorizePanelCleanup(ctx, bot.ID, 9, 70, proof) == nil {
		t.Fatal("unknown replacement accepted")
	}
}
