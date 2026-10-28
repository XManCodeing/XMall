package application

import (
	"context"
	"encoding/json"
	"errors"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/jsonslice"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

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

func TestValidateOrderChannelEligibilityUsesPersistedOrderIdentity(t *testing.T) {
	levelID := uint(2)
	channel := paymentdomain.PaymentChannel{
		PaymentRoles: jsonslice.Strings{constants.PaymentRoleMember},
		MemberLevels: jsonslice.Uints{levelID},
		PaymentTypes: jsonslice.Strings{constants.PaymentTypeOrder},
	}
	memberOrder := &orderdomain.Order{UserID: 10, MemberLevelID: &levelID}
	if err := validateOrderChannelEligibility(channel, memberOrder); err != nil {
		t.Fatalf("eligible persisted member order rejected: %v", err)
	}
	if err := validateOrderChannelEligibility(channel, &orderdomain.Order{}); err != ErrPaymentChannelNotAllowedForProduct {
		t.Fatalf("guest bypass error = %v, want channel rejection", err)
	}
	wrongLevel := uint(3)
	if err := validateOrderChannelEligibility(channel, &orderdomain.Order{UserID: 10, MemberLevelID: &wrongLevel}); err != ErrPaymentChannelNotAllowedForProduct {
		t.Fatalf("member-level bypass error = %v, want channel rejection", err)
	}
}

func TestValidateWalletChannelEligibilityRejectsOrderOnlyChannel(t *testing.T) {
	channel := paymentdomain.PaymentChannel{
		PaymentRoles: jsonslice.Strings{constants.PaymentRoleMember},
		PaymentTypes: jsonslice.Strings{constants.PaymentTypeOrder},
	}
	user := &userdomain.User{ID: 10}
	if err := validateWalletChannelEligibility(channel, user); err != ErrPaymentChannelNotAllowedForRecharge {
		t.Fatalf("wallet payment-type bypass error = %v, want recharge rejection", err)
	}
}

func TestBuildOrderSubject(t *testing.T) {
	tests := []struct {
		name  string
		order *orderdomain.Order
		want  string
	}{
		{
			name: "nil order",
			want: "",
		},
		{
			name: "always uses order number regardless of item titles",
			order: &orderdomain.Order{
				OrderNo: "DJ-LEGACY-1",
				Items: []orderdomain.OrderItem{
					{TitleJSON: jsonmap.JSON{"zh-CN": "父订单商品"}},
				},
			},
			want: "DJ-LEGACY-1",
		},
		{
			name: "trims order number",
			order: &orderdomain.Order{
				OrderNo: " DJ-FALLBACK-1 ",
			},
			want: "DJ-FALLBACK-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildOrderSubject(tt.order); got != tt.want {
				t.Fatalf("buildOrderSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

type emptyProviderRefProvider struct {
	onCreate           func(paymentcontract.GatewayCreateInput)
	displayChannelType string
	createErr          error
}

func registerTestGateway(t *testing.T, svc *PaymentService, providerType, channelType string, gateway paymentcontract.GatewayProvider) {
	t.Helper()
	registry, ok := svc.paymentProviderRegistry.(interface {
		Register(string, string, paymentcontract.GatewayProvider)
	})
	if !ok {
		t.Fatal("payment provider registry does not support test registration")
	}
	registry.Register(providerType, channelType, gateway)
}

func (p emptyProviderRefProvider) Type() string {
	return constants.PaymentProviderOfficial + ":" + constants.PaymentChannelTypeWechat
}

func (p emptyProviderRefProvider) ValidateConfig(jsonmap.JSON, string) error {
	return nil
}

func (p emptyProviderRefProvider) CreatePayment(_ context.Context, _ jsonmap.JSON, input paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	if p.onCreate != nil {
		p.onCreate(input)
	}
	if p.createErr != nil {
		return nil, p.createErr
	}
	return &paymentcontract.GatewayCreateResult{
		QRCodeURL:          "weixin://wxpay/bizpayurl?pr=test",
		DisplayChannelType: p.displayChannelType,
	}, nil
}

type fakeGatewaySecurityTester struct {
	emptyProviderRefProvider
	result *paymentcontract.GatewaySecurityTestResult
	err    error
}

func (p fakeGatewaySecurityTester) TestSecurity(context.Context, jsonmap.JSON) (*paymentcontract.GatewaySecurityTestResult, error) {
	return p.result, p.err
}

func TestChannelSecurityUsesSavedWechatPayChannel(t *testing.T) {
	service, db := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		Name:            "WeChat Pay",
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		ConfigJSON: jsonmap.JSON{
			"verification_mode": "combined",
		},
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	expected := &paymentcontract.GatewaySecurityTestResult{
		VerificationMode:         "wechatpay_public_key",
		ResponseSerial:           "PUB_KEY_ID_3000000001",
		RequestSignatureAccepted: true,
		ResponseSignatureValid:   true,
		EchoMessageMatched:       true,
	}
	registerTestGateway(t, service, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, fakeGatewaySecurityTester{
		result: expected,
	})

	result, err := service.TestChannelSecurity(context.Background(), channel.ID)
	if err != nil {
		t.Fatalf("test channel security: %v", err)
	}
	if result != expected {
		t.Fatalf("security result = %+v, want %+v", result, expected)
	}
}

func TestChannelSecurityMapsSignatureFailure(t *testing.T) {
	service, db := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		Name:            "WeChat Pay",
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		ConfigJSON:      jsonmap.JSON{},
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	registerTestGateway(t, service, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, fakeGatewaySecurityTester{
		err: paymentcontract.ErrGatewaySignatureInvalid,
	})

	_, err := service.TestChannelSecurity(context.Background(), channel.ID)
	if !errors.Is(err, ErrPaymentGatewayResponseInvalid) {
		t.Fatalf("signature failure = %v, want ErrPaymentGatewayResponseInvalid", err)
	}
}

func TestShouldUseGatewayOrderNo(t *testing.T) {
	tests := []struct {
		name    string
		channel *paymentdomain.PaymentChannel
		want    bool
	}{
		{
			name: "epay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderEpay,
			},
			want: true,
		},
		{
			name: "epusdt",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderBepusdt,
			},
			want: true,
		},
		{
			name: "okpay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderOkpay,
			},
			want: true,
		},
		{
			name: "tokenpay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderTokenpay,
			},
			want: true,
		},
		{
			name: "official wechat",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderOfficial,
				ChannelType:  constants.PaymentChannelTypeWechat,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldUseGatewayOrderNo(tc.channel); got != tc.want {
				t.Fatalf("shouldUseGatewayOrderNo() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveGatewayOrderNo(t *testing.T) {
	channel := &paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderBepusdt}
	payment := &paymentdomain.Payment{ID: 123}

	gotFirst := resolveGatewayOrderNo(channel, payment)
	if gotFirst == "" {
		t.Fatalf("resolveGatewayOrderNo() should generate gateway order no")
	}
	if gotFirst == "DJP123" {
		t.Fatalf("resolveGatewayOrderNo() should not leak payment id, got %s", gotFirst)
	}
	if !strings.HasPrefix(gotFirst, "DJP") {
		t.Fatalf("resolveGatewayOrderNo() should keep DJP prefix, got %s", gotFirst)
	}

	payment.GatewayOrderNo = "CUSTOM-1"
	if got := resolveGatewayOrderNo(channel, payment); got != "CUSTOM-1" {
		t.Fatalf("resolveGatewayOrderNo() should reuse stored value, got %s", got)
	}
	payment.GatewayOrderNo = ""

	gotFallback := resolveGatewayOrderNo(channel, payment)
	if gotFallback == "" {
		t.Fatalf("resolveGatewayOrderNo() fallback should not be empty")
	}
	if gotFallback == "DJP123" {
		t.Fatalf("resolveGatewayOrderNo() fallback should not leak payment id, got %s", gotFallback)
	}
	if !strings.HasPrefix(gotFallback, "DJP") {
		t.Fatalf("resolveGatewayOrderNo() fallback should keep DJP prefix, got %s", gotFallback)
	}
	if gotFallback == gotFirst {
		t.Fatalf("resolveGatewayOrderNo() should generate new gateway order no for a new payment attempt")
	}

	official := &paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderOfficial}
	if got := resolveGatewayOrderNo(official, payment); got == "" {
		t.Fatalf("official provider should also use gateway order no")
	}
}

func TestApplyProviderPaymentFallsBackToWechatGatewayOrderNoWhenProviderRefEmpty(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTWECHAT001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(1)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(1)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(1)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("1.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	var providerInputOrderNo string
	registerTestGateway(t, svc, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, emptyProviderRefProvider{
		onCreate: func(input paymentcontract.GatewayCreateInput) {
			providerInputOrderNo = strings.TrimSpace(input.OrderNo)
		},
	})

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if !strings.HasPrefix(payment.GatewayOrderNo, "DJP") {
		t.Fatalf("payment gateway order no should keep DJP prefix, got %s", payment.GatewayOrderNo)
	}
	if providerInputOrderNo != payment.GatewayOrderNo {
		t.Fatalf("wechat create order no = %s, want %s", providerInputOrderNo, payment.GatewayOrderNo)
	}
	if payment.ProviderRef != payment.GatewayOrderNo {
		t.Fatalf("provider ref fallback = %s, want gateway order no %s", payment.ProviderRef, payment.GatewayOrderNo)
	}
	if payment.ProviderRef == order.OrderNo {
		t.Fatalf("provider ref should not fall back to business order no")
	}
}

func TestApplyProviderPaymentStoresDisplayChannelType(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTDISPLAY001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("10.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	registerTestGateway(t, svc, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, emptyProviderRefProvider{
		displayChannelType: "usdt.arbitrum",
	})

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.ProviderPayload == nil {
		t.Fatalf("provider payload should be recorded")
	}
	if got := payment.ProviderPayload["display_channel_type"]; got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type = %v, want usdt.arbitrum", got)
	}
}

func TestPaymentDisplayChannelTypeLifecycle(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTDISPLAYLIFECYCLE001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		Name:            "BEpusdt Arbitrum",
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentProviderBepusdt,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.NewFromInt(10)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	registerTestGateway(t, svc, constants.PaymentProviderBepusdt, "", emptyProviderRefProvider{
		displayChannelType: "usdt.arbitrum",
	})
	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("create provider payment failed: %v", err)
	}
	if got := notificationPayloadString(payment.ProviderPayload, "display_channel_type"); got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type after create = %q, want usdt.arbitrum", got)
	}

	paidAt := time.Now()
	if _, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     payment.GatewayOrderNo,
		ChannelID:   channel.ID,
		Status:      constants.PaymentStatusSuccess,
		ProviderRef: "BEP-CALLBACK-1",
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      &paidAt,
		Payload: jsonmap.JSON{
			"trade_id": "BEP-CALLBACK-1",
			"status":   float64(2),
		},
	}); err != nil {
		t.Fatalf("handle callback failed: %v", err)
	}

	storedPayment, err := svc.GetPayment(payment.ID)
	if err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if got := notificationPayloadString(storedPayment.ProviderPayload, "display_channel_type"); got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type after callback reload = %q, want usdt.arbitrum", got)
	}
	if got := notificationPayloadString(storedPayment.ProviderPayload, "trade_id"); got != "BEP-CALLBACK-1" {
		t.Fatalf("callback trade_id after reload = %q, want BEP-CALLBACK-1", got)
	}

	notificationPayload := svc.buildOrderNotificationPayload(order, storedPayment)
	if got := notificationPayloadString(notificationPayload, "payment_channel"); got != "bepusdt/usdt.arbitrum" {
		t.Fatalf("notification payment_channel = %q, want bepusdt/usdt.arbitrum", got)
	}

	adminPayments, _, err := svc.ListPayments(paymentcontract.ListFilter{
		Page:        1,
		PageSize:    10,
		OrderID:     order.ID,
		Lightweight: true,
	})
	if err != nil {
		t.Fatalf("list admin payments failed: %v", err)
	}
	if len(adminPayments) != 1 {
		t.Fatalf("admin payment count = %d, want 1", len(adminPayments))
	}
	if got := adminPayments[0].DisplayChannelType; got != "usdt.arbitrum" {
		t.Fatalf("admin display_channel_type = %q, want usdt.arbitrum", got)
	}
}

