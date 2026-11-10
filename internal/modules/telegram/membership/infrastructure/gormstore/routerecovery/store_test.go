package routerecovery

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fixture creates only synthetic local SQLite tables, including minimal boundary projections.
func fixture(t *testing.T) (*Store, *gorm.DB, Request) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	models := append(Models(), &d.InviteCleanupCycle{}, &d.InviteCleanupItem{}, &d.AccessInvite{}, &d.AccessResult{}, &d.AccessOperation{}, &d.AccessJoin{}, &d.AccessMember{}, &d.LedgerSource{}, &d.LedgerProjection{}, &d.LedgerRevocation{}, &d.IdentityMigration{})
	must(t, db.AutoMigrate(models...))
	for _, sql := range []string{
		"CREATE TABLE telegram_bot_instances (id integer PRIMARY KEY, generation integer, mode text, enabled boolean, state text)",
		"CREATE TABLE telegram_group_targets (id integer PRIMARY KEY, recovery_task_id integer, purpose text, enabled boolean, bot_instance_id integer, telegram_chat_id integer, route_version integer, version integer)",
		"CREATE TABLE telegram_group_routes (target_id integer, bot_instance_id integer, telegram_chat_id integer, route_version integer, retired_at datetime)",
		"CREATE TABLE telegram_bot_deliveries (id integer PRIMARY KEY, bot_id integer, purpose text, status text)",
		"INSERT INTO telegram_bot_instances VALUES (1, 7, 'internal', 1, 'running')",
		"INSERT INTO telegram_group_targets VALUES (2, 3, 'membership', 1, 1, -20, 5, 9)",
		"INSERT INTO telegram_group_routes VALUES (2, 1, -10, 4, '2026-01-01'), (2, 1, -20, 5, NULL)",
	} {
		must(t, db.Exec(sql).Error)
	}
	store, err := New(db)
	must(t, err)
	return store, db, Request{TaskID: 3, TargetID: 2, BotID: 1, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 7, Purpose: "membership"}
}

// must fails immediately for err, keeping test fixtures explicit and deterministic.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestEmptyScopeRequiresCompleteFence verifies that zero records never bypass scope checks.
func TestEmptyScopeRequiresCompleteFence(t *testing.T) {
	for name, mutate := range map[string]string{
		"generation": "UPDATE telegram_bot_instances SET generation = 8",
		"task":       "UPDATE telegram_group_targets SET recovery_task_id = 9",
		"purpose":    "UPDATE telegram_group_targets SET purpose = 'support'",
		"route":      "UPDATE telegram_group_targets SET route_version = 6",
		"history":    "DELETE FROM telegram_group_routes WHERE route_version = 4",
	} {
		t.Run(name, func(t *testing.T) {
			store, db, request := fixture(t)
			must(t, db.Exec(mutate).Error)
			result, err := store.RecoverRoute(context.Background(), request)
			if err == nil || result.Verified {
				t.Fatalf("scope passed: %+v %v", result, err)
			}
		})
	}
}

// TestDurableRescan preserves unknowns through restart and only accepts actual closed evidence.
func TestDurableRescan(t *testing.T) {
	store, db, request := fixture(t)
	now := time.Now().UTC()
	invite := d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "active", ExpiresAt: now.Add(time.Hour)}
	operation := d.AccessOperation{ID: 1, BusinessKey: "synthetic:1", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, Kind: "create", State: "unknown"}
	member := d.AccessMember{TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, TelegramID: 42, SourceID: 1, Status: "member", IsMember: true, ObservedAt: now, ObservationSource: "query"}
	must(t, db.Create(&invite).Error)
	must(t, db.Create(&operation).Error)
	must(t, db.Create(&member).Error)
	result, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if result.Verified || result.Remaining != 2 || result.Unknown != 1 {
		t.Fatalf("optimistic result: %+v", result)
	}
	store, err = New(db)
	must(t, err)
	must(t, db.Model(&invite).Update("state", "revoked").Error)
	must(t, db.Model(&operation).Update("state", "failed").Error)
	must(t, db.Model(&member).Updates(map[string]any{"status": "left", "is_member": false}).Error)
	result, err = store.RecoverRoute(context.Background(), request)
	must(t, err)
	if !result.Verified {
		t.Fatalf("closed evidence not resumed: %+v", result)
	}
	var count int64
	must(t, db.Model(&Evidence{}).Count(&count).Error)
	if count != 3 {
		t.Fatalf("lost evidence: %d", count)
	}
}

// TestStableTargetLateSourceAndExpiry proves immutable purchase facts and original term reuse.
func TestStableTargetLateSourceAndExpiry(t *testing.T) {
	store, db, request := fixture(t)
	first, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if !first.Verified {
		t.Fatal(first)
	}
	paid := time.Now().UTC().AddDate(0, 0, -10)
	source := d.LedgerSource{ID: 1, TargetID: 2, OrderID: 20, OrderItemID: 21, Operation: "grant", AccountID: 9, BeneficiaryTelegramID: 42, PaidAt: paid, Kind: d.TermMonths, Months: 1}
	must(t, db.Create(&source).Error)
	must(t, db.Create(&d.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 1}).Error)
	result, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if !result.Verified {
		t.Fatal(result)
	}
	var proof Evidence
	must(t, db.First(&proof, "kind = ?", "source").Error)
	if proof.ExpiresAt == nil || !proof.ExpiresAt.Equal(paid.Add(30*24*time.Hour)) || proof.OrderItemID != 21 || proof.TargetID != 2 {
		t.Fatalf("term reset: %+v", proof)
	}
	var saved d.LedgerSource
	must(t, db.First(&saved, 1).Error)
	if saved.TargetID != source.TargetID || saved.BeneficiaryTelegramID != source.BeneficiaryTelegramID || !saved.PaidAt.Equal(paid) {
		t.Fatal("immutable source changed")
	}
}

// TestConcurrentIdempotency verifies one durable run and CAS-safe rescans using one connection.
func TestConcurrentIdempotency(t *testing.T) {
	store, db, request := fixture(t)
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.RecoverRoute(context.Background(), request)
			if err != nil || !result.Verified {
				t.Errorf("recovery: %+v %v", result, err)
			}
		}()
	}
	wait.Wait()
	var count int64
	must(t, db.Model(&Run{}).Count(&count).Error)
	if count != 1 {
		t.Fatalf("runs: %d", count)
	}
}
