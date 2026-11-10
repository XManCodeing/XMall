package application

import (
	"context"
	"errors"
	"fmt"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/provider"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// manualFixture 使用已有真实 SQLite 支付服务夹具，不访问外部支付或 Telegram。
func manualFixture(t *testing.T) (*PaymentService, *gorm.DB, *orderdomain.Order, *paymentdomain.Payment, paymentcontract.ManualConfirmationInput) {
	t.Helper()
	service, db := setupPaymentServiceWalletTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&paymentdomain.ManualConfirmation{}, &paymentdomain.PaymentEvidenceClaim{}, &paymentdomain.PaymentReceiptAnomaly{}, &membershipdomain.OrderItemSnapshot{}, &membershipdomain.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour)
	paid := created.Add(time.Minute)
	amount := money.FromDecimal(decimal.NewFromInt(10))
	order := &orderdomain.Order{OrderNo: "manual-fixture", Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: amount, CreatedAt: created}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	payment := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, Status: constants.PaymentStatusPending, Currency: "CNY", Amount: amount, CreatedAt: created}
	if err := db.Create(payment).Error; err != nil {
		t.Fatal(err)
	}
	input := paymentcontract.ManualConfirmationInput{RequestID: "manual-request-001", ExpectedStatus: constants.PaymentStatusPending, Amount: "10.00", Currency: "CNY", PaidAt: paid, TransactionID: "bank-transaction-001", Reason: "核对银行到账记录", AdminID: 41}
	return service, db, order, payment, input
}

// manualProgressSpy counts non-idempotent spend dispatches for isolated replay tests.
type manualProgressSpy struct{ calls int }

// OnOrderPaid records one dispatch; user/amount are synthetic inputs, and no I/O is performed.
func (spy *manualProgressSpy) OnOrderPaid(_ uint, _ decimal.Decimal) error { spy.calls++; return nil }

// OnRechargeCompleted is unused by order confirmation and returns no error.
func (*manualProgressSpy) OnRechargeCompleted(_ uint, _ decimal.Decimal) error { return nil }

