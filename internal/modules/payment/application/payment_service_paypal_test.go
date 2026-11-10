package application

import (
	"context"
	"errors"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"sync/atomic"
	"testing"
	"time"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	"github.com/XManCodeing/XMall/internal/constants"
)

func TestPaymentGatewayResponseCannotOverwriteSuccessfulCallback(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "callback-race", "CALLBACK-RACE")
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(input paymentcontract.GatewayCreateInput) {
		_, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: input.PaymentID, OrderNo: input.OrderNo, ChannelID: channel.ID, Status: constants.PaymentStatusSuccess, Amount: input.Amount, Currency: input.Currency})
		if err != nil {
			t.Errorf("callback: %v", err)
		}
	}})
	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "callback"})
	if err != nil {
		t.Fatal(err)
	}
	var stored paymentdomain.Payment
	if err := db.First(&stored, result.Payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != constants.PaymentStatusSuccess || stored.PaidAt == nil || stored.CreationState != "ready" {
		t.Fatalf("callback overwritten: %+v", stored)
	}
}

func TestCreationLocalFailureReleasesReservationAndPreservesOperationKey(t *testing.T) {
	for _, kind := range []string{"missing_registry", "config", "request_build", "unclassified_config", "transport"} {
		t.Run(kind, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			channel, order := createFeePolicyOrderFixture(t, db, "creation-failure", "CREATION-FAILURE")
			user := userdomain.User{Email: "creation@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(30))}).Error; err != nil {
				t.Fatal(err)
			}
			calls := 0
			cause := paymentcontract.GatewayNotSubmitted(paymentcontract.ErrGatewayConfigInvalid)
			if kind == "request_build" {
				cause = paymentcontract.GatewayNotSubmitted(paymentcontract.ErrGatewayRequestFailed)
			}
			uncertain := kind == "transport" || kind == "unclassified_config"
			if kind == "transport" {
				cause = paymentcontract.ErrGatewayRequestFailed
			}
			if kind == "unclassified_config" {
				cause = paymentcontract.ErrGatewayConfigInvalid
			}
			registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }, createErr: cause})
			registry := svc.paymentProviderRegistry
			if kind == "missing_registry" {
				svc.paymentProviderRegistry = nil
			}
			input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, UseBalance: true, OperationKey: "failed-operation"}
			_, err := svc.CreatePayment(input)
			if err == nil || errors.Is(err, ErrPaymentCreationUncertain) != uncertain {
				t.Fatalf("outcome: %v", err)
			}
			wantBalance, wantState := "30.00", "not_submitted"
			if uncertain {
				wantBalance, wantState = "0.00", "unknown"
			}
			if got := walletBalance(t, db, user.ID); got != wantBalance {
				t.Fatalf("wallet=%s want %s", got, wantBalance)
			}
			var stored paymentdomain.Payment
			if err := db.Where("order_id = ?", order.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.CreationState != wantState {
				t.Fatalf("state=%s", stored.CreationState)
			}
			_, err = svc.CreatePayment(input)
			if uncertain && !errors.Is(err, ErrPaymentCreationUncertain) || !uncertain && !errors.Is(err, ErrPaymentCreateFailed) {
				t.Fatalf("replay: %v", err)
			}
			input.UseBalance = false
			if _, err := svc.CreatePayment(input); !errors.Is(err, ErrPaymentOperationConflict) {
				t.Fatalf("conflict: %v", err)
			}
			input.OperationKey = "new-operation"
			otherChannel := *channel
			otherChannel.ID = 0
			otherChannel.Name = "alternate-channel"
			if err := db.Create(&otherChannel).Error; err != nil {
				t.Fatal(err)
			}
			input.ChannelID = otherChannel.ID
			svc.paymentProviderRegistry = registry
			registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
			result, err := svc.CreatePayment(input)
			if uncertain {
				if !errors.Is(err, ErrPaymentCreationUncertain) {
					t.Fatalf("uncertain retry: %v", err)
				}
				return
			}
			if err != nil || result.Payment.ID == stored.ID {
				t.Fatalf("new operation: %+v %v", result, err)
			}
			wantCalls := 2
			if kind == "missing_registry" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestCreationPersistenceFailureBoundaries(t *testing.T) {
	for _, failAt := range []int{2, 3} {
		svc, db := setupPaymentServiceWalletTest(t)
		channel, order := createFeePolicyOrderFixture(t, db, "persistence", "PERSISTENCE")
		calls := 0
		registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
		svc.paymentRepo = &creationFailingTransactionStore{Store: svc.paymentRepo, failAt: failAt}
		_, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "persist"})
		var stored paymentdomain.Payment
		if readErr := db.Where("order_id = ?", order.ID).First(&stored).Error; readErr != nil {
			t.Fatal(readErr)
		}
		if failAt == 2 {
			if calls != 0 || stored.CreationState != "not_submitted" || errors.Is(err, ErrPaymentCreationUncertain) {
				t.Fatalf("before dispatch: calls=%d state=%s err=%v", calls, stored.CreationState, err)
			}
		} else if calls != 1 || stored.CreationState != "unknown" || !errors.Is(err, ErrPaymentCreationUncertain) {
			t.Fatalf("after dispatch: calls=%d state=%s err=%v", calls, stored.CreationState, err)
		}
	}
}

