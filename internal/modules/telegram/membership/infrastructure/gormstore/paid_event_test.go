package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"path/filepath"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	pd "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"strings"
)

// TestLifecycleQueryDialects verifies the production query builder without opening
// PostgreSQL or reading configuration; SQLite execution is covered by the replay test.
func TestLifecycleQueryDialects(t *testing.T) {
	for _, dialector := range []gorm.Dialector{sqlite.Open(":memory:"), postgres.New(postgres.Config{DSN: "host=unused user=synthetic dbname=synthetic sslmode=disable"})} {
		t.Run(dialector.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := connection.Close(); err != nil {
					t.Error(err)
				}
			})
			query := unfinishedLifecycleQuery(db, d.AccessMember{TargetID: 1, BotID: 2, ChatID: -3, TelegramID: 4})
			var count int64
			if err := query.Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			updated := query.Update("cleanup_cycle_id", 5)
			if updated.Error != nil || strings.Contains(strings.ToUpper(updated.Statement.SQL.String()), " FROM ") {
				t.Fatalf("count contaminated update: %s %v", updated.Statement.SQL.String(), updated.Error)
			}
			var operation d.AccessOperation
			selected := query.Order("id").First(&operation)
			if selected.Error != nil || strings.Contains(strings.ToUpper(selected.Statement.SQL.String()), "COUNT(") {
				t.Fatalf("count contaminated select: %s %v", selected.Statement.SQL.String(), selected.Error)
			}
		})
	}
}

// TestLifecycleExistingTaskCleanupReplay isolates COUNT/UPDATE/SELECT state across
// repeated recovery attempts and preserves the original task's cleanup binding.
func TestLifecycleExistingTaskCleanupReplay(t *testing.T) {
	_, db, audit, _ := refundExecutionFixture(t)
	var member d.AccessMember
	if err := db.Where("target_id = ?", 1).First(&member).Error; err != nil {
		t.Fatal(err)
	}
	var initial d.AccessOperation
	if err := db.Where("kind = ? AND target_id = ?", "lifecycle", 1).First(&initial).Error; err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 3; replay++ {
		var operationID uint
		err := db.Transaction(func(tx *gorm.DB) error { return enqueueLifecycle(tx, member, audit.CreatedAt, &operationID) })
		if err != nil || operationID != initial.ID {
			t.Fatalf("recovery task changed: %d %v", operationID, err)
		}
	}
	var result d.AccessOperation
	if err := db.First(&result, initial.ID).Error; err != nil || result.CleanupCycleID == 0 || result.CleanupCycleID != initial.CleanupCycleID {
		t.Fatalf("cleanup binding: %+v %v", result, err)
	}
	var count int64
	if err := db.Model(&d.AccessOperation{}).Where("kind = ? AND target_id = ?", "lifecycle", 1).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate task: %d %v", count, err)
	}
}

