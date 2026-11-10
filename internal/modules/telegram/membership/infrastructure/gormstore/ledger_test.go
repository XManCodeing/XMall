package gormstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	ud "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	pd "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	wd "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TestAccessTaskPurchaseProvenance verifies exact purchase roots, buyer string precision,
// and empty buyer identity for unknown or alien applicants; no current binding is consulted.
func TestAccessTaskPurchaseProvenance(t *testing.T) {
	harness, ctx := accessSetup(t)
	seedAccessPurchases(t, harness)
	all, total, err := harness.service.AccessTasksPage(ctx, 9, 1, 10)
	if err != nil || total != 4 || len(all) != 4 {
		t.Fatalf("all: %+v %d %v", all, total, err)
	}
	for index, expected := range []string{"USER-PARENT", "USER-NEWER", "", ""} {
		buyer := ""
		if expected != "" {
			buyer = "9007199254740993"
		}
		if all[index].OrderNo != expected || all[index].ChatTitle != "实际群名称" || all[index].BuyerTelegramID != buyer {
			t.Fatalf("provenance %d: %+v", index, all[index])
		}
	}
	filtered, total, err := harness.service.AccessTasksPage(ctx, 9, 1, 1, "USER-PARENT")
	if err != nil || total != 1 || len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("filter: %+v %d %v", filtered, total, err)
	}
	filtered, total, err = harness.store.AccessTasksPage(ctx, 9, 2, 1, "USER-PARENT")
	if err != nil || total != 1 || len(filtered) != 0 {
		t.Fatalf("offset before filter: %+v %d %v", filtered, total, err)
	}
	for _, filter := range []string{"INTERNAL-CHILD", "USER-%", "missing"} {
		filtered, total, err = harness.store.AccessTasksPage(ctx, 9, 1, 10, filter)
		if err != nil || total != 0 || len(filtered) != 0 {
			t.Fatalf("unexpected filter %s: %+v %d %v", filter, filtered, total, err)
		}
	}
	encoded, err := json.Marshal(all[0])
	if err != nil || !strings.Contains(string(encoded), `"buyer_telegram_id":"9007199254740993"`) || !strings.Contains(string(encoded), `"telegram_id":"9007199254740993"`) || !strings.Contains(string(encoded), `"chat_id":"-1001"`) {
		t.Fatalf("unsafe IDs: %s %v", encoded, err)
	}
	encoded, err = json.Marshal(all[3])
	if err != nil || !strings.Contains(string(encoded), `"buyer_telegram_id":""`) || !strings.Contains(string(encoded), `"telegram_id":"9007199254740994"`) {
		t.Fatalf("applicant mistaken for buyer: %s %v", encoded, err)
	}
	cursor, err := harness.service.ListAccessTasks(ctx, 9, 1, 1)
	if err != nil || len(cursor) != 1 || cursor[0].OrderNo != "USER-NEWER" || cursor[0].BuyerTelegramID != "9007199254740993" {
		t.Fatalf("cursor: %+v %v", cursor, err)
	}
	if _, _, err := harness.service.AccessTasksPage(ctx, 9, 1, 10, "one", "two"); err != c.ErrInvalid {
		t.Fatalf("repeated filter: %v", err)
	}
	if err := harness.db.Model(&d.LedgerSource{}).Where("id = 11").Update("payment_order_id", 20).Error; err != nil {
		t.Fatal(err)
	}
	corrupt, err := harness.store.AccessTask(ctx, 9, 1)
	if err != nil || corrupt.OrderNo != "" || corrupt.BuyerTelegramID != "" {
		t.Fatalf("unrelated payment root leaked: %+v %v", corrupt, err)
	}
	filtered, total, err = harness.store.AccessTasksPage(ctx, 8, 1, 10, "USER-NEWER")
	if err != nil || total != 0 || len(filtered) != 0 {
		t.Fatalf("cross-bot filter: %+v %d %v", filtered, total, err)
	}
}

