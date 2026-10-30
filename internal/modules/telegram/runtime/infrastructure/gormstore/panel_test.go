package gormstore

import (
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestPanelResultWriteFailureRollsBackAndCanPersistWithoutResending(t *testing.T) {
	s, db, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "one", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	item, err := s.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}
	must(t, s.PreparePanel(ctx, item, &msg))
	injected := errors.New("injected write failure")
	must(t, db.Callback().Update().Before("gorm:update").Register("panel_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_bot_sessions" {
			tx.AddError(injected)
		}
	}))
	wantErr(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 77}, "", time.Now()), injected)
	must(t, db.Callback().Update().Remove("panel_failure"))
	var saved d.Delivery
	must(t, db.First(&saved, item.ID).Error)
	if saved.Status != "processing" || saved.MessageID != 0 {
		t.Fatal(saved)
	}
	must(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 77}, "", time.Now()))
	got, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.PanelMessageID != 77 {
		t.Fatal(got)
	}
}

func TestPanelOlderSuccessfulSendRegistersTargetWithoutOverwritingShopping(t *testing.T) {
	s, _, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelRevision: 1, State: "home", ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	row := &d.Delivery{BotID: bot.ID, Key: "render-1", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1, Operation: "edit"}
	must(t, s.Enqueue(ctx, row))
	item, err := s.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}
	must(t, s.PreparePanel(ctx, item, &msg))
	if msg.Operation != "send" {
		t.Fatal(msg)
	}
	// A new business state is saved while HTTP is in progress.
	session.PanelRevision = 2
	session.State = "product"
	must(t, s.SaveSession(ctx, session, session.Version))
	must(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 42}, "", time.Now()))
	must(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 42}, "", time.Now()))
	got, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.PanelMessageID != 42 || got.PanelRevision != 2 || got.State != "product" || got.PanelDeliveredRevision != 1 {
		t.Fatalf("%+v", got)
	}
	// A stale in-memory shopping object cannot erase the registered target.
	session.PanelRevision = 3
	must(t, s.SaveSession(ctx, session, session.Version))
	got, err = s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.PanelMessageID != 42 {
		t.Fatal(got)
	}
}

func TestPanelUnknownFreezesCreationAndTrustedCallbackClaims(t *testing.T) {
	s, db, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "one", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	first, err := s.ClaimDelivery(ctx, bot.ID, "first", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}
	must(t, s.PreparePanel(ctx, first, &msg))
	// Crash between HTTP and result registration.
	must(t, db.Model(first).Update("claimed_until", time.Now().UTC().Add(-time.Minute)).Error)
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "two", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 2}))
	session.PanelRevision = 2
	must(t, s.SaveSession(ctx, session, session.Version))
	second, err := s.ClaimDelivery(ctx, bot.ID, "second", time.Now().Add(time.Minute))
	must(t, err)
	msg.PanelRevision = 2
	wantErr(t, s.PreparePanel(ctx, second, &msg), c.ErrStale)
	msg.TargetMessageID = 55
	must(t, s.PreparePanel(ctx, second, &msg))
	if msg.Operation != "edit" || msg.TargetMessageID != 55 {
		t.Fatal(msg)
	}
	must(t, s.FinishPanel(ctx, second, "sent", c.DeliveryResult{MessageID: 55}, "", time.Now()))
	// Expiry removes sensitive data, never the physical panel.
	must(t, db.Model(session).Updates(map[string]interface{}{"expires_at": time.Now().Add(-time.Hour), "data_cipher": "secret"}).Error)
	must(t, s.Cleanup(ctx, bot.ID))
	got, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.DataCipher != "" || got.PanelMessageID != 55 {
		t.Fatal(got)
	}
}

func TestPanelCrashEditRetriesSameTargetOnly(t *testing.T) {
	s, db, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelMessageEpoch: "one", PanelMessageID: 40, PanelState: "ready", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "edit", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	item, err := s.ClaimDelivery(ctx, bot.ID, "first", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}
	must(t, s.PreparePanel(ctx, item, &msg))
	must(t, db.Model(item).Update("claimed_until", time.Now().UTC().Add(-time.Minute)).Error)
	retry, err := s.ClaimDelivery(ctx, bot.ID, "second", time.Now().Add(time.Minute))
	must(t, err)
	if retry == nil || retry.ID != item.ID || retry.TargetMessageID != 40 || retry.Attempts != 2 {
		t.Fatal(retry)
	}
	// Never reinterpret the retry as an edit to a later physical target.
	must(t, db.Model(session).Update("panel_message_id", 41).Error)
	wantErr(t, s.PreparePanel(ctx, retry, &msg), c.ErrConflict)
}

