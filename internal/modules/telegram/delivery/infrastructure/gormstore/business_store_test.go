package gormstore

import (
	"errors"
	"fmt"
	identitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestCurrentSourceRequiresUnambiguousPrivateContact(t *testing.T) {
	for _, scenario := range []string{"valid", "no_contact", "two_bots", "revoked", "disabled_user", "group", "unreachable", "wrong_user", "disabled_bot", "external_bot", "two_identities", "specified_bot"} {
		t.Run(scenario, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:source_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { sqlDB.Close() })
			if err := db.AutoMigrate(&userdomain.User{}, &identitydomain.Identity{}, &runtimedomain.Contact{}, &runtimedomain.Instance{}, &delivery.Event{}); err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			user := userdomain.User{ID: 1, Status: "active"}
			must(db.Create(&user).Error)
			must(db.Create(&identitydomain.Identity{UserID: 1, Provider: "telegram", ProviderUserID: "8"}).Error)
			must(db.Create(&runtimedomain.Instance{ID: 7, TelegramID: 77, Enabled: true, Mode: "internal"}).Error)
			contact := runtimedomain.Contact{BotID: 7, UserID: 1, TelegramUserID: 8, ChatID: 8, Reachable: true}
			switch scenario {
			case "group":
				contact.ChatID = -8
			case "unreachable":
				contact.Reachable = false
			case "wrong_user":
				contact.UserID = 2
			}
			if scenario != "no_contact" {
				must(db.Create(&contact).Error)
			}
			var requested uint
			if scenario == "two_bots" || scenario == "specified_bot" {
				must(db.Create(&runtimedomain.Instance{ID: 9, TelegramID: 99, Enabled: true, Mode: "internal"}).Error)
				must(db.Create(&runtimedomain.Contact{BotID: 9, UserID: 1, TelegramUserID: 8, ChatID: 8, Reachable: true}).Error)
				if scenario == "specified_bot" {
					requested = 7
				}
			}
			if scenario == "disabled_bot" {
				must(db.Model(&runtimedomain.Instance{}).Where("id = 7").Update("enabled", false).Error)
			}
			if scenario == "external_bot" {
				must(db.Model(&runtimedomain.Instance{}).Where("id = 7").Update("mode", "external").Error)
			}
			if scenario == "two_identities" {
				must(db.Create(&identitydomain.Identity{UserID: 1, Provider: "telegram", ProviderUserID: "10"}).Error)
			}
			rollback := errors.New("rollback")
			err = db.Transaction(func(tx *gorm.DB) error {
				// Revocation must be visible within this transaction, not through
				// a global connection (which would deadlock this one-connection DB).
				if scenario == "revoked" {
					if err := tx.Where("user_id = 1").Delete(&identitydomain.Identity{}).Error; err != nil {
						return err
					}
				}
				if scenario == "disabled_user" {
					if err := tx.Model(&userdomain.User{}).Where("id = 1").Update("status", "disabled").Error; err != nil {
						return err
					}
				}
				store := NewBusinessStore(tx)
				source, err := store.ResolveCurrentSource(1, requested)
				if err != nil {
					return err
				}
				want := scenario == "valid" || scenario == "specified_bot"
				if (source != nil) != want {
					t.Fatalf("source=%+v want destination=%v", source, want)
				}
				if source != nil {
					if source.BotID != 7 || source.TelegramID != 8 || source.PrivateChatID != 8 {
						t.Fatalf("source=%+v", source)
					}
					if err := store.Append(&delivery.Event{EventKey: "current", Kind: delivery.EventOrderPaid, UserID: 1, BotID: source.BotID, TelegramID: source.TelegramID, PrivateChatID: source.PrivateChatID}); err != nil {
						return err
					}
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			var count int64
			must(db.Model(&delivery.Event{}).Count(&count).Error)
			if count != 0 {
				t.Fatalf("event escaped rollback: %d", count)
			}
		})
	}
}

func TestBusinessEventsRollbackDedupAndLeases(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:events_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&delivery.OrderOperation{}, &delivery.OrderSource{}, &delivery.Event{}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback")
	err = db.Transaction(func(tx *gorm.DB) error {
		store := NewBusinessStore(tx)
		if _, _, err := store.ReserveOperation("key", "hash", 1); err != nil {
			return err
		}
		if err := store.SaveSource(4, 1, delivery.Source{BotID: 7, TelegramID: 8, PrivateChatID: 8}); err != nil {
			return err
		}
		if err := store.Append(&delivery.Event{EventKey: "fulfillment:4", Kind: delivery.EventOrderFulfilled, OrderID: 4, FulfillmentID: 6, UserID: 1, BotID: 7, TelegramID: 8, PrivateChatID: 8}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, model := range []any{&delivery.OrderOperation{}, &delivery.OrderSource{}, &delivery.Event{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rollback %T: %d %v", model, count, err)
		}
	}
	store := NewBusinessStore(db)
	for i := 0; i < 2; i++ {
		if err := store.Append(&delivery.Event{EventKey: "fulfillment:4", Kind: delivery.EventOrderFulfilled, OrderID: 4, FulfillmentID: 6, UserID: 1, BotID: 7, TelegramID: 8, PrivateChatID: 8}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Second)
	rows, err := store.Scan(now, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("scan %d %v", len(rows), err)
	}
	id := rows[0].ID
	if ok, err := store.Claim(id, "first", now, time.Minute); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, err := store.Claim(id, "second", now, time.Minute); ok || err != nil {
		t.Fatalf("double claim %v %v", ok, err)
	}
	later := now.Add(2 * time.Minute)
	if ok, err := store.Claim(id, "second", later, time.Minute); !ok || err != nil {
		t.Fatalf("reclaim %v %v", ok, err)
	}
	if err := store.MarkDelivered(id, "first", later); !errors.Is(err, delivery.ErrLeaseLost) {
		t.Fatalf("stale owner %v", err)
	}
	if err := store.MarkDelivered(id, "second", later); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if err := store.Append(&delivery.Event{EventKey: fmt.Sprintf("other:%d", i), Kind: delivery.EventOrderPaid, UserID: 1, BotID: 8, TelegramID: 8, PrivateChatID: 8}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append(&delivery.Event{EventKey: "bot7:ready", Kind: delivery.EventOrderPaid, UserID: 1, BotID: 7, TelegramID: 8, PrivateChatID: 8}); err != nil {
		t.Fatal(err)
	}
	botRows, err := store.ScanBot(7, time.Now().Add(time.Second), 1)
	if err != nil || len(botRows) != 1 || botRows[0].BotID != 7 {
		t.Fatalf("bot starvation: %v %v", botRows, err)
	}
	if err := db.Where("event_key <> ?", "fulfillment:4").Delete(&delivery.Event{}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = store.Scan(later.Add(time.Hour), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("delivered scanned %v %v", rows, err)
	}
}