func TestApplyProviderPaymentUsesGatewayOrderNoForBepusdt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTGATEWAY001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(50)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(50)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(50)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	var gotOrderID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request failed: %v", err)
		}
		gotOrderID = strings.TrimSpace(payload["order_id"].(string))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":200,"message":"ok","data":{"trade_id":"TRX-1001","order_id":"` + gotOrderID + `","amount":"50.00","actual_amount":"7.00","token":"USDT","expiration_time":1800,"payment_url":"https://pay.example.com/checkout"}}`))
	}))
	defer server.Close()

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentChannelTypeUsdtTrc20,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url": server.URL,
			"auth_token":  "token-001",
			"trade_type":  "usdt.trc20",
			"fiat":        "CNY",
			"notify_url":  "https://example.com/callback",
			"return_url":  "https://example.com/pay-return",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("50.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if payment.GatewayOrderNo == order.OrderNo {
		t.Fatalf("payment gateway order no should differ from business order no, got %s", payment.GatewayOrderNo)
	}
	if !strings.HasPrefix(payment.GatewayOrderNo, "DJP") {
		t.Fatalf("payment gateway order no should keep DJP prefix, got %s", payment.GatewayOrderNo)
	}
	if gotOrderID != payment.GatewayOrderNo {
		t.Fatalf("epusdt order_id = %s, want %s", gotOrderID, payment.GatewayOrderNo)
	}
	if payment.ProviderRef != "TRX-1001" {
		t.Fatalf("provider ref = %s, want TRX-1001", payment.ProviderRef)
	}
}

