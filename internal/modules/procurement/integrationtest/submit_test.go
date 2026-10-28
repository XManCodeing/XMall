package procurement_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/homles698698-lab/XMall/internal/config"
	orderrefund "github.com/homles698698-lab/XMall/internal/modules/order/application/refund"
	ordercontract "github.com/homles698698-lab/XMall/internal/modules/order/contract"
	ordstore "github.com/homles698698-lab/XMall/internal/modules/order/infrastructure/gormstore"
	procurementapp "github.com/homles698698-lab/XMall/internal/modules/procurement/application"
	procurementstore "github.com/homles698698-lab/XMall/internal/modules/procurement/infrastructure/gormstore"
	procurementreader "github.com/homles698698-lab/XMall/internal/modules/procurement/infrastructure/orderreader"
	"github.com/homles698698-lab/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	orderdomain "github.com/homles698698-lab/XMall/internal/modules/order/domain"

	mappingdomain "github.com/homles698698-lab/XMall/internal/modules/catalog/mapping/domain"

	"github.com/homles698698-lab/XMall/internal/constants"
	procurementcontract "github.com/homles698698-lab/XMall/internal/modules/procurement/contract"
	siteconnectionapp "github.com/homles698698-lab/XMall/internal/modules/siteconnection/application"
)

type safeConnection struct {
	cancel  func() error
	calls   atomic.Int32
	lookups atomic.Int32
	get     func(string) (*procurementcontract.UpstreamOrder, error)
	lookup  func() (*procurementcontract.UpstreamOrder, error)
	create  func() (*procurementcontract.CreateOrderResult, error)
}

func (c *safeConnection) Open(uint) (procurementcontract.UpstreamConnection, error) { return c, nil }
func (c *safeConnection) CallbackURL() string                                       { return "" }
func (c *safeConnection) RetryMax() int                                             { return 5 }
func (c *safeConnection) RetryIntervals() string                                    { return "[1]" }
func (c *safeConnection) CreateOrder(context.Context, procurementcontract.CreateOrderRequest) (*procurementcontract.CreateOrderResult, error) {
	c.calls.Add(1)
	return c.create()
}
func (c *safeConnection) GetOrder(context.Context, uint) (*procurementcontract.UpstreamOrder, error) {
	return nil, errors.New("not available")
}
func (c *safeConnection) CancelOrder(context.Context, uint) error { return errors.New("not available") }
func (c *safeConnection) LookupOrder(context.Context, string) (*procurementcontract.UpstreamOrder, error) {
	c.lookups.Add(1)
	if c.lookup != nil {
		return c.lookup()
	}
	return nil, errors.New("unknown")
}

func (c *safeConnection) GetOrderExternal(_ context.Context, id string) (*procurementcontract.UpstreamOrder, error) {
	return c.get(id)
}
func (c *safeConnection) CancelOrderExternal(context.Context, string) error {
	if c.cancel != nil {
		return c.cancel()
	}
	return errors.New("not supported")
}

type failingEmailLifecycle struct {
	*procurementstore.Lifecycle
	fail     bool
	attempts int
}

func (l *failingEmailLifecycle) EnqueueStatusEmail(uint, string) (bool, error) {
	l.attempts++
	if l.fail {
		return false, errors.New("queue unavailable")
	}
	return false, nil
}
func TestDeliveryNotificationFailurePersistsForSweep(t *testing.T) {
	db := setupProcurementTestDB(t)
	local := createProcTestOrder(t, db, "NOTIFY-REPAIR", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "accepted")
	l := &failingEmailLifecycle{Lifecycle: procurementstore.NewLifecycle(db, nil, nil, config.EmailConfig{}), fail: true}
	svc := procurementapp.NewService(procurementapp.Options{Repository: procurementstore.New(db), Orders: procurementreader.New(ordstore.New(db, "test-guest-credential-secret-with-32-bytes")), Connections: &safeConnection{}, OrderLifecycle: l})
	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", &procurementcontract.Fulfillment{Status: "delivered", Payload: "card"}); err != nil {
		t.Fatal(err)
	}
	var saved ProcurementOrder
	db.First(&saved, proc.ID)
	if !saved.NotificationPending || saved.Status != "fulfilled" {
		t.Fatalf("notification not durable: %+v", saved)
	}
	l.fail = false
	svc.SyncAcceptedOrders()
	db.First(&saved, proc.ID)
	if saved.NotificationPending || l.attempts != 2 {
		t.Fatalf("repair failed: pending=%v attempts=%d", saved.NotificationPending, l.attempts)
	}
}

