package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	channel "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setup(t *testing.T) (*Store, *gorm.DB, context.Context, *d.Instance) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if err := db.WithContext(ctx).AutoMigrate(&d.BotOwnership{}, &channel.Client{}, &d.Instance{}, &d.Contact{}, &d.Inbox{}, &d.Session{}, &d.Delivery{}); err != nil {
		t.Fatal(err)
	}
	must(t, db.Create(&d.BotOwnership{ID: 1, State: "lock"}).Error)
	s := New(db)
	bot := &d.Instance{TelegramID: 123, TokenCipher: "cipher", Enabled: true, Mode: "internal"}
	must(t, s.Create(ctx, bot))
	return s, db, ctx, bot
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

type ownershipAPI struct {
	c.API
	id            int64
	beforeWebhook func()
}

func (a ownershipAPI) Identity(context.Context, string) (c.BotIdentity, error) {
	return c.BotIdentity{ID: a.id}, nil
}
func (a ownershipAPI) Webhook(context.Context, string) (string, error) {
	if a.beforeWebhook != nil {
		a.beforeWebhook()
	}
	return "", nil
}

func TestLegacyOwnershipBlocksOrdinaryPathsAndAllowsExplicitCutover(t *testing.T) {
	s, db, ctx, bot := setup(t)
	manager := app.NewManager(s, ownershipAPI{id: 123}, "ownership-test-key")
	cipher, err := manager.Seal("123:obsolete-token")
	must(t, err)
	source := channel.Client{Name: "legacy", ChannelKey: "test-channel", ChannelType: "telegram_bot", ChannelSecret: "unused", BotToken: cipher}
	must(t, db.Create(&source).Error)
	must(t, db.Create(&d.BotOwnership{ChannelID: source.ID, State: "unresolved", SnapshotCipher: source.BotToken}).Error)
	// A formerly created and enabled ordinary instance must not restart.
	_, err = s.BeginGeneration(ctx, bot.ID, bot.Version)
	wantErr(t, err, c.ErrConflict)
	must(t, db.Model(bot).Updates(map[string]interface{}{"enabled": false, "token_cipher": cipher}).Error)
	on := true
	wantErr(t, manager.Configure(ctx, bot.ID, bot.Version, &on, nil), c.ErrConflict)
	wrongCipher, err := manager.Seal("999:different-bot")
	must(t, err)
	wantErr(t, s.Configure(ctx, bot.ID, bot.Version, map[string]interface{}{"token_cipher": wrongCipher}), c.ErrConflict)
	wantErr(t, s.Heartbeat(ctx, bot.ID, bot.Generation, "running", "", bot.Version), c.ErrConflict)
	rotated := "123:rotated-token"
	wantErr(t, manager.Configure(ctx, bot.ID, bot.Version, &on, &rotated), c.ErrConflict)
	for _, fields := range []map[string]interface{}{{"enabled": 1}, {"enabled": "true"}, {"mode": "internal"}, {"channel_client_id": source.ID}, {"telegram_id": int64(456)}, {"cutover_completed": true}} {
		if s.Configure(ctx, bot.ID, bot.Version, fields) == nil {
			t.Fatal("ownership metadata was mutable")
		}
	}
	// Even disabled/deleted channels still reserve their bot's identity.
	must(t, db.Model(&source).Updates(map[string]interface{}{"status": 0, "deleted_at": time.Now()}).Error)
	if _, err := manager.Create(ctx, rotated); err == nil {
		t.Fatal("ordinary create bypassed cutover")
	}
	must(t, db.Model(&source).Update("deleted_at", nil).Error)
	input := &d.Instance{TelegramID: 123, ChannelClientID: &source.ID, TokenCipher: cipher, Version: 1}
	if _, err := s.ImportLegacy(ctx, input, "stale-source"); err == nil {
		t.Fatal("concurrent source replacement accepted")
	}
	imported, err := s.ImportLegacy(ctx, input, cipher)
	must(t, err)
	if imported.ID != bot.ID || imported.Mode != "external" || imported.Enabled || imported.Generation == bot.Generation {
		t.Fatal("existing instance not fenced")
	}
	repeated, err := s.ImportLegacy(ctx, input, cipher)
	must(t, err)
	if repeated.Version != imported.Version {
		t.Fatal("idempotent import changed version")
	}
	wantErr(t, s.Configure(ctx, bot.ID, imported.Version, map[string]interface{}{"enabled": true}), c.ErrConflict)
	must(t, s.SwitchCutover(ctx, bot.ID, imported.Version, "cutover_pending"))
	wantErr(t, s.Configure(ctx, bot.ID, imported.Version+1, map[string]interface{}{"token_cipher": cipher}), c.ErrConflict)
	must(t, s.SwitchCutover(ctx, bot.ID, imported.Version+1, "internal"))
	must(t, manager.Configure(ctx, bot.ID, imported.Version+2, &on, &rotated))
	_, err = s.BeginGeneration(ctx, bot.ID, imported.Version+3)
	must(t, err)
}

