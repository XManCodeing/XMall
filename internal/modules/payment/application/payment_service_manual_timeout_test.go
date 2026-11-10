package application

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	carddomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	categorydomain "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	coupondomain "github.com/XManCodeing/XMall/internal/modules/coupon/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TestPaymentChannelRoundTripRequiresNewAttempt covers A -> B -> A without
// exposing the superseded A checkout, while preserving duplicate-key replay.
func TestPaymentChannelRoundTripRequiresNewAttempt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "round-trip", "ROUND-TRIP")
	other := *channel
	other.ID = 0
	other.Name = "alternate"
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
	input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "channel-a"}
	first, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: other.ID, OperationKey: "channel-b"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := svc.CreatePayment(input)
		var retry *paymentcontract.PaymentAttemptRetryError
		if result != nil || !errors.As(err, &retry) || calls != 2 {
			t.Fatalf("old A replay: result=%+v err=%v calls=%d", result, err, calls)
		}
	}
	_, err = svc.CreatePayment(input)
	var retry *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &retry) {
		t.Fatal(err)
	}
	input.OperationKey = retry.OperationKey
	latest, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.CreatePayment(input)
	if err != nil || replay.Payment.ID != latest.Payment.ID || latest.Payment.ID == first.Payment.ID || calls != 3 {
		t.Fatalf("new A: %+v %v calls=%d", replay, err, calls)
	}
	// A late receipt retains the superseded anomaly and follows existing paid-once rules.
	late, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: first.Payment.ID, OrderNo: order.OrderNo, ChannelID: channel.ID, Currency: "CNY", Status: constants.PaymentStatusSuccess, Amount: first.Payment.Amount, ProviderRef: "old-a-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	if late.ExceptionCode != constants.PaymentExceptionSupersededSucceeded {
		t.Fatalf("late exception=%s", late.ExceptionCode)
	}
	var stored orderdomain.Order
	if err := db.First(&stored, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PaidAt == nil || stored.Status != constants.OrderStatusPaid {
		t.Fatalf("late payment lost authoritative receipt: %+v", stored)
	}
}

// TestPaymentNotSubmittedRetrySameChannel checks configuration recovery with a
// deterministic new attempt; reusing either original or successor stays idempotent.
func TestPaymentNotSubmittedRetrySameChannel(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "retry", "RETRY")
	user := &userdomain.User{Email: "retry-wallet@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(30))}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }, createErr: paymentcontract.GatewayNotSubmitted(paymentcontract.ErrGatewayConfigInvalid)})
	input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, UseBalance: true, OperationKey: "original"}
	_, err := svc.CreatePayment(input)
	var first *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &first) {
		t.Fatalf("missing explicit retry: %v", err)
	}
	if walletBalance(t, db, user.ID) != "30.00" {
		t.Fatal("not-submitted attempt retained wallet funds")
	}
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
	_, err = svc.CreatePayment(input)
	var again *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &again) || again.OperationKey != first.OperationKey || calls != 1 {
		t.Fatalf("original key changed: %v calls=%d", err, calls)
	}
	input.OperationKey = first.OperationKey
	created, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.CreatePayment(input)
	if err != nil || created.Payment.ID != replayed.Payment.ID || calls != 2 {
		t.Fatalf("successor duplicated: %v calls=%d", err, calls)
	}
	if walletBalance(t, db, user.ID) != "0.00" || created.Payment.Amount.StringFixed(2) != "70.00" {
		t.Fatal("retry wallet reservation changed")
	}
}

// TestPaymentMixedBalanceRoundTrip retains wallet accounting through A -> B -> A.
// Stale-key replay must not touch funds; the explicit successor reserves once.
func TestPaymentMixedBalanceRoundTrip(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	user := &userdomain.User{Email: "roundtrip@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(5))}).Error; err != nil {
		t.Fatal(err)
	}
	channelA := createUnderpaidChannel(t, db, svc, "A", constants.PaymentChannelTypeWechat)
	channelB := createUnderpaidChannel(t, db, svc, "B", constants.PaymentChannelTypeAlipay)
	order := createUnderpaidOrder(t, db, "MIXED-ROUNDTRIP", user.ID, 15)
	input := CreatePaymentInput{OrderID: order.ID, ChannelID: channelA.ID, UseBalance: true, OperationKey: "a"}
	first, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channelB.ID, OperationKey: "b"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreatePayment(input)
	var retry *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &retry) || walletBalance(t, db, user.ID) != "5.00" {
		t.Fatalf("stale replay changed funds: %v", err)
	}
	input.OperationKey = retry.OperationKey
	created, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.CreatePayment(input)
	if err != nil || created.Payment.ID != replayed.Payment.ID || created.Payment.ID == first.Payment.ID || walletBalance(t, db, user.ID) != "0.00" || created.Payment.Amount.StringFixed(2) != "10.00" {
		t.Fatalf("mixed retry=%+v err=%v balance=%s", created, err, walletBalance(t, db, user.ID))
	}
}