type recoveryQueue struct{ polls int }

func (q *recoveryQueue) EnqueueSubmit(uint, time.Duration) error {
	return errors.New("must never resubmit")
}
func (q *recoveryQueue) EnqueuePoll(uint, time.Duration) error { q.polls++; return nil }

type recoveryNotifier struct{ calls int }

func (n *recoveryNotifier) NotifyFailure(*ProcurementOrder, string) error { n.calls++; return nil }
func TestUnknownLookupNilRequeuesBoundedAndAlerts(t *testing.T) {
	db := setupProcurementTestDB(t)
	local := createProcTestOrder(t, db, "UNKNOWN-POLL", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "unknown")
	c := &safeConnection{lookup: func() (*procurementcontract.UpstreamOrder, error) { return nil, nil }}
	q, n := &recoveryQueue{}, &recoveryNotifier{}
	svc := procurementapp.NewService(procurementapp.Options{Repository: procurementstore.New(db), Orders: procurementreader.New(ordstore.New(db, "test-guest-credential-secret-with-32-bytes")), Connections: c, Queue: q, Notifications: n})
	for range 12 {
		if err := svc.PollUpstreamStatus(proc.ID); err != nil {
			t.Fatal(err)
		}
	}
	if q.polls != 9 || n.calls != 1 || c.calls.Load() != 0 {
		t.Fatalf("polls=%d alerts=%d creates=%d", q.polls, n.calls, c.calls.Load())
	}
}