func TestOwnershipRechecksAfterNetworkAndConcurrentImport(t *testing.T) {
	s, db, ctx, bot := setup(t)
	manager := app.NewManager(s, ownershipAPI{id: 123}, "ownership-test-key")
	cipher, err := manager.Seal("123:old-token")
	must(t, err)
	must(t, db.Model(bot).Updates(map[string]interface{}{"enabled": false, "token_cipher": cipher}).Error)
	source := channel.Client{Name: "late", ChannelKey: "late-key", ChannelType: "telegram_bot", ChannelSecret: "unused", BotToken: cipher}
	manager.API = ownershipAPI{id: 123, beforeWebhook: func() {
		must(t, db.Create(&source).Error)
		must(t, db.Create(&d.BotOwnership{ChannelID: source.ID, State: "unresolved", SnapshotCipher: source.BotToken}).Error)
	}}
	on := true
	wantErr(t, manager.Configure(ctx, bot.ID, 1, &on, nil), c.ErrConflict)
	manager.API = ownershipAPI{id: 123}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.Create(ctx, "123:new-token"); err == nil {
				t.Error("concurrent create succeeded")
			}
			if err := manager.Configure(ctx, bot.ID, 1, &on, nil); err == nil {
				t.Error("concurrent enable succeeded")
			}
		}()
	}
	_, err = s.ImportLegacy(ctx, &d.Instance{TelegramID: 123, ChannelClientID: &source.ID, TokenCipher: cipher}, cipher)
	must(t, err)
	wg.Wait()
	row, err := s.Get(ctx, bot.ID)
	must(t, err)
	if row.Mode != "external" || row.Enabled || row.ChannelClientID == nil {
		t.Fatal("concurrent ordinary operation escaped import")
	}
}

func TestCutoverAndOrdinaryEnableShareVersionFence(t *testing.T) {
	s, db, ctx, bot := setup(t)
	manager := app.NewManager(s, ownershipAPI{id: 123}, "ownership-test-key")
	cipher, err := manager.Seal("123:old")
	must(t, err)
	source := channel.Client{Name: "legacy", ChannelKey: "race-cutover", ChannelType: "telegram_bot", ChannelSecret: "unused", BotToken: cipher}
	must(t, db.Create(&source).Error)
	must(t, db.Create(&d.BotOwnership{ChannelID: source.ID, State: "unresolved", SnapshotCipher: source.BotToken}).Error)
	row, err := s.ImportLegacy(ctx, &d.Instance{TelegramID: 123, ChannelClientID: &source.ID, TokenCipher: cipher}, cipher)
	must(t, err)
	must(t, s.SwitchCutover(ctx, bot.ID, row.Version, "cutover_pending"))
	version := row.Version + 1
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := s.Configure(ctx, bot.ID, version, map[string]interface{}{"enabled": true}); err == nil {
				t.Error("enable raced through pending cutover")
			}
			if err := s.Configure(ctx, bot.ID, version, map[string]interface{}{"token_cipher": cipher}); err == nil {
				t.Error("rotation raced through pending cutover")
			}
		}()
	}
	close(start)
	must(t, s.SwitchCutover(ctx, bot.ID, version, "internal"))
	wg.Wait()
	row, err = s.Get(ctx, bot.ID)
	must(t, err)
	if row.Enabled || !row.CutoverCompleted || row.Version != version+1 {
		t.Fatal("unsafe cutover race outcome")
	}
}