// TestPaymentReplayRejectsExpiredAndUncertain checks all guarded replay states
// without external I/O; only a proven terminal attempt gets a retry instruction.
func TestPaymentReplayRejectsExpiredAndUncertain(t *testing.T) {
	for _, scenario := range []string{"order_expired", "order_cancelled", "attempt_expired", "superseded", "not_submitted", "unknown", "processing", "other_unknown", "failed", "exception_success"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			past := now.Add(-time.Minute)
			key := "original"
			order := orderdomain.Order{Status: constants.OrderStatusPendingPayment}
			attempt := paymentdomain.Payment{ID: 1, OperationKey: &key, CreationState: "ready", Status: constants.PaymentStatusPending}
			var payments []paymentdomain.Payment
			want := ErrPaymentCreateFailed
			wantRetry := false
			switch scenario {
			case "order_expired":
				order.ExpiresAt = &past
				want = paymentcontract.ErrReplayOrderInvalid
			case "order_cancelled":
				order.Status = constants.OrderStatusCanceled
				want = paymentcontract.ErrReplayOrderInvalid
			case "attempt_expired":
				attempt.ExpiredAt = &past
				wantRetry = true
			case "superseded":
				attempt.SupersededAt = &past
				wantRetry = true
			case "not_submitted":
				attempt.CreationState = "not_submitted"
				wantRetry = true
			case "unknown", "processing":
				attempt.CreationState = scenario
				payments = []paymentdomain.Payment{attempt}
				want = ErrPaymentCreationUncertain
			case "other_unknown":
				payments = []paymentdomain.Payment{{CreationState: "unknown", Status: constants.PaymentStatusInitiated}}
				want = ErrPaymentCreationUncertain
			case "failed":
				attempt.Status = constants.PaymentStatusFailed
				wantRetry = true
			case "exception_success":
				attempt.Status = constants.PaymentStatusSuccess
				attempt.ExceptionCode = constants.PaymentExceptionSupersededSucceeded
				want = nil
			}
			err := paymentcontract.ValidatePaymentReplay(&order, &attempt, payments, now)
			var retry *paymentcontract.PaymentAttemptRetryError
			if !errors.Is(err, want) || errors.As(err, &retry) != wantRetry {
				t.Fatalf("err=%v retry=%+v", err, retry)
			}
		})
	}
}