type creationFailingTransactionStore struct {
	paymentcontract.Store
	calls, failAt int
}

func (s *creationFailingTransactionStore) WithinTransaction(fn func(paymentcontract.Transaction) error) error {
	s.calls++
	if s.calls == s.failAt {
		return errors.New("injected persistence failure")
	}
	return s.Store.WithinTransaction(fn)
}

func TestCreationErrorDoesNotRefundSuccessfulCallback(t *testing.T) {
	for _, cause := range []error{paymentcontract.GatewayNotSubmitted(paymentcontract.ErrGatewayConfigInvalid), errors.New("response lost")} {
		svc, db := setupPaymentServiceWalletTest(t)
		channel, order := createFeePolicyOrderFixture(t, db, "failure-callback", "FAILURE-CALLBACK")
		user := userdomain.User{Email: "race@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(order).Update("user_id", user.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(30))}).Error; err != nil {
			t.Fatal(err)
		}
		registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{createErr: cause, onCreate: func(in paymentcontract.GatewayCreateInput) {
			if _, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: in.PaymentID, OrderNo: in.OrderNo, ChannelID: channel.ID, Status: constants.PaymentStatusSuccess, Amount: in.Amount, Currency: in.Currency}); err != nil {
				t.Fatal(err)
			}
		}})
		input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, UseBalance: true, OperationKey: "callback-race"}
		_, _ = svc.CreatePayment(input)
		if got := walletBalance(t, db, user.ID); got != "0.00" {
			t.Fatalf("paid funds refunded: %s", got)
		}
		result, err := svc.CreatePayment(input)
		if err != nil || !result.OrderPaid {
			t.Fatalf("callback not preserved: %+v %v", result, err)
		}
	}
}