func TestApplyProviderPaymentUsesGatewayOrderNoForOkpay(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTOKPAY001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	var gotUniqueID string
	var gotAmount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form failed: %v", err)
		}
		gotUniqueID = strings.TrimSpace(r.PostForm.Get("unique_id"))
		gotAmount = strings.TrimSpace(r.PostForm.Get("amount"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","code":200,"data":{"order_id":"OKPAY-ORDER-1001","pay_url":"https://pay.example.com/okpay"}}`))
	}))
	defer server.Close()

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOkpay,
		ChannelType:     constants.PaymentChannelTypeUsdt,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":    server.URL,
			"merchant_id":    "shop-1001",
			"merchant_token": "token-1001",
			"return_url":     "https://example.com/pay",
			"callback_url":   "https://api.example.com/api/v1/payments/callback",
			"exchange_rate":  "7",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("88.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if gotUniqueID != payment.GatewayOrderNo {
		t.Fatalf("okpay unique_id = %s, want %s", gotUniqueID, payment.GatewayOrderNo)
	}
	if gotAmount != "616.00000000" {
		t.Fatalf("okpay amount = %s, want 616.00000000", gotAmount)
	}
	if payment.ProviderRef != "OKPAY-ORDER-1001" {
		t.Fatalf("provider ref = %s, want OKPAY-ORDER-1001", payment.ProviderRef)
	}
	if payment.PayURL != "https://pay.example.com/okpay" {
		t.Fatalf("unexpected pay url: %s", payment.PayURL)
	}
	if payment.QRCode != "https://pay.example.com/okpay" {
		t.Fatalf("unexpected qr code: %s", payment.QRCode)
	}
}