// timeoutFixture builds a real parent/child reservation and optionally executes the production timeout cancel path.
// membership controls the immutable order-item snapshot; no external service or production DB is used.
func timeoutFixture(t *testing.T, canceled, membership bool) (*PaymentService, *gorm.DB, *orderdomain.Order, *paymentdomain.Payment, paymentcontract.ManualConfirmationInput, *productdomain.ProductSKU) {
	t.Helper()
	service, db, root, payment, input := manualFixture(t)
	if err := db.AutoMigrate(&coupondomain.Coupon{}, &coupondomain.CouponUsage{}, &carddomain.Secret{}, &membershipdomain.ProductConfig{}, &categorydomain.Category{}); err != nil {
		t.Fatal(err)
	}
	category := &categorydomain.Category{Slug: "timeout-category", NameJSON: jsonmap.JSON{}, IsActive: true}
	if err := db.Create(category).Error; err != nil {
		t.Fatal(err)
	}
	service.enforcePublicSale = true
	product := &productdomain.Product{Slug: "timeout-product", TitleJSON: jsonmap.JSON{}, ManualStockTotal: 1, CategoryID: category.ID, IsActive: true}
	if err := db.Create(product).Error; err != nil {
		t.Fatal(err)
	}
	sku := &productdomain.ProductSKU{ProductID: product.ID, SKUCode: "timeout-sku", ManualStockTotal: 1, ManualStockLocked: 1}
	if err := db.Create(sku).Error; err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(-time.Minute)
	root.ExpiresAt = &expires
	if err := db.Save(root).Error; err != nil {
		t.Fatal(err)
	}
	child := &orderdomain.Order{OrderNo: "timeout-child", ParentID: &root.ID, Status: constants.OrderStatusPendingPayment, Currency: root.Currency, TotalAmount: root.TotalAmount, ExpiresAt: &expires, CreatedAt: root.CreatedAt}
	if err := db.Create(child).Error; err != nil {
		t.Fatal(err)
	}
	item := &orderdomain.OrderItem{OrderID: child.ID, ProductID: product.ID, SKUID: sku.ID, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: constants.FulfillmentTypeManual}
	if err := db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	if membership {
		if err := db.Create(&membershipdomain.ProductConfig{ProductID: product.ID, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&membershipdomain.OrderItemSnapshot{OrderItemID: item.ID, ProductID: product.ID, SKUID: sku.ID, AccountID: child.UserID, Quantity: 1, Targets: []membershipdomain.SnapshotTarget{{TargetID: 1}}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if canceled {
		cancelTimeoutFixture(t, service, db, root.ID)
		input.ExpectedStatus = constants.PaymentStatusExpired
	}
	return service, db, root, payment, input, sku
}

// cancelTimeoutFixture invokes the actual order cancellation service using the shared temporary transaction stores.
func cancelTimeoutFixture(t *testing.T, service *PaymentService, db *gorm.DB, id uint) {
	t.Helper()
	orders := orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: orderstore.New(db, "test-guest-credential-secret-with-32-bytes"), WalletService: service.walletSvc})
	if _, err := orders.CancelExpiredOrder(id); err != nil {
		t.Fatal(err)
	}
}

// TestManualTimeoutConfirmation covers both ordinary/membership and pending/canceled orders with explicit confirmation.
func TestManualTimeoutConfirmation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, membership := range []bool{false, true} {
			name := "pending"
			if canceled {
				name = "canceled"
			}
			if membership {
				name += "_membership"
			}
			t.Run(name, func(t *testing.T) {
				service, db, root, payment, input, sku := timeoutFixture(t, canceled, membership)
				review, err := service.GetManualConfirmation(payment.ID)
				if err != nil || !review.CanConfirm || !review.RequiresTimeoutConfirmation {
					t.Fatalf("review %+v: %v", review, err)
				}
				encoded, err := json.Marshal(review)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]interface{}
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["requires_timeout_confirmation"] != true {
					t.Fatal(string(encoded))
				}
				if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
					t.Fatalf("missing consent: %v", err)
				}
				assertTimeoutNoEvidence(t, db)
				input.ConfirmTimeout = true
				for attempt := 0; attempt < 2; attempt++ {
					result, err := service.ConfirmManually(payment.ID, input)
					if err != nil || result.Replayed != (attempt == 1) {
						t.Fatalf("confirm %+v: %v", result, err)
					}
				}
				if err := db.First(sku, sku.ID).Error; err != nil || sku.ManualStockLocked != 0 || sku.ManualStockSold != 1 {
					t.Fatalf("stock %+v: %v", sku, err)
				}
				var orders []orderdomain.Order
				if err := db.Where("id = ? OR parent_id = ?", root.ID, root.ID).Find(&orders).Error; err != nil {
					t.Fatal(err)
				}
				for _, order := range orders {
					if order.PaidAt == nil || order.CanceledAt != nil || order.Status == constants.OrderStatusCanceled {
						t.Fatalf("order %+v", order)
					}
				}
				var events int64
				if err := db.Model(&membershipdomain.PaidEvent{}).Count(&events).Error; err != nil {
					t.Fatal(err)
				}
				if membership && events != 1 || !membership && events != 0 {
					t.Fatalf("events %d", events)
				}
			})
		}
	}
}

