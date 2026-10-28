package channelclientstore

import (
	"context"
	"fmt"
	"github.com/XManCodeing/XMall/internal/crypto"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"sync"
	"testing"
	"time"

	channelclientdomain "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:channelclient_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&channelclientdomain.Client{}, &d.BotOwnership{}, &d.Instance{}); err != nil {
		t.Fatalf("migrate channel client failed: %v", err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	if err := db.Create(&d.BotOwnership{ID: 1, State: "lock"}).Error; err != nil {
		t.Fatal(err)
	}
	s := New(db)
	s.SetCredentialSecret("test-ownership")
	return s
}

func TestPersistentHistoryAndReverseOwnership(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seal := func(token string) string {
		v, e := crypto.Encrypt(crypto.DeriveKey("test-ownership"), token)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	r := runtime.New(s.db)
	r.SetCredentialOpener(s.open)
	legacy := &channelclientdomain.Client{Name: "legacy", ChannelType: "telegram_bot", ChannelKey: "legacy", ChannelSecret: "synthetic", BotToken: seal("123:old"), Status: 1}
	if err := s.Create(legacy); err != nil {
		t.Fatal(err)
	}
	stale := *legacy
	legacy.BotToken = seal("123:rotated")
	if err := s.Update(legacy); err != nil {
		t.Fatal("same bot rotation rejected", err)
	}
	legacy.BotToken = ""
	if err := s.Update(legacy); err != nil {
		t.Fatal(err)
	}
	bot := &d.Instance{TelegramID: 123, TokenCipher: seal("123:new"), Mode: "internal"}
	if err := r.Create(ctx, bot); err == nil {
		t.Fatal("clear erased ownership")
	}
	// Pre-v5 ordinary instances must also fail to enable after clearing.
	if err := s.db.Create(bot).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Configure(ctx, bot.ID, bot.Version, map[string]interface{}{"enabled": true}); err == nil {
		t.Fatal("cleared history enabled old ordinary instance")
	}
	if err := s.db.Delete(bot).Error; err != nil {
		t.Fatal(err)
	}
	bot.ID = 0
	if err := s.Update(&stale); err == nil {
		t.Fatal("stale save accepted")
	}
	legacy.BotToken = seal("456:other")
	if err := s.Update(legacy); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, bot); err == nil {
		t.Fatal("replacement erased old identity")
	}
	if err := s.Delete(legacy.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, bot); err == nil {
		t.Fatal("delete erased ownership")
	}
	internal := &d.Instance{TelegramID: 789, TokenCipher: seal("789:internal"), Mode: "internal"}
	if err := r.Create(ctx, internal); err != nil {
		t.Fatal(err)
	}
	reverse := &channelclientdomain.Client{Name: "reverse", ChannelType: "telegram_bot", ChannelKey: "reverse", ChannelSecret: "synthetic", BotToken: seal("789:rotated"), Status: 1}
	if err := s.Create(reverse); err == nil {
		t.Fatal("reverse create accepted")
	}
	reverse.ID = 0
	reverse.BotToken = seal("999:free")
	if err := s.Create(reverse); err != nil {
		t.Fatal(err)
	}
	reverse.BotToken = seal("789:rotated")
	if err := s.Update(reverse); err == nil {
		t.Fatal("reverse update accepted")
	}
}

func TestCutoverFencesLegacyAndStaleCredentials(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cipher, _ := crypto.Encrypt(crypto.DeriveKey("test-ownership"), "123:old")
	legacy := &channelclientdomain.Client{Name: "legacy", ChannelType: "telegram_bot", ChannelKey: "cutover", ChannelSecret: "old", BotToken: cipher, Status: 1}
	if err := s.Create(legacy); err != nil {
		t.Fatal(err)
	}
	r := runtime.New(s.db)
	r.SetCredentialOpener(s.open)
	bot, err := r.ImportLegacy(ctx, &d.Instance{TelegramID: 123, ChannelClientID: &legacy.ID, TokenCipher: cipher, Version: 1}, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.SwitchCutover(ctx, bot.ID, bot.Version, "cutover_pending"); err != nil {
		t.Fatal(err)
	}
	if credential, err := s.FindByChannelKey(legacy.ChannelKey); err != nil || credential != nil {
		t.Fatal("pending channel credential resolved", err)
	}
	if err = s.Update(legacy); err == nil {
		t.Fatal("pending channel allowed")
	}
	legacy.Status = 0
	legacy.ChannelSecret = "revoked"
	if err = s.Update(legacy); err != nil {
		t.Fatal(err)
	}
	if err = r.SwitchCutover(ctx, bot.ID, bot.Version+1, "internal"); err != nil {
		t.Fatal(err)
	}
	legacy.Status = 1
	if err = s.Update(legacy); err == nil {
		t.Fatal("old channel regained permissions")
	}
}

func TestConcurrentReverseCreateSerializesOwnership(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		s := newTestStore(t)
		cipher, _ := crypto.Encrypt(crypto.DeriveKey("test-ownership"), "123:race")
		r := runtime.New(s.db)
		r.SetCredentialOpener(s.open)
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results <- r.Create(context.Background(), &d.Instance{TelegramID: 123, TokenCipher: cipher, Mode: "internal"})
		}()
		go func() {
			defer wg.Done()
			<-start
			results <- s.Create(&channelclientdomain.Client{Name: "race", ChannelType: "telegram_bot", ChannelKey: "race", BotToken: cipher, ChannelSecret: "test", Status: 1})
		}()
		close(start)
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			}
		}
		if success != 1 {
			t.Fatalf("expected exactly one owner, got %d", success)
		}
	}
}