func TestRefundClaimSharedTransactionLock(t *testing.T) {
	for _, refundFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(refundFirst), func(t *testing.T) {
			db := setupProcurementTestDB(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			local := createProcTestOrder(t, db, "LOCK-ORDER", "paid", "upstream")
			proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "pending")
			repo := procurementstore.New(db)
			if !refundFirst {
				ok, err := repo.ClaimSubmission(proc, map[string]interface{}{})
				if err != nil || !ok {
					t.Fatalf("claim %v %v", ok, err)
				}
				err = ordstore.New(db, "test-guest-credential-secret-with-32-bytes").WithinTransaction(func(tx ordercontract.Transaction) error {
					return tx.Orders().(ordercontract.RefundGuard).LockRefundScope(local.ID)
				})
				if !errors.Is(err, ordercontract.ErrProcurementRefundBlocked) {
					t.Fatalf("refund not blocked: %v", err)
				}
				return
			}
			locked, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				completed <- db.Transaction(func(tx *gorm.DB) error {
					store := ordstore.New(tx, "test-guest-credential-secret-with-32-bytes")
					if err := store.LockRefundScope(local.ID); err != nil {
						return err
					}
					close(locked)
					<-release
					return store.UpdateStatus(local.ID, "refunded", nil)
				})
			}()
			<-locked
			claimed := make(chan bool, 1)
			claimErr := make(chan error, 1)
			go func() {
				ok, err := repo.ClaimSubmission(proc, map[string]interface{}{})
				claimed <- ok
				claimErr <- err
			}()
			select {
			case <-claimed:
				t.Fatal("claim escaped refund transaction lock")
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			if <-claimed {
				t.Fatal("refunded order was claimed")
			}
			if err := <-claimErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParentRefundChecksEveryChildProcurement(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	parent := createProcTestOrder(t, db, "REFUND-PARENT", "paid", "upstream")
	child := createProcTestOrder(t, db, "REFUND-CHILD", "paid", "upstream")
	db.Model(child).Update("parent_id", parent.ID)
	createTestProcurementOrder(t, db, 1, child.ID, child.OrderNo, "unknown")
	now := time.Now()
	db.Model(parent).Update("paid_at", now)
	svc := orderrefund.New(ordstore.New(db, "test-guest-credential-secret-with-32-bytes"), nil, nil, nil, nil, nil)
	_, _, err := svc.AdminManualRefund(orderrefund.AdminManualRefundInput{OrderID: parent.ID, Amount: money.FromDecimal(decimal.NewFromInt(1))})
	if !errors.Is(err, ordercontract.ErrProcurementRefundBlocked) {
		t.Fatalf("parent refund escaped guard: %v", err)
	}
}

func TestCancelConcurrentTimeoutDoesNotReplay(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	local := createProcTestOrder(t, db, "CANCEL-CAS", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "accepted")
	db.Model(proc).Update("upstream_order_external_id", "opaque")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := &safeConnection{cancel: func() error { calls.Add(1); close(entered); <-release; return context.DeadlineExceeded }, get: func(string) (*procurementcontract.UpstreamOrder, error) {
		return &procurementcontract.UpstreamOrder{ExternalID: "opaque", Status: "pending"}, nil
	}}
	svc := newSafeService(db, c)
	done := make(chan error, 1)
	go func() { done <- svc.CancelManual(proc.ID) }()
	<-entered
	for range 5 {
		_ = svc.CancelManual(proc.ID)
	}
	close(release)
	<-done
	_ = svc.PollUpstreamStatus(proc.ID)
	_ = svc.CancelManual(proc.ID)
	var saved ProcurementOrder
	db.First(&saved, proc.ID)
	if calls.Load() != 1 || saved.Status != "unknown" || !saved.CancellationPending {
		t.Fatalf("cancel calls=%d state=%s", calls.Load(), saved.Status)
	}
}

func TestPendingFulfillmentNeverDelivers(t *testing.T) {
	db := setupProcurementTestDB(t)
	local := createProcTestOrder(t, db, "PENDING-PAYLOAD", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "pending")
	db.Model(proc).Update("request_snapshot", `{"SKUID":1,"Quantity":1}`)
	c := &safeConnection{create: func() (*procurementcontract.CreateOrderResult, error) {
		return &procurementcontract.CreateOrderResult{OK: true, ExternalID: "pending-id", Amount: "1", Fulfillment: &procurementcontract.Fulfillment{Status: "pending", Payload: "not-a-card"}}, nil
	}}
	svc := newSafeService(db, c)
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", &procurementcontract.Fulfillment{Status: "pending", Payload: "not-a-card"}); err == nil {
		t.Fatal("pending callback accepted")
	}
	_ = svc.HandleUpstreamCallback(proc.ID, "partially_refunded", &procurementcontract.Fulfillment{Status: "pending", Payload: "not-a-card"})
	var count int64
	db.Table("fulfillments").Count(&count)
	if count != 0 {
		t.Fatal("pending payload delivered")
	}
}

func TestPartialRefundStillPollsOpaqueIDAndDelivers(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	local := createProcTestOrder(t, db, "PARTIAL-OPAQUE", "partially_refunded", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "partially_refunded")
	db.Model(proc).Update("upstream_order_external_id", "001-A/string")
	calls := 0
	c := &safeConnection{get: func(id string) (*procurementcontract.UpstreamOrder, error) {
		if id != "001-A/string" {
			t.Fatalf("opaque id corrupted: %s", id)
		}
		calls++
		return &procurementcontract.UpstreamOrder{Status: "delivered", Fulfillment: &procurementcontract.Fulfillment{Payload: "remaining-card"}}, nil
	}}
	svc := newSafeService(db, c)
	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatal(err)
	}
	var saved orderdomain.Order
	db.First(&saved, local.ID)
	var count int64
	db.Table("fulfillments").Where("order_id = ?", local.ID).Count(&count)
	if calls != 1 || count != 1 || saved.Status != "partially_refunded" {
		t.Fatalf("calls=%d fulfillment=%d status=%s", calls, count, saved.Status)
	}
}