// assertTimeoutNoEvidence verifies a rejected transaction never claims evidence or writes a paid outbox/audit.
func assertTimeoutNoEvidence(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, model := range []interface{}{&paymentdomain.PaymentEvidenceClaim{}, &paymentdomain.ManualConfirmation{}, &membershipdomain.PaidEvent{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("unexpected side effects %T: %d %v", model, count, err)
		}
	}
}

// TestManualTimeoutAttemptOnly covers an expired payment attempt while its order deadline has not expired.
func TestManualTimeoutAttemptOnly(t *testing.T) {
	service, db, _, payment, input := manualFixture(t)
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(payment).Updates(map[string]interface{}{"status": constants.PaymentStatusExpired, "expired_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	input.ExpectedStatus = constants.PaymentStatusExpired
	review, err := service.GetManualConfirmation(payment.ID)
	if err != nil || !review.CanConfirm || !review.RequiresTimeoutConfirmation {
		t.Fatalf("review %+v %v", review, err)
	}
	if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
		t.Fatal(err)
	}
	assertTimeoutNoEvidence(t, db)
	input.ConfirmTimeout = true
	if _, err := service.ConfirmManually(payment.ID, input); err != nil {
		t.Fatal(err)
	}
}

// TestManualTimeoutInventoryRollback verifies failed re-reservation rolls back parent/children, evidence and payment facts.
func TestManualTimeoutInventoryRollback(t *testing.T) {
	service, db, root, payment, input, sku := timeoutFixture(t, true, true)
	if err := db.Model(sku).Update("manual_stock_total", 0).Error; err != nil {
		t.Fatal(err)
	}
	input.ConfirmTimeout = true
	if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, orderapp.ErrManualStockInsufficient) {
		t.Fatalf("insufficient stock: %v", err)
	}
	assertTimeoutNoEvidence(t, db)
	if err := db.First(payment, payment.ID).Error; err != nil || payment.Status != constants.PaymentStatusExpired || payment.PaidAt != nil {
		t.Fatalf("payment %+v: %v", payment, err)
	}
	var orders []orderdomain.Order
	if err := db.Where("id = ? OR parent_id = ?", root.ID, root.ID).Find(&orders).Error; err != nil {
		t.Fatal(err)
	}
	for _, order := range orders {
		if order.Status != constants.OrderStatusCanceled || order.CanceledAt == nil || order.CancellationReason != "timeout" {
			t.Fatalf("order %+v", order)
		}
	}
}

// TestManualTimeoutConcurrent verifies the existing transaction lock makes recovery and paid outbox exactly once.
func TestManualTimeoutConcurrent(t *testing.T) {
	service, db, _, payment, input, sku := timeoutFixture(t, true, true)
	input.ConfirmTimeout = true
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
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
	for _, model := range []interface{}{&paymentdomain.ManualConfirmation{}, &membershipdomain.PaidEvent{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("count %T %d %v", model, count, err)
		}
	}
	if err := db.First(sku, sku.ID).Error; err != nil || sku.ManualStockSold != 1 {
		t.Fatalf("stock %+v: %v", sku, err)
	}
}

// TestManualTimeoutWalletRefund verifies an actual cancel refund cannot be counted as a still-paid wallet contribution.
func TestManualTimeoutWalletRefund(t *testing.T) {
	service, db, root, payment, input, _ := timeoutFixture(t, false, false)
	root.UserID = 99
	root.TotalAmount = money.FromDecimal(decimal.NewFromInt(15))
	root.WalletPaidAmount = money.FromDecimal(decimal.NewFromInt(5))
	if err := db.Save(root).Error; err != nil {
		t.Fatal(err)
	}
	account := &walletdomain.Account{UserID: root.UserID, Balance: money.FromDecimal(decimal.NewFromInt(20))}
	if err := db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	cancelTimeoutFixture(t, service, db, root.ID)
	input.ExpectedStatus, input.ConfirmTimeout = constants.PaymentStatusExpired, true
	if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, ErrPaymentAmountMismatch) {
		t.Fatalf("refunded wallet: %v", err)
	}
	if err := db.First(account, account.ID).Error; err != nil || !account.Balance.Decimal.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("wallet %+v %v", account, err)
	}
	if err := db.First(root, root.ID).Error; err != nil || !root.WalletPaidAmount.Decimal.IsZero() || root.Status != constants.OrderStatusCanceled {
		t.Fatalf("order %+v %v", root, err)
	}
	assertTimeoutNoEvidence(t, db)
}

