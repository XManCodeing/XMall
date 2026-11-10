package channelwiring

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/i18n"
	channel "github.com/XManCodeing/XMall/internal/modules/channelapi/transport/http"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentapp "github.com/XManCodeing/XMall/internal/modules/payment/application"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	wallet "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type balanceFailureStore struct {
	paymentcontract.Store
	err error
}

// WithinTransaction returns a fixture failure through the actual payment service.
func (store balanceFailureStore) WithinTransaction(func(paymentcontract.Transaction) error) error {
	return store.err
}

type balanceIdentity struct{ channel.IdentityService }

// ProvisionTelegramChannelUserID returns the fixture actor; no remote identity is provisioned.
func (balanceIdentity) ProvisionTelegramChannelUserID(channel.TelegramIdentityInput) (uint, error) {
	return 1, nil
}

type balanceOrders struct{ channel.Orders }

// GetOrderByUser authorizes the fixture order for the requested actor.
func (balanceOrders) GetOrderByUser(orderID, userID uint) (*orderdomain.Order, error) {
	return &orderdomain.Order{ID: orderID, UserID: userID}, nil
}

// TestBalanceHTTPAdapter checks the full handler/adapter error path in every API locale.
func TestBalanceHTTPAdapter(t *testing.T) {
	if got := mapError(errors.Join(paymentcontract.ErrPaymentCreationUncertain, wallet.ErrInsufficientBalance)); !errors.Is(got, paymentcontract.ErrPaymentCreationUncertain) || errors.Is(got, wallet.ErrInsufficientBalance) {
		t.Fatalf("uncertainty lost priority: %v", got)
	}
	gin.SetMode(gin.TestMode)
	for _, locale := range []string{i18n.LocaleZH, i18n.LocaleTW, i18n.LocaleEN} {
		for _, source := range []error{wallet.ErrInsufficientBalance, fmt.Errorf("debit: %w", wallet.ErrInsufficientBalance), wallet.ErrOnlyPaymentRequired} {
			service := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{PaymentStore: balanceFailureStore{err: source}})
			handler := &channel.Handler{Dependencies: channel.Dependencies{UserAuthService: balanceIdentity{}, OrderService: balanceOrders{}, PaymentService: paymentAdapter{payments: service}}}
			router := gin.New()
			router.POST("/channel/payments", handler.CreatePayment)
			request := httptest.NewRequest(http.MethodPost, "/channel/payments", strings.NewReader(`{"order_id":81,"channel_user_id":"1","use_balance":true}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept-Language", locale)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var body response.ChannelResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			key, code := "error.wallet_insufficient_balance", "insufficient_balance"
			if source == wallet.ErrOnlyPaymentRequired {
				key, code = "error.wallet_only_payment_required", "wallet_only_payment_required"
			}
			if recorder.Code != http.StatusBadRequest || body.StatusCode != response.CodeBadRequest || body.ErrorCode != code || body.Msg != i18n.T(locale, key) || body.Msg == key {
				t.Fatalf("locale=%s source=%v status=%d body=%s", locale, source, recorder.Code, recorder.Body.String())
			}
		}
	}
}