// TestManualReplayDoesNotIncrementSpend verifies repeated evidence never repeats spend accounting.
// t uses the existing single-connection fixture and no real payment service.
func TestManualReplayDoesNotIncrementSpend(t *testing.T) {
	service, db, order, payment, input := manualFixture(t)
	order.UserID = 81
	if err := db.Save(order).Error; err != nil {
		t.Fatal(err)
	}
	progress := &manualProgressSpy{}
	service.SetMemberLevelService(progress)
	for range 3 {
		if _, err := service.ConfirmManually(payment.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	if progress.calls != 1 {
		t.Fatalf("spend accounting dispatched %d times", progress.calls)
	}
}

// manualStaleReadStore supplies one pre-confirmation snapshot, then uses the real transactional store.
type manualStaleReadStore struct {
	paymentcontract.Store
	stale paymentdomain.Payment
}

// GetByID simulates a callback read racing a committed confirmation without changing persisted data.
func (store manualStaleReadStore) GetByID(_ uint) (*paymentdomain.Payment, error) {
	copy := store.stale
	return &copy, nil
}

// TestManualConfirmationRejectsStalePendingCallback verifies the callback re-reads under the shared lock.
// t uses only synthetic SQLite state and asserts success and original proof survive.
func TestManualConfirmationRejectsStalePendingCallback(t *testing.T) {
	service, db, _, payment, input := manualFixture(t)
	stale := *payment
	if _, err := service.ConfirmManually(payment.ID, input); err != nil {
		t.Fatal(err)
	}
	service.paymentRepo = manualStaleReadStore{Store: service.paymentRepo, stale: stale}
	_, err := service.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusPending, Currency: "CNY", Amount: payment.Amount, ProviderRef: "stale-ref"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.First(payment, payment.ID).Error; err != nil || payment.Status != constants.PaymentStatusSuccess || payment.ProviderRef != input.TransactionID {
		t.Fatalf("stale callback overwrote confirmation: %+v err=%v", payment, err)
	}
}

// TestManualConfirmationAtomicReplay 覆盖独立审计、可信时间、会员 outbox 和证据重放保护。
func TestManualConfirmationAtomicReplay(t *testing.T) {
	service, db, order, payment, input := manualFixture(t)
	item := orderdomain.OrderItem{OrderID: order.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		result, err := service.ConfirmManually(payment.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Replayed != (index == 1) || result.Confirmation.AdminID != 41 || !result.PaidAt.Equal(input.PaidAt) {
			t.Fatalf("result %+v", result)
		}
	}
	if err := db.First(payment, payment.ID).Error; err != nil || payment.CallbackAt != nil {
		t.Fatalf("forged callback %+v %v", payment, err)
	}
	if err := db.First(order, order.ID).Error; err != nil || !order.PaidAt.Equal(input.PaidAt) {
		t.Fatalf("time %+v %v", order, err)
	}
	var count int64
	if err := db.Model(&membershipdomain.PaidEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("outbox %d %v", count, err)
	}
	changed := input
	changed.Reason = "改变原证据"
	if _, err := service.ConfirmManually(payment.ID, changed); !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
		t.Fatal(err)
	}
}

// TestManualConfirmationRollback 注入审计或 outbox 写入错误，验证订单、支付和证据槽位原子回滚。
func TestManualConfirmationRollback(t *testing.T) {
	for _, table := range []string{"payment_manual_confirmations", "membership_paid_events"} {
		t.Run(table, func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			item := orderdomain.OrderItem{OrderID: order.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"}
			if err := db.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Before("gorm:create").Register("manual_test_failure", func(tx *gorm.DB) {
				if (table == "payment_manual_confirmations" && tx.Statement.Table == table) || (table == "membership_paid_events" && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "PaidEvent") {
					tx.AddError(errors.New("injected persistence failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ConfirmManually(payment.ID, input); err == nil {
				t.Fatal("expected rollback")
			}
			if err := db.First(payment, payment.ID).Error; err != nil || payment.Status != constants.PaymentStatusPending {
				t.Fatalf("payment %+v %v", payment, err)
			}
			if err := db.First(order, order.ID).Error; err != nil || order.PaidAt != nil {
				t.Fatalf("order %+v %v", order, err)
			}
			var count int64
			if err := db.Model(&paymentdomain.PaymentEvidenceClaim{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("claims %d %v", count, err)
			}
		})
	}
}

// TestManualConfirmationConcurrentAndLateCallback 验证并发重放和迟到额外流水的持久异常。
func TestManualConfirmationConcurrentAndLateCallback(t *testing.T) {
	service, db, _, payment, input := manualFixture(t)
	var wait sync.WaitGroup
	results := make(chan error, 6)
	for index := 0; index < 6; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := service.ConfirmManually(payment.ID, input); results <- err }()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	callback := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "CNY", ProviderRef: input.TransactionID}
	if _, err := service.HandleCallback(callback); err != nil {
		t.Fatal(err)
	}
	callback.ProviderRef = "additional-bank-receipt"
	if _, err := service.HandleCallback(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&paymentdomain.ManualConfirmation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("audit %d %v", count, err)
	}
	if err := db.Model(&paymentdomain.PaymentReceiptAnomaly{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("anomaly %d %v", count, err)
	}
	if err := db.First(payment, payment.ID).Error; err != nil || payment.ExceptionCode != "" || payment.ProviderRef != input.TransactionID || !payment.PaidAt.Equal(input.PaidAt) {
		t.Fatalf("evidence %+v %v", payment, err)
	}
}

// TestManualConfirmationRejectsUnsafeFacts 拒绝缺少证据、金额币种时刻错误及关闭退款订单。
func TestManualConfirmationRejectsUnsafeFacts(t *testing.T) {
	for _, kind := range []string{"amount", "currency", "future", "before_creation", "missing_evidence", "canceled", "expired", "refunded", "child"} {
		t.Run(kind, func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			switch kind {
			case "amount":
				input.Amount = "9.00"
			case "currency":
				input.Currency = "USD"
			case "future":
				input.PaidAt = time.Now().Add(time.Hour)
			case "before_creation":
				input.PaidAt = order.CreatedAt.Add(-time.Hour)
			case "missing_evidence":
				input.TransactionID = ""
			case "canceled":
				order.Status = constants.OrderStatusCanceled
			case "expired":
				expired := time.Now().Add(-time.Minute)
				order.ExpiresAt = &expired
			case "refunded":
				order.RefundedAmount = money.FromDecimal(decimal.NewFromInt(1))
			case "child":
				parentID := uint(999)
				order.ParentID = &parentID
			}
			if err := db.Save(order).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.ConfirmManually(payment.ID, input); err == nil {
				t.Fatal("unsafe confirmation accepted")
			}
		})
	}
}

// TestManualMixedParentAndCallbackRace 验证父子单、余额混合付款及人工/网关并发沿同一付款根完成。
func TestManualMixedParentAndCallbackRace(t *testing.T) {
	service, db, order, payment, input := manualFixture(t)
	order.TotalAmount = money.FromDecimal(decimal.NewFromInt(15))
	order.WalletPaidAmount = money.FromDecimal(decimal.NewFromInt(5))
	if err := db.Save(order).Error; err != nil {
		t.Fatal(err)
	}
	child := &orderdomain.Order{OrderNo: "manual-child", ParentID: &order.ID, Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: order.TotalAmount, CreatedAt: order.CreatedAt}
	if err := db.Create(child).Error; err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { _, err := service.ConfirmManually(payment.ID, input); results <- err }()
	go func() {
		_, err := service.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "CNY", ProviderRef: input.TransactionID, PaidAt: &input.PaidAt})
		results <- err
	}()
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(order, order.ID).Error; err != nil || !order.WalletPaidAmount.Decimal.Equal(decimal.NewFromInt(5)) || !order.OnlinePaidAmount.Decimal.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("mixed %+v %v", order, err)
	}
	if err := db.First(child, child.ID).Error; err != nil || child.Status != constants.OrderStatusPaid || !child.PaidAt.Equal(input.PaidAt) {
		t.Fatalf("child %+v %v", child, err)
	}
}

// TestSharedReceiptCrossRootCompetition 固定首个占位事务的提交顺序，覆盖人工/网关两种赢家。
func TestSharedReceiptCrossRootCompetition(t *testing.T) {
	for _, manualFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(manualFirst), func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			otherOrder := *order
			otherOrder.ID = 0
			otherOrder.OrderNo = "cross-root"
			if err := db.Create(&otherOrder).Error; err != nil {
				t.Fatal(err)
			}
			otherPayment := *payment
			otherPayment.ID = 0
			otherPayment.OrderID = otherOrder.ID
			if err := db.Create(&otherPayment).Error; err != nil {
				t.Fatal(err)
			}
			entered, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			if err := db.Callback().Create().After("gorm:create").Register("hold_receipt_claim", func(tx *gorm.DB) {
				if tx.Statement.Table == "payment_evidence_claims" {
					once.Do(func() { close(entered); <-release })
				}
			}); err != nil {
				t.Fatal(err)
			}
			manual := func() error { _, err := service.ConfirmManually(payment.ID, input); return err }
			gateway := func() error {
				_, err := service.HandleCallback(PaymentCallbackInput{PaymentID: otherPayment.ID, Status: constants.PaymentStatusSuccess, Amount: otherPayment.Amount, Currency: "CNY", ProviderRef: input.TransactionID, PaidAt: &input.PaidAt})
				return err
			}
			first, second := manual, gateway
			if !manualFirst {
				first, second = gateway, manual
			}
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- first() }()
			<-entered
			go func() { close(started); secondDone <- second() }()
			<-started
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			secondErr := <-secondDone
			if manualFirst && secondErr != nil {
				t.Fatal(secondErr)
			}
			if !manualFirst && !errors.Is(secondErr, paymentcontract.ErrManualConfirmationConflict) {
				t.Fatalf("manual collision %v", secondErr)
			}
			var paidCount, claimCount, anomalyCount int64
			if err := db.Model(&orderdomain.Order{}).Where("paid_at IS NOT NULL").Count(&paidCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&paymentdomain.PaymentEvidenceClaim{}).Count(&claimCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&paymentdomain.PaymentReceiptAnomaly{}).Count(&anomalyCount).Error; err != nil {
				t.Fatal(err)
			}
			if paidCount != 1 || claimCount != 1 || (manualFirst && anomalyCount != 1) {
				t.Fatalf("paid=%d claims=%d anomalies=%d", paidCount, claimCount, anomalyCount)
			}
			// 同笔重放不新增审计/异常，也不能解锁输家订单。
			if err := gateway(); err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&orderdomain.Order{}).Where("paid_at IS NOT NULL").Count(&paidCount).Error; err != nil || paidCount != 1 {
				t.Fatalf("replay paid=%d err=%v", paidCount, err)
			}
		})
	}
}