// TestManualTimeoutAuditFailureRollsBackStock ensures a failure after reservation and outbox creation rolls every resource back.
func TestManualTimeoutAuditFailureRollsBackStock(t *testing.T) {
	service, db, root, payment, input, sku := timeoutFixture(t, true, true)
	if err := db.Callback().Create().Before("gorm:create").Register("timeout_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "payment_manual_confirmations" {
			tx.AddError(errors.New("injected timeout audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	input.ConfirmTimeout = true
	if _, err := service.ConfirmManually(payment.ID, input); err == nil {
		t.Fatal("expected rollback")
	}
	assertTimeoutNoEvidence(t, db)
	if err := db.First(sku, sku.ID).Error; err != nil || sku.ManualStockSold != 0 || sku.ManualStockLocked != 0 {
		t.Fatalf("stock %+v %v", sku, err)
	}
	if err := db.First(root, root.ID).Error; err != nil || root.Status != constants.OrderStatusCanceled {
		t.Fatalf("root %+v %v", root, err)
	}
}

// TestManualTimeoutAutoInventory verifies released card secrets are re-reserved and insufficient cards leave no partial recovery.
func TestManualTimeoutAutoInventory(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "insufficient", true: "available"}[available], func(t *testing.T) {
			service, db, root, payment, input, sku := timeoutFixture(t, false, false)
			var child orderdomain.Order
			if err := db.Where("parent_id = ?", root.ID).First(&child).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&orderdomain.OrderItem{}).Where("order_id = ?", child.ID).Update("fulfillment_type", constants.FulfillmentTypeAuto).Error; err != nil {
				t.Fatal(err)
			}
			secret := &carddomain.Secret{ProductID: sku.ProductID, SKUID: sku.ID, Secret: "synthetic-card", Status: carddomain.StatusReserved, OrderID: &child.ID}
			if err := db.Create(secret).Error; err != nil {
				t.Fatal(err)
			}
			cancelTimeoutFixture(t, service, db, root.ID)
			if !available {
				if err := db.Model(secret).Update("status", carddomain.StatusUsed).Error; err != nil {
					t.Fatal(err)
				}
			}
			input.ExpectedStatus, input.ConfirmTimeout = constants.PaymentStatusExpired, true
			_, err := service.ConfirmManually(payment.ID, input)
			if !available {
				if !errors.Is(err, orderapp.ErrCardSecretInsufficient) {
					t.Fatal(err)
				}
				assertTimeoutNoEvidence(t, db)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := db.First(secret, secret.ID).Error; err != nil || secret.Status != carddomain.StatusReserved || secret.OrderID == nil || *secret.OrderID != child.ID {
				t.Fatalf("reservation %+v %v", secret, err)
			}
		})
	}
}

// TestManualTimeoutManualCancelProvenance verifies a user cancellation after expiry is still not a timeout cancellation.
func TestManualTimeoutManualCancelProvenance(t *testing.T) {
	service, db, root, payment, input, _ := timeoutFixture(t, false, false)
	root.UserID = 12
	if err := db.Model(root).Update("user_id", root.UserID).Error; err != nil {
		t.Fatal(err)
	}
	orders := orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: orderstore.New(db, "test-guest-credential-secret-with-32-bytes"), WalletService: service.walletSvc})
	if _, err := orders.CancelOrder(root.ID, root.UserID); err != nil {
		t.Fatal(err)
	}
	if err := db.First(root, root.ID).Error; err != nil || root.CancellationReason != "manual" {
		t.Fatalf("cause %+v %v", root, err)
	}
	input.ExpectedStatus, input.ConfirmTimeout = constants.PaymentStatusExpired, true
	if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
		t.Fatal(err)
	}
	assertTimeoutNoEvidence(t, db)
}