func TestApplyProviderPaymentBuildsRedirectURLForEpay(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTEPAYREDIRECT001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(20)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(20)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(20)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeAlipay,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("20.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment redirect failed: %v", err)
	}

	if payment.PayURL == "" {
		t.Fatalf("pay url should not be empty")
	}
	if !strings.HasPrefix(payment.PayURL, "https://gateway.example.com/submit.php?") {
		t.Fatalf("unexpected pay url: %s", payment.PayURL)
	}
	if payment.QRCode != "" {
		t.Fatalf("qr code should stay empty in redirect mode")
	}
	if payment.ProviderPayload == nil {
		t.Fatalf("provider payload should be recorded")
	}
}

func TestValidateChannelRejectsInvalidEpayInteractionMode(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionPage,
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
	}
	if err := svc.ValidateChannel(channel); err == nil {
		t.Fatalf("ValidateChannel should reject invalid epay interaction mode")
	}
}

func TestValidateChannelRejectsBepusdtCashierQR(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentProviderBepusdt,
		InteractionMode: constants.PaymentInteractionQR,
		ConfigJSON: jsonmap.JSON{
			"gateway_url": "https://bepusdt.example.com",
			"auth_token":  "token-001",
			"order_mode":  constants.PaymentBepusdtOrderModeCashier,
			"notify_url":  "https://api.example.com/api/v1/payments/callback",
			"return_url":  "https://shop.example.com/pay",
		},
	}
	if err := svc.ValidateChannel(channel); err == nil {
		t.Fatal("ValidateChannel should reject bepusdt cashier + qr")
	}
}

func TestValidateChannelAllowsUnlimitedMaxAmount(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionRedirect,
		MinAmount:       money.FromDecimal(decimal.RequireFromString("10.00")),
		MaxAmount:       money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
	}

	if err := svc.ValidateChannel(channel); err != nil {
		t.Fatalf("ValidateChannel should allow max_amount=0 (unlimited): %v", err)
	}
}