func newSafeService(db *gorm.DB, c *safeConnection) *procurementapp.Service {
	return procurementapp.NewService(procurementapp.Options{Repository: procurementstore.New(db), Orders: procurementreader.New(ordstore.New(db, "test-guest-credential-secret-with-32-bytes")), Connections: c, OrderLifecycle: procurementstore.NewLifecycle(db, nil, nil, config.EmailConfig{})})
}

func TestSubmissionClaimConcurrentTimeoutAndManualRetry(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	local := createProcTestOrder(t, db, "SAFE-CONCURRENT", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "pending")
	db.Model(proc).Update("request_snapshot", `{"SKUID":1,"Quantity":1,"DownstreamOrderNo":"SAFE-CONCURRENT"}`)
	entered, release := make(chan struct{}), make(chan struct{})
	c := &safeConnection{create: func() (*procurementcontract.CreateOrderResult, error) {
		close(entered)
		<-release
		return nil, context.DeadlineExceeded
	}}
	svc := newSafeService(db, c)
	var wg sync.WaitGroup
	wg.Go(func() { _ = svc.SubmitToUpstream(proc.ID) })
	<-entered
	for range 12 {
		wg.Go(func() { _ = svc.SubmitToUpstream(proc.ID) })
	}
	close(release)
	wg.Wait()
	for range 3 {
		_ = svc.RetryManual(proc.ID)
		svc.SyncAcceptedOrders()
	}
	if c.calls.Load() != 1 {
		t.Fatalf("debit calls=%d, want 1", c.calls.Load())
	}
	var saved ProcurementOrder
	db.First(&saved, proc.ID)
	if saved.Status != "unknown" {
		t.Fatalf("state=%s", saved.Status)
	}
	if err := svc.CancelManual(proc.ID); err == nil {
		t.Fatal("unknown must not cancel locally")
	}
}

func TestSubmissionPersistenceFailureNeverResubmits(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	local := createProcTestOrder(t, db, "SAFE-DB-FAIL", "paid", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "pending")
	db.Model(proc).Update("request_snapshot", `{"SKUID":1,"Quantity":1}`)
	c := &safeConnection{create: func() (*procurementcontract.CreateOrderResult, error) {
		_ = db.Exec(`CREATE TRIGGER fail_accept BEFORE UPDATE ON procurement_orders WHEN NEW.status = 'accepted' BEGIN SELECT RAISE(FAIL, 'injected'); END`).Error
		return &procurementcontract.CreateOrderResult{OK: true, ExternalID: "opaque-9", Amount: "10.00"}, nil
	}}
	svc := newSafeService(db, c)
	if err := svc.SubmitToUpstream(proc.ID); err == nil {
		t.Fatal("expected injected persistence failure")
	}
	var saved ProcurementOrder
	db.First(&saved, proc.ID)
	if saved.Status != "submitting" {
		t.Fatalf("state=%s", saved.Status)
	}
	_ = db.Exec("DROP TRIGGER fail_accept").Error
	db.Model(proc).Update("updated_at", time.Now().Add(-time.Minute))
	c.lookup = func() (*procurementcontract.UpstreamOrder, error) {
		return &procurementcontract.UpstreamOrder{ExternalID: "opaque-9", Amount: "10.00", Status: "delivered", Fulfillment: &procurementcontract.Fulfillment{Payload: "recovered-card"}}, nil
	}
	_ = svc.SubmitToUpstream(proc.ID)
	_ = svc.RetryManual(proc.ID)
	db.First(&saved, proc.ID)
	if saved.Status != "fulfilled" || saved.UpstreamOrderExternalID != "opaque-9" {
		t.Fatalf("recovery failed: %+v", saved)
	}
	if c.calls.Load() != 1 {
		t.Fatalf("create calls=%d", c.calls.Load())
	}
}