// TestManualTimeoutRejectsUnprovenCancellation rejects manual cancellations, legacy missing evidence and unsafe states.
func TestManualTimeoutRejectsUnprovenCancellation(t *testing.T) {
	for _, reason := range []string{"", "manual", "refunded", "success", "child_manual"} {
		t.Run(reason, func(t *testing.T) {
			service, db, root, payment, input, _ := timeoutFixture(t, true, false)
			updates := map[string]interface{}{"cancellation_reason": reason}
			switch reason {
			case "refunded":
				updates = map[string]interface{}{"refunded_amount": 1}
			case "success":
				updates = map[string]interface{}{"status": constants.OrderStatusPaid}
			case "child_manual":
				if err := db.Model(&orderdomain.Order{}).Where("parent_id = ?", root.ID).Update("cancellation_reason", "manual").Error; err != nil {
					t.Fatal(err)
				}
				updates = map[string]interface{}{"cancellation_reason": "timeout"}
			}
			if err := db.Model(root).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			input.ConfirmTimeout = true
			if _, err := service.ConfirmManually(payment.ID, input); !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
				t.Fatal(err)
			}
			assertTimeoutNoEvidence(t, db)
		})
	}
}

// TestAuditExchangeReceipt verifies exact foreign receipts, fees and wallet coverage.
// Under/overpayment must fail without fulfillment; exact receipts also pass manual review.
func TestAuditExchangeReceipt(t *testing.T) {
	for _, mode := range []string{"callback", "manual", "under", "over"} {
		for _, rate := range []string{"0.137", "0.5"} {
			t.Run(mode+rate, func(t *testing.T) {
				service, db, order, payment, input := manualFixture(t)
				order.TotalAmount = money.FromDecimal(decimal.NewFromInt(34))
				order.WalletPaidAmount = money.FromDecimal(decimal.NewFromInt(5))
				payment.Amount = money.FromDecimal(decimal.RequireFromString("29.01").Mul(decimal.RequireFromString(rate)).Round(2))
				payment.Currency, payment.FeePolicy = "USD", constants.PaymentFeePolicyCustomerSurcharge
				payment.FeeAmount = money.FromDecimal(decimal.RequireFromString("0.01"))
				payment.ProviderPayload = jsonmap.JSON{"original_amount": "29.01", "original_currency": "CNY", "exchange_rate": rate}
				if err := db.Save(order).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Save(payment).Error; err != nil {
					t.Fatal(err)
				}
				amount := payment.Amount.Decimal
				if mode == "under" {
					amount = amount.Sub(decimal.RequireFromString("0.01"))
				}
				if mode == "over" {
					amount = amount.Add(decimal.RequireFromString("0.01"))
				}
				var err error
				if mode == "manual" {
					input.Amount, input.Currency = amount.StringFixed(2), "USD"
					_, err = service.ConfirmManually(payment.ID, input)
				} else {
					_, err = service.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, ChannelID: payment.ChannelID, OrderNo: order.OrderNo, Status: constants.PaymentStatusSuccess, Amount: money.FromDecimal(amount), Currency: "USD"})
				}
				invalid := mode == "under" || mode == "over"
				if invalid && !errors.Is(err, ErrPaymentAmountMismatch) || !invalid && err != nil {
					t.Fatalf("receipt: %v", err)
				}
				var stored orderdomain.Order
				if err := db.First(&stored, order.ID).Error; err != nil {
					t.Fatal(err)
				}
				if (stored.PaidAt != nil) == invalid {
					t.Fatalf("paid_at=%v invalid=%v", stored.PaidAt, invalid)
				}
			})
		}
	}
}

// TestAuditFailedCallbackUnblocksOnlyProvenAttempt verifies terminal convergence and durable replay.
// Original-key replay must not dispatch a new external request after session loss.
func TestAuditFailedCallbackUnblocksOnlyProvenAttempt(t *testing.T) {
	service, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "audit-retry", "AUDIT-RETRY")
	calls := 0
	registerTestGateway(t, service, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
	original := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "audit-original"}
	first, err := service.CreatePayment(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(first.Payment).Update("creation_state", "unknown").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePayment(original); !errors.Is(err, ErrPaymentCreationUncertain) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := service.HandleCallback(PaymentCallbackInput{PaymentID: first.Payment.ID, ChannelID: channel.ID, OrderNo: order.OrderNo, Status: constants.PaymentStatusFailed}); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreatePayment(original)
	var retry *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("failed retry: %v", err)
	}
	successor := original
	successor.OperationKey = retry.OperationKey
	created, err := service.CreatePayment(successor)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		replayed, err := service.CreatePayment(original)
		if err != nil || replayed.Payment.ID != created.Payment.ID || calls != 2 {
			t.Fatalf("chain replay: %v calls=%d", err, calls)
		}
	}
	if _, err := service.HandleCallback(PaymentCallbackInput{PaymentID: created.Payment.ID, ChannelID: channel.ID, OrderNo: order.OrderNo, Status: constants.PaymentStatusSuccess, Amount: created.Payment.Amount, Currency: created.Payment.Currency}); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreatePayment(original)
	if err != nil || !replayed.OrderPaid || replayed.Payment.ID != created.Payment.ID || calls != 2 {
		t.Fatalf("paid successor replay: %v calls=%d", err, calls)
	}
}