func TestHandleCallbackAcceptsGatewayOrderNoForOrderPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTCALLBACK001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentChannelTypeUsdtTrc20,
		InteractionMode: constants.PaymentInteractionRedirect,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		GatewayOrderNo:  "DJP501",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	updated, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     payment.GatewayOrderNo,
		ChannelID:   payment.ChannelID,
		Status:      constants.PaymentStatusPending,
		ProviderRef: "TRX-PENDING-1",
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      ptrTime(time.Now()),
	})
	if err != nil {
		t.Fatalf("HandleCallback should accept gateway order no, got: %v", err)
	}
	if updated == nil || updated.ID != payment.ID {
		t.Fatalf("expected updated payment")
	}
}

func TestHandleCallbackAcceptsGatewayOrderNoForWalletRecharge(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	payment.GatewayOrderNo = "DJP8801"
	if err := db.Save(payment).Error; err != nil {
		t.Fatalf("save payment gateway order no failed: %v", err)
	}

	input := buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusPending, "CALLBACK-PENDING-GATEWAY")
	input.OrderNo = payment.GatewayOrderNo

	updated, err := svc.HandleCallback(input)
	if err != nil {
		t.Fatalf("HandleCallback should accept recharge gateway order no, got: %v", err)
	}
	if updated == nil || updated.ID != payment.ID {
		t.Fatalf("expected updated payment")
	}
}

func TestCalculatePaymentAmountsKeepsChannelFeeOutOfUserPayment(t *testing.T) {
	tests := []struct {
		name        string
		baseAmount  string
		feeRate     string
		fixedFee    string
		customerFee bool
		wantPayment string
		wantFee     string
		wantPolicy  string
	}{
		{
			name:        "percentage and fixed fee",
			baseAmount:  "100.00",
			feeRate:     "2.00",
			fixedFee:    "1.00",
			wantPayment: "100.00",
			wantFee:     "3.00",
			wantPolicy:  constants.PaymentFeePolicyMerchantAbsorbed,
		},
		{
			name:        "rounds payment and fee independently",
			baseAmount:  "12.345",
			feeRate:     "1.50",
			fixedFee:    "0.10",
			wantPayment: "12.35",
			wantFee:     "0.29",
			wantPolicy:  constants.PaymentFeePolicyMerchantAbsorbed,
		},
		{
			name:        "customer surcharge compatibility mode",
			baseAmount:  "100.00",
			feeRate:     "2.00",
			fixedFee:    "1.00",
			customerFee: true,
			wantPayment: "103.00",
			wantFee:     "3.00",
			wantPolicy:  constants.PaymentFeePolicyCustomerSurcharge,
		},
		{
			name:        "zero fee",
			baseAmount:  "88.00",
			feeRate:     "0",
			fixedFee:    "0",
			wantPayment: "88.00",
			wantFee:     "0.00",
			wantPolicy:  constants.PaymentFeePolicyNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paymentAmount, feeAmount, feePolicy := calculatePaymentAmounts(
				decimal.RequireFromString(tt.baseAmount),
				decimal.RequireFromString(tt.feeRate),
				decimal.RequireFromString(tt.fixedFee),
				tt.customerFee,
			)
			if got := paymentAmount.StringFixed(2); got != tt.wantPayment {
				t.Fatalf("payment amount want %s got %s", tt.wantPayment, got)
			}
			if got := feeAmount.StringFixed(2); got != tt.wantFee {
				t.Fatalf("fee amount want %s got %s", tt.wantFee, got)
			}
			if feePolicy != tt.wantPolicy {
				t.Fatalf("fee policy want %s got %s", tt.wantPolicy, feePolicy)
			}
		})
	}
}