func TestOwnershipFailsClosedAndUsesIdentityNotCredentialEquality(t *testing.T) {
	s, db, ctx, _ := setup(t)
	manager := app.NewManager(s, ownershipAPI{id: 456}, "ownership-test-key")
	cipher, err := crypto.Encrypt(crypto.DeriveKey("ownership-test-key"), "456:revoked")
	must(t, err)
	source := channel.Client{Name: "legacy", ChannelKey: "identity-key", ChannelType: "telegram_bot", ChannelSecret: "unused", BotToken: cipher}
	must(t, db.Create(&source).Error)
	must(t, db.Create(&d.BotOwnership{ChannelID: source.ID, State: "unresolved", SnapshotCipher: source.BotToken}).Error)
	if _, err := manager.Create(ctx, "456:replacement"); err == nil {
		t.Fatal("rotated token evaded identity ownership")
	}
	manager.API = ownershipAPI{id: 789}
	if _, err := manager.Create(ctx, "789:new-bot"); err != nil {
		t.Fatal("unrelated bot blocked", err)
	}
	must(t, db.Model(&d.BotOwnership{}).Where("channel_id = ? AND telegram_id = 0", source.ID).Updates(map[string]interface{}{"state": "unresolved", "snapshot_cipher": "unreadable-test-cipher"}).Error)
	if _, err := manager.Create(ctx, "999:unknown"); err == nil {
		t.Fatal("unreadable ownership accepted")
	}
}