func TestConcurrentLegacyUpdateAndCutover(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		s := newTestStore(t)
		ctx := context.Background()
		key := crypto.DeriveKey("test-ownership")
		cipher, _ := crypto.Encrypt(key, "123:old")
		other, _ := crypto.Encrypt(key, "456:new")
		legacy := &channelclientdomain.Client{Name: "legacy", ChannelType: "telegram_bot", ChannelKey: "race-update", BotToken: cipher, ChannelSecret: "synthetic", Status: 1}
		if err := s.Create(legacy); err != nil {
			t.Fatal(err)
		}
		r := runtime.New(s.db)
		r.SetCredentialOpener(s.open)
		bot, err := r.ImportLegacy(ctx, &d.Instance{TelegramID: 123, ChannelClientID: &legacy.ID, TokenCipher: cipher, Version: 1}, cipher)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; legacy.BotToken = other; results <- s.Update(legacy) }()
		go func() {
			defer wg.Done()
			<-start
			results <- r.SwitchCutover(ctx, bot.ID, bot.Version, "cutover_pending")
		}()
		close(start)
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			}
		}
		if success != 1 {
			t.Fatalf("update and takeover both won or failed: %d", success)
		}
	}
}

func TestUnknownUpgradeHistoryCannotBeReplaced(t *testing.T) {
	s := newTestStore(t)
	row := channelclientdomain.Client{Name: "unknown", ChannelType: "telegram_bot", ChannelKey: "unknown", ChannelSecret: "test", Status: 1}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&d.BotOwnership{ChannelID: row.ID, State: "unresolved"}).Error; err != nil {
		t.Fatal(err)
	}
	row.BotToken, _ = crypto.Encrypt(crypto.DeriveKey("test-ownership"), "123:new")
	if err := s.Update(&row); err == nil {
		t.Fatal("new credential overwrote unknown history")
	}
	r := runtime.New(s.db)
	r.SetCredentialOpener(s.open)
	if err := r.Create(context.Background(), &d.Instance{TelegramID: 123, TokenCipher: row.BotToken, Mode: "internal"}); err == nil {
		t.Fatal("unknown history failed open")
	}
	if err := s.RepairTelegramHistory(row.ID, row.Revision, []int64{123}); err == nil {
		t.Fatal("active history repair accepted")
	}
	original, _ := s.FindByID(row.ID)
	original.Status = 0
	if err := s.Update(original); err != nil {
		t.Fatal(err)
	}
	if err := s.RepairTelegramHistory(row.ID, original.Revision, []int64{123, 456}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(context.Background(), &d.Instance{TelegramID: 123, TokenCipher: row.BotToken, Mode: "internal"}); err == nil {
		t.Fatal("attested history not reserved")
	}
}

func TestStorePreservesSoftDeleteAndLastUsedSemantics(t *testing.T) {
	store := newTestStore(t)
	client := &channelclientdomain.Client{
		Name:          "Telegram Bot",
		ChannelType:   "generic",
		ChannelKey:    "channel-key",
		ChannelSecret: "ciphertext",
		Status:        1,
	}
	if err := store.Create(client); err != nil {
		t.Fatalf("create client failed: %v", err)
	}

	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.UpdateLastUsed(client.ID, usedAt); err != nil {
		t.Fatalf("update last-used failed: %v", err)
	}
	active, err := store.FindActiveByChannelType("generic")
	if err != nil {
		t.Fatalf("find active client failed: %v", err)
	}
	if active == nil || active.LastUsedAt == nil || !active.LastUsedAt.Equal(usedAt) {
		t.Fatalf("unexpected active client: %#v", active)
	}

	deletedAt := time.Now().UTC()
	if err := store.Delete(client.ID, deletedAt); err != nil {
		t.Fatalf("soft delete client failed: %v", err)
	}
	byID, err := store.FindByID(client.ID)
	if err != nil {
		t.Fatalf("find soft-deleted client failed: %v", err)
	}
	if byID != nil {
		t.Fatalf("soft-deleted client must be hidden: %#v", byID)
	}
	items, err := store.FindAll()
	if err != nil {
		t.Fatalf("list clients failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("soft-deleted client must be absent from list: %#v", items)
	}
}
