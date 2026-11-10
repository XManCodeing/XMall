package gormstore

import (
	"context"
	"errors"
	"fmt"
	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/workflows/membershiprefund"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// retryFunds uses real durable metadata, rejecting any unexpected monetary retry.
type retryFunds struct {
	*Store
	commits int
}

// CommitMembershipRefund models an existing request replay without monetary mutation.
func (funds *retryFunds) CommitMembershipRefund(_ context.Context, input order.MembershipRefundInput) (*order.MembershipRefundRequest, error) {
	funds.commits++
	return funds.GetMembershipRefund(input.RequestKey)
}

// retryRights tracks local disposal independently from durable progress writes.
type retryRights struct {
	fail     bool
	calls    int
	disposed bool
}

// RefundCandidates returns verified synthetic evidence and previously committed disposal.
func (rights *retryRights) RefundCandidates(context.Context, uint) ([]membership.RefundCandidate, error) {
	return []membership.RefundCandidate{{RefundSelection: membership.RefundSelection{RefundID: 5, OrderItemID: 8}, Disposed: rights.disposed}}, nil
}

// DisposeRefund simulates a permanent dependency failure without exposing sensitive messages.
func (rights *retryRights) DisposeRefund(context.Context, membership.RefundSelection) error {
	rights.calls++
	if rights.fail {
		return errors.New("synthetic permanent failure")
	}
	rights.disposed = true
	return nil
}

// openRetryStore reopens the same SQLite file to prove retries do not depend on process memory.
func openRetryStore(t *testing.T, path string) (*Store, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
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
	if err := db.AutoMigrate(&orderdomain.MembershipRefundRequest{}); err != nil {
		t.Fatal(err)
	}
	return New(db, "synthetic-retry-secret"), db
}

// TestRefundRetryBackoffRestartAndManualRecovery covers permanent failure, exact due times and retained liability.
func TestRefundRetryBackoffRestartAndManualRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	store, db := openRetryStore(t, path)
	request := order.MembershipRefundRequest{RequestKey: "durable-retry-key", ActorAdminID: 7, OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rights := &retryRights{fail: true}
	funds := &retryFunds{Store: store}
	workflow := &membershiprefund.Workflow{Funds: funds, Rights: rights, Now: func() time.Time { return now }, Authorize: func(context.Context, uint, string) error { return nil }}
	read := func(context.Context, uint, int) ([]order.MembershipRefundRequest, error) {
		row, err := funds.GetMembershipRefund(request.RequestKey)
		if err != nil {
			return nil, err
		}
		return []order.MembershipRefundRequest{*row}, nil
	}
	for attempt := uint(1); attempt <= order.MembershipRefundRetryLimit; attempt++ {
		if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err == nil {
			t.Fatal("permanent failure lost")
		}
		row, err := funds.GetMembershipRefund(request.RequestKey)
		if err != nil || row.RetryAttempts != attempt || row.RefundID != 5 {
			t.Fatal(row, err)
		}
		if attempt == order.MembershipRefundRetryLimit {
			if row.RightsState != "pending" {
				t.Fatal(row)
			}
			break
		}
		if row.RetryAfter == nil || !row.RetryAfter.Equal(now.Add(order.MembershipRefundRetryDelay(attempt))) {
			t.Fatal(row)
		}
		now = row.RetryAfter.Add(-time.Nanosecond)
		if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err != nil || rights.calls != int(attempt) {
			t.Fatal(err, rights.calls)
		}
		now = *row.RetryAfter
		// Reopen both adapter and workflow; scheduling remains in the database.
		funds.Store, _ = openRetryStore(t, path)
		workflow = &membershiprefund.Workflow{Funds: funds, Rights: rights, Now: func() time.Time { return now }, Authorize: workflow.Authorize}
	}
	now = now.Add(24 * time.Hour)
	if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err != nil || rights.calls != int(order.MembershipRefundRetryLimit) || funds.commits != 0 {
		t.Fatal(err, rights.calls, funds.commits)
	}
	listed, err := funds.ListMembershipRefunds(request.OrderID, request.ActorAdminID)
	if err != nil || len(listed) != 1 || listed[0].RightsState != "pending" || listed[0].RetryAttempts != order.MembershipRefundRetryLimit {
		t.Fatal("exhausted liability must remain UI-resumable", listed, err)
	}
	rights.fail = false
	row, err := workflow.Execute(context.Background(), order.MembershipRefundInput{RequestKey: request.RequestKey})
	if err != nil || row.RightsState != "queued" || row.RefundID != 5 || funds.commits != 1 {
		t.Fatal(row, err)
	}
}

// TestRefundRetryClaimCASAndCrash verifies one owner and stale-worker fencing across separate stores.
func TestRefundRetryClaimCASAndCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim.db")
	store, db := openRetryStore(t, path)
	other, _ := openRetryStore(t, path)
	request := order.MembershipRefundRequest{RequestKey: "durable-claim-key", RefundID: 5, FundsState: "completed", RightsState: "pending"}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var group sync.WaitGroup
	claims := make(chan *order.MembershipRefundRequest, 2)
	for _, worker := range []*Store{store, other} {
		group.Add(1)
		go func(worker *Store) {
			defer group.Done()
			claim, err := worker.ClaimMembershipRefund(context.Background(), request, now, false)
			if err != nil {
				t.Error(err)
			}
			claims <- claim
		}(worker)
	}
	group.Wait()
	close(claims)
	var first *order.MembershipRefundRequest
	for claim := range claims {
		if claim != nil {
			if first != nil {
				t.Fatal("duplicate claim")
			}
			first = claim
		}
	}
	if first == nil {
		t.Fatal("no claim")
	}
	if claim, err := other.ClaimMembershipRefund(context.Background(), *first, now, true); err != nil || claim != nil {
		t.Fatal("manual stole lease", claim, err)
	}
	// Crash before Finish: a fresh store waits for both lease and provisional backoff.
	if claim, err := other.ClaimMembershipRefund(context.Background(), *first, first.RetryAfter.Add(-time.Nanosecond), false); err != nil || claim != nil {
		t.Fatal(claim, err)
	}
	second, err := other.ClaimMembershipRefund(context.Background(), *first, *first.RetryAfter, false)
	if err != nil || second == nil || second.RetryAttempts != 2 {
		t.Fatal(second, err)
	}
	if err := store.FinishMembershipRefund(context.Background(), *first, *first.RetryAfter, true); !errors.Is(err, order.ErrRefundRequestConflict) {
		t.Fatal("stale finish", err)
	}
	if err := other.FinishMembershipRefund(context.Background(), *second, *first.RetryAfter, true); err != nil {
		t.Fatal(err)
	}
}