func TestGenerationAndConfigurationCAS(t *testing.T) {
	s, _, ctx, bot := setup(t)
	must(t, s.Configure(ctx, bot.ID, 1, map[string]interface{}{"username": "new"}))
	wantErr(t, s.Configure(ctx, bot.ID, 1, map[string]interface{}{"enabled": false}), c.ErrConflict)
	_, err := s.BeginGeneration(ctx, bot.ID, 1)
	wantErr(t, err, c.ErrStale)
	first, err := s.BeginGeneration(ctx, bot.ID, 2)
	must(t, err)
	second, err := s.BeginGeneration(ctx, bot.ID, 2)
	must(t, err)
	if second.Generation != first.Generation+1 {
		t.Fatal("generation not incremented")
	}
	wantErr(t, s.Heartbeat(ctx, bot.ID, first.Generation, "running", "", 2), c.ErrStale)
	must(t, s.Heartbeat(ctx, bot.ID, second.Generation, "running", "https://api.telegram.org/bot123:secret/sendMessage", 2))
	got, err := s.Get(ctx, bot.ID)
	must(t, err)
	if strings.Contains(got.LastError, "secret") {
		t.Fatal("secret leaked")
	}
	must(t, s.Configure(ctx, bot.ID, 2, map[string]interface{}{"enabled": false}))
	_, err = s.BeginGeneration(ctx, bot.ID, 3)
	wantErr(t, err, c.ErrStale)
	must(t, s.Configure(ctx, bot.ID, 3, map[string]interface{}{"enabled": false}))
	_, err = s.BeginGeneration(ctx, bot.ID, 4)
	wantErr(t, err, c.ErrStale)
	rows, err := s.List(ctx)
	must(t, err)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	_, err = s.Get(ctx, 999)
	wantErr(t, err, c.ErrNotFound)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.List(canceled)
	wantErr(t, err, context.Canceled)
}
func TestAcceptAtomicDedupAndStale(t *testing.T) {
	s, db, ctx, bot := setup(t)
	gen, err := s.BeginGeneration(ctx, bot.ID, 1)
	must(t, err)
	must(t, s.Accept(ctx, bot.ID, gen.Generation, []d.Inbox{{UpdateID: 2, PayloadCipher: "original"}, {UpdateID: 1, PayloadCipher: "one"}, {UpdateID: 2, PayloadCipher: "duplicate"}}, 3))
	must(t, s.Accept(ctx, bot.ID, gen.Generation, []d.Inbox{{UpdateID: 2, PayloadCipher: "overwrite"}}, 2))
	var count int64
	must(t, db.Model(&d.Inbox{}).Count(&count).Error)
	if count != 2 {
		t.Fatal(count)
	}
	var row d.Inbox
	must(t, db.Where("update_id = ?", 2).First(&row).Error)
	if row.PayloadCipher != "original" {
		t.Fatal("duplicate overwritten")
	}
	got, err := s.Get(ctx, bot.ID)
	must(t, err)
	if got.Offset != 3 {
		t.Fatal(got.Offset)
	}
	_, err = s.BeginGeneration(ctx, bot.ID, 1)
	must(t, err)
	wantErr(t, s.Accept(ctx, bot.ID, gen.Generation, []d.Inbox{{UpdateID: 4}}, 5), c.ErrStale)
	// Fail after one insert: neither that insert nor the offset may commit.
	must(t, db.Exec("CREATE TRIGGER reject_update BEFORE INSERT ON telegram_bot_inbox WHEN NEW.update_id = 6 BEGIN SELECT RAISE(ABORT, 'injected'); END").Error)
	if err := s.Accept(ctx, bot.ID, gen.Generation+1, []d.Inbox{{UpdateID: 5}, {UpdateID: 6}}, 7); err == nil {
		t.Fatal("expected failure")
	}
	got, err = s.Get(ctx, bot.ID)
	must(t, err)
	if got.Offset != 3 {
		t.Fatal("offset committed on failure")
	}
	must(t, db.Model(&d.Inbox{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("partial inbox committed")
	}
}
func TestInboxOrderedClaimAndReclaim(t *testing.T) {
	s, db, ctx, bot := setup(t)
	must(t, s.Accept(ctx, bot.ID, 0, []d.Inbox{{UpdateID: 20}, {UpdateID: 10}}, 21))
	until := time.Now().Add(time.Minute)
	row, err := s.ClaimInbox(ctx, bot.ID, "first", until)
	must(t, err)
	if row == nil || row.UpdateID != 10 || row.Attempts != 1 {
		t.Fatalf("first: %+v", row)
	}
	blocked, err := s.ClaimInbox(ctx, bot.ID, "blocked", until)
	must(t, err)
	if blocked != nil {
		t.Fatal("overtook processing")
	}
	must(t, db.Model(row).Update("claimed_until", time.Now().UTC().Add(-time.Second)).Error)
	reclaimed, err := s.ClaimInbox(ctx, bot.ID, "second", until)
	must(t, err)
	if reclaimed == nil || reclaimed.ID != row.ID || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim: %+v", reclaimed)
	}
	wantErr(t, s.FinishInbox(ctx, row.ID, "first", "done", "", time.Time{}), c.ErrStale)
	must(t, s.FinishInbox(ctx, row.ID, "second", "retry", "secret", until))
	blocked, err = s.ClaimInbox(ctx, bot.ID, "blocked", until)
	must(t, err)
	if blocked != nil {
		t.Fatal("overtook delayed retry")
	}
	must(t, db.Model(row).Update("available_at", time.Now().UTC().Add(-time.Second)).Error)
	reclaimed, err = s.ClaimInbox(ctx, bot.ID, "third", until)
	must(t, err)
	if reclaimed == nil {
		t.Fatal("retry not claimed")
	}
	must(t, s.FinishInbox(ctx, row.ID, "third", "done", "", time.Time{}))
	next, err := s.ClaimInbox(ctx, bot.ID, "next", until)
	must(t, err)
	if next == nil || next.UpdateID != 20 {
		t.Fatalf("next: %+v", next)
	}
}
func TestContactUpsertSessionCAS(t *testing.T) {
	s, _, ctx, bot := setup(t)
	contact, err := s.Contact(ctx, bot.ID, 1)
	must(t, err)
	if contact != nil {
		t.Fatal(contact)
	}
	must(t, s.SaveContact(ctx, &d.Contact{BotID: bot.ID, TelegramUserID: 1, ChatID: 10, Reachable: true, UserID: 4}))
	must(t, s.SaveContact(ctx, &d.Contact{BotID: bot.ID, TelegramUserID: 1, ChatID: 11, Reachable: false, Locale: "zh"}))
	contact, err = s.Contact(ctx, bot.ID, 1)
	must(t, err)
	if contact.ChatID != 11 || contact.Reachable || contact.UserID != 0 {
		t.Fatal(contact)
	}
	row := &d.Session{BotID: bot.ID, TelegramUserID: 1, ChatID: 11, State: "start"}
	must(t, s.SaveSession(ctx, row, 0))
	if row.Version != 1 {
		t.Fatal(row)
	}
	duplicate := &d.Session{BotID: bot.ID, TelegramUserID: 1, ChatID: 11}
	wantErr(t, s.SaveSession(ctx, duplicate, 0), c.ErrConflict)
	row.State = "next"
	must(t, s.SaveSession(ctx, row, 1))
	wantErr(t, s.SaveSession(ctx, row, 1), c.ErrConflict)
	got, err := s.GetSession(ctx, bot.ID, 1, 11)
	must(t, err)
	if got.Version != 2 || got.State != "next" {
		t.Fatal(got)
	}
}
func TestDeliveryDedupPriorityUnknownAndClaimFence(t *testing.T) {
	s, db, ctx, bot := setup(t)
	low := &d.Delivery{BotID: bot.ID, Key: "low", Priority: 1}
	high := &d.Delivery{BotID: bot.ID, Key: "high", Priority: 10}
	must(t, s.Enqueue(ctx, low))
	must(t, s.Enqueue(ctx, high))
	must(t, s.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "high", Priority: 100, PayloadCipher: "overwrite"}))
	rows, total, err := s.ListDeliveries(ctx, bot.ID, 0, 1, 20)
	must(t, err)
	if total != 2 || len(rows) != 2 {
		t.Fatal(total)
	}
	until := time.Now().Add(time.Minute)
	claimed, err := s.ClaimDelivery(ctx, bot.ID, "first", until)
	must(t, err)
	if claimed == nil || claimed.ID != high.ID || claimed.Priority != 10 {
		t.Fatalf("claim: %+v", claimed)
	}
	must(t, db.Model(claimed).Update("claimed_until", time.Now().UTC().Add(-time.Second)).Error)
	next, err := s.ClaimDelivery(ctx, bot.ID, "second", until)
	must(t, err)
	if next == nil || next.ID != low.ID {
		t.Fatalf("next: %+v", next)
	}
	var unknown d.Delivery
	must(t, db.First(&unknown, high.ID).Error)
	if unknown.Status != "unknown" || unknown.Claim != "" {
		t.Fatal(unknown)
	}
	wantErr(t, s.FinishDelivery(ctx, high.ID, "first", "sent", 42, "", time.Time{}), c.ErrStale)
	wantErr(t, s.FinishDelivery(ctx, low.ID, "wrong", "sent", 42, "", time.Time{}), c.ErrStale)
	must(t, s.FinishDelivery(ctx, low.ID, "second", "retry", 0, "bot-token-secret", time.Now().UTC().Add(-time.Second)))
	next, err = s.ClaimDelivery(ctx, bot.ID, "third", until)
	must(t, err)
	if next == nil || next.ID != low.ID || next.Attempts != 2 {
		t.Fatalf("retry: %+v", next)
	}
	if strings.Contains(next.LastError, "secret") {
		t.Fatal("error leaked")
	}
	must(t, s.FinishDelivery(ctx, low.ID, "third", "sent", 42, "", time.Time{}))
	next, err = s.ClaimDelivery(ctx, bot.ID, "fourth", until)
	must(t, err)
	if next != nil {
		t.Fatal("unknown resent")
	}
}