// TestSharedReceiptHistoricalAndChannelScope 验证未回填槽位的历史流水仍优先，跨渠道同号不冲突。
func TestSharedReceiptHistoricalAndChannelScope(t *testing.T) {
	for _, sameChannel := range []bool{true, false} {
		t.Run(fmt.Sprint(sameChannel), func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			historical := *payment
			historical.ID = 0
			historical.OrderID = 999
			historical.Status = constants.PaymentStatusSuccess
			historical.ProviderRef = input.TransactionID
			historical.PaidAt = &input.PaidAt
			if !sameChannel {
				historical.ChannelID++
			}
			if err := db.Create(&historical).Error; err != nil {
				t.Fatal(err)
			}
			callback := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "CNY", ProviderRef: input.TransactionID, PaidAt: &input.PaidAt}
			for count := 0; count < 2; count++ {
				if _, err := service.HandleCallback(callback); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(order, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if (order.PaidAt == nil) != sameChannel {
				t.Fatalf("historical guard %+v", order)
			}
			var claims []paymentdomain.PaymentEvidenceClaim
			if err := db.Find(&claims).Error; err != nil || len(claims) != 1 {
				t.Fatalf("claims %+v %v", claims, err)
			}
			expected := payment.ID
			if sameChannel {
				expected = historical.ID
			}
			if claims[0].PaymentID != expected {
				t.Fatalf("wrong owner %+v", claims)
			}
		})
	}
}