func TestAtomicDeliveryRollbackAndLateRefundGuard(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	local := createProcTestOrder(t, db, "SAFE-DELIVERY", "fulfilling", "upstream")
	proc := createTestProcurementOrder(t, db, 1, local.ID, local.OrderNo, "accepted")
	svc := newSafeService(db, &safeConnection{})
	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", nil); err == nil {
		t.Fatal("missing payload accepted")
	}
	_ = db.Exec(`CREATE TRIGGER fail_delivery BEFORE UPDATE ON orders WHEN NEW.status = 'delivered' BEGIN SELECT RAISE(FAIL, 'injected'); END`).Error
	payload := &procurementcontract.Fulfillment{Payload: "secret-card"}
	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", payload); err == nil {
		t.Fatal("expected rollback")
	}
	var count int64
	db.Table("fulfillments").Count(&count)
	var saved ProcurementOrder
	db.First(&saved, proc.ID)
	if count != 0 || saved.Status != "accepted" {
		t.Fatalf("partial commit: count=%d status=%s", count, saved.Status)
	}
	_ = db.Exec("DROP TRIGGER fail_delivery").Error
	db.Model(local).Update("status", "refunded")
	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", payload); err == nil {
		t.Fatal("late delivery must not overwrite refund")
	}
	var current orderdomain.Order
	db.First(&current, local.ID)
	if current.Status != "refunded" {
		t.Fatal(current.Status)
	}
}

// ── SubmitToUpstream tests ──

func TestSubmitToUpstream_Success(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SUBMIT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	// 创建 product mapping 和 sku mapping
	pm := &mappingdomain.Mapping{
		ConnectionID:      1,
		LocalProductID:    1,
		UpstreamProductID: 101,
		IsActive:          true,
	}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{
		ProductMappingID: pm.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	}
	db.Create(sm)

	// mock upstream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"order_id": 999,
			"order_no": "UP-999",
			"status":   "accepted",
			"amount":   "50.00",
			"currency": "CNY",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name:      "test-upstream",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")

	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	// 验证采购单状态 = accepted
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "accepted" {
		t.Errorf("expected procurement status 'accepted', got %q", updatedProc.Status)
	}
	if updatedProc.UpstreamOrderID != 999 {
		t.Errorf("expected upstream_order_id=999, got %d", updatedProc.UpstreamOrderID)
	}

	// 验证本地订单状态 = fulfilling
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_NonRetryableError_Rejects(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-NONRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "product_out_of_stock",
			"error_message": "product out of stock",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 不可重试错误应返回 error
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "unknown" {
		t.Errorf("expected procurement status 'unknown', got %q", updatedProc.Status)
	}

	// 验证本地订单状态回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q after rejection, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_RetryableError_Retries(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-RETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "temporary failure",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 3,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 可重试错误不应返回 error（已入队重试）
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("expected no error for retryable failure, got: %v", err)
	}

	// 验证采购单状态 = failed（而非 rejected）
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "unknown" {
		t.Errorf("expected procurement status 'failed', got %q", updatedProc.Status)
	}
	if updatedProc.RetryCount != 0 {
		t.Errorf("expected retry_count=0, got %d", updatedProc.RetryCount)
	}
}