// TestAuditManualUnknownBoundary preserves own-attempt manual recovery but blocks competing unknowns.
// Both paths record already received funds and never initiate a gateway debit.
func TestAuditManualUnknownBoundary(t *testing.T) {
	for _, competing := range []bool{false, true} {
		t.Run(map[bool]string{false: "self", true: "other"}[competing], func(t *testing.T) {
			service, db, order, payment, input := manualFixture(t)
			if err := db.Model(payment).Update("creation_state", "unknown").Error; err != nil {
				t.Fatal(err)
			}
			if competing {
				other := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 2, Status: constants.PaymentStatusInitiated, CreationState: "unknown", Amount: payment.Amount, Currency: "CNY"}
				if err := db.Create(other).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.ConfirmManually(payment.ID, input)
			if competing && !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) || !competing && err != nil {
				t.Fatalf("manual: %v", err)
			}
		})
	}
}

// TestAuditProviderPublicationRollback injects retirement failure and checks atomic rollback.
// The external result stays unknown instead of publishing a second checkout or permitting retry.
func TestAuditProviderPublicationRollback(t *testing.T) {
	service, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "audit-publication", "AUDIT-PUBLICATION")
	registerTestGateway(t, service, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{})
	first, err := service.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "first"})
	if err != nil {
		t.Fatal(err)
	}
	name := "audit_supersede_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if _, present := fields["superseded_at"]; present {
				tx.AddError(errors.New("injected retirement failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Update().Remove(name); err != nil {
			t.Error(err)
		}
	})
	_, err = service.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "second"})
	if !errors.Is(err, ErrPaymentCreationUncertain) {
		t.Fatalf("publication: %v", err)
	}
	var attempts []paymentdomain.Payment
	if err := db.Where("order_id = ?", order.ID).Order("id").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].ID != first.Payment.ID || attempts[0].SupersededAt != nil || attempts[1].CreationState != "unknown" || attempts[1].PayURL != "" {
		t.Fatalf("attempts=%+v", attempts)
	}
}

// TestAuditCallbackBeforeCreateFailure keeps proven failure retryable in the same response.
// A concurrent terminal callback must not poison the Bot session with an uncertain result.
func TestAuditCallbackBeforeCreateFailure(t *testing.T) {
	service, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "audit-race", "AUDIT-RACE")
	registerTestGateway(t, service, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{
		onCreate: func(input paymentcontract.GatewayCreateInput) {
			if _, err := service.HandleCallback(PaymentCallbackInput{PaymentID: input.PaymentID, ChannelID: channel.ID, OrderNo: input.OrderNo, Status: constants.PaymentStatusFailed}); err != nil {
				t.Fatal(err)
			}
		},
		createErr: paymentcontract.ErrGatewayRequestFailed,
	})
	_, err := service.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "audit-race"})
	var retry *paymentcontract.PaymentAttemptRetryError
	if !errors.As(err, &retry) || errors.Is(err, ErrPaymentCreationUncertain) {
		t.Fatalf("callback race: %v", err)
	}
}