func TestCreateOrderPaymentSendsBaseAmountToGatewayAndStoresMerchantFee(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	channel := &paymentdomain.PaymentChannel{
		Name:            "Merchant Fee Gateway",
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.RequireFromString("2.00")),
		FixedFee:        money.FromDecimal(decimal.RequireFromString("1.00")),
		ConfigJSON:      jsonmap.JSON{},
		IsActive:        true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create payment channel failed: %v", err)
	}
	order := &orderdomain.Order{
		OrderNo:                 "DJ-MERCHANT-FEE-ORDER",
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(100)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(100)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	var gatewayAmount money.Amount
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{
		onCreate: func(input paymentcontract.GatewayCreateInput) {
			gatewayAmount = input.Amount
		},
	})

	result, err := svc.CreatePayment(CreatePaymentInput{
		OrderID:   order.ID,
		ChannelID: channel.ID,
		Context:   context.Background(),
	})
	if err != nil {
		t.Fatalf("create payment failed: %v", err)
	}
	if result.Payment == nil {
		t.Fatal("expected payment result")
	}
	if got := gatewayAmount.StringFixed(2); got != "100.00" {
		t.Fatalf("gateway amount want 100.00 got %s", got)
	}
	if got := result.Payment.Amount.StringFixed(2); got != "100.00" {
		t.Fatalf("payment amount want 100.00 got %s", got)
	}
	if got := result.Payment.FeeAmount.StringFixed(2); got != "3.00" {
		t.Fatalf("merchant fee want 3.00 got %s", got)
	}
	if result.Payment.FeePolicy != constants.PaymentFeePolicyMerchantAbsorbed {
		t.Fatalf("fee policy want %s got %s", constants.PaymentFeePolicyMerchantAbsorbed, result.Payment.FeePolicy)
	}
}

func TestCreateWalletRechargeSendsRechargeAmountToGatewayAndStoresMerchantFee(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	user := &userdomain.User{
		Email:        "merchant-fee-recharge@example.com",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	channel := &paymentdomain.PaymentChannel{
		Name:            "Merchant Fee Recharge Gateway",
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.RequireFromString("2.00")),
		FixedFee:        money.FromDecimal(decimal.RequireFromString("1.00")),
		ConfigJSON:      jsonmap.JSON{},
		IsActive:        true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create payment channel failed: %v", err)
	}

	var gatewayAmount money.Amount
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{
		onCreate: func(input paymentcontract.GatewayCreateInput) {
			gatewayAmount = input.Amount
		},
	})

	result, err := svc.CreateWalletRechargePayment(CreateWalletRechargePaymentInput{
		UserID:    user.ID,
		ChannelID: channel.ID,
		Amount:    money.FromDecimal(decimal.NewFromInt(100)),
		Currency:  "CNY",
		Context:   context.Background(),
	})
	if err != nil {
		t.Fatalf("create wallet recharge failed: %v", err)
	}
	if got := gatewayAmount.StringFixed(2); got != "100.00" {
		t.Fatalf("gateway amount want 100.00 got %s", got)
	}
	if got := result.Payment.Amount.StringFixed(2); got != "100.00" {
		t.Fatalf("payment amount want 100.00 got %s", got)
	}
	if got := result.Payment.FeeAmount.StringFixed(2); got != "3.00" {
		t.Fatalf("merchant fee want 3.00 got %s", got)
	}
	if result.Payment.FeePolicy != constants.PaymentFeePolicyMerchantAbsorbed {
		t.Fatalf("fee policy want %s got %s", constants.PaymentFeePolicyMerchantAbsorbed, result.Payment.FeePolicy)
	}
	if got := result.Recharge.Amount.StringFixed(2); got != "100.00" {
		t.Fatalf("recharge amount want 100.00 got %s", got)
	}
	if got := result.Recharge.PayableAmount.StringFixed(2); got != "100.00" {
		t.Fatalf("recharge payable amount want 100.00 got %s", got)
	}
}

func createFeePolicyOrderFixture(t *testing.T, db *gorm.DB, channelName, orderNo string) (*paymentdomain.PaymentChannel, *orderdomain.Order) {
	t.Helper()
	now := time.Now()
	channel := &paymentdomain.PaymentChannel{
		Name: channelName, ProviderType: constants.PaymentProviderOfficial,
		ChannelType: constants.PaymentChannelTypeWechat, InteractionMode: constants.PaymentInteractionQR,
		FeeRate: money.FromDecimal(decimal.RequireFromString("2.00")), FixedFee: money.FromDecimal(decimal.RequireFromString("1.00")),
		ConfigJSON: jsonmap.JSON{}, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create payment channel failed: %v", err)
	}
	order := &orderdomain.Order{
		OrderNo: orderNo, Status: constants.OrderStatusPendingPayment, Currency: "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(100)), TotalAmount: money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount: money.FromDecimal(decimal.Zero), PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		WalletPaidAmount: money.FromDecimal(decimal.Zero), OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(100)),
		RefundedAmount: money.FromDecimal(decimal.Zero), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	return channel, order
}

func TestCreateOrderPaymentSnapshotsCustomerSurchargeCompatibilityMode(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	if _, err := svc.settingService.Update(constants.SettingKeyPaymentConfig, map[string]interface{}{
		constants.SettingFieldCustomerFeeEnabled: true,
	}); err != nil {
		t.Fatalf("enable customer fee compatibility mode: %v", err)
	}
	channel, order := createFeePolicyOrderFixture(t, db, "Customer Fee Gateway", "DJ-CUSTOMER-FEE")

	var gatewayAmount money.Amount
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{onCreate: func(input paymentcontract.GatewayCreateInput) {
		gatewayAmount = input.Amount
	}})
	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
	if err != nil {
		t.Fatalf("create customer fee payment failed: %v", err)
	}
	if got := gatewayAmount.StringFixed(2); got != "103.00" {
		t.Fatalf("gateway amount want 103.00 got %s", got)
	}
	if result.Payment.FeePolicy != constants.PaymentFeePolicyCustomerSurcharge {
		t.Fatalf("fee policy want %s got %s", constants.PaymentFeePolicyCustomerSurcharge, result.Payment.FeePolicy)
	}
}