// TestSharedReceiptRechargeCollision 验证充值不能使用订单已占用的流水增加余额。
func TestSharedReceiptRechargeCollision(t *testing.T) {
	service, db, _, payment, input := manualFixture(t)
	rechargePayment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	rechargePayment.ChannelID = payment.ChannelID
	if err := db.Save(rechargePayment).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmManually(payment.ID, input); err != nil {
		t.Fatal(err)
	}
	callback := PaymentCallbackInput{PaymentID: rechargePayment.ID, Status: constants.PaymentStatusSuccess, Amount: rechargePayment.Amount, Currency: rechargePayment.Currency, ProviderRef: input.TransactionID}
	for index := 0; index < 2; index++ {
		if _, err := service.HandleCallback(callback); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(recharge, recharge.ID).Error; err != nil || recharge.Status == constants.WalletRechargeStatusSuccess {
		t.Fatalf("recharge %+v %v", recharge, err)
	}
	var count int64
	if err := db.Model(&walletdomain.Transaction{}).Where("type = ?", constants.WalletTxnTypeRecharge).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("credit count %d %v", count, err)
	}
	if err := db.Model(&paymentdomain.PaymentReceiptAnomaly{}).Where("payment_id = ?", rechargePayment.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("anomalies %d %v", count, err)
	}
}

// TestSharedReceiptStorageFailureFailsClosed 验证缺失唯一表/异常表必须回滚，不退回仅 payment ID 去重。
func TestSharedReceiptStorageFailureFailsClosed(t *testing.T) {
	for _, table := range []string{"payment_evidence_claims", "payment_receipt_anomalies"} {
		t.Run(table, func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			if table == "payment_receipt_anomalies" {
				historical := *payment
				historical.ID = 0
				historical.OrderID = 999
				historical.ProviderRef = input.TransactionID
				historical.Status = constants.PaymentStatusSuccess
				if err := db.Create(&historical).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Migrator().DropTable(table); err != nil {
				t.Fatal(err)
			}
			_, err := service.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "CNY", ProviderRef: input.TransactionID})
			if err == nil {
				t.Fatal("missing durable evidence table accepted")
			}
			if err := db.First(order, order.ID).Error; err != nil || order.PaidAt != nil {
				t.Fatalf("unsafe payment %+v %v", order, err)
			}
		})
	}
}

// TestMembershipPaidCallbackRepair 验证可信重复回调补齐独立事件，不需要联系人、Source 或当前商品。
func TestMembershipPaidCallbackRepair(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&membershipdomain.OrderItemSnapshot{}, &membershipdomain.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	order := orderdomain.Order{OrderNo: "member-repair", Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paidAt}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: order.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
		t.Fatal(err)
	}
	payment := paymentdomain.Payment{OrderID: order.ID, Currency: "CNY", Status: constants.PaymentStatusSuccess, PaidAt: &paidAt, Amount: money.FromDecimal(decimal.NewFromInt(10))}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if _, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Currency: "CNY", Amount: payment.Amount}); err != nil {
			t.Fatal(err)
		}
	}
	var events []membershipdomain.PaidEvent
	if err := db.Find(&events).Error; err != nil || len(events) != 1 || !events[0].PaidAt.Equal(paidAt) {
		t.Fatalf("events %+v %v", events, err)
	}
}