// seedAccessPurchases inserts synthetic immutable purchase evidence for harness.
// t receives fixture failures; no live business records or external services are accessed.
func seedAccessPurchases(t *testing.T, harness *accessHarness) {
	t.Helper()
	parentID := uint(10)
	rows := []any{
		&od.Order{ID: 10, OrderNo: "USER-PARENT", UserID: 7},
		&od.Order{ID: 11, OrderNo: "INTERNAL-CHILD", ParentID: &parentID, UserID: 7},
		&od.Order{ID: 20, OrderNo: "USER-NEWER", UserID: 7},
		&od.OrderItem{ID: 11, OrderID: 11, TitleJSON: jsonmap.JSON{}}, &od.OrderItem{ID: 20, OrderID: 20, TitleJSON: jsonmap.JSON{}},
		&d.OrderItemSnapshot{OrderItemID: 11, AccountID: 7, BeneficiaryTelegramID: harness.identity.TelegramID},
		&d.OrderItemSnapshot{OrderItemID: 20, AccountID: 7, BeneficiaryTelegramID: harness.identity.TelegramID},
		&d.LedgerSource{ID: 11, OrderItemID: 11, OrderID: 11, PaymentOrderID: 10, AccountID: 7, BeneficiaryTelegramID: harness.identity.TelegramID, TargetID: 1, Operation: "grant"},
		&d.LedgerSource{ID: 20, OrderItemID: 20, OrderID: 20, PaymentOrderID: 20, AccountID: 7, BeneficiaryTelegramID: harness.identity.TelegramID, TargetID: 1, Operation: "grant"},
	}
	for _, row := range rows {
		if err := harness.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := harness.db.Model(&gd.Target{}).Where("id = 1").Updates(map[string]any{"name": "逻辑会员群", "obs_title": "实际群名称"}).Error; err != nil {
		t.Fatal(err)
	}
	for index, source := range []uint{11, 20, 0, 11} {
		invite := d.AccessInvite{ID: uint(index + 1), AccountID: 7, TelegramID: harness.identity.TelegramID, TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, SourceID: source}
		operation := d.AccessOperation{ID: uint(index + 1), BusinessKey: fmt.Sprint("task-order:", index), Kind: "create", InviteID: invite.ID, AccountID: 7, TelegramID: invite.TelegramID, TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1}
		if index == 3 { // An alien applicant cannot inherit the invitation owner's purchase.
			operation.TelegramID++
			operation.Kind = "decline"
		}
		if err := harness.db.Create(&invite).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Create(&operation).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestAccessTaskMissingPurchaseAndChangedRoute preserves operation IDs and logical labels.
// Missing purchase evidence never falls back to the original task subject as buyer.
func TestAccessTaskMissingPurchaseAndChangedRoute(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.Model(&gd.Target{}).Where("id = 1").Updates(map[string]any{"name": "历史逻辑群", "obs_title": "新物理群", "telegram_chat_id": -2002, "route_version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	operation := d.AccessOperation{BusinessKey: "missing-provenance", BotID: 9, TargetID: 1, ChatID: -1001, TelegramID: harness.identity.TelegramID, RouteVersion: 1}
	if err := harness.db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	row, err := harness.store.AccessTask(ctx, 9, operation.ID)
	if err != nil || row.OrderNo != "" || row.BuyerTelegramID != "" || row.ChatTitle != "历史逻辑群" || row.ChatID != -1001 {
		t.Fatalf("historical route: %+v %v", row, err)
	}
}

// TestNumberedAdminPages exercises real COUNT/OFFSET, bot/kind filters and cursor compatibility.
func TestNumberedAdminPages(t *testing.T) {
	h, ctx := accessSetup(t)
	for index := 1; index <= 25; index++ {
		bot, kind := uint(9), "lifecycle"
		if index > 23 {
			bot = 10
		}
		if index == 23 {
			kind = "create"
		}
		row := d.AccessOperation{BusinessKey: fmt.Sprintf("page:%d", index), BotID: bot, Kind: kind, State: "pending"}
		if err := h.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		join := d.AccessJoin{BotID: bot, UpdateID: int64(index), State: "needs_review"}
		if index == 23 {
			join.State = "succeeded"
		}
		if err := h.db.Create(&join).Error; err != nil {
			t.Fatal(err)
		}
	}
	tasks, total, err := h.service.AccessTasksPage(ctx, 9, 1, 10)
	if err != nil || total != 1 || len(tasks) != 1 || tasks[0].ID != 23 {
		t.Fatalf("tasks=%+v total=%d err=%v", tasks, total, err)
	}
	reviews, total, err := h.service.AccessReviewsPage(ctx, 9, 3, 10)
	if err != nil || total != 22 || len(reviews) != 2 || reviews[0].ID != 21 {
		t.Fatalf("reviews=%+v total=%d err=%v", reviews, total, err)
	}
	lifecycle, total, err := h.store.LifecycleTasksPage(ctx, 9, 3, 10)
	if err != nil || total != 22 || len(lifecycle) != 2 || lifecycle[0].ID != 21 {
		t.Fatalf("lifecycle=%+v total=%d err=%v", lifecycle, total, err)
	}
	empty, total, err := h.store.AccessTasksPage(ctx, 9, 4, 10)
	if err != nil || total != 1 || len(empty) != 0 {
		t.Fatalf("empty=%+v total=%d err=%v", empty, total, err)
	}
	legacy, err := h.service.ListAccessTasks(ctx, 9, 20, 10)
	if err != nil || len(legacy) != 1 || legacy[0].ID != 23 {
		t.Fatalf("cursor=%+v err=%v", legacy, err)
	}
	for _, input := range [][3]int{{0, 1, 10}, {9, 0, 10}, {9, 1, 101}, {9, int(^uint(0) >> 1), 100}} {
		if _, _, err := h.store.AccessTasksPage(ctx, uint(input[0]), input[1], input[2]); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid page: %v", input)
		}
	}
}

// TestLifecycleFilteredPages checks real state-filtered totals, page boundaries and stored observations.
func TestLifecycleFilteredPages(t *testing.T) {
	harness, ctx := accessSetup(t)
	observed := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for index, state := range []string{"pending", "creating", "retryable", "needs_review", "failed", "succeeded"} {
		row := d.AccessOperation{BusinessKey: fmt.Sprintf("filter:%d", index), BotID: 9, Kind: "lifecycle", State: state, MemberStatus: "left", ObservedAt: &observed, BanOutcome: "succeeded", UnbanOutcome: "failed"}
		if err := harness.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []struct {
		status string
		total  int64
	}{{"", 6}, {"processing", 3}, {"needs_review", 2}, {"completed", 1}} {
		rows, total, err := harness.store.LifecycleTasksPage(ctx, 9, 1, 1, spec.status)
		if err != nil || total != spec.total || len(rows) != 1 {
			t.Fatalf("status=%s total=%d rows=%+v err=%v", spec.status, total, rows, err)
		}
		if rows[0].MemberStatus != "left" || !rows[0].ObservedAt.Equal(observed) || rows[0].BanOutcome != "succeeded" || rows[0].UnbanOutcome != "failed" {
			t.Fatalf("lost independent evidence: %+v", rows[0])
		}
		empty, sameTotal, err := harness.store.LifecycleTasksPage(ctx, 9, int(spec.total)+1, 1, spec.status)
		if err != nil || sameTotal != total || len(empty) != 0 {
			t.Fatalf("incorrect filtered offset: %s %v", spec.status, err)
		}
	}
	for _, filter := range [][]string{{"invalid"}, {"processing", "completed"}} {
		if _, _, err := harness.store.LifecycleTasksPage(ctx, 9, 1, 20, filter...); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid filter: %v %v", filter, err)
		}
	}
	rows, total, err := harness.store.LifecycleTasksPage(ctx, 10, 1, 20, "completed")
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("cross-bot rows exposed: %+v %d %v", rows, total, err)
	}
}

// TestAttentionAcrossBotsAndPages uses isolated SQLite and the real runtime receipt projection.
// More than one candidate page proves that a late second-bot failure cannot be hidden by pagination.
func TestAttentionAcrossBotsAndPages(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(&rd.Delivery{}); err != nil {
		t.Fatal(err)
	}
	harness.service.Deliveries = rt.New(harness.db)
	for index := 1; index <= 105; index++ {
		bot := uint(9)
		if index > 100 {
			bot = 10
		}
		invite := d.AccessInvite{ID: uint(index), BotID: bot, TelegramID: int64(index), TargetID: 1, State: "active", ExpiresAt: time.Now().Add(time.Hour)}
		operation := d.AccessOperation{BusinessKey: fmt.Sprintf("summary:%d", index), Kind: "create", InviteID: invite.ID, BotID: bot, State: "succeeded"}
		for _, row := range []any{&invite, &d.AccessSlot{TelegramID: invite.TelegramID, TargetID: 1, InviteID: invite.ID}, &operation} {
			if err := harness.db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	// Latest receipt wins: old failure recovered by a confirmed delivery is no anomaly.
	deliveries := []rd.Delivery{
		{BotID: 9, Key: "membership-invite:1:old", Status: "failed"},
		{BotID: 9, Key: "membership-invite:1:new", Status: "sent", MessageID: 10},
		{BotID: 9, Key: "membership-invite:2:queued", Status: "retry"},
		{BotID: 10, Key: "membership-invite:101:fail", Status: "failed"},
		{BotID: 10, Key: "membership-invite:102:unknown", Status: "sent"},
		{BotID: 10, Key: "membership-invite:103:retired", Status: "failed"},
	}
	if err := harness.db.Create(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&d.AccessInvite{}).Where("id = ?", 103).Update("state", "expired").Error; err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{"pending", "creating", "succeeded", "failed", "unknown", "needs_review", "retryable"} {
		row := d.AccessOperation{BusinessKey: fmt.Sprintf("state:%d", index), BotID: uint(index%2 + 9), Kind: "approve", State: state}
		if err := harness.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	summary, err := harness.service.AccessAttention(ctx)
	if err != nil || summary.AttentionCount != 6 {
		t.Fatalf("attention=%+v err=%v", summary, err)
	}
	if _, err := harness.store.ListAccessTasks(ctx, 0, 0, 20); err == nil {
		t.Fatal("summary must not relax list bot scope")
	}
	rows, err := harness.store.ListAccessTasks(ctx, 9, 0, 20)
	if err != nil || len(rows) != 20 {
		t.Fatalf("list pagination changed: %d %v", len(rows), err)
	}
}

// TestAttentionReviewAndLifecycleExclusions keeps completed reviews and successful terminal work
// excluded even when frozen historical phase/outcome evidence remains present.
func TestAttentionReviewAndLifecycleExclusions(t *testing.T) {
	harness, ctx := accessSetup(t)
	now := time.Now()
	reviews := []d.AccessJoin{
		{BotID: 9, UpdateID: 1, State: "needs_review"},
		{BotID: 10, UpdateID: 2, State: "needs_review"},
		{BotID: 10, UpdateID: 3, State: "needs_review", ReviewedAt: &now},
		{BotID: 10, UpdateID: 4, State: "approved"},
	}
	if err := harness.db.Create(&reviews).Error; err != nil {
		t.Fatal(err)
	}
	operations := []d.AccessOperation{
		{State: "pending", LifecyclePhase: "check"},
		{State: "creating", LifecyclePhase: "ban"},
		{State: "succeeded", LifecyclePhase: "unknown_ban", BanOutcome: "failed", UnbanOutcome: "unknown"},
		{State: "failed"}, {State: "retryable"}, {State: "needs_review"}, {State: "unknown"},
		{State: "pending", LifecyclePhase: "unknown_ban"},
		{State: "pending", LifecyclePhase: "unknown_unban"},
		{State: "pending", BanOutcome: "failed"},
		{State: "creating", UnbanOutcome: "unknown"},
	}
	for index := range operations {
		operations[index].BusinessKey = fmt.Sprintf("lifecycle-summary:%d", index)
		operations[index].Kind = "lifecycle"
		operations[index].BotID = uint(index%2 + 9)
	}
	if err := harness.db.Create(&operations).Error; err != nil {
		t.Fatal(err)
	}
	accessCount, err := harness.store.CountAccessAttention(ctx)
	if err != nil || accessCount != 0 {
		t.Fatalf("lifecycle leaked into admission attention: %d %v", accessCount, err)
	}
	review, err := harness.service.ReviewAttention(ctx)
	if err != nil || review.AttentionCount != 2 {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	lifecycle := &a.Lifecycle{Store: harness.store}
	summary, err := lifecycle.LifecycleAttention(ctx)
	if err != nil || summary.AttentionCount != 8 {
		t.Fatalf("lifecycle=%+v err=%v", summary, err)
	}
	// Successful member removal does not hide failed/unknown historical invitation cleanup.
	if err := harness.db.AutoMigrate(&d.InviteCleanupItem{}); err != nil {
		t.Fatal(err)
	}
	cleanupTask := d.AccessOperation{BusinessKey: "lifecycle-summary:cleanup-only", Kind: "lifecycle", State: "succeeded", CleanupCycleID: 99}
	if err := harness.db.Create(&cleanupTask).Error; err != nil {
		t.Fatal(err)
	}
	items := []d.InviteCleanupItem{{CycleID: 99, InviteID: 1, State: "failed"}, {CycleID: 99, InviteID: 2, State: "unknown"}}
	if err := harness.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	summary, err = lifecycle.LifecycleAttention(ctx)
	if err != nil || summary.AttentionCount != 9 {
		t.Fatalf("cleanup liabilities must count once: %+v err=%v", summary, err)
	}
	if err := harness.db.Model(&d.InviteCleanupItem{}).Where("cycle_id = ?", 99).Update("state", "succeeded").Error; err != nil {
		t.Fatal(err)
	}
	summary, err = lifecycle.LifecycleAttention(ctx)
	if err != nil || summary.AttentionCount != 8 {
		t.Fatalf("settled cleanup must disappear: %+v err=%v", summary, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := lifecycle.LifecycleAttention(cancelled); err == nil {
		t.Fatal("cancelled query returned a misleading count")
	}
}

// ledgerCompletion 用事务内标记模拟系统履约，验证错误发生时标记也一并回滚。
type ledgerCompletion struct {
	tx      *gorm.DB
	failure error
}

// CompleteMembership 使用传入 ctx/items/paidAt 写入事务内测试标记，返回预设失败。
func (completion ledgerCompletion) CompleteMembership(ctx context.Context, items []uint, paidAt time.Time) error {
	for _, item := range items {
		if err := completion.tx.WithContext(ctx).Exec("INSERT INTO ledger_test_completions (item_id, paid_at) VALUES (?, ?) ON CONFLICT(item_id) DO NOTHING", item, paidAt).Error; err != nil {
			return err
		}
	}
	return completion.failure
}

// ledgerSetup 为 t 构造可信父子会员订单/支付，无 Bot 联系人及运行时依赖；返回已装配仓储。
func ledgerSetup(t *testing.T) (*s.Store, *gorm.DB, c.PaidGrant) {
	t.Helper()
	_, db, now := snapshotSetup(t)
	if err := db.AutoMigrate(&d.LedgerSource{}, &d.LedgerRevocation{}, &d.LedgerProjection{}, &d.LedgerOutbox{}, &d.IdentityMigration{}, &pd.Payment{}, &wd.Transaction{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE ledger_test_completions (item_id integer PRIMARY KEY, paid_at datetime NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&od.Order{}).Where("id IN ?", []uint{9, 10}).Updates(map[string]any{"status": constants.OrderStatusPaid, "paid_at": now, "currency": "CNY", "total_amount": 10, "online_paid_amount": 10}).Error; err != nil {
		t.Fatal(err)
	}
	payment := pd.Payment{ID: 1, OrderID: 9, Status: constants.PaymentStatusSuccess, PaidAt: &now, Currency: "CNY", Amount: money.FromDecimal(decimal.NewFromInt(10))}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := d.OrderItemSnapshot{OrderItemID: 11, ProductID: 1, SKUID: 1, AccountID: 7, BeneficiaryTelegramID: 9007199254740993, IdentityVersion: 4, ConfigVersion: 1, Kind: d.TermMonths, Months: 2, DurationSeconds: 60 * 86400, Quantity: 1, Targets: []d.SnapshotTarget{{TargetID: 1, RouteVersion: 1, BotInstanceID: 9, TelegramChatID: -1001}}, CreatedAt: now}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	store := s.New(db).WithSystemFulfillment(func(tx *gorm.DB) fc.MembershipSystemStore { return ledgerCompletion{tx: tx} })
	return store, db, c.PaidGrant{OrderID: 9, PaymentID: 1, PaidAt: now}
}

// ledgerCount 返回 db 中 model 的行数，查询失败立即终止 t。
func ledgerCount(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var count int64
	if err := db.Model(model).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// TestLedgerConvertedPayment preserves trusted conversion, replay and revocation boundaries.
func TestLedgerConvertedPayment(t *testing.T) {
	for _, scenario := range []string{"valid", "mixed", "fee", "currency", "rate", "amount", "exception"} {
		t.Run(scenario, func(t *testing.T) {
			store, db, input := ledgerSetup(t)
			payload := jsonmap.JSON{"original_currency": "CNY", "original_amount": "10", "exchange_rate": "0.11"}
			updates := map[string]any{"currency": "GBP", "amount": "1.10", "provider_payload": payload}
			switch scenario {
			case "mixed":
				payload["original_amount"] = "6"
				updates["amount"] = "0.66"
				if err := db.Model(&od.Order{}).Where("id IN ?", []uint{9, 10}).Updates(map[string]any{"online_paid_amount": 6, "wallet_paid_amount": 4}).Error; err != nil {
					t.Fatal(err)
				}
			case "fee":
				payload["original_amount"] = "11"
				updates["amount"], updates["fee_amount"], updates["fee_policy"] = "1.21", "1", constants.PaymentFeePolicyCustomerSurcharge
			case "currency":
				payload["original_currency"] = "USD"
			case "rate":
				payload["exchange_rate"] = "0"
			case "amount":
				payload["original_amount"] = "9"
			case "exception":
				updates["exception_code"] = "amount_mismatch"
			}
			if err := db.Model(&pd.Payment{}).Where("id = 1").Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			valid := scenario == "valid" || scenario == "mixed" || scenario == "fee"
			for replay := 0; replay < 2; replay++ {
				err := store.GrantPaid(context.Background(), input)
				if valid && err != nil || !valid && !errors.Is(err, c.ErrPaymentEvidence) {
					t.Fatalf("grant %s: %v", scenario, err)
				}
			}
			if !valid {
				if ledgerCount(t, db, &d.LedgerSource{}) != 0 {
					t.Fatal("invalid grant persisted")
				}
				return
			}
			if ledgerCount(t, db, &d.LedgerSource{}) != 1 {
				t.Fatal("duplicate converted grant")
			}
			key := d.SourceKey{OrderItemID: 11, EntitlementScope: d.EntitlementScope{TargetID: 1, BeneficiaryTelegramID: 9007199254740993}}
			if err := store.RevokeSource(context.Background(), key, input.PaidAt); err != nil {
				t.Fatal(err)
			}
			if err := store.GrantPaid(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			projection, err := store.Recalculate(context.Background(), key.EntitlementScope, input.PaidAt)
			if err != nil || projection.Active {
				t.Fatalf("revived converted grant: %+v %v", projection, err)
			}
		})
	}
}

// TestLedgerGrantAtomicReplay 验证单连接、无联系人、父子事件幂等及固定付款时间。
func TestLedgerGrantAtomicReplay(t *testing.T) {
	store, db, input := ledgerSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range []uint{10, 9, 10, 9} {
		input.OrderID = id
		if err := store.GrantPaid(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 || ledgerCount(t, db, &d.LedgerOutbox{}) != 1 {
		t.Fatal("duplicate source/task")
	}
	var source d.LedgerSource
	if err := db.First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.OrderItemID != 11 || source.OrderID != 10 || source.PaymentOrderID != 9 || !source.PaidAt.Equal(input.PaidAt) {
		t.Fatalf("wrong source: %+v", source)
	}
	var task d.LedgerOutbox
	if err := db.First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.State != "pending" {
		t.Fatal("grant falsely claimed admission")
	}
	projection, err := store.Recalculate(ctx, d.EntitlementScope{BeneficiaryTelegramID: source.BeneficiaryTelegramID, TargetID: 1}, input.PaidAt.Add(time.Hour))
	if err != nil || projection.ExpiresAt == nil || !projection.ExpiresAt.Equal(input.PaidAt.Add(60*24*time.Hour)) {
		t.Fatalf("expiry drift: %+v %v", projection, err)
	}
}

// TestLatePaidGrantPreservesHistoryAfterBotReplacement verifies stable-target fulfillment.
// t owns the fixture; changing current routing must not rewrite purchase identity or extend terms.
func TestLatePaidGrantPreservesHistoryAfterBotReplacement(t *testing.T) {
	store, db, input := ledgerSetup(t)
	if err := db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{
		"bot_instance_id": 99, "telegram_chat_id": -2001, "route_version": 2, "recovery_task_id": 123,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// Late trusted payment remains recordable even while destination admission is fenced.
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.GrantPaid(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	var snapshot d.OrderItemSnapshot
	if err := db.First(&snapshot, "order_item_id = ?", 11).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Targets) != 1 || snapshot.Targets[0].BotInstanceID != 9 || snapshot.Targets[0].TelegramChatID != -1001 || snapshot.Targets[0].RouteVersion != 1 {
		t.Fatalf("purchase route rewritten: %+v", snapshot.Targets)
	}
	var source d.LedgerSource
	if err := db.First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.TargetID != 1 || source.BeneficiaryTelegramID != snapshot.BeneficiaryTelegramID || !source.PaidAt.Equal(input.PaidAt) || ledgerCount(t, db, &d.LedgerSource{}) != 1 {
		t.Fatalf("late payment lost or duplicated purchase facts: %+v", source)
	}
	projection, err := store.Recalculate(context.Background(), d.EntitlementScope{BeneficiaryTelegramID: source.BeneficiaryTelegramID, TargetID: 1}, input.PaidAt.Add(time.Hour))
	if err != nil || projection.ExpiresAt == nil || !projection.ExpiresAt.Equal(input.PaidAt.Add(60*24*time.Hour)) {
		t.Fatalf("disaster recovery extended term: %+v, %v", projection, err)
	}
}

// TestLedgerGrantRollback 验证未装配履约和履约失败均不泄漏来源/投影/outbox/履约标记。
func TestLedgerGrantRollback(t *testing.T) {
	_, db, input := ledgerSetup(t)
	failure := errors.New("fulfillment failed")
	stores := []*s.Store{s.New(db), s.New(db).WithSystemFulfillment(func(tx *gorm.DB) fc.MembershipSystemStore { return ledgerCompletion{tx: tx, failure: failure} })}
	for _, store := range stores {
		if err := store.GrantPaid(context.Background(), input); err == nil {
			t.Fatal("expected fail closed")
		}
		for _, model := range []any{&d.LedgerSource{}, &d.LedgerProjection{}, &d.LedgerOutbox{}} {
			if ledgerCount(t, db, model) != 0 {
				t.Fatalf("partial commit %T", model)
			}
		}
		var count int64
		if err := db.Table("ledger_test_completions").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("completion leaked %d %v", count, err)
		}
	}
}

// TestLedgerPaymentEvidence 验证取消、退款、错误付款时间/支付归属不会授予或泄漏任务。
func TestLedgerPaymentEvidence(t *testing.T) {
	scenarios := []struct {
		name   string
		change func(*gorm.DB, *c.PaidGrant) error
	}{
		{"wrong time", func(_ *gorm.DB, input *c.PaidGrant) error { input.PaidAt = input.PaidAt.Add(time.Second); return nil }},
		{"wrong payment", func(db *gorm.DB, _ *c.PaidGrant) error {
			return db.Model(&pd.Payment{}).Where("id = 1").Update("order_id", 88).Error
		}},
		{"canceled child", func(db *gorm.DB, _ *c.PaidGrant) error {
			return db.Model(&od.Order{}).Where("id = 10").Update("status", constants.OrderStatusCanceled).Error
		}},
		{"refund", func(db *gorm.DB, _ *c.PaidGrant) error {
			return db.Model(&od.Order{}).Where("id = 9").Update("refunded_amount", 1).Error
		}},
		{"exception", func(db *gorm.DB, _ *c.PaidGrant) error {
			return db.Model(&pd.Payment{}).Where("id = 1").Update("exception_code", "underpaid_succeeded").Error
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			store, db, input := ledgerSetup(t)
			if err := scenario.change(db, &input); err != nil {
				t.Fatal(err)
			}
			if err := store.GrantPaid(context.Background(), input); err == nil {
				t.Fatal("untrusted grant accepted")
			}
			if ledgerCount(t, db, &d.LedgerSource{}) != 0 || ledgerCount(t, db, &d.LedgerOutbox{}) != 0 {
				t.Fatal("leaked state")
			}
		})
	}
}

// TestLedgerWalletEvidence 验证全额钱包必须有匹配的真实扣款流水，不能靠事件伪造。
func TestLedgerWalletEvidence(t *testing.T) {
	store, db, input := ledgerSetup(t)
	input.PaymentID = 0
	if err := db.Model(&od.Order{}).Where("id IN ?", []uint{9, 10}).Updates(map[string]any{"online_paid_amount": 0, "wallet_paid_amount": 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); !errors.Is(err, c.ErrPaymentEvidence) {
		t.Fatalf("missing debit: %v", err)
	}
	id := uint(9)
	debit := wd.Transaction{OrderID: &id, UserID: 7, Type: constants.WalletTxnTypeOrderPay, Direction: constants.WalletTxnDirectionOut, Amount: money.FromDecimal(decimal.NewFromInt(10)), Currency: "CNY", Reference: "wallet-test", CreatedAt: input.PaidAt}
	if err := db.Create(&debit).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

// TestLedgerConcurrentGrant 验证同首行的多个父子授予竞争最终仅保存一份来源。
func TestLedgerConcurrentGrant(t *testing.T) {
	store, db, input := ledgerSetup(t)
	var workers sync.WaitGroup
	results := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- store.GrantPaid(context.Background(), input) }()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 || ledgerCount(t, db, &d.LedgerOutbox{}) != 1 {
		t.Fatal("duplicate concurrent grants")
	}
}

// seedLifecycleUsers inserts out-of-order identities, cross-state chat history and foreign scope.
// It returns persisted rows so tests can compare exact task identities and retry evidence.
func seedLifecycleUsers(t *testing.T, harness *accessHarness) []d.AccessOperation {
	t.Helper()
	rows := []d.AccessOperation{
		{TelegramID: 300, ChatID: -1, State: "pending"},
		{TelegramID: 100, ChatID: -1, State: "succeeded"},
		{TelegramID: 200, ChatID: -1, State: "failed"},
		{TelegramID: 100, ChatID: -2, State: "pending"},
		{TelegramID: 100, ChatID: -1, State: "needs_review"},
		{TelegramID: 200, ChatID: -2, State: "succeeded"},
		{TelegramID: 100, ChatID: -3, State: "failed", BotID: 10},
		{TelegramID: 50, ChatID: -1, State: "failed", BotID: 10},
		{TelegramID: 100, ChatID: -4, State: "failed", Kind: "approve"},
		{TelegramID: 25, ChatID: -1, State: "failed", Kind: "approve"},
	}
	for index := range rows {
		row := &rows[index]
		row.BusinessKey = fmt.Sprintf("grouped-user:%d", index)
		if row.BotID == 0 {
			row.BotID = 9
		}
		if row.Kind == "" {
			row.Kind = "lifecycle"
		}
		row.Attempt, row.LastAttemptAt, row.UpdatedAt = 3, &harness.now, harness.now
		if err := harness.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

// TestLifecycleUserPages verifies DB user pagination, full histories, independent states and bot isolation.
func TestLifecycleUserPages(t *testing.T) {
	harness, ctx := accessSetup(t)
	seeded := seedLifecycleUsers(t, harness)
	for index, identity := range []int64{100, 200, 300} {
		groups, total, err := harness.store.LifecycleUsersPage(ctx, 9, index+1, 1)
		if err != nil || total != 3 || len(groups) != 1 || groups[0].TelegramID != identity {
			t.Fatalf("page=%d groups=%+v total=%d err=%v", index+1, groups, total, err)
		}
		tasks := groups[0].Tasks
		if len(tasks) != 3-index {
			t.Fatalf("partial history: %+v", tasks)
		}
		for taskIndex, task := range tasks {
			if task.TelegramID != identity || task.BotID != 9 || task.Attempt != 3 || task.LastAttemptAt == nil || !task.LastAttemptAt.Equal(harness.now) || !task.UpdatedAt.Equal(harness.now) {
				t.Fatalf("lost scope/evidence: %+v", task)
			}
			if taskIndex > 0 && tasks[taskIndex-1].ID >= task.ID {
				t.Fatal("unstable task order")
			}
		}
		if identity == 100 && (tasks[0].ID != seeded[1].ID || tasks[1].ID != seeded[3].ID || tasks[2].ID != seeded[4].ID) {
			t.Fatalf("same-chat history missing: %+v", tasks)
		}
	}
	groups, total, err := harness.store.LifecycleUsersPage(ctx, 9, 4, 1)
	if err != nil || total != 3 || len(groups) != 0 || groups == nil {
		t.Fatalf("empty page lost total: %+v %d %v", groups, total, err)
	}
	groups, total, err = harness.store.LifecycleUsersPage(ctx, 10, 1, 10)
	if err != nil || total != 2 || len(groups) != 2 || groups[0].TelegramID != 50 || len(groups[1].Tasks) != 1 {
		t.Fatalf("bot isolation: %+v %d %v", groups, total, err)
	}
}

// TestLifecycleUserFilters selects matching users but retains all their states and historical tasks.
func TestLifecycleUserFilters(t *testing.T) {
	harness, ctx := accessSetup(t)
	seedLifecycleUsers(t, harness)
	for _, status := range []string{"processing", "needs_review", "completed"} {
		groups, total, err := harness.store.LifecycleUsersPage(ctx, 9, 1, 1, status)
		if err != nil || total != 2 || len(groups) != 1 || groups[0].TelegramID != 100 || len(groups[0].Tasks) != 3 {
			t.Fatalf("status=%s groups=%+v total=%d err=%v", status, groups, total, err)
		}
		groups, total, err = harness.store.LifecycleUsersPage(ctx, 9, 2, 1, status)
		identity := int64(200)
		if status == "processing" {
			identity = 300
		}
		if err != nil || total != 2 || len(groups) != 1 || groups[0].TelegramID != identity {
			t.Fatalf("filtered second page: %+v %d %v", groups, total, err)
		}
	}
	// Legacy numbered/cursor readers still paginate tasks, not users or expanded histories.
	legacy, total, err := harness.store.LifecycleTasksPage(ctx, 9, 1, 1, "completed")
	if err != nil || total != 2 || len(legacy) != 1 || legacy[0].State != "succeeded" || legacy[0].Attempt != 3 {
		t.Fatalf("legacy page changed: %+v %d %v", legacy, total, err)
	}
	cursor, err := harness.store.ListLifecycle(ctx, 9, 0, 1)
	if err != nil || len(cursor) != 1 || cursor[0].TelegramID != 300 || cursor[0].LastAttemptAt == nil {
		t.Fatalf("legacy cursor changed: %+v %v", cursor, err)
	}
}

// TestLifecycleUserHistoryExceedsPageLimit proves the numbered size never limits a selected user's tasks.
func TestLifecycleUserHistoryExceedsPageLimit(t *testing.T) {
	harness, ctx := accessSetup(t)
	for index := 0; index < 105; index++ {
		row := d.AccessOperation{BusinessKey: fmt.Sprintf("long-history:%d", index), BotID: 9, Kind: "lifecycle", TelegramID: 100, ChatID: -1, State: "succeeded"}
		if index == 104 {
			row.State = "needs_review"
		}
		if err := harness.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	groups, total, err := harness.store.LifecycleUsersPage(ctx, 9, 1, 1, "needs_review")
	if err != nil || total != 1 || len(groups) != 1 || len(groups[0].Tasks) != 105 {
		t.Fatalf("history truncated: %+v %d %v", groups, total, err)
	}
	if groups[0].Tasks[0].State != "succeeded" || groups[0].Tasks[104].State != "needs_review" {
		t.Fatal("historical and current states must remain independent")
	}
}

// TestLifecycleUserInvalidParameters rejects bad scope, pagination and repeated/unknown filters.
func TestLifecycleUserInvalidParameters(t *testing.T) {
	harness, ctx := accessSetup(t)
	for _, input := range [][3]int{{0, 1, 10}, {9, 0, 10}, {9, 1, 0}, {9, 1, 101}, {9, int(^uint(0) >> 1), 100}} {
		if _, _, err := harness.store.LifecycleUsersPage(ctx, uint(input[0]), input[1], input[2]); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid pagination: %v %v", input, err)
		}
	}
	for _, status := range [][]string{{"invalid"}, {"processing", "completed"}} {
		if _, _, err := harness.store.LifecycleUsersPage(ctx, 9, 1, 20, status...); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid filter: %v %v", status, err)
		}
	}
}

// TestLifecycleUsernameSearch checks literal username matching, exact IDs and complete bot-scoped histories.
func TestLifecycleUsernameSearch(t *testing.T) {
	harness, ctx := accessSetup(t)
	seedLifecycleUsers(t, harness)
	for index, profile := range []struct{ identity, username string }{{"100", "Seven_Time"}, {"200", "SevenXTime"}} {
		user := ud.User{ID: uint(80 + index), Email: fmt.Sprintf("search%d@example.invalid", index), Status: constants.UserStatusActive}
		if err := harness.db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Create(&ei.Identity{UserID: user.ID, Provider: "telegram", ProviderUserID: profile.identity, Username: profile.username}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, keyword := range []string{"100", "  @SEVEN_time ", "seven_time", "_"} {
		groups, total, err := harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 1, "completed", keyword)
		if err != nil || total != 1 || len(groups) != 1 || groups[0].TelegramID != 100 || groups[0].TelegramUsername != "Seven_Time" || len(groups[0].Tasks) != 3 {
			t.Fatalf("keyword=%q groups=%+v total=%d err=%v", keyword, groups, total, err)
		}
	}
	groups, total, err := harness.store.SearchLifecycleUsersPage(ctx, 9, 2, 1, "", "seven")
	if err != nil || total != 2 || len(groups) != 1 || groups[0].TelegramID != 200 {
		t.Fatalf("user pagination: %+v %d %v", groups, total, err)
	}
	groups, total, err = harness.store.SearchLifecycleUsersPage(ctx, 10, 1, 10, "", "Seven_Time")
	if err != nil || total != 1 || len(groups) != 1 || len(groups[0].Tasks) != 1 || groups[0].Tasks[0].BotID != 10 {
		t.Fatalf("bot scope: %+v %d %v", groups, total, err)
	}
	groups, total, err = harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 10, "", "300")
	if err != nil || total != 1 || len(groups) != 1 || groups[0].TelegramUsername != "" {
		t.Fatalf("absent username: %+v %d %v", groups, total, err)
	}
	if err := harness.db.Model(&ei.Identity{}).Where("provider_user_id = ?", "100").Update("username", "").Error; err != nil {
		t.Fatal(err)
	}
	groups, total, err = harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 10, "", "seven_time")
	if err != nil || total != 0 || len(groups) != 0 {
		t.Fatalf("cleared name still matched: %+v %d %v", groups, total, err)
	}
	if err := harness.db.Delete(&ud.User{}, 81).Error; err != nil {
		t.Fatal(err)
	}
	groups, total, err = harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 10, "", "200")
	if err != nil || total != 1 || len(groups) != 1 || groups[0].TelegramUsername != "" {
		t.Fatalf("deleted user profile leaked: %+v %d %v", groups, total, err)
	}
}

// TestAccessReviewSearch verifies scoped application pagination, literal username fragments and missing profiles.
func TestAccessReviewSearch(t *testing.T) {
	h, ctx := accessSetup(t)
	user := ud.User{ID: 80, Email: "review@example.invalid", Status: constants.UserStatusActive}
	if err := h.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&ei.Identity{UserID: user.ID, Provider: "telegram", ProviderUserID: "100", Username: "Seven_Time"}).Error; err != nil {
		t.Fatal(err)
	}
	for index, row := range []d.AccessJoin{
		{BotID: 9, TelegramID: 100, State: "needs_review"}, {BotID: 9, TelegramID: 100, State: "needs_review"},
		{BotID: 10, TelegramID: 100, State: "needs_review"}, {BotID: 9, TelegramID: 100, State: "approved"},
		{BotID: 9, TelegramID: 9007199254740993, State: "needs_review"},
	} {
		row.UpdateID = int64(index + 1)
		if err := h.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, keyword := range []string{"100", " @SEVEN_time ", "seven", "_"} {
		rows, total, err := h.service.SearchAccessReviewsPage(ctx, 9, 2, 1, keyword)
		if err != nil || total != 2 || len(rows) != 1 || rows[0].TelegramUsername != "Seven_Time" || rows[0].ID != 2 {
			t.Fatalf("%q: %+v %d %v", keyword, rows, total, err)
		}
	}
	rows, total, err := h.service.SearchAccessReviewsPage(ctx, 9, 1, 10, "9007199254740993")
	if err != nil || total != 1 || len(rows) != 1 || rows[0].TelegramUsername != "" {
		t.Fatalf("missing profile: %+v %d %v", rows, total, err)
	}
	rows, total, err = h.service.SearchAccessReviewsPage(ctx, 10, 1, 10, "seven")
	if err != nil || total != 1 || len(rows) != 1 || rows[0].BotID != 10 {
		t.Fatalf("scope: %+v %d %v", rows, total, err)
	}
	for _, keyword := range []string{"%", "@", "@@name", "9223372036854775808", "name' OR 1=1"} {
		if _, _, err := h.service.SearchAccessReviewsPage(ctx, 9, 1, 10, keyword); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted %q: %v", keyword, err)
		}
	}
	if err := h.db.Model(&ei.Identity{}).Where("user_id = ?", user.ID).Update("username", "").Error; err != nil {
		t.Fatal(err)
	}
	rows, total, err = h.service.SearchAccessReviewsPage(ctx, 9, 1, 10, "seven")
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("stale username: %+v %d %v", rows, total, err)
	}
	rows, total, err = h.service.AccessReviewsPage(ctx, 9, 1, 10)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("unfiltered page: %+v %d %v", rows, total, err)
	}
}

// TestReviewStatusPages filters by actual review timestamp without changing evidence state.
func TestReviewStatusPages(t *testing.T) {
	h, ctx := accessSetup(t)
	now := time.Now()
	for index, row := range []d.AccessJoin{
		{BotID: 9, TelegramID: 100, State: "needs_review"},
		{BotID: 9, TelegramID: 100, State: "needs_review", ReviewedAt: &now},
		{BotID: 10, TelegramID: 100, State: "needs_review"},
	} {
		row.UpdateID = int64(index + 1)
		if err := h.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		status string
		total  int64
		id     uint
	}{{"pending", 1, 1}, {"reviewed", 1, 2}, {"all", 2, 1}} {
		rows, total, err := h.service.SearchAccessReviewsPage(ctx, 9, 1, 1, "100", test.status)
		if err != nil || total != test.total || len(rows) != 1 || rows[0].ID != test.id {
			t.Fatalf("%s: %+v %d %v", test.status, rows, total, err)
		}
	}
	if err := h.service.RecoverAccess(ctx, 9, 1, 42, c.AccessRecoveryInput{Action: "review_join", Reason: "operator_verified"}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := h.service.SearchAccessReviewsPage(ctx, 9, 1, 20, "", "pending")
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("reviewed still pending: %+v %d %v", rows, total, err)
	}
	rows, total, err = h.service.SearchAccessReviewsPage(ctx, 9, 1, 20, "", "reviewed")
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("review history lost: %+v %d %v", rows, total, err)
	}
	for _, status := range [][]string{{""}, {"unknown"}, {"pending", "reviewed"}} {
		if _, _, err := h.service.SearchAccessReviewsPage(ctx, 9, 1, 20, "", status...); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted status %v: %v", status, err)
		}
	}
}

// TestLifecycleKeywordBoundary rejects SQL syntax and invalid IDs without changing valid big IDs.
func TestLifecycleKeywordBoundary(t *testing.T) {
	harness, ctx := accessSetup(t)
	for _, keyword := range []string{"@", "@@alice", "%", "name' OR 1=1", "0", "9223372036854775808", strings.Repeat("a", 33)} {
		if _, _, err := harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 10, "", keyword); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted %q: %v", keyword, err)
		}
	}
	task := d.AccessOperation{BusinessKey: "search-big-id", Kind: "lifecycle", BotID: 9, TelegramID: harness.identity.TelegramID, ChatID: -1, State: "pending"}
	if err := harness.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	groups, total, err := harness.store.SearchLifecycleUsersPage(ctx, 9, 1, 10, "", "9007199254740993")
	if err != nil || total != 1 || len(groups) != 1 || groups[0].TelegramID != harness.identity.TelegramID {
		t.Fatalf("big ID: %+v %d %v", groups, total, err)
	}
	for _, value := range []string{" @SEVEN_Time ", "@123", "9007199254740993", " "} {
		normalized, id, err := c.LifecycleKeyword(value)
		again, nextID, nextErr := c.LifecycleKeyword(normalized)
		if err != nil || nextErr != nil || normalized != again || id != nextID {
			t.Fatalf("non-idempotent %q", value)
		}
	}
}

// TestLedgerHistoryAndOrdinary 验证配置改变不影响快照且普通订单完全无账本副作用。
func TestLedgerHistoryAndOrdinary(t *testing.T) {
	store, db, input := ledgerSetup(t)
	if err := db.Model(&d.ProductConfig{}).Where("product_id = 1").Update("version", 99).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&d.OrderItemSnapshot{}).Where("order_item_id = 11").Update("months", 3).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err == nil {
		t.Fatal("snapshot mutation accepted")
	}
	if err := db.Create(&od.Order{ID: 77, OrderNo: fmt.Sprint("ordinary", 77), Status: constants.OrderStatusPendingPayment}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.New(db).GrantPaid(context.Background(), c.PaidGrant{OrderID: 77, PaidAt: input.PaidAt}); err != nil {
		t.Fatal(err)
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 {
		t.Fatal("ordinary side effect")
	}
}