func TestCreateOrderPaymentSupersedesLegacyFeeLinkByDefault(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "Legacy Replacement Gateway", "DJ-LEGACY-REPLACE")
	now := time.Now()
	legacy := &paymentdomain.Payment{
		OrderID: order.ID, ChannelID: channel.ID, ProviderType: channel.ProviderType,
		ChannelType: channel.ChannelType, InteractionMode: channel.InteractionMode,
		Amount: money.FromDecimal(decimal.NewFromInt(103)), FeeRate: channel.FeeRate, FixedFee: channel.FixedFee,
		FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyLegacyCustomerSurcharge,
		Currency: "CNY", Status: constants.PaymentStatusPending, QRCode: "legacy-fee-link", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy payment failed: %v", err)
	}
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{})

	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
	if err != nil {
		t.Fatalf("replace legacy payment failed: %v", err)
	}
	if result.Payment.ID == legacy.ID {
		t.Fatal("legacy fee payment should be replaced")
	}
	if result.Payment.FeePolicy != constants.PaymentFeePolicyMerchantAbsorbed || result.Payment.Amount.StringFixed(2) != "100.00" {
		t.Fatalf("unexpected replacement payment: %+v", result.Payment)
	}
	var storedLegacy paymentdomain.Payment
	if err := db.First(&storedLegacy, legacy.ID).Error; err != nil {
		t.Fatalf("reload legacy payment failed: %v", err)
	}
	if storedLegacy.Status != constants.PaymentStatusExpired || storedLegacy.SupersededAt == nil || storedLegacy.SupersededByPaymentID == nil || *storedLegacy.SupersededByPaymentID != result.Payment.ID {
		t.Fatalf("legacy payment was not superseded correctly: %+v", storedLegacy)
	}
}

func TestCreateOrderPaymentKeepsLegacyFeeLinkWhenReplacementGatewayFails(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "Failed Legacy Replacement Gateway", "DJ-LEGACY-REPLACE-FAIL")
	now := time.Now()
	legacy := &paymentdomain.Payment{
		OrderID: order.ID, ChannelID: channel.ID, ProviderType: channel.ProviderType,
		ChannelType: channel.ChannelType, InteractionMode: channel.InteractionMode,
		Amount: money.FromDecimal(decimal.NewFromInt(103)), FeeRate: channel.FeeRate, FixedFee: channel.FixedFee,
		FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyLegacyCustomerSurcharge,
		Currency: "CNY", Status: constants.PaymentStatusPending, QRCode: "legacy-fee-link", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy payment failed: %v", err)
	}
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, emptyProviderRefProvider{createErr: errors.New("gateway unavailable")})

	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()}); err == nil {
		t.Fatal("expected replacement gateway failure")
	}
	var storedLegacy paymentdomain.Payment
	if err := db.First(&storedLegacy, legacy.ID).Error; err != nil {
		t.Fatalf("reload legacy payment failed: %v", err)
	}
	if storedLegacy.Status != constants.PaymentStatusPending || storedLegacy.SupersededAt != nil || storedLegacy.SupersededByPaymentID != nil {
		t.Fatalf("failed replacement must leave legacy payment usable: %+v", storedLegacy)
	}
}