func TestPaymentCreationDurableUnknownBlocksRetries(t *testing.T) {
	for _, key := range []string{"", "checkout-pay"} {
		t.Run("key="+key, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			channel, order := createFeePolicyOrderFixture(t, db, "durable", "DURABLE")
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			var calls atomic.Int32
			registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls.Add(1); close(entered); <-release }, createErr: errors.New("response lost")})
			input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: key}
			go func() { _, err := svc.CreatePayment(input); done <- err }()
			<-entered
			other := *svc
			if _, err := other.CreatePayment(input); !errors.Is(err, ErrPaymentCreationUncertain) {
				t.Errorf("concurrent call: %v", err)
			}
			close(release)
			if err := <-done; !errors.Is(err, ErrPaymentCreationUncertain) {
				t.Fatal(err)
			}
			var payment paymentdomain.Payment
			if err := db.Where("order_id = ?", order.ID).First(&payment).Error; err != nil {
				t.Fatal(err)
			}
			if payment.CreationState != "unknown" || payment.GatewayOrderNo == "" {
				t.Fatalf("lost reference: %+v", payment)
			}
			input.OperationKey = "new-key"
			input.ChannelID = 0
			input.UseBalance = true
			if _, err := other.CreatePayment(input); !errors.Is(err, ErrPaymentCreationUncertain) {
				t.Fatalf("retry: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("gateway calls=%d", calls.Load())
			}
		})
	}
}
func TestPaymentOperationReplayAndConflict(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "replay", "REPLAY")
	var calls int
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(paymentcontract.GatewayCreateInput) { calls++ }})
	input := CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, OperationKey: "stable"}
	first, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreatePayment(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Payment.ID != second.Payment.ID || calls != 1 {
		t.Fatalf("duplicate: %d", calls)
	}
	input.UseBalance = true
	if _, err := svc.CreatePayment(input); !errors.Is(err, ErrPaymentOperationConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

type cancellationRecoveryStore struct {
	ordercontract.Store
	scan func(context.Context, uint) ([]uint, error)
	get  func(context.Context, uint) (*orderdomain.Order, error)
}

func (s *cancellationRecoveryStore) ListPaidWithoutFulfillment(ctx context.Context, after uint, _ time.Time, _ int) ([]uint, error) {
	return s.scan(ctx, after)
}
func (s *cancellationRecoveryStore) GetByIDContext(ctx context.Context, id uint) (*orderdomain.Order, error) {
	return s.get(ctx, id)
}

type cancellationRecoveryQueue struct {
	affiliateRetryQueue
	enqueue func(context.Context, uint) error
}

func (q *cancellationRecoveryQueue) EnqueueOrderAutoFulfillContext(ctx context.Context, id uint) error {
	return q.enqueue(ctx, id)
}

type cancellationProcurement struct {
	create func(context.Context, uint) error
}

func (*cancellationProcurement) CreateForOrder(uint) error { panic("uncancellable procurement") }
func (p *cancellationProcurement) CreateForOrderContext(ctx context.Context, id uint) error {
	return p.create(ctx, id)
}
func TestRecoveryCancellationReachesEveryIOBoundary(t *testing.T) {
	for _, boundary := range []string{"scan", "load", "queue", "procurement"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			block := func(got context.Context) error {
				if got.Done() == nil {
					t.Error("cancellation context discarded")
				}
				close(entered)
				<-got.Done()
				return got.Err()
			}
			calls := 0
			store := &cancellationRecoveryStore{scan: func(ctx context.Context, after uint) ([]uint, error) {
				calls++
				if boundary == "scan" {
					return nil, block(ctx)
				}
				if after == 0 {
					return []uint{1, 2}, nil
				}
				return nil, nil
			}, get: func(ctx context.Context, id uint) (*orderdomain.Order, error) {
				if boundary == "load" {
					return nil, block(ctx)
				}
				kind := constants.FulfillmentTypeAuto
				if boundary == "procurement" {
					kind = constants.FulfillmentTypeUpstream
				}
				return &orderdomain.Order{ID: id, Status: constants.OrderStatusPaid, Items: []orderdomain.OrderItem{{FulfillmentType: kind}}}, nil
			}}
			svc := &PaymentService{orderRepo: store, queue: &cancellationRecoveryQueue{enqueue: func(ctx context.Context, _ uint) error { return block(ctx) }}, procurementSvc: &cancellationProcurement{create: func(ctx context.Context, _ uint) error { return block(ctx) }}}
			done := make(chan error, 1)
			go func() { _, err := svc.RecoverPaidFulfillment(ctx, 2); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("I/O boundary not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery did not stop")
			}
			if calls != 1 {
				t.Fatalf("scanned again after cancellation: %d", calls)
			}
		})
	}
}
func TestRecoveryRejectsNonAdvancingCursor(t *testing.T) {
	store := &cancellationRecoveryStore{scan: func(context.Context, uint) ([]uint, error) { return []uint{1}, nil }, get: func(context.Context, uint) (*orderdomain.Order, error) { return nil, nil }}
	_, err := (&PaymentService{orderRepo: store}).RecoverPaidFulfillment(context.Background(), 1)
	if err == nil {
		t.Fatal("non advancing scan loop accepted")
	}
}
func TestRecoveryStoreQueriesUseCanceledContext(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	store := svc.orderRepo.(ordercontract.FulfillmentRecoveryStore)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListPaidWithoutFulfillment(ctx, 0, time.Now(), 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan: %v", err)
	}
	if _, err := store.GetByIDContext(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("load: %v", err)
	}
}

type recoveryQueue struct {
	affiliateRetryQueue
	ids []uint
}

func (q *recoveryQueue) EnqueueOrderAutoFulfillContext(ctx context.Context, id uint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.EnqueueOrderAutoFulfill(id)
}
func (q *recoveryQueue) EnqueueOrderAutoFulfill(id uint) error { q.ids = append(q.ids, id); return nil }
func TestRecoverPaidFulfillmentSkipsDeliveredAndManual(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	q := &recoveryQueue{}
	svc.queue = q
	old := time.Now().Add(-time.Hour)
	var want uint
	for i, kind := range []string{constants.FulfillmentTypeManual, constants.FulfillmentTypeAuto, constants.FulfillmentTypeAuto} {
		order := orderdomain.Order{OrderNo: []string{"MANUAL", "MISSING", "DONE"}[i], Status: constants.OrderStatusPaid, UserID: 1, UpdatedAt: old, CreatedAt: old}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		item := orderdomain.OrderItem{OrderID: order.ID, FulfillmentType: kind, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "test"}}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			want = order.ID
		}
		if i == 2 {
			if err := db.Create(&fulfillmentdomain.Fulfillment{OrderID: order.ID, Type: kind, Status: constants.FulfillmentStatusDelivered, Payload: "existing"}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := svc.RecoverPaidFulfillment(context.Background(), 1)
	if err != nil || n != 1 || len(q.ids) != 1 || q.ids[0] != want {
		t.Fatalf("recovery n=%d ids=%v err=%v", n, q.ids, err)
	}
}

func TestShouldUseCNYPaymentCurrency(t *testing.T) {
	if shouldUseCNYPaymentCurrency(nil) {
		t.Fatalf("nil channel should not force CNY")
	}
	if !shouldUseCNYPaymentCurrency(&paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypeWechat}) {
		t.Fatalf("official wechat should force CNY")
	}
	if !shouldUseCNYPaymentCurrency(&paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypeAlipay}) {
		t.Fatalf("official alipay should force CNY")
	}
	if shouldUseCNYPaymentCurrency(&paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypePaypal}) {
		t.Fatalf("official paypal should not force CNY")
	}
}

