package channelclientstore

import (
	"fmt"
	"sync"
	"testing"
	"time"

	channelclientapp "github.com/XManCodeing/XMall/internal/modules/channelclient/application"
	channelclientdomain "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	"github.com/XManCodeing/XMall/internal/upstream"
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
	// Channel CRUD and authentication do not need any runtime tables.
	if err := db.AutoMigrate(&channelclientdomain.Client{}); err != nil {
		t.Fatalf("migrate channel client failed: %v", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	return New(db)
}

func TestStorePreservesSoftDeleteAndLastUsedSemantics(t *testing.T) {
	store := newTestStore(t)
	client := &channelclientdomain.Client{
		Name: "Generic channel", ChannelType: "generic", ChannelKey: "channel-key",
		ChannelSecret: "ciphertext", Status: 1,
	}
	if err := store.Create(client); err != nil {
		t.Fatal(err)
	}
	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.UpdateLastUsed(client.ID, usedAt); err != nil {
		t.Fatal(err)
	}
	active, err := store.FindActiveByChannelType("generic")
	if err != nil || active == nil || active.LastUsedAt == nil || !active.LastUsedAt.Equal(usedAt) {
		t.Fatalf("unexpected active client: %#v, %v", active, err)
	}
	if err := store.Delete(client.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, lookup := range []func() (*channelclientdomain.Client, error){
		func() (*channelclientdomain.Client, error) { return store.FindByID(client.ID) },
		func() (*channelclientdomain.Client, error) { return store.FindByChannelKey(client.ChannelKey) },
		func() (*channelclientdomain.Client, error) { return store.FindActiveByChannelType("generic") },
	} {
		if row, err := lookup(); err != nil || row != nil {
			t.Fatalf("soft-deleted client must be hidden: %#v, %v", row, err)
		}
	}
	if items, err := store.FindAll(); err != nil || len(items) != 0 {
		t.Fatalf("soft-deleted client must be absent: %#v, %v", items, err)
	}
	if err := store.Update(client); err == nil {
		t.Fatal("stale update resurrected deleted client")
	}
	if err := store.UpdateLastUsed(client.ID, usedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var deleted channelclientdomain.Client
	if err := store.db.First(&deleted, client.ID).Error; err != nil {
		t.Fatal(err)
	}
	if deleted.Status != 0 || deleted.Revision != client.Revision+1 || !deleted.LastUsedAt.Equal(usedAt) {
		t.Fatalf("deleted client changed unexpectedly: %#v", deleted)
	}
}

func TestChannelUpdateRejectsStaleRevisionWithoutOwnershipLock(t *testing.T) {
	store := newTestStore(t)
	client := &channelclientdomain.Client{Name: "initial", ChannelType: "generic", ChannelKey: "revision", ChannelSecret: "secret", Status: 1}
	if err := store.Create(client); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		copy := *client
		copy.Name = name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- store.Update(&copy)
		}()
	}
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
		t.Fatalf("expected one revision update, got %d", success)
	}
	current, err := store.FindByID(client.ID)
	if err != nil || current == nil || current.Revision != 2 {
		t.Fatal(current, err)
	}
	current.ChannelType = "other"
	if err := store.Update(current); err == nil {
		t.Fatal("channel type changed")
	}
}

func TestChannelAuthenticationAndSecretRotationWithoutRuntimeSchema(t *testing.T) {
	store := newTestStore(t)
	service := channelclientapp.NewService(store, "isolated-test-secret")
	for _, channelType := range []string{"generic", "telegram_bot"} {
		t.Run(channelType, func(t *testing.T) {
			client, err := service.CreateChannelClient("channel", channelType, "", "123:test-only", "")
			if err != nil {
				t.Fatal(err)
			}
			verify := func(secret string) error {
				timestamp := time.Now().Unix()
				body := []byte(`{"test":true}`)
				signature := upstream.Sign(secret, "POST", "/api/v1/channel/test", timestamp, body)
				_, err := service.VerifyChannelSignature(client.ChannelKey, signature, timestamp, "POST", "/api/v1/channel/test", body)
				return err
			}
			if err := verify(client.ChannelSecret); err != nil {
				t.Fatal(err)
			}
			rotated, err := service.ResetChannelClientSecret(client.ID)
			if err != nil || rotated.Status != 1 {
				t.Fatal(rotated, err)
			}
			if err := verify(client.ChannelSecret); err == nil {
				t.Fatal("old signature accepted")
			}
			if err := verify(rotated.ChannelSecret); err != nil {
				t.Fatal(err)
			}
			if err := service.UpdateChannelClientStatus(client.ID, 0); err != nil {
				t.Fatal(err)
			}
			if err := verify(rotated.ChannelSecret); err != channelclientapp.ErrDisabled {
				t.Fatal("disabled channel authenticated", err)
			}
		})
	}
}