func TestPanelMergesOnlyUntouchedRendersAndKeepsNotices(t *testing.T) {
	s, db, ctx, bot := setup(t)
	rows := []d.Delivery{
		{BotID: bot.ID, Key: "old", ChatID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1},
		{BotID: bot.ID, Key: "notice", ChatID: 9, Purpose: "notice", Priority: 200},
		{BotID: bot.ID, Key: "new", ChatID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 2},
	}
	for n := range rows {
		must(t, s.Enqueue(ctx, &rows[n]))
	}
	var old d.Delivery
	must(t, db.First(&old, rows[0].ID).Error)
	if old.Status != "superseded" {
		t.Fatal(old)
	}
	item, err := s.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	if item.Key != "notice" {
		t.Fatal(item)
	}
}

func TestPhotoFallbackRegistersActualMessageKind(t *testing.T) {
	s, _, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "photo", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	item, err := s.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	message := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1, PhotoURL: "/uploads/missing.png"}
	must(t, s.PreparePanel(ctx, item, &message))
	if item.MessageKind != "photo" {
		t.Fatal("expected photo intent")
	}
	must(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 50, MessageKind: "text"}, "", time.Now()))
	got, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.PanelKind != "text" {
		t.Fatal("fallback left stale photo type")
	}
}

func TestPanelPhotoToTextCreatesBeforeCleaningOld(t *testing.T) {
	s, _, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelMessageEpoch: "one", PanelMessageID: 40, PanelKind: "photo", PanelState: "ready", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "text", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	item, err := s.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1, Text: "order paid"}
	must(t, s.PreparePanel(ctx, item, &msg))
	if msg.Operation != "send" || item.PreviousMessageID != 40 || msg.TargetKind != "photo" {
		t.Fatalf("photo/text transition: %+v %+v", msg, item)
	}
	before, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if before.PanelMessageID != 40 {
		t.Fatal("old panel discarded before replacement succeeded")
	}
	must(t, s.FinishPanel(ctx, item, "sent", c.DeliveryResult{MessageID: 41, MessageKind: "text"}, "", time.Now()))
	after, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if after.PanelMessageID != 41 || after.PanelKind != "text" {
		t.Fatal("text replacement not registered")
	}
}

func TestPanelDefiniteMissingTargetAllowsOneReplacement(t *testing.T) {
	s, _, ctx, bot := setup(t)
	session := &d.Session{BotID: bot.ID, TelegramUserID: 9, ChatID: 9, PanelEpoch: "one", PanelMessageEpoch: "one", PanelMessageID: 40, PanelState: "ready", PanelRevision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	must(t, s.SaveSession(ctx, session, 0))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "edit", ChatID: 9, TelegramUserID: 9, Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}))
	item, err := s.ClaimDelivery(ctx, bot.ID, "first", time.Now().Add(time.Minute))
	must(t, err)
	msg := c.Message{Purpose: "panel", PanelEpoch: "one", PanelRevision: 1}
	must(t, s.PreparePanel(ctx, item, &msg))
	if msg.Operation != "edit" {
		t.Fatal(msg)
	}
	must(t, s.ReplacePanel(ctx, item))
	must(t, s.FinishDelivery(ctx, item.ID, item.Claim, "retry", 0, "", time.Now()))
	item, err = s.ClaimDelivery(ctx, bot.ID, "second", time.Now().Add(time.Minute))
	must(t, err)
	must(t, s.PreparePanel(ctx, item, &msg))
	if msg.Operation != "send" {
		t.Fatal(msg)
	}
	must(t, s.FinishPanel(ctx, item, "unknown", c.DeliveryResult{}, "", time.Now()))
	got, err := s.GetSession(ctx, bot.ID, 9, 9)
	must(t, err)
	if got.PanelState != "unknown" {
		t.Fatal(got)
	}
}