func TestHandleSubmitFailure_MaxRetriesExhausted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-MAXRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	productMapping := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(productMapping)
	db.Create(&mappingdomain.SKUMapping{
		ProductMappingID: productMapping.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "timeout after retries",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 2, RetryIntervals: "[30,60]",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "failed")
	// 设置 retry_count 已达上限
	db.Model(proc).Update("retry_count", 2)

	svc := newTestProcurementService(db, connSvc)

	// 通过公开提交入口验证：可重试错误在次数耗尽后仍必须转为 rejected。
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "unknown" {
		t.Errorf("expected procurement status 'unknown', got %q", updatedProc.Status)
	}

	// 验证本地订单回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestCancelManual_AcceptedKeepsStatusWhenUpstreamRefuses(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-CANCEL-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)

	// 上游已支付的订单不允许取消
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": "cancel_not_allowed"})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL, ApiKey: "key", ApiSecret: "secret",
		Protocol: constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	db.Model(proc).Update("upstream_order_id", 999)

	svc := newTestProcurementService(db, connSvc)
	if err := svc.CancelManual(proc.ID); !errors.Is(err, procurementcontract.ErrUpstreamCancelRefused) {
		t.Fatalf("expected ErrUpstreamCancelRefused, got %v", err)
	}
	var reloaded ProcurementOrder
	db.First(&reloaded, proc.ID)
	if reloaded.Status != constants.ProcurementStatusAccepted {
		t.Fatalf("expected procurement to stay accepted, got %q", reloaded.Status)
	}
}

// 上游以非 200 + 错误码拒单（如余额不足）时按错误码判断，不可重试的直接拒绝，不反复打上游。
func TestSubmitToUpstream_HTTPErrorWithNonRetryableCode_Rejects(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-HTTP402-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": "insufficient_balance", "error_message": "balance not enough"})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	_ = newTestProcurementService(db, connSvc).SubmitToUpstream(proc.ID)

	var updated ProcurementOrder
	db.First(&updated, proc.ID)
	if updated.Status != "unknown" || updated.RetryCount != 0 {
		t.Fatalf("expected rejected without retries, got status=%q retry_count=%d", updated.Status, updated.RetryCount)
	}
}

// 本地订单已退款时（如巡检补投历史积压单），不能再去上游下单扣款。
func TestSubmitToUpstream_LocalOrderRefunded_RejectsWithoutCallingUpstream(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-REFUNDED-001", constants.OrderStatusRefunded, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true})

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "order_id": 999, "order_no": "UP-999"})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "failed")
	if err := newTestProcurementService(db, connSvc).SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	var updated ProcurementOrder
	db.First(&updated, proc.ID)
	if called || updated.Status != "unknown" {
		t.Fatalf("expected rejected without upstream call, got called=%v status=%q", called, updated.Status)
	}
}

// 下单响应回来前采购单已被回调推进或被管理员取消：不回退状态，但必须补记上游订单号供对账与退款同步。
func TestSubmitToUpstream_StatusChangedDuringRequest_KeepsStatusAndRecordsUpstreamOrder(t *testing.T) {
	for _, concurrent := range []string{constants.ProcurementStatusFulfilled, constants.ProcurementStatusCanceled} {
		t.Run(concurrent, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "PROC-RACE-"+concurrent, constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
			db.Create(pm)
			db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true})

			var procID uint
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				db.Model(&ProcurementOrder{}).Where("id = ?", procID).Update("status", concurrent)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "order_id": 999, "order_no": "UP-999", "amount": "50.00", "currency": "CNY"})
			}))
			defer server.Close()

			connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
			conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
				Name: "test-upstream", BaseURL: server.URL,
				ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
			})
			procID = createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending").ID
			if err := newTestProcurementService(db, connSvc).SubmitToUpstream(procID); err != nil {
				t.Fatalf("SubmitToUpstream: %v", err)
			}

			var updated ProcurementOrder
			db.First(&updated, procID)
			if updated.Status != concurrent || updated.UpstreamOrderID != 999 || updated.UpstreamOrderNo != "UP-999" {
				t.Fatalf("expected status=%q with upstream order recorded, got status=%q upstream_order_id=%d no=%q",
					concurrent, updated.Status, updated.UpstreamOrderID, updated.UpstreamOrderNo)
			}
		})
	}
}