// refundTestFixture creates two real targets, a parent refund, and historical member observations.
// It uses MaxOpenConns=1, exact snapshot hashes, and no network clients.
func refundTestFixture(t *testing.T) (*Store, *gorm.DB, c.RefundSelection) {
	t.Helper()
	db, now := paidEventDB(t)
	models := append(LedgerModels(), LifecycleModels()...)
	models = append(models, AccessModels()...)
	models = append(models, &od.OrderRefundRecord{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	seedPaidEvent(t, db, now, 1, "")
	if err := db.Model(&od.Order{}).Where("id IN ?", []uint{1, 1001}).Updates(map[string]any{"user_id": 7, "refunded_amount": 1}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := d.OrderItemSnapshot{OrderItemID: 1, AccountID: 7, BeneficiaryTelegramID: 9007199254740993, Kind: d.TermPermanent, Quantity: 1, Targets: []d.SnapshotTarget{{TargetID: 1}, {TargetID: 2}}, CreatedAt: now}
	if err := db.Save(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	for _, target := range []uint{1, 2} {
		source := d.LedgerSource{ID: target, OrderItemID: 1, TargetID: target, Operation: "grant", BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, AccountID: 7, OrderID: 1001, PaymentOrderID: 1, PaidAt: now, Kind: d.TermPermanent, SnapshotHash: hash, CreatedAt: now}
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		member := d.AccessMember{TargetID: target, BotID: 9, ChatID: -int64(target), TelegramID: snapshot.BeneficiaryTelegramID, SourceID: target, Status: "member", IsMember: true, ObservedAt: now, RouteVersion: 1}
		if err := db.Create(&member).Error; err != nil {
			t.Fatal(err)
		}
	}
	refund := od.OrderRefundRecord{ID: 1, UserID: 7, OrderID: 1, Type: constants.OrderRefundTypeManual, Amount: money.FromDecimal(decimal.NewFromInt(1)), CreatedAt: now}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&d.RefundNotice{RefundID: 1, OrderID: 1, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	return New(db), db, c.RefundSelection{RefundID: 1, OrderItemID: 1, AccountID: 7, ActorAdminID: 3, ExpectedVersion: 1, SnapshotHash: hash, SourceIDs: []uint{1, 2}}
}

// TestRefundDispositionRejectsUntrustedFacts rejects cross-account, unrelated order and absent local evidence.
func TestRefundDispositionRejectsUntrustedFacts(t *testing.T) {
	scenarios := []struct {
		name   string
		change func(*gorm.DB) error
	}{
		{"cross_account", func(db *gorm.DB) error {
			return db.Model(&od.OrderRefundRecord{}).Where("id = 1").Update("user_id", 99).Error
		}},
		{"unrelated_order", func(db *gorm.DB) error {
			return db.Model(&od.OrderRefundRecord{}).Where("id = 1").Update("order_id", 999).Error
		}},
		{"deleted_refund", func(db *gorm.DB) error {
			return db.Model(&od.OrderRefundRecord{}).Where("id = 1").Update("deleted_at", time.Now()).Error
		}},
		{"unsupported_type", func(db *gorm.DB) error {
			return db.Model(&od.OrderRefundRecord{}).Where("id = 1").Update("type", "upstream").Error
		}},
		{"no_local_amount", func(db *gorm.DB) error {
			return db.Model(&od.Order{}).Where("id = 1").Update("refunded_amount", 0).Error
		}},
		{"no_event", func(db *gorm.DB) error { return db.Where("refund_id = 1").Delete(&d.RefundNotice{}).Error }},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			store, db, input := refundTestFixture(t)
			if err := scenario.change(db); err != nil {
				t.Fatal(err)
			}
			if err := store.DisposeRefund(context.Background(), input); err == nil {
				t.Fatal("untrusted fact accepted")
			}
			var count int64
			if err := db.Model(&d.LedgerRevocation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("revocation leaked", err)
			}
		})
	}
}

// TestRefundDispositionAtomicReplay validates durable per-target queue, idempotence and hostile selection.
func TestRefundDispositionAtomicReplay(t *testing.T) {
	store, db, input := refundTestFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := store.RefundCandidates(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("candidates %v %v", rows, err)
	}
	for _, mutate := range []func(*c.RefundSelection){func(value *c.RefundSelection) { value.AccountID++ }, func(value *c.RefundSelection) { value.ExpectedVersion++ }, func(value *c.RefundSelection) { value.SourceIDs = []uint{1} }, func(value *c.RefundSelection) { value.SnapshotHash = "wrong" }} {
		invalid := input
		mutate(&invalid)
		if err := store.DisposeRefund(ctx, invalid); err == nil {
			t.Fatal("accepted invalid selection")
		}
	}
	for range 2 {
		if err := store.DisposeRefund(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	for model, want := range map[any]int64{&d.LedgerRevocation{}: 2, &d.RefundDisposition{}: 1, &d.AccessOperation{}: 2} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count %d: %v", model, count, err)
		}
	}
}

// TestRefundDispositionRollbackAndCoverage proves whole-purchase rollback and preserved independent coverage.
func TestRefundDispositionRollbackAndCoverage(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "limited", true: "permanent"}[permanent], func(t *testing.T) {
			store, db, input := refundTestFixture(t)
			now := time.Now().UTC()
			kind, months := d.TermMonths, int64(1)
			if permanent {
				kind, months = d.TermPermanent, 0
			}
			for _, target := range []uint{1, 2} {
				other := d.LedgerSource{OrderItemID: 99, TargetID: target, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, OrderID: 999, PaymentOrderID: 999, PaidAt: now, Kind: kind, Months: months, CreatedAt: now}
				if err := db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			fail := errors.New("audit unavailable")
			if err := db.Callback().Create().Before("gorm:create").Register("refund:audit_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "telegram_membership_refund_dispositions" {
					tx.AddError(fail)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.DisposeRefund(context.Background(), input); !errors.Is(err, fail) {
				t.Fatalf("failure %v", err)
			}
			var count int64
			if err := db.Model(&d.LedgerRevocation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("partial revoke", count, err)
			}
			if err := db.Callback().Create().Remove("refund:audit_failure"); err != nil {
				t.Fatal(err)
			}
			if err := store.DisposeRefund(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&d.AccessOperation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("covered member scheduled", count, err)
			}
		})
	}
}

// paidEventDB 创建独立 SQLite 单连接数据库；无联系人、当前商品或任何外部服务。
func paidEventDB(t *testing.T) (*gorm.DB, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "paid.db")), &gorm.Config{})
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
	if err := db.AutoMigrate(&od.Order{}, &od.OrderItem{}, &pd.Payment{}, &d.OrderItemSnapshot{}, &d.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	return db, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
}

