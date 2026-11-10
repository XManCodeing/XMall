package paymentbootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/app/container"
	"github.com/XManCodeing/XMall/internal/i18n"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	ordertransport "github.com/XManCodeing/XMall/internal/modules/order/transport/http"
	paymentapp "github.com/XManCodeing/XMall/internal/modules/payment/application"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	reseller "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	wallet "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
	"testing"

	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	paymenttransport "github.com/XManCodeing/XMall/internal/modules/payment/transport/http"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// manualWiringPaymentStore 只允许生产 GET 用例所需的读取，其余嵌入接口调用会暴露错误装配。
type manualWiringPaymentStore struct {
	paymentcontract.Store
	paymentcontract.ManualConfirmationStore
	requestedID uint
}

// GetByID 记录 id 并返回合成支付；本测试不访问数据库或网关。
func (store *manualWiringPaymentStore) GetByID(id uint) (*paymentdomain.Payment, error) {
	store.requestedID = id
	return &paymentdomain.Payment{ID: id, OrderID: 81, ChannelID: 71, Status: "pending", Currency: "CNY"}, nil
}

// FindManualConfirmation 返回 paymentID 的空审计，验证查询走实际 PaymentService。
func (store *manualWiringPaymentStore) FindManualConfirmation(paymentID uint) (*paymentdomain.ManualConfirmation, error) {
	return nil, nil
}

type manualWiringOrderStore struct{ ordercontract.Store }

// GetByID 返回 id 对应的未过期主订单，不连接任何业务基础设施。
func (manualWiringOrderStore) GetByID(id uint) (*orderdomain.Order, error) {
	expires := time.Now().Add(time.Hour)
	return &orderdomain.Order{ID: id, OrderNo: "synthetic-order", Status: "pending_payment", ExpiresAt: &expires}, nil
}

type manualWiringChannelStore struct{ paymentcontract.ChannelStore }

// TestNewWiresManualConfirmationPaymentService 验证 New 的实际 handler 使用容器原 PaymentService，非替代业务路径。
func TestNewWiresManualConfirmationPaymentService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payments := &manualWiringPaymentStore{}
	service := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{PaymentStore: payments, OrderStore: manualWiringOrderStore{}})
	handlers := New(&container.Container{PaymentService: service, PaymentStore: payments, PaymentChannelStore: manualWiringChannelStore{}})
	if handlers.ManualConfirmation == nil {
		t.Fatal("production manual confirmation handler missing")
	}
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(61)); ctx.Next() })
	router.GET("/payments/:id/manual-confirmation", handlers.ManualConfirmation.GetManualConfirmation)
	router.POST("/payments/:id/manual-confirmation", handlers.ManualConfirmation.ConfirmManually)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/payments/91/manual-confirmation", nil))
	var envelope struct {
		Code int                                      `json:"status_code"`
		Data paymentcontract.ManualConfirmationReview `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || envelope.Data.PaymentID != 91 || envelope.Data.OrderID != 81 || payments.requestedID != 91 {
		t.Fatalf("manual review did not use original service: status=%d body=%s", response.Code, response.Body.String())
	}
	// 空复核事实由真实服务验证失败，不能误入网关回调或事务写入。
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/payments/91/manual-confirmation", strings.NewReader(`{}`)))
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != http.StatusBadRequest {
		t.Fatalf("manual POST must invoke original validation: status=%d body=%s", response.Code, response.Body.String())
	}
}

type balanceFailureStore struct {
	paymentcontract.Store
	err error
}

// WithinTransaction injects a business failure at the actual service boundary.
func (store balanceFailureStore) WithinTransaction(func(paymentcontract.Transaction) error) error {
	return store.err
}

type balanceOrderLookup struct {
	paymenttransport.UserOrderLookup
	paymenttransport.GuestOrderLookup
}

// GetOrderByUserOrderNoForTenant returns the authorized fixture order for HTTP creation.
func (balanceOrderLookup) GetOrderByUserOrderNoForTenant(reseller.TenantContext, string, uint) (*orderdomain.Order, error) {
	return &orderdomain.Order{ID: 81, OrderNo: "balance-order"}, nil
}

// TestBalanceHTTPAdapter exercises handler -> production adapter -> application and back.
func TestBalanceHTTPAdapter(t *testing.T) {
	if got := mapTransportError(errors.Join(paymentcontract.ErrPaymentCreationUncertain, wallet.ErrInsufficientBalance)); !errors.Is(got, paymentcontract.ErrPaymentCreationUncertain) || errors.Is(got, wallet.ErrInsufficientBalance) {
		t.Fatalf("uncertainty lost priority: %v", got)
	}
	gin.SetMode(gin.TestMode)
	for _, locale := range []string{i18n.LocaleZH, i18n.LocaleTW, i18n.LocaleEN} {
		for _, source := range []error{wallet.ErrInsufficientBalance, fmt.Errorf("debit: %w", wallet.ErrInsufficientBalance), wallet.ErrOnlyPaymentRequired} {
			service := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{PaymentStore: balanceFailureStore{err: source}})
			lookup := balanceOrderLookup{}
			handler := paymenttransport.NewWriteHandler(lookup, lookup, writerAdapter{payments: service})
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("user_id", uint(1)); ctx.Next() })
			router.POST("/payments", handler.CreatePayment)
			request := httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(`{"order_no":"balance-order","use_balance":true}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept-Language", locale)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var body response.Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			key := "error.wallet_insufficient_balance"
			if source == wallet.ErrOnlyPaymentRequired {
				key = "error.wallet_only_payment_required"
			}
			if recorder.Code != http.StatusOK || body.StatusCode != response.CodeBadRequest || body.Msg != i18n.T(locale, key) || body.Msg == key {
				t.Fatalf("locale=%s source=%v status=%d body=%s", locale, source, recorder.Code, recorder.Body.String())
			}
		}
	}
}

