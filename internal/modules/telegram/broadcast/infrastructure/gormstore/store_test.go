package broadcaststore

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcastcontract "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/contract"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupStoreTest(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:telegram_broadcast_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&broadcastdomain.Broadcast{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return New(db)
}

// TestRecoveryFenceRejectsBroadcastWrites verifies the real transaction-bound guard.
// It uses one connection and preserves existing jobs when create/update/retry are blocked.
func TestRecoveryFenceRejectsBroadcastWrites(t *testing.T) {
	repo := setupStoreTest(t)
	if err := repo.db.AutoMigrate(&runtimedomain.Instance{}); err != nil {
		t.Fatal(err)
	}
	connection, err := repo.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	instance := runtimedomain.Instance{ID: 1, Version: 1, Mode: "internal", State: "stopped"}
	if err := repo.db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	repo.WithBotRecoveryGuard(func(tx *gorm.DB, bot uint) error {
		return runtimestore.New(tx).GuardRecoveryWrite(ctx, bot)
	})
	job := &broadcastdomain.Broadcast{BotID: 1, Title: "original", Status: "pending"}
	if err := repo.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := runtimestore.New(repo.db).BlockRecovery(ctx, runtimecontract.RecoveryFence{BotID: 1, TaskID: 91, Version: 1}); err != nil {
		t.Fatal(err)
	}
	job.Title = "must not persist"
	for name, operation := range map[string]func() error{
		"create": func() error { return repo.Create(ctx, &broadcastdomain.Broadcast{BotID: 1}) },
		"update": func() error { return repo.Update(ctx, job) },
		"retry":  func() error { return repo.RetryBroadcastDelivery(ctx, 1, job.ID, 1) },
	} {
		if err := operation(); !errors.Is(err, runtimecontract.ErrRecoveryBlocked) {
			t.Fatalf("%s bypassed fence: %v", name, err)
		}
	}
	actual, err := repo.GetByID(ctx, job.ID)
	if err != nil || actual == nil || actual.Title != "original" {
		t.Fatalf("historical job changed: %+v %v", actual, err)
	}
	legacy := &broadcastdomain.Broadcast{Title: "legacy", Status: "pending"}
	if err := repo.db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacy.Status = "needs_review"
	if err := repo.Update(ctx, legacy); err != nil {
		t.Fatalf("legacy quarantine blocked: %v", err)
	}
	// Changing the request's owner cannot evade the original Bot's fence.
	job.BotID = 0
	if err := repo.Update(ctx, job); err == nil {
		t.Fatal("broadcast ownership mutation accepted")
	}
}

func TestStoreListPagination(t *testing.T) {
	repo := setupStoreTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	items := []broadcastdomain.Broadcast{
		{
			Title:         "Spring Promo",
			RecipientType: constants.TelegramBroadcastRecipientTypeAll,
			Status:        constants.TelegramBroadcastStatusCompleted,
			MessageHTML:   "<b>1</b>",
			CreatedAt:     now.Add(-2 * time.Hour),
			UpdatedAt:     now.Add(-2 * time.Hour),
		},
		{
			Title:         "VIP Users",
			RecipientType: constants.TelegramBroadcastRecipientTypeSpecific,
			Status:        constants.TelegramBroadcastStatusFailed,
			MessageHTML:   "<b>2</b>",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}
	for i := range items {
		if err := repo.Create(context.Background(), &items[i]); err != nil {
			t.Fatalf("create broadcast failed: %v", err)
		}
	}
	deletedAt := now.Add(time.Hour)
	deleted := broadcastdomain.Broadcast{
		Title:         "Deleted",
		RecipientType: constants.TelegramBroadcastRecipientTypeAll,
		Status:        constants.TelegramBroadcastStatusCompleted,
		MessageHTML:   "<b>deleted</b>",
		DeletedAt:     &deletedAt,
	}
	if err := repo.Create(context.Background(), &deleted); err != nil {
		t.Fatalf("create deleted broadcast fixture failed: %v", err)
	}

	rows, total, err := repo.List(context.Background(), broadcastcontract.ListFilter{
		Page:     1,
		PageSize: 1,
	})
	if err != nil {
		t.Fatalf("list broadcasts failed: %v", err)
	}
	if total != 2 || len(rows) != 1 {
		t.Fatalf("unexpected filter result: total=%d rows=%d", total, len(rows))
	}
	if rows[0].Title != "VIP Users" {
		t.Fatalf("unexpected broadcast title: %s", rows[0].Title)
	}
	deletedRow, err := repo.GetByID(context.Background(), deleted.ID)
	if err != nil {
		t.Fatalf("get deleted broadcast failed: %v", err)
	}
	if deletedRow != nil {
		t.Fatalf("soft-deleted broadcast must be excluded: %#v", deletedRow)
	}
}

// Hold the only connection until after cancellation is observed. A missing
// WithContext must not pass just because the connection eventually becomes free.
func TestStoreAndRecoveryCancelConnectionWait(t *testing.T) {
	for _, name := range []string{"create", "get", "list", "update", "reconcile", "process"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", name, deadline), func(t *testing.T) {
				repo := setupStoreTest(t)
				job := &broadcastdomain.Broadcast{Status: "pending", Title: "original"}
				if err := repo.Create(context.Background(), job); err != nil {
					t.Fatal(err)
				}
				db, err := repo.db.DB()
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				conn, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
				}
				defer cancel()
				waits := db.Stats().WaitCount
				done := make(chan error, 1)
				go func() {
					svc := app.NewRuntimeService(repo, nil, nil, nil)
					switch name {
					case "create":
						done <- repo.Create(ctx, &broadcastdomain.Broadcast{Title: "must not persist"})
					case "get":
						_, err := repo.GetByID(ctx, job.ID)
						done <- err
					case "list":
						_, _, err := repo.List(ctx, broadcastcontract.ListFilter{})
						done <- err
					case "update":
						copy := *job
						copy.Title = "must not persist"
						done <- repo.Update(ctx, &copy)
					case "reconcile":
						done <- svc.Reconcile(ctx)
					case "process":
						done <- svc.ProcessBroadcast(ctx, job.ID)
					}
				}()
				until := time.Now().Add(2 * time.Second)
				for db.Stats().WaitCount == waits {
					if time.Now().After(until) {
						t.Fatal("operation never waited for the occupied connection")
					}
					runtime.Gosched()
				}
				want := context.DeadlineExceeded
				if !deadline {
					cancel()
					want = context.Canceled
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("want %v, got %v", want, err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("database wait ignored cancellation")
				}
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				rows, total, err := repo.List(context.Background(), broadcastcontract.ListFilter{})
				if err != nil || total != 1 || rows[0].Title != "original" || rows[0].Status != "pending" {
					t.Fatalf("cancelled operation changed persisted data: %+v total=%d err=%v", rows, total, err)
				}
			})
		}
	}
}