// seedPaidEvent 保存已付父子单与只挂真实子项的快照；返回付款引用。
func seedPaidEvent(t *testing.T, db *gorm.DB, now time.Time, id uint, exception string) {
	t.Helper()
	childID := id + 1000
	rows := []any{
		&od.Order{ID: id, OrderNo: "parent-" + now.Add(time.Duration(id)).Format(time.RFC3339Nano), Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &now},
		&od.Order{ID: childID, ParentID: &id, OrderNo: "child-" + now.Add(time.Duration(id)).Format(time.RFC3339Nano), Status: constants.OrderStatusFulfilling, Currency: "CNY", PaidAt: &now},
		&od.OrderItem{ID: id, OrderID: childID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"},
		&d.OrderItemSnapshot{OrderItemID: id, Quantity: 1, Targets: []d.SnapshotTarget{{TargetID: 1}}},
		&pd.Payment{ID: id, OrderID: id, Status: constants.PaymentStatusSuccess, PaidAt: &now, Currency: "CNY", ExceptionCode: exception},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// appendPaidTest 通过真实事务追加事件，确保 SQLite 不回落全局连接。
func appendPaidTest(db *gorm.DB, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		store, err := InTransaction(tx)
		if err != nil {
			return err
		}
		return store.AppendPaid(context.Background(), id, id)
	})
}

// TestPaidEventClaimCannotBypassNewBackoff 验证读取候选后其他工作进程安排的退避不会被旧 CAS 覆盖。
func TestPaidEventClaimCannotBypassNewBackoff(t *testing.T) {
	db, paidAt := paidEventDB(t)
	seedPaidEvent(t, db, paidAt, 1, "")
	if err := appendPaidTest(db, 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)
	injected := false
	const callback = "test:membership-backoff"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
		if injected || query.Statement.Table != "telegram_membership_paid_events" {
			return
		}
		if _, ok := query.Statement.Dest.(*d.PaidEvent); !ok {
			return
		}
		injected = true
		if err := db.Model(&d.PaidEvent{}).Where("id = 1").Updates(map[string]any{"attempts": 1, "available_at": now.Add(time.Hour)}).Error; err != nil {
			query.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Callback().Query().Remove(callback); err != nil {
			t.Error(err)
		}
	}()
	event, err := New(db).ClaimPaid(context.Background(), "stale-candidate", now, time.Minute)
	if err != nil || event != nil || !injected {
		t.Fatalf("stale candidate bypassed backoff: event=%v err=%v injected=%v", event, err, injected)
	}
}

// TestPaidEventSnapshotAppendAndFence 验证历史付款时间、父子去重、无联系人及过期 owner 栅栏。
func TestPaidEventSnapshotAppendAndFence(t *testing.T) {
	db, paidAt := paidEventDB(t)
	seedPaidEvent(t, db, paidAt, 1, "")
	for index := 0; index < 2; index++ {
		if err := appendPaidTest(db, 1); err != nil {
			t.Fatal(err)
		}
	}
	var events []d.PaidEvent
	if err := db.Find(&events).Error; err != nil || len(events) != 1 || !events[0].PaidAt.Equal(paidAt) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	store, ctx := New(db), context.Background()
	now := time.Now().UTC().Add(time.Second)
	first, err := store.ClaimPaid(ctx, "first", now, time.Second)
	if err != nil || first == nil {
		t.Fatalf("claim %v %v", first, err)
	}
	second, err := store.ClaimPaid(ctx, "second", now.Add(2*time.Second), time.Minute)
	if err != nil || second == nil {
		t.Fatalf("reclaim %v %v", second, err)
	}
	if err := store.FinishPaid(ctx, first.ID, "first", now.Add(3*time.Second)); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale ack=%v", err)
	}
	if err := store.RetryPaid(ctx, first.ID, "first", now.Add(3*time.Second), now.Add(time.Hour)); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale retry=%v", err)
	}
	if err := store.FinishPaid(ctx, second.ID, "second", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
}