// TestMembershipWalletReplayRepairsPaidEvent 验证余额付款成功与幂等重放均追加事件；普通商品门控不变。
func TestMembershipWalletReplayRepairsPaidEvent(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&membershipdomain.OrderItemSnapshot{}, &membershipdomain.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	user := userdomain.User{Email: "event-wallet@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	amount := money.FromDecimal(decimal.NewFromInt(10))
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: amount}).Error; err != nil {
		t.Fatal(err)
	}
	order := orderdomain.Order{OrderNo: "wallet-member-repair", UserID: user.ID, Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: amount}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: order.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
		t.Fatal(err)
	}
	// 内部历史 fixture 不启用或放开任何生产会员销售门控，仍使用真实钱包扣款路径。
	input := CreatePaymentInput{OrderID: order.ID, UseBalance: true, OperationKey: "wallet-replay-event"}
	if result, err := svc.CreatePayment(input); err != nil || !result.OrderPaid {
		t.Fatalf("wallet result=%+v err=%v", result, err)
	}
	var event membershipdomain.PaidEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&event).Error; err != nil {
		t.Fatal(err)
	}
	if result, err := svc.CreatePayment(input); err != nil || !result.OrderPaid {
		t.Fatalf("replay result=%+v err=%v", result, err)
	}
	var restored membershipdomain.PaidEvent
	if err := db.First(&restored).Error; err != nil || !restored.PaidAt.Equal(event.PaidAt) || restored.PaymentID != event.PaymentID {
		t.Fatalf("restored %+v %v", restored, err)
	}
}

// TestMembershipCallbackAppendFailureRetries 验证真实回调中 outbox 写入失败回滚支付/订单，修复 schema 后原回调可重试。
func TestMembershipCallbackAppendFailureRetries(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&membershipdomain.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	_, order := createFeePolicyOrderFixture(t, db, "paid-event", "paid-event-rollback")
	item := orderdomain.OrderItem{OrderID: order.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: "manual"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
		t.Fatal(err)
	}
	payment := paymentdomain.Payment{OrderID: order.ID, Currency: "CNY", Status: constants.PaymentStatusPending, Amount: order.TotalAmount}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	paidAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	input := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "CNY", PaidAt: &paidAt}
	if _, err := svc.HandleCallback(input); err == nil {
		t.Fatal("append failure swallowed")
	}
	if err := db.First(&payment, payment.ID).Error; err != nil || payment.Status != constants.PaymentStatusPending {
		t.Fatalf("payment not rolled back %+v %v", payment, err)
	}
	if err := db.First(order, order.ID).Error; err != nil || order.PaidAt != nil {
		t.Fatalf("order not rolled back %+v %v", order, err)
	}
	if err := db.AutoMigrate(&membershipdomain.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleCallback(input); err != nil {
		t.Fatal(err)
	}
	var event membershipdomain.PaidEvent
	if err := db.First(&event).Error; err != nil || !event.PaidAt.Equal(paidAt) {
		t.Fatalf("event %+v %v", event, err)
	}
}

// paymentExternalProbe 验证真实继续支付入口在持有事务前调用补查端口。
type paymentExternalProbe struct {
	db    *gorm.DB
	calls int
}

func (*paymentExternalProbe) Checkout(context.Context, []membership.CheckoutLine) (*membership.CapabilityProof, error) {
	return nil, nil
}
func (p *paymentExternalProbe) Payment(ctx context.Context, _ *orderdomain.Order) error {
	p.calls++
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var count int64
	if err := p.db.WithContext(ctx).Model(&orderdomain.Order{}).Count(&count).Error; err != nil {
		return err
	}
	return membership.ErrProbeRetry
}

// TestPaymentExternalPreflightWiring 覆盖创建付款和旧网关收款前的事务外接线。
func TestPaymentExternalPreflightWiring(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	channel, order := createFeePolicyOrderFixture(t, db, "external-preflight", "EXTERNAL-PREFLIGHT")
	svc.enforcePublicSale = true
	probe := &paymentExternalProbe{db: db}
	svc.SetMembershipPreflight(probe)
	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID}); !errors.Is(err, membership.ErrProbeRetry) {
		t.Fatal(err)
	}
	if err := svc.checkPaymentPublicSale(order.ID); !errors.Is(err, membership.ErrProbeRetry) {
		t.Fatal(err)
	}
	if probe.calls != 2 {
		t.Fatal(probe.calls)
	}
}