// TestRefundRetryMigrationPreservesLegacyIntent verifies nullable/defaulted columns on a pre-v47 table.
func TestRefundRetryMigrationPreservesLegacyIntent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE membership_refund_requests (
		id INTEGER PRIMARY KEY, request_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
		actor_admin_id INTEGER, method TEXT NOT NULL, amount TEXT, remark TEXT,
		existing_refund_id INTEGER, order_id INTEGER NOT NULL, refund_id INTEGER NOT NULL,
		item_ids TEXT NOT NULL, funds_state TEXT NOT NULL, rights_state TEXT NOT NULL,
		created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO membership_refund_requests
		(id, request_key, fingerprint, actor_admin_id, method, order_id, refund_id, item_ids, funds_state, rights_state)
		VALUES (1, 'legacy-refund-key', 'unchanged', 7, 'manual', 2, 5, '[8]', 'completed', 'pending')`).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.AutoMigrate(&orderdomain.MembershipRefundRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	row, err := New(db, "synthetic-retry-secret").GetMembershipRefund("legacy-refund-key")
	if err != nil || row == nil || row.RefundID != 5 || row.Fingerprint != "unchanged" || row.ItemIDs[0] != 8 || row.RetryVersion != 0 || row.RetryAttempts != 0 || row.RetryAfter != nil || row.RightsState != "pending" {
		t.Fatal(row, err)
	}
}

// TestRefundRetryMalformedRowDoesNotStarve verifies bad intent is durably bounded while later work succeeds.
func TestRefundRetryMalformedRowDoesNotStarve(t *testing.T) {
	store, db := openRetryStore(t, filepath.Join(t.TempDir(), "malformed.db"))
	rows := []order.MembershipRefundRequest{
		{RequestKey: "", RefundID: 5, FundsState: "completed", RightsState: "pending"},
		{RequestKey: "later-valid-key", ActorAdminID: 7, OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	funds, rights := &retryFunds{Store: store}, &retryRights{}
	workflow := membershiprefund.Workflow{Funds: funds, Rights: rights}
	read := func(context.Context, uint, int) ([]order.MembershipRefundRequest, error) { return rows, nil }
	next, count, err := workflow.Recover(context.Background(), read, 0, 10)
	if !errors.Is(err, order.ErrRefundRequestInvalid) || next != rows[1].ID || count != 2 || rights.calls != 1 {
		t.Fatal(next, count, err, rights.calls)
	}
	bad, err := store.GetMembershipRefund("")
	if err != nil || bad.RetryAttempts != 1 || bad.RetryAfter == nil {
		t.Fatal(bad, err)
	}
}

// TestRefundRetryFailedProgressWrite preserves preclaimed schedule after successful disposal and failed bookkeeping.
func TestRefundRetryFailedProgressWrite(t *testing.T) {
	store, db := openRetryStore(t, filepath.Join(t.TempDir(), "write.db"))
	request := order.MembershipRefundRequest{RequestKey: "durable-write-key", ActorAdminID: 7, OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_retry_finish BEFORE UPDATE ON membership_refund_requests WHEN NEW.retry_lease_until IS NULL BEGIN SELECT RAISE(ABORT, 'synthetic progress failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	funds, rights := &retryFunds{Store: store}, &retryRights{}
	workflow := membershiprefund.Workflow{Funds: funds, Rights: rights, Now: func() time.Time { return now }}
	read := func(context.Context, uint, int) ([]order.MembershipRefundRequest, error) {
		row, err := store.GetMembershipRefund(request.RequestKey)
		if err != nil {
			return nil, err
		}
		return []order.MembershipRefundRequest{*row}, nil
	}
	if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err == nil {
		t.Fatal("write error lost")
	}
	row, err := store.GetMembershipRefund(request.RequestKey)
	if err != nil || row.RetryAttempts != 1 || row.RetryAfter == nil || row.RetryLeaseUntil == nil || !rights.disposed {
		t.Fatal(row, err)
	}
	if err := db.Exec("DROP TRIGGER fail_retry_finish").Error; err != nil {
		t.Fatal(err)
	}
	now = *row.RetryAfter
	if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err != nil || rights.calls != 1 || funds.commits != 0 {
		t.Fatal(err, rights.calls, funds.commits)
	}
}

func setupOrderRefundRecordRepositoryTest(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:order_refund_record_repo_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&userdomain.User{},
		&externalidentitydomain.Identity{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&orderdomain.OrderRefundRecord{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	return New(db, "test-guest-credential-secret-with-32-bytes"), db
}

func TestOrderRefundRecordRepositoryListAdminFilters(t *testing.T) {
	repo, db := setupOrderRefundRecordRepositoryTest(t)
	now := time.Now().UTC().Truncate(time.Second)

	user1 := &userdomain.User{
		Email:        "member-alpha@example.com",
		DisplayName:  "Alpha",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	user2 := &userdomain.User{
		Email:        "member-beta@example.com",
		DisplayName:  "Beta",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user1).Error; err != nil {
		t.Fatalf("create user1 failed: %v", err)
	}
	if err := db.Create(user2).Error; err != nil {
		t.Fatalf("create user2 failed: %v", err)
	}
	if err := db.Create(&externalidentitydomain.Identity{
		UserID:         user1.ID,
		Provider:       "telegram",
		ProviderUserID: "6059928735",
		Username:       "tg_alpha",
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatalf("create user oauth identity failed: %v", err)
	}

	order1 := &orderdomain.Order{
		OrderNo:          "DJ-REFUND-001",
		UserID:           user1.ID,
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(100)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(100)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		CreatedAt:        now.Add(-4 * time.Hour),
		UpdatedAt:        now.Add(-4 * time.Hour),
	}
	order2 := &orderdomain.Order{
		OrderNo:          "DJ-REFUND-002",
		UserID:           0,
		GuestEmail:       "guest-refund@example.com",
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(80)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(80)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(80)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		CreatedAt:        now.Add(-3 * time.Hour),
		UpdatedAt:        now.Add(-3 * time.Hour),
	}
	order3 := &orderdomain.Order{
		OrderNo:          "DJ-REFUND-003",
		UserID:           user2.ID,
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(60)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(60)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(60)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		CreatedAt:        now.Add(-2 * time.Hour),
		UpdatedAt:        now.Add(-2 * time.Hour),
	}
	for _, order := range []*orderdomain.Order{order1, order2, order3} {
		if err := db.Create(order).Error; err != nil {
			t.Fatalf("create order failed: %v", err)
		}
	}

	items := []orderdomain.OrderItem{
		{
			OrderID:         order1.ID,
			ProductID:       1,
			TitleJSON:       jsonmap.JSON{"zh-CN": "会员商品A"},
			UnitPrice:       money.FromDecimal(decimal.NewFromInt(100)),
			CostPrice:       money.FromDecimal(decimal.NewFromInt(50)),
			Quantity:        1,
			TotalPrice:      money.FromDecimal(decimal.NewFromInt(100)),
			CouponDiscount:  money.FromDecimal(decimal.Zero),
			FulfillmentType: constants.FulfillmentTypeManual,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		{
			OrderID:         order2.ID,
			ProductID:       2,
			TitleJSON:       jsonmap.JSON{"zh-CN": "游客商品B"},
			UnitPrice:       money.FromDecimal(decimal.NewFromInt(80)),
			CostPrice:       money.FromDecimal(decimal.NewFromInt(40)),
			Quantity:        1,
			TotalPrice:      money.FromDecimal(decimal.NewFromInt(80)),
			CouponDiscount:  money.FromDecimal(decimal.Zero),
			FulfillmentType: constants.FulfillmentTypeManual,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		{
			OrderID:         order3.ID,
			ProductID:       3,
			TitleJSON:       jsonmap.JSON{"zh-CN": "其他商品C"},
			UnitPrice:       money.FromDecimal(decimal.NewFromInt(60)),
			CostPrice:       money.FromDecimal(decimal.NewFromInt(30)),
			Quantity:        1,
			TotalPrice:      money.FromDecimal(decimal.NewFromInt(60)),
			CouponDiscount:  money.FromDecimal(decimal.Zero),
			FulfillmentType: constants.FulfillmentTypeManual,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}
	for idx := range items {
		if err := db.Create(&items[idx]).Error; err != nil {
			t.Fatalf("create order item failed: %v", err)
		}
	}

	record1 := &orderdomain.OrderRefundRecord{
		UserID:     user1.ID,
		OrderID:    order1.ID,
		Type:       constants.OrderRefundTypeManual,
		Amount:     money.FromDecimal(decimal.NewFromInt(10)),
		Currency:   "CNY",
		Remark:     "member refund",
		CreatedAt:  now.Add(-90 * time.Minute),
		UpdatedAt:  now.Add(-90 * time.Minute),
		GuestEmail: "",
	}
	record2 := &orderdomain.OrderRefundRecord{
		UserID:     0,
		GuestEmail: "guest-refund@example.com",
		OrderID:    order2.ID,
		Type:       constants.OrderRefundTypeWallet,
		Amount:     money.FromDecimal(decimal.NewFromInt(20)),
		Currency:   "CNY",
		Remark:     "guest refund",
		CreatedAt:  now.Add(-30 * time.Minute),
		UpdatedAt:  now.Add(-30 * time.Minute),
	}
	record3 := &orderdomain.OrderRefundRecord{
		UserID:     user2.ID,
		OrderID:    order3.ID,
		Type:       constants.OrderRefundTypeManual,
		Amount:     money.FromDecimal(decimal.NewFromInt(5)),
		Currency:   "CNY",
		Remark:     "old refund",
		CreatedAt:  now.Add(-10 * 24 * time.Hour),
		UpdatedAt:  now.Add(-10 * 24 * time.Hour),
		GuestEmail: "",
	}
	for _, record := range []*orderdomain.OrderRefundRecord{record1, record2, record3} {
		if err := db.Create(record).Error; err != nil {
			t.Fatalf("create refund record failed: %v", err)
		}
	}

	rows, total, err := repo.ListRefundRecordsAdmin(ordercontract.RefundRecordListFilter{
		Page:        1,
		PageSize:    20,
		UserKeyword: "tg_alpha",
	})
	if err != nil {
		t.Fatalf("list by user keyword failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != record1.ID {
		t.Fatalf("unexpected list by user keyword result: total=%d rows=%+v", total, rows)
	}

	rows, total, err = repo.ListRefundRecordsAdmin(ordercontract.RefundRecordListFilter{
		Page:           1,
		PageSize:       20,
		OrderNo:        "DJ-REFUND-002",
		GuestEmail:     "guest-refund@example.com",
		ProductKeyword: "游客商品",
	})
	if err != nil {
		t.Fatalf("list by order/guest/product filter failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != record2.ID {
		t.Fatalf("unexpected list by order/guest/product result: total=%d rows=%+v", total, rows)
	}

	createdFrom := now.Add(-40 * time.Minute)
	createdTo := now.Add(10 * time.Minute)
	rows, total, err = repo.ListRefundRecordsAdmin(ordercontract.RefundRecordListFilter{
		Page:        1,
		PageSize:    20,
		CreatedFrom: &createdFrom,
		CreatedTo:   &createdTo,
	})
	if err != nil {
		t.Fatalf("list by created range failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != record2.ID {
		t.Fatalf("unexpected list by created range result: total=%d rows=%+v", total, rows)
	}
}

func TestOrderRefundRecordRepositoryGetByID(t *testing.T) {
	repo, db := setupOrderRefundRecordRepositoryTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	record := &orderdomain.OrderRefundRecord{
		UserID:     1,
		OrderID:    2,
		Type:       constants.OrderRefundTypeManual,
		Amount:     money.FromDecimal(decimal.NewFromInt(12)),
		Currency:   "CNY",
		CreatedAt:  now,
		UpdatedAt:  now,
		GuestEmail: "",
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}

	got, err := repo.GetRefundRecordByID(record.ID)
	if err != nil {
		t.Fatalf("get by id failed: %v", err)
	}
	if got == nil || got.ID != record.ID {
		t.Fatalf("unexpected get by id result: %+v", got)
	}

	missing, err := repo.GetRefundRecordByID(record.ID + 100)
	if err != nil {
		t.Fatalf("get missing by id failed: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing record should be nil, got %+v", missing)
	}
}
