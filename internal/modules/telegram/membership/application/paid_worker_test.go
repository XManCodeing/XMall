package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// testPaidLedger 模拟真实 ledger 的来源幂等性，并记录传入付款时刻。
type testPaidLedger struct {
	grants map[uint]c.PaidGrant
	calls  int
	fail   uint
}

// GrantPaid 在 fail 指定付款模拟失败，其余按 PaymentID 幂等；返回冲突或模拟错误。
func (ledger *testPaidLedger) GrantPaid(_ context.Context, grant c.PaidGrant) error {
	ledger.calls++
	if grant.PaymentID == ledger.fail {
		return errors.New("local ledger unavailable")
	}
	if previous, ok := ledger.grants[grant.PaymentID]; ok && previous != grant {
		return c.ErrConflict
	}
	ledger.grants[grant.PaymentID] = grant
	return nil
}

// ackFailureStore 模拟授予已提交后确认失败；不改变原租约以支持 TTL 恢复。
type ackFailureStore struct {
	c.PaidEventStore
	fail bool
}

// FinishPaid 在首次 ack 时模拟进程失联，其余转发真实持久化实现。
func (store *ackFailureStore) FinishPaid(ctx context.Context, id uint, owner string, now time.Time) error {
	if store.fail {
		store.fail = false
		return errors.New("ack unavailable")
	}
	return store.PaidEventStore.FinishPaid(ctx, id, owner, now)
}

// workerFixture 创建 SQLite 单连接 outbox 与可控调度时钟，不装配销售、Telegram 或联系人。
func workerFixture(t *testing.T) (*gorm.DB, *s.Store, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "worker.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&d.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	return db, s.New(db), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// TestPaidWorkerCrashReplayKeepsPaidAt 验证先授予后 ack 失败的安全重放，停售/未启动不影响本地权益。
func TestPaidWorkerCrashReplayKeepsPaidAt(t *testing.T) {
	db, store, now := workerFixture(t)
	paidAt := now.AddDate(-2, 0, 0)
	event := d.PaidEvent{OrderID: 1, PaymentID: 1, PaidAt: paidAt, AvailableAt: now}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	ledger := &testPaidLedger{grants: map[uint]c.PaidGrant{}}
	broken := &ackFailureStore{PaidEventStore: store, fail: true}
	worker, err := a.NewPaidWorker(broken, ledger, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(context.Background(), 10); err == nil || count != 0 {
		t.Fatalf("first %d %v", count, err)
	}
	now = now.Add(2 * time.Minute)
	if count, err := worker.RunOnce(context.Background(), 10); err != nil || count != 1 {
		t.Fatalf("replay %d %v", count, err)
	}
	if ledger.calls != 2 || len(ledger.grants) != 1 || !ledger.grants[1].PaidAt.Equal(paidAt) {
		t.Fatalf("grants %+v calls %d", ledger.grants, ledger.calls)
	}
	if count, err := worker.RunOnce(context.Background(), 10); err != nil || count != 0 {
		t.Fatalf("completed replay %d %v", count, err)
	}
}

// TestPaidWorkerFailureDoesNotStarve 验证首条永久失败不能饿死后页，恢复成功沿用原付款时点。
func TestPaidWorkerFailureDoesNotStarve(t *testing.T) {
	db, store, now := workerFixture(t)
	for id := uint(1); id <= 3; id++ {
		if err := db.Create(&d.PaidEvent{OrderID: id, PaymentID: id, PaidAt: now.AddDate(-1, 0, 0), AvailableAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ledger := &testPaidLedger{grants: map[uint]c.PaidGrant{}, fail: 1}
	worker, err := a.NewPaidWorker(store, ledger, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(context.Background(), 2)
	if err == nil || count != 1 {
		t.Fatalf("first %d %v", count, err)
	}
	count, err = worker.RunOnce(context.Background(), 2)
	if err != nil || count != 1 {
		t.Fatalf("later %d %v", count, err)
	}
	ledger.fail = 0
	now = now.Add(time.Minute)
	count, err = worker.RunOnce(context.Background(), 2)
	if err != nil || count != 1 || len(ledger.grants) != 3 {
		t.Fatalf("recover %d %v", count, err)
	}
}