// TestConvertedMembershipOrderCannotPay 验证普通待付单转换后，包含子订单、余额和旧支付链接均拒绝。
func TestConvertedMembershipOrderCannotPay(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "parent"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			if err := db.AutoMigrate(&membershipdomain.ProductConfig{}, &membershipdomain.OrderItemSnapshot{}); err != nil {
				t.Fatal(err)
			}
			svc.enforcePublicSale = true
			channel, order := createFeePolicyOrderFixture(t, db, "sale-guard", "SALE-GUARD")
			itemOrderID := order.ID
			if child {
				childOrder := &orderdomain.Order{OrderNo: "SALE-GUARD-CHILD", ParentID: &order.ID, Status: constants.OrderStatusPendingPayment, Currency: "CNY"}
				if err := db.Create(childOrder).Error; err != nil {
					t.Fatal(err)
				}
				itemOrderID = childOrder.ID
			}
			if err := db.Create(&orderdomain.OrderItem{OrderID: itemOrderID, ProductID: 81, Quantity: 1, TitleJSON: jsonmap.JSON{"en": "ordinary"}}).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.checkPaymentPublicSale(order.ID); err != nil {
				t.Fatalf("ordinary order: %v", err)
			}
			registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{})
			if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "old-operation"}); err != nil {
				t.Fatalf("ordinary payment must pass: %v", err)
			}
			if err := db.Create(&membershipdomain.ProductConfig{ProductID: 81, Enabled: false}).Error; err != nil {
				t.Fatal(err)
			}
			for _, input := range []CreatePaymentInput{
				{OrderID: order.ID, ChannelID: channel.ID},
				{OrderID: order.ID, UseBalance: true},
				{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "old-operation"},
			} {
				if _, err := svc.CreatePayment(input); !errors.Is(err, membership.ErrInvalid) {
					t.Fatalf("payment should reject: %v", err)
				}
			}
			payment := &paymentdomain.Payment{OrderID: order.ID, ChannelID: channel.ID, Status: constants.PaymentStatusPending, ProviderRef: "old-link"}
			if _, err := svc.captureViaRegistry(CapturePaymentInput{}, payment, channel); !errors.Is(err, membership.ErrInvalid) {
				t.Fatalf("capture should reject before gateway: %v", err)
			}
			if err := svc.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
				loaded, err := tx.Orders().GetByIDForUpdateWithChildren(order.ID)
				if err != nil {
					return err
				}
				return svc.markOrderPaid(tx, loaded, 1, time.Now())
			}); !errors.Is(err, productcontract.ErrMembershipSaleClosed) {
				t.Fatalf("callback must not fulfill membership order: %v", err)
			}
			var count int64
			if err := db.Model(&paymentdomain.Payment{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("membership conversion created extra payments: %d", count)
			}
		})
	}
}

// TestHistoricalSnapshotRejectsBeforeMoney 验证商品已不存在时仍在钱包/网关/重试之前拒绝。
// 故意损坏固定数量，使测试不依赖当前商品、绑定或群配置的存在。
func TestHistoricalSnapshotRejectsBeforeMoney(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			svc, db, order, payment, replay := callbackSaleFixture(t, child)
			var item orderdomain.OrderItem
			if err := db.Where("product_id = ?", 81).First(&item).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&orderdomain.Order{}).Where("id = ?", item.OrderID).Update("user_id", order.UserID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot := membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, ProductID: item.ProductID, SKUID: item.SKUID, AccountID: order.UserID, Quantity: 2}
			if err := db.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
			for _, input := range []CreatePaymentInput{replay, {OrderID: order.ID, UseBalance: true}, {OrderID: order.ID, ChannelID: payment.ChannelID}} {
				if _, err := svc.CreatePayment(input); !errors.Is(err, membership.ErrInvalid) {
					t.Fatalf("expected snapshot rejection: %v", err)
				}
			}
			channel, err := svc.channelRepo.GetByID(payment.ChannelID)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.applyProviderPayment(replay, order, channel, payment); !errors.Is(err, membership.ErrInvalid) {
				t.Fatalf("provider: %v", err)
			}
			if _, err := svc.captureViaRegistry(CapturePaymentInput{}, payment, channel); !errors.Is(err, membership.ErrInvalid) {
				t.Fatalf("capture: %v", err)
			}
			var movements, payments int64
			if err := db.Model(&walletdomain.Transaction{}).Count(&movements).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&paymentdomain.Payment{}).Count(&payments).Error; err != nil {
				t.Fatal(err)
			}
			if movements != 0 || payments != 1 {
				t.Fatalf("money changed: wallet=%d payments=%d", movements, payments)
			}
		})
	}
}

// TestPublicSaleMissingPortFailsClosed 验证已启用门控不能因仓储缺少能力而放行。
func TestPublicSaleMissingPortFailsClosed(t *testing.T) {
	svc := &PaymentService{enforcePublicSale: true}
	if err := svc.checkOrderPublicSale(nil, &orderdomain.Order{Items: []orderdomain.OrderItem{{ProductID: 1}}}); !errors.Is(err, productcontract.ErrSalePolicyUnavailable) {
		t.Fatalf("expected missing port error: %v", err)
	}
}