func TestCreateOrderPaymentCanReuseLegacyFeeLinkUntilExpiry(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	if _, err := svc.settingService.Update(constants.SettingKeyPaymentConfig, map[string]interface{}{
		constants.SettingFieldReuseLegacyOrderFeePayment: true,
	}); err != nil {
		t.Fatalf("enable legacy payment reuse: %v", err)
	}
	channel, order := createFeePolicyOrderFixture(t, db, "Legacy Reuse Gateway", "DJ-LEGACY-REUSE")
	now := time.Now()
	legacy := &paymentdomain.Payment{
		OrderID: order.ID, ChannelID: channel.ID, ProviderType: channel.ProviderType,
		ChannelType: channel.ChannelType, InteractionMode: channel.InteractionMode,
		Amount: money.FromDecimal(decimal.NewFromInt(103)), FeeRate: channel.FeeRate, FixedFee: channel.FixedFee,
		FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyLegacyCustomerSurcharge,
		Currency: "CNY", Status: constants.PaymentStatusPending, QRCode: "legacy-fee-link", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy payment failed: %v", err)
	}

	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
	if err != nil {
		t.Fatalf("reuse legacy payment failed: %v", err)
	}
	if result.Payment.ID != legacy.ID || result.Payment.FeePolicy != constants.PaymentFeePolicyLegacyCustomerSurcharge {
		t.Fatalf("legacy payment was not reused: %+v", result.Payment)
	}
}

func TestSupersededPaymentLateSuccessIsRecordedWithoutDuplicateFulfillment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, order := createFeePolicyOrderFixture(t, db, "Late Callback Gateway", "DJ-LATE-CALLBACK")
	now := time.Now()
	replacement := &paymentdomain.Payment{
		OrderID: order.ID, ChannelID: channel.ID, ProviderType: channel.ProviderType, ChannelType: channel.ChannelType,
		InteractionMode: channel.InteractionMode, Amount: money.FromDecimal(decimal.NewFromInt(100)),
		FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyMerchantAbsorbed,
		Currency: "CNY", Status: constants.PaymentStatusPending, QRCode: "replacement-link", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(replacement).Error; err != nil {
		t.Fatalf("create replacement payment failed: %v", err)
	}
	supersededAt := now.Add(-time.Minute)
	superseded := &paymentdomain.Payment{
		OrderID: order.ID, ChannelID: channel.ID, ProviderType: channel.ProviderType, ChannelType: channel.ChannelType,
		InteractionMode: channel.InteractionMode, Amount: money.FromDecimal(decimal.NewFromInt(103)),
		FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyLegacyCustomerSurcharge,
		Currency: "CNY", Status: constants.PaymentStatusExpired, QRCode: "legacy-link", ExpiredAt: &supersededAt,
		SupersededAt: &supersededAt, SupersededByPaymentID: &replacement.ID, CreatedAt: now.Add(-time.Hour), UpdatedAt: supersededAt,
	}
	if err := db.Create(superseded).Error; err != nil {
		t.Fatalf("create superseded payment failed: %v", err)
	}

	oldSuccess, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID: superseded.ID, OrderNo: order.OrderNo, ChannelID: channel.ID,
		Status: constants.PaymentStatusSuccess, Amount: superseded.Amount, Currency: "CNY", ProviderRef: "late-old-success",
	})
	if err != nil {
		t.Fatalf("handle superseded success failed: %v", err)
	}
	if oldSuccess.ExceptionCode != constants.PaymentExceptionSupersededSucceeded {
		t.Fatalf("superseded success exception = %q", oldSuccess.ExceptionCode)
	}
	var storedOrder orderdomain.Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload paid order failed: %v", err)
	}
	if storedOrder.Status != constants.OrderStatusPaid || storedOrder.PaidAt == nil {
		t.Fatalf("superseded payment did not fulfill pending order: %+v", storedOrder)
	}
	var expiredReplacement paymentdomain.Payment
	if err := db.First(&expiredReplacement, replacement.ID).Error; err != nil {
		t.Fatalf("reload replacement payment failed: %v", err)
	}
	if expiredReplacement.Status != constants.PaymentStatusExpired {
		t.Fatalf("replacement link should expire after old success: %+v", expiredReplacement)
	}

	duplicate, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID: replacement.ID, OrderNo: order.OrderNo, ChannelID: channel.ID,
		Status: constants.PaymentStatusSuccess, Amount: replacement.Amount, Currency: "CNY", ProviderRef: "late-replacement-success",
	})
	if err != nil {
		t.Fatalf("handle duplicate success failed: %v", err)
	}
	if duplicate.Status != constants.PaymentStatusSuccess || duplicate.ExceptionCode != constants.PaymentExceptionDuplicateSucceeded {
		t.Fatalf("duplicate payment was not flagged: %+v", duplicate)
	}
}
