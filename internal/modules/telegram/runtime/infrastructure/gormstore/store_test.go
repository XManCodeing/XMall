package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
	if err := db.WithContext(ctx).AutoMigrate(&d.Instance{}, &d.Contact{}, &d.Inbox{}, &d.Session{}, &d.Delivery{}); err != nil {
		t.Fatal(err)
	}
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

type identityAPI struct {
	c.API
	id int64
}

func (a identityAPI) Identity(context.Context, string) (c.BotIdentity, error) {
	return c.BotIdentity{ID: a.id}, nil
}
func (a identityAPI) Webhook(context.Context, string) (string, error) { return "", nil }

func TestNativeInstanceIgnoresLegacyTablesAndHistory(t *testing.T) {
	for _, state := range []string{"legacy", "unresolved", "pending", "internal"} {
		t.Run(state, func(t *testing.T) {
			s, db, ctx, _ := setup(t)
			// Deliberately no ownership lock row. Unreadable historical credentials
			// and even multiple former owners cannot reserve a native identity.
			must(t, db.Exec("CREATE TABLE channel_clients (id INTEGER PRIMARY KEY, bot_token TEXT, deleted_at DATETIME)").Error)
			must(t, db.Exec("INSERT INTO channel_clients VALUES (1, 'unreadable', NULL)").Error)
			must(t, db.Exec("CREATE TABLE telegram_bot_ownership (id INTEGER PRIMARY KEY, channel_id INTEGER, telegram_id INTEGER, state TEXT, snapshot_cipher TEXT, lock_version INTEGER)").Error)
			must(t, db.Exec("INSERT INTO telegram_bot_ownership VALUES (2, 1, 456, ?, 'unreadable', 0), (3, 2, 456, ?, 'unreadable', 0)", state, state).Error)
			manager := app.NewManager(s, identityAPI{id: 456}, "native-test-key")
			bot, err := manager.Create(ctx, "456:new-token")
			must(t, err)
			on := true
			rotated := "456:rotated-token"
			must(t, manager.Configure(ctx, bot.ID, bot.Version, &on, &rotated))
			generation, err := s.BeginGeneration(ctx, bot.ID, bot.Version+1)
			must(t, err)
			must(t, s.Heartbeat(ctx, bot.ID, generation.Generation, "running", "", generation.Version))
			_, err = manager.Create(ctx, "456:another-token")
			wantErr(t, err, c.ErrDuplicate)
			wantErr(t, err, c.ErrConflict)
			var unchanged int64
			must(t, db.Table("telegram_bot_ownership").Where("state = ? AND snapshot_cipher = ? AND lock_version = 0", state, "unreadable").Count(&unchanged).Error)
			if unchanged != 2 {
				t.Fatal("native lifecycle mutated legacy history")
			}
		})
	}
}

func TestConcurrentNativeCreationPreservesTelegramIdentity(t *testing.T) {
	s, db, ctx, _ := setup(t)
	manager := app.NewManager(s, identityAPI{id: 456}, "native-test-key")
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := manager.Create(ctx, "456:new-token")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			wantErr(t, err, c.ErrDuplicate)
			wantErr(t, err, c.ErrConflict)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful creates = %d, want 1", succeeded)
	}
	var count int64
	must(t, db.Model(&d.Instance{}).Where("telegram_id = ?", 456).Count(&count).Error)
	if count != 1 {
		t.Fatalf("native instances = %d, want 1", count)
	}
}

func TestNativeConfigurationProtectsIdentityAndVersion(t *testing.T) {
	s, _, ctx, bot := setup(t)
	manager := app.NewManager(s, identityAPI{id: 123}, "native-test-key")
	token := "123:valid-token"
	must(t, manager.Configure(ctx, bot.ID, bot.Version, nil, &token))
	wrongCipher, err := manager.Seal("999:different-bot")
	must(t, err)
	wantErr(t, s.Configure(ctx, bot.ID, bot.Version+1, map[string]interface{}{"token_cipher": wrongCipher}), c.ErrConflict)
	for _, fields := range []map[string]interface{}{{"enabled": 1}, {"enabled": "true"}, {"mode": "internal"}, {"telegram_id": int64(456)}} {
		if s.Configure(ctx, bot.ID, bot.Version+1, fields) == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			results <- s.Configure(ctx, bot.ID, bot.Version+1, map[string]interface{}{"enabled": true})
		}()
	}
	close(start)
	succeeded := 0
	for range 8 {
		if err := <-results; err == nil {
			succeeded++
		} else {
			wantErr(t, err, c.ErrConflict)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful configuration writes = %d, want 1", succeeded)
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