// callbackSaleFixture 创建纯内存普通订单与已签发支付，用于真实收款事实回归。
// child 决定商品归属子单；返回服务、数据库、订单、支付与原始创建输入。
func callbackSaleFixture(t *testing.T, child bool) (*PaymentService, *gorm.DB, *orderdomain.Order, *paymentdomain.Payment, CreatePaymentInput) {
	t.Helper()
	svc, db := setupPaymentServiceWalletTest(t)
	if err := db.AutoMigrate(&membershipdomain.ProductConfig{}, &membershipdomain.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	svc.enforcePublicSale = true
	channel, order := createFeePolicyOrderFixture(t, db, "callback-sale", "CALLBACK-SALE")
	user := &userdomain.User{Email: "callback-sale@example.com", PasswordHash: "test", Status: "active"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	order.UserID = user.ID
	if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	itemOrderID := order.ID
	if child {
		childOrder := &orderdomain.Order{OrderNo: "CALLBACK-CHILD", ParentID: &order.ID, Status: constants.OrderStatusPendingPayment, Currency: "CNY"}
		if err := db.Create(childOrder).Error; err != nil {
			t.Fatal(err)
		}
		itemOrderID = childOrder.ID
	}
	if err := db.Create(&orderdomain.OrderItem{OrderID: itemOrderID, ProductID: 81, Quantity: 1, TitleJSON: jsonmap.JSON{"en": "ordinary"}}).Error; err != nil {
		t.Fatal(err)
	}
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{})
	input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "existing-operation"}
	result, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	return svc, db, order, result.Payment, input
}

