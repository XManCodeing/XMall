package gormstore

import (
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// TestMembershipPanelNeverCreates checks completion edits cannot claim an unknown
// send, migrate to another message, or invoke the ordinary replacement fallback.
func TestMembershipPanelNeverCreates(t *testing.T) {
	for _, scenario := range []string{"ready", "missing", "unknown", "changed", "photo", "revision"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, ctx, bot := setup(t)
			session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelMessageEpoch: "one", PanelMessageID: 77, PanelState: "ready", PanelKind: "text", PanelRevision: 2, ExpiresAt: time.Now().Add(time.Hour)}
			switch scenario {
			case "missing":
				session.PanelMessageID = 0
			case "unknown":
				session.PanelState = "unknown"
			case "changed":
				session.PanelMessageID = 88
			case "photo":
				session.PanelKind = "photo"
			case "revision":
				session.PanelRevision = 3
			}
			must(t, store.SaveSession(ctx, session, 0))
			must(t, store.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "membership-invite:4:panel:one:2", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 2, Operation: "edit", TargetMessageID: 77}))
			item, err := store.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
			must(t, err)
			message := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 2, Operation: "edit", TargetMessageID: 77}
			err = store.PreparePanel(ctx, item, &message)
			if scenario == "ready" {
				if err != nil || message.Operation != "edit" || message.TargetMessageID != 77 {
					t.Fatal(message, err)
				}
			} else if !errors.Is(err, c.ErrConflict) {
				t.Fatal("completion migrated physical message", err)
			}
			if !errors.Is(store.ReplacePanel(ctx, item), c.ErrConflict) {
				t.Fatal("completion allowed fallback send")
			}
		})
	}
}

// TestPanelSessionScanBounded confirms cursor and expiry boundaries use existing rows.
func TestPanelSessionScanBounded(t *testing.T) {
	store, _, ctx, bot := setup(t)
	at := time.Now()
	for _, user := range []int64{9, 10, 11} {
		row := &d.Session{BotID: bot.ID, TelegramUserID: user, ChatID: user, PanelMessageID: 77, ExpiresAt: at.Add(time.Hour)}
		if user == 11 {
			row.ExpiresAt = at.Add(-time.Hour)
		}
		must(t, store.SaveSession(ctx, row, 0))
	}
	rows, err := store.PanelSessionsAfter(ctx, bot.ID, 0, 1, at)
	must(t, err)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	rows, err = store.PanelSessionsAfter(ctx, bot.ID, rows[0].ID, 25, at)
	must(t, err)
	if len(rows) != 1 || rows[0].TelegramUserID != 10 {
		t.Fatal(rows)
	}
	if _, err = store.PanelSessionsAfter(ctx, bot.ID, 0, 101, at); !errors.Is(err, c.ErrConflict) {
		t.Fatal("unbounded scan", err)
	}
}