func TestPickFirstNonEmpty(t *testing.T) {
	if got := pickFirstNonEmpty("", " ", "abc", "def"); got != "abc" {
		t.Fatalf("expected abc, got %s", got)
	}
	if got := pickFirstNonEmpty("", " "); got != "" {
		t.Fatalf("expected empty value, got %s", got)
	}
}

func TestShouldMarkFulfilling(t *testing.T) {
	if shouldMarkFulfilling(nil) {
		t.Fatalf("nil order should not be fulfilling")
	}
	order := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if shouldMarkFulfilling(order) {
		t.Fatalf("auto items should not require fulfilling")
	}
	order = &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if !shouldMarkFulfilling(order) {
		t.Fatalf("manual items should require fulfilling")
	}
}

func TestHasManualFulfillmentItems(t *testing.T) {
	if hasManualFulfillmentItems(nil) {
		t.Fatalf("nil order should not have manual items")
	}
	upstreamOnly := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeUpstream}}}
	if hasManualFulfillmentItems(upstreamOnly) {
		t.Fatalf("upstream-only order should not trigger manual fulfillment pending")
	}
	autoOnly := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if hasManualFulfillmentItems(autoOnly) {
		t.Fatalf("auto-only order should not have manual items")
	}
	manualOnly := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if !hasManualFulfillmentItems(manualOnly) {
		t.Fatalf("manual order should have manual items")
	}
	emptyType := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: "  "}}}
	if !hasManualFulfillmentItems(emptyType) {
		t.Fatalf("empty fulfillment type should be treated as manual")
	}
	mixedUpstreamManual := &orderdomain.Order{Items: []orderdomain.OrderItem{
		{FulfillmentType: constants.FulfillmentTypeUpstream},
		{FulfillmentType: constants.FulfillmentTypeManual},
	}}
	if !hasManualFulfillmentItems(mixedUpstreamManual) {
		t.Fatalf("mixed upstream+manual order should have manual items")
	}
}

func TestIsOrderFullyAutoFulfill(t *testing.T) {
	if isOrderFullyAutoFulfill(nil) {
		t.Fatalf("nil order should not be fully auto")
	}

	autoSingle := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if !isOrderFullyAutoFulfill(autoSingle) {
		t.Fatalf("single auto order should be fully auto")
	}

	manualSingle := &orderdomain.Order{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if isOrderFullyAutoFulfill(manualSingle) {
		t.Fatalf("single manual order should not be fully auto")
	}

	mixedSingle := &orderdomain.Order{Items: []orderdomain.OrderItem{
		{FulfillmentType: constants.FulfillmentTypeAuto},
		{FulfillmentType: constants.FulfillmentTypeManual},
	}}
	if isOrderFullyAutoFulfill(mixedSingle) {
		t.Fatalf("mixed single order should not be fully auto")
	}

	parentAllAuto := &orderdomain.Order{Children: []orderdomain.Order{
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
	}}
	if !isOrderFullyAutoFulfill(parentAllAuto) {
		t.Fatalf("parent with all auto children should be fully auto")
	}

	parentMixed := &orderdomain.Order{Children: []orderdomain.Order{
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}},
	}}
	if isOrderFullyAutoFulfill(parentMixed) {
		t.Fatalf("parent with mixed children should not be fully auto")
	}

	parentAllManual := &orderdomain.Order{Children: []orderdomain.Order{
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeUpstream}}},
		{Items: []orderdomain.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}},
	}}
	if isOrderFullyAutoFulfill(parentAllManual) {
		t.Fatalf("parent with all manual/upstream children should not be fully auto")
	}

	emptyOrder := &orderdomain.Order{}
	if isOrderFullyAutoFulfill(emptyOrder) {
		t.Fatalf("order without items or children should not be fully auto")
	}
}