// TestMembershipCallbackPreservesCollectedMoney 覆盖父子单、迟到与不足额成功，不自动退还或充值。
func TestMembershipCallbackPreservesCollectedMoney(t *testing.T) {
	for _, kind := range []string{"parent", "child", "late", "underpaid"} {
		t.Run(kind, func(t *testing.T) {
			svc, db, order, payment, replay := callbackSaleFixture(t, kind == "child")
			if err := db.Create(&membershipdomain.ProductConfig{ProductID: 81, Enabled: false}).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "late" {
				if err := db.Model(order).Update("status", constants.OrderStatusCanceled).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(payment).Updates(map[string]interface{}{"status": constants.PaymentStatusExpired, "superseded_at": time.Now()}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "underpaid" {
				payment.Amount = money.FromDecimal(decimal.NewFromInt(40))
				if err := db.Model(payment).Update("amount", payment.Amount).Error; err != nil {
					t.Fatal(err)
				}
			}
			paidAt := time.Now().UTC().Truncate(time.Second)
			input := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: payment.Currency, ProviderRef: "verified-provider", PaidAt: &paidAt, Payload: jsonmap.JSON{"verified_fact": "kept"}}
			for _, status := range []string{constants.PaymentStatusSuccess, constants.PaymentStatusSuccess, constants.PaymentStatusFailed, constants.PaymentStatusExpired} {
				input.Status = status
				updated, err := svc.HandleCallback(input)
				if err != nil {
					t.Fatal(err)
				}
				if updated.Status != constants.PaymentStatusSuccess || updated.ExceptionCode != constants.PaymentExceptionMembershipSaleClosed {
					t.Fatalf("lost success or exception: %+v", updated)
				}
			}
			stored, err := svc.paymentRepo.GetByID(payment.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.PaidAt == nil || !stored.PaidAt.Equal(paidAt) || stored.ProviderRef != "verified-provider" || stored.ProviderPayload["verified_fact"] != "kept" {
				t.Fatalf("lost provider facts: %+v", stored)
			}
			loaded, err := svc.orderRepo.GetByID(order.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.PaidAt != nil || loaded.Status == constants.OrderStatusPaid {
				t.Fatal("membership order fulfilled")
			}
			for _, child := range loaded.Children {
				if child.PaidAt != nil || child.Status != constants.OrderStatusPendingPayment {
					t.Fatal("child fulfilled")
				}
			}
			var movements int64
			if err := db.Model(&walletdomain.Transaction{}).Count(&movements).Error; err != nil {
				t.Fatal(err)
			}
			if movements != 0 {
				t.Fatal("membership rejection must not create wallet movement")
			}
			result, err := svc.CreatePayment(replay)
			if err != nil || result.Payment.ID != stored.ID || result.Payment.Status != constants.PaymentStatusSuccess || result.OrderPaid {
				t.Fatalf("successful replay blocked: %v", err)
			}
		})
	}
}

// TestOrdinarySuccessRemainsHistorical 验证普通商品成功后转换配置，重复/乱序回调和重放保持原事实。
func TestOrdinarySuccessRemainsHistorical(t *testing.T) {
	svc, db, order, payment, replay := callbackSaleFixture(t, false)
	input := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: payment.Currency}
	updated, err := svc.HandleCallback(input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ExceptionCode != "" || updated.PaidAt == nil {
		t.Fatal("ordinary success regressed")
	}
	if err := db.Create(&membershipdomain.ProductConfig{ProductID: 81}).Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{constants.PaymentStatusSuccess, constants.PaymentStatusExpired} {
		input.Status = status
		updated, err = svc.HandleCallback(input)
		if err != nil || updated.ExceptionCode != "" || updated.Status != constants.PaymentStatusSuccess {
			t.Fatalf("history changed: %v", err)
		}
	}
	// 模拟两个并发回调都读到旧 pending；事务内重复成功也不得重新分类。
	if _, _, fulfilled, err := svc.applyPaymentUpdate(payment, order, constants.PaymentStatusSuccess, input, time.Now()); err != nil || fulfilled {
		t.Fatalf("stale callback must preserve historical success: %v", err)
	}
	result, err := svc.CreatePayment(replay)
	if err != nil || result.Payment.ID != payment.ID || !result.OrderPaid {
		t.Fatalf("historical replay failed: %v", err)
	}
	loaded, err := svc.orderRepo.GetByID(order.ID)
	if err != nil || loaded.PaidAt == nil {
		t.Fatalf("lost paid order: %v", err)
	}
}

// TestCallbackSaleQueryFailureRetries 验证数据库失败不被误认为明确会员异常，恢复后可重试成功。
func TestCallbackSaleQueryFailureRetries(t *testing.T) {
	svc, db, _, payment, _ := callbackSaleFixture(t, false)
	if err := db.Migrator().DropTable(&membershipdomain.ProductConfig{}); err != nil {
		t.Fatal(err)
	}
	input := PaymentCallbackInput{PaymentID: payment.ID, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: payment.Currency}
	if _, err := svc.HandleCallback(input); err == nil {
		t.Fatal("database failure should retry")
	}
	stored, err := svc.paymentRepo.GetByID(payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == constants.PaymentStatusSuccess || stored.ExceptionCode != "" {
		t.Fatal("SQL failure misclassified")
	}
	if err := db.AutoMigrate(&membershipdomain.ProductConfig{}, &membershipdomain.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleCallback(input); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

// recordingCapturer 捕获传入 ValidateConfig 的第二参数,用于回归测试。
type recordingCapturer struct {
	receivedMode string
}

func (r *recordingCapturer) Type() string { return "test_capturer" }

func (r *recordingCapturer) ValidateConfig(_ jsonmap.JSON, mode string) error {
	r.receivedMode = mode
	return errors.New("stop-test")
}

func (r *recordingCapturer) CreatePayment(_ context.Context, _ jsonmap.JSON, _ paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	return nil, errors.New("not-implemented")
}

func (r *recordingCapturer) QueryPayment(_ context.Context, _ jsonmap.JSON, _ string) (*paymentcontract.GatewayQueryResult, error) {
	return nil, errors.New("not-implemented")
}

// 防止重新引入 stripe/wechat capture 全部失败的回归:
// captureViaRegistry 必须把 channel.InteractionMode(而非 channel.ChannelType)
// 传给 Capturer.ValidateConfig,否则 stripe/wechat adapter 会判定 mode 非法。
func TestCaptureViaRegistry_PassesInteractionModeNotChannelType(t *testing.T) {
	cap := &recordingCapturer{}
	reg := provider.NewRegistry()
	reg.Register("test_provider", "test_channel", cap)

	svc := &PaymentService{paymentProviderRegistry: reg}

	channel := &paymentdomain.PaymentChannel{
		ID:              1,
		ProviderType:    "test_provider",
		ChannelType:     "test_channel",
		InteractionMode: constants.PaymentInteractionQR,
	}
	payment := &paymentdomain.Payment{
		ID:          1,
		ChannelID:   1,
		ProviderRef: "ref-123",
	}

	_, _ = svc.captureViaRegistry(CapturePaymentInput{}, payment, channel)

	if cap.receivedMode != channel.InteractionMode {
		t.Fatalf("ValidateConfig received mode=%q, want %q (channel.InteractionMode)",
			cap.receivedMode, channel.InteractionMode)
	}
	if cap.receivedMode == channel.ChannelType {
		t.Fatalf("regression: ValidateConfig received channel.ChannelType=%q", cap.receivedMode)
	}
}