type balanceCreatedOrder struct {
	ordertransport.OrderCreateService
}

// CreateOrder returns the persisted fixture order for create-and-pay recovery.
func (balanceCreatedOrder) CreateOrder(ordertransport.CreateOrderInput) (*orderdomain.Order, error) {
	return &orderdomain.Order{ID: 81, OrderNo: "balance-order"}, nil
}

type balanceOrderPayment struct{ err error }

// CreatePayment mirrors the order adapter's intentional preservation of wallet contract errors.
func (creator balanceOrderPayment) CreatePayment(ordertransport.CreatePaymentInput) (*ordertransport.CreatePaymentResult, error) {
	return nil, creator.err
}

// TestCreateAndPayBalanceHTTP verifies localized business failure with created-order recovery data.
func TestCreateAndPayBalanceHTTP(t *testing.T) {
	for _, locale := range []string{i18n.LocaleZH, i18n.LocaleTW, i18n.LocaleEN} {
		for _, source := range []error{wallet.ErrInsufficientBalance, wallet.ErrOnlyPaymentRequired} {
			handler := ordertransport.NewCreateHandler(balanceCreatedOrder{}, nil, nil, balanceOrderPayment{err: source})
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("user_id", uint(1)); ctx.Next() })
			router.POST("/orders/create-and-pay", handler.CreateOrderAndPay)
			request := httptest.NewRequest(http.MethodPost, "/orders/create-and-pay", strings.NewReader(`{"items":[{"product_id":1,"quantity":1}],"use_balance":true}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept-Language", locale)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var body response.Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			key := "error.wallet_insufficient_balance"
			if source == wallet.ErrOnlyPaymentRequired {
				key = "error.wallet_only_payment_required"
			}
			if body.StatusCode != response.CodeBadRequest || body.Msg != i18n.T(locale, key) {
				t.Fatalf("body=%s", recorder.Body.String())
			}
			data, ok := body.Data.(map[string]interface{})
			if !ok || data["order_no"] != "balance-order" || data["payment_error"] != body.Msg {
				t.Fatalf("recovery data=%+v", body.Data)
			}
		}
	}
}

// TestMembershipTransportErrors verifies production wiring preserves actionable membership failures.
func TestMembershipTransportErrors(t *testing.T) {
	for _, mapping := range []struct{ source, target error }{
		{membership.ErrPermanentFullyCovered, paymenttransport.ErrMembershipPermanentCovered},
		{membership.ErrPermanentPartiallyCovered, paymenttransport.ErrMembershipPermanentPartial},
		{membership.ErrInvalid, paymenttransport.ErrMembershipInvalid},
		{membership.ErrUntrustedIdentity, paymenttransport.ErrMembershipBindingRequired},
		{membership.ErrConflict, paymenttransport.ErrMembershipPurchaseChanged},
		{membership.ErrProbeRetry, paymenttransport.ErrMembershipUnavailable},
		{membership.ErrCapability, paymenttransport.ErrMembershipUnavailable},
		{membership.ErrOverlapUnavailable, paymenttransport.ErrMembershipUnavailable},
		{membership.ErrSalesClosed, paymenttransport.ErrMembershipSalesClosed},
		{ei.ErrTelegramIdentityChanged, paymenttransport.ErrMembershipBindingRequired},
		{product.ErrMembershipSaleClosed, paymenttransport.ErrMembershipSalesClosed},
	} {
		if actual := mapTransportError(fmt.Errorf("internal detail: %w", mapping.source)); !errors.Is(actual, mapping.target) {
			t.Fatalf("%v mapped to %v; want %v", mapping.source, actual, mapping.target)
		}
	}
}
