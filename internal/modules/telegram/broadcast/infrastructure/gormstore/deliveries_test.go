package broadcaststore

import (
	"context"
	"errors"
	"testing"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	b "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
)

func TestRetryDeliveryAllowlistAndScope(t *testing.T) {
	s := setupStoreTest(t)
	if err := s.db.AutoMigrate(&d.Delivery{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job := &b.Broadcast{BotID: 7, DeliveryVersion: 1, Status: "failed", FailedCount: 1}
	if err := s.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := []struct {
		name, status, detail string
		message              int64
		claim                string
		until                *time.Time
		allowed              bool
	}{
		{"rejection", "failed", "telegram rejected message", 0, "", nil, true},
		{"unknown", "unknown", "telegram rejected message", 0, "", nil, false},
		{"sent", "sent", "telegram rejected message", 0, "", nil, false},
		{"processing", "processing", "telegram rejected message", 0, "", nil, false},
		{"pending", "pending", "telegram rejected message", 0, "", nil, false},
		{"retry", "retry", "telegram rejected message", 0, "", nil, false},
		{"cancelled", "cancelled", "telegram rejected message", 0, "", nil, false},
		{"authorization", "failed", "recipient authorization failed", 0, "", nil, false},
		{"stamp", "failed", "recipient binding changed", 0, "", nil, false},
		{"payload", "failed", "message payload unavailable", 0, "", nil, false},
		{"unrecognized", "failed", "something else", 0, "", nil, false},
		{"message_exists", "failed", "telegram rejected message", 123, "", nil, false},
		{"claimed", "failed", "telegram rejected message", 0, "owner", nil, false},
		{"lease", "failed", "telegram rejected message", 0, "", &now, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := d.Delivery{BotID: 7, BroadcastID: job.ID, Key: tc.name, Status: tc.status, LastError: tc.detail, MessageID: tc.message, Claim: tc.claim, ClaimedUntil: tc.until, Attempts: 8, PayloadCipher: "original-stamp-cipher"}
			if err := s.db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			for _, scope := range [][3]uint{{8, job.ID, row.ID}, {7, job.ID + 1, row.ID}, {7, job.ID, row.ID + 1000}} {
				if err := s.RetryBroadcastDelivery(ctx, scope[0], scope[1], scope[2]); !errors.Is(err, app.ErrNotFound) {
					t.Fatalf("scope: %v", err)
				}
			}
			err := s.RetryBroadcastDelivery(ctx, 7, job.ID, row.ID)
			if tc.allowed && err != nil {
				t.Fatal(err)
			}
			if !tc.allowed && !errors.Is(err, app.ErrRetryDenied) {
				t.Fatalf("want denial: %v", err)
			}
			var got d.Delivery
			if err := s.db.First(&got, row.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.PayloadCipher != row.PayloadCipher || got.Attempts != 8 || got.Key != row.Key {
				t.Fatal("immutable delivery data changed")
			}
			if tc.allowed {
				if got.Status != "retry" {
					t.Fatal(got.Status)
				}
				if err := s.RetryBroadcastDelivery(ctx, 7, job.ID, row.ID); !errors.Is(err, app.ErrRetryDenied) {
					t.Fatalf("duplicate retry: %v", err)
				}
				job, _ := s.GetByID(context.Background(), job.ID)
				if job.Status != "running" || job.CompletedAt != nil || job.FailedCount != 0 {
					t.Fatal(job)
				}
			} else if got.Status != row.Status || got.LastError != row.LastError {
				t.Fatal("denied row changed")
			}
		})
	}
}

func TestRetryAfterPayloadCleanupDenied(t *testing.T) {
	s := setupStoreTest(t)
	if err := s.db.AutoMigrate(&d.Delivery{}, &d.Inbox{}, &d.Session{}); err != nil {
		t.Fatal(err)
	}
	job := &b.Broadcast{BotID: 7, DeliveryVersion: 1, Status: "failed", FailedCount: 1}
	if err := s.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	row := d.Delivery{BotID: 7, BroadcastID: job.ID, Key: "expired-body", Status: "failed", LastError: "telegram rejected message", PayloadCipher: "cipher", UpdatedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := runtimestore.New(s.db).Cleanup(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if err := s.db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.PayloadCipher != "" || d.CanRetryBroadcastDelivery(row) {
		t.Fatal("cleaned payload remains retryable")
	}
	if err := s.RetryBroadcastDelivery(context.Background(), 7, job.ID, row.ID); !errors.Is(err, app.ErrRetryDenied) {
		t.Fatalf("retry: %v", err)
	}
	got, err := s.GetByID(context.Background(), job.ID)
	if err != nil || got.Status != "failed" || got.FailedCount != 1 {
		t.Fatalf("job changed: %+v %v", got, err)
	}
}

func TestRetryCASMissDoesNotUpdateJob(t *testing.T) {
	s := setupStoreTest(t)
	if err := s.db.AutoMigrate(&d.Delivery{}); err != nil {
		t.Fatal(err)
	}
	job := &b.Broadcast{BotID: 7, DeliveryVersion: 1, Status: "failed", FailedCount: 1}
	if err := s.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	row := d.Delivery{BotID: 7, BroadcastID: job.ID, Key: "cleanup-race", Status: "failed", LastError: "telegram rejected message", PayloadCipher: "cipher"}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	// Inject cleanup after the application read and before its conditional UPDATE.
	if err := s.db.Exec("CREATE TRIGGER clear_before_retry BEFORE UPDATE OF status ON " + row.TableName() + " WHEN NEW.status = 'retry' BEGIN UPDATE " + row.TableName() + " SET payload_cipher = '' WHERE id = OLD.id; SELECT RAISE(IGNORE); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RetryBroadcastDelivery(context.Background(), 7, job.ID, row.ID); !errors.Is(err, app.ErrRetryDenied) {
		t.Fatalf("cleanup race accepted: %v", err)
	}
	got, err := s.GetByID(context.Background(), job.ID)
	if err != nil || got.Status != "failed" || got.FailedCount != 1 {
		t.Fatalf("job changed: %+v %v", got, err)
	}
}

func TestRetryRollsBackWhenJobUpdateFails(t *testing.T) {
	s := setupStoreTest(t)
	if err := s.db.AutoMigrate(&d.Delivery{}); err != nil {
		t.Fatal(err)
	}
	job := &b.Broadcast{BotID: 7, DeliveryVersion: 1, Status: "failed"}
	if err := s.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	row := d.Delivery{BotID: 7, BroadcastID: job.ID, Key: "rollback", PayloadCipher: "cipher", Status: "failed", LastError: "telegram rejected message"}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("CREATE TRIGGER reject_job BEFORE UPDATE ON " + job.TableName() + " BEGIN SELECT RAISE(ABORT, 'injected'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RetryBroadcastDelivery(context.Background(), 7, job.ID, row.ID); err == nil {
		t.Fatal("expected error")
	}
	var got d.Delivery
	if err := s.db.First(&got, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatal("partial retry committed")
	}
}

func TestDeliveryPaginationSeparatesBotAndBroadcast(t *testing.T) {
	s := setupStoreTest(t)
	if err := s.db.AutoMigrate(&d.Delivery{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []d.Delivery{
		{BotID: 7, BroadcastID: 1, Key: "a"}, {BotID: 7, BroadcastID: 1, Key: "b"},
		{BotID: 8, BroadcastID: 1, Key: "otherbot"}, {BotID: 7, BroadcastID: 2, Key: "otherjob"},
	} {
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := runtimestore.New(s.db)
	for page := 1; page <= 3; page++ {
		rows, total, err := runtime.ListDeliveries(context.Background(), 7, 1, page, 1)
		if err != nil || total != 2 {
			t.Fatalf("total=%d err=%v", total, err)
		}
		if page < 3 && (len(rows) != 1 || rows[0].BotID != 7 || rows[0].BroadcastID != 1) {
			t.Fatal(rows)
		}
		if page == 3 && len(rows) != 0 {
			t.Fatal(rows)
		}
	}
}