// TestAuditMembershipExchange appends one membership paid event for a rounded foreign receipt.
// The fixture uses a non-divisible exchange rate and exercises both callback and manual entry.
func TestAuditMembershipExchange(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "manual"}[manual], func(t *testing.T) {
			service, db, root, payment, input, _ := timeoutFixture(t, false, true)
			future := time.Now().Add(time.Hour)
			if err := db.Model(&orderdomain.Order{}).Where("id = ? OR parent_id = ?", root.ID, root.ID).Updates(map[string]interface{}{"total_amount": "29", "expires_at": future}).Error; err != nil {
				t.Fatal(err)
			}
			payment.Amount, payment.Currency = money.FromDecimal(decimal.RequireFromString("3.97")), "USD"
			payment.ProviderPayload = jsonmap.JSON{"original_amount": "29", "original_currency": "CNY", "exchange_rate": "0.137"}
			if err := db.Save(payment).Error; err != nil {
				t.Fatal(err)
			}
			if manual {
				input.Amount, input.Currency = "3.97", "USD"
				if _, err := service.ConfirmManually(payment.ID, input); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := service.HandleCallback(PaymentCallbackInput{PaymentID: payment.ID, ChannelID: payment.ChannelID, OrderNo: root.OrderNo, Status: constants.PaymentStatusSuccess, Amount: payment.Amount, Currency: "USD"}); err != nil {
					t.Fatal(err)
				}
			}
			var count int64
			if err := db.Model(&membershipdomain.PaidEvent{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("paid events=%d err=%v", count, err)
			}
		})
	}
}

// TestAuditWalletShortfallClassification verifies an insufficient wallet rolls back the partial debit.
// The account contains less than the order total and no online channel is selected.
func TestAuditWalletShortfallClassification(t *testing.T) {
	service, db, order, _, _ := manualFixture(t)
	user := &userdomain.User{Email: "audit-wallet@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(3))}).Error; err != nil {
		t.Fatal(err)
	}
	_, err := service.CreatePayment(CreatePaymentInput{OrderID: order.ID, UseBalance: true})
	if !errors.Is(err, walletcontract.ErrInsufficientBalance) || walletBalance(t, db, user.ID) != "3.00" {
		t.Fatalf("wallet error=%v", err)
	}
}

// TestWalletPaymentModes checks policy precedence, rollback and mixed-payment replay.
func TestWalletPaymentModes(t *testing.T) {
	for _, test := range []struct {
		name                            string
		walletOnly, useBalance, channel bool
		want                            error
	}{
		{"wallet-only implicit", true, false, false, walletcontract.ErrOnlyPaymentRequired},
		{"wallet-only explicit", true, true, false, walletcontract.ErrOnlyPaymentRequired},
		{"wallet-only rejects online", true, true, true, walletcontract.ErrOnlyPaymentRequired},
		{"balance shortfall", false, true, false, walletcontract.ErrInsufficientBalance},
		{"missing method", false, false, false, ErrPaymentInvalid},
		{"mixed payment", false, true, true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db := setupPaymentServiceWalletTest(t)
			channel, order := createFeePolicyOrderFixture(t, db, "balance-mode", "BALANCE-MODE")
			user := &userdomain.User{Email: "mode@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
			if err := db.Create(user).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(3))}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.settingService.Update(constants.SettingKeyWalletConfig, map[string]interface{}{constants.SettingFieldWalletOnlyPayment: test.walletOnly}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			registerTestGateway(t, service, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
			input := CreatePaymentInput{OrderID: order.ID, UseBalance: test.useBalance, OperationKey: "balance-mode"}
			if test.channel {
				input.ChannelID = channel.ID
			}
			result, err := service.CreatePayment(input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.want == nil {
				if result == nil || result.Payment == nil || calls != 1 || walletBalance(t, db, user.ID) != "0.00" {
					t.Fatalf("mixed payment=%+v calls=%d", result, calls)
				}
				replay, err := service.CreatePayment(input)
				if err != nil || replay.Payment.ID != result.Payment.ID || calls != 1 {
					t.Fatalf("mixed replay=%+v error=%v calls=%d", replay, err, calls)
				}
				return
			}
			var stored orderdomain.Order
			if err := db.First(&stored, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			var payments, transactions int64
			if err := db.Model(&paymentdomain.Payment{}).Where("order_id = ?", order.ID).Count(&payments).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&walletdomain.Transaction{}).Where("user_id = ?", user.ID).Count(&transactions).Error; err != nil {
				t.Fatal(err)
			}
			if walletBalance(t, db, user.ID) != "3.00" || !stored.WalletPaidAmount.Decimal.IsZero() || payments != 0 || transactions != 0 || calls != 0 {
				t.Fatalf("failed payment changed funds: order=%+v payments=%d transactions=%d calls=%d", stored, payments, transactions, calls)
			}
		})
	}
}