// TestPaidEventRecoveryAllHistoryAndAnomalies 验证分页补漏无短期窗口、各种异常不授予。
func TestPaidEventRecoveryAllHistoryAndAnomalies(t *testing.T) {
	db, paidAt := paidEventDB(t)
	seedPaidEvent(t, db, paidAt, 1, "")
	seedPaidEvent(t, db, paidAt, 2, constants.PaymentExceptionUnderpaidSucceeded)
	seedPaidEvent(t, db, paidAt, 3, constants.PaymentExceptionMembershipSaleClosed)
	seedPaidEvent(t, db, paidAt, 4, "")
	seedPaidEvent(t, db, paidAt, 5, "")
	if err := db.Model(&od.Order{}).Where("id = ?", 5).Update("status", constants.OrderStatusCanceled).Error; err != nil {
		t.Fatal(err)
	}
	store, ctx := New(db), context.Background()
	after, count, err := store.RecoverPaid(ctx, 0, 1)
	if err != nil || after != 1 || count != 1 {
		t.Fatalf("page1 %d %d %v", after, count, err)
	}
	after, count, err = store.RecoverPaid(ctx, after, 1)
	if err != nil || after != 4 || count != 1 {
		t.Fatalf("page2 %d %d %v", after, count, err)
	}
	_, count, err = store.RecoverPaid(ctx, after, 1)
	if err != nil || count != 0 {
		t.Fatalf("end %d %v", count, err)
	}
	for _, id := range []uint{2, 3, 5} {
		if err := appendPaidTest(db, id); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	if err := db.Model(&d.PaidEvent{}).Count(&total).Error; err != nil || total != 2 {
		t.Fatalf("total %d %v", total, err)
	}
}

// TestPaidEventRecoveryFailureFairness 验证损坏的旧事件不阻塞后续历史页，下一轮仍能重试。
func TestPaidEventRecoveryFailureFairness(t *testing.T) {
	db, paidAt := paidEventDB(t)
	seedPaidEvent(t, db, paidAt, 1, "")
	seedPaidEvent(t, db, paidAt, 2, "")
	if err := db.Create(&d.PaidEvent{OrderID: 1, PaymentID: 1, PaidAt: paidAt.Add(time.Hour), AvailableAt: paidAt}).Error; err != nil {
		t.Fatal(err)
	}
	after, count, err := New(db).RecoverPaid(context.Background(), 0, 2)
	if err == nil || after != 2 || count != 2 {
		t.Fatalf("page %d %d %v", after, count, err)
	}
	var event d.PaidEvent
	if err := db.Where("order_id = ?", 2).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("order_id = ?", 1).Delete(&d.PaidEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(db).RecoverPaid(context.Background(), 0, 2); err != nil {
		t.Fatal(err)
	}
}

// TestPaidEventAppendRollbackAndOrdinary 验证写事件失败回滚本地付款，普通订单不依赖事件表。
func TestPaidEventAppendRollbackAndOrdinary(t *testing.T) {
	db, paidAt := paidEventDB(t)
	seedPaidEvent(t, db, paidAt, 1, "")
	if err := db.Migrator().DropTable(&d.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&pd.Payment{}).Where("id = ?", 1).Update("provider_ref", "rollback-marker").Error; err != nil {
			return err
		}
		store, err := InTransaction(tx)
		if err != nil {
			return err
		}
		return store.AppendPaid(context.Background(), 1, 1)
	})
	if err == nil {
		t.Fatal("missing outbox schema silently accepted")
	}
	var payment pd.Payment
	if err := db.First(&payment, 1).Error; err != nil || payment.ProviderRef != "" {
		t.Fatalf("rollback %v %+v", err, payment)
	}
	if err := db.Delete(&d.OrderItemSnapshot{}, "order_item_id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := appendPaidTest(db, 1); err != nil {
		t.Fatalf("ordinary depends on outbox: %v", err)
	}
	if err := db.Migrator().DropTable(&d.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := appendPaidTest(db, 1); err != nil {
		t.Fatalf("legacy ordinary: %v", err)
	}
}
