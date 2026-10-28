package common_test

import (
	"context"
	"errors"
	"testing"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	alipay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/alipay"
	bepusdt "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/bepusdt"
	dujiaopay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/dujiaopay"
	epay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/epay"
	epusdt "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/epusdt"
	okpay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/okpay"
	paypal "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/paypal"
	stripe "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/stripe"
	tokenpay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/tokenpay"
	wechatpay "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/adapters/wechatpay"
	"github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gateway/common"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

func TestCreationBoundaryDoesNotInferOutcomeFromErrorCategory(t *testing.T) {
	for _, cause := range []error{paymentcontract.ErrGatewayConfigInvalid, paymentcontract.ErrGatewayRequestFailed, paymentcontract.ErrGatewaySignatureInvalid, paymentcontract.ErrGatewayAuthFailed, errors.New("EOF")} {
		ctx, classify := common.TrackCreation(context.Background())
		if err := classify(cause); !errors.Is(err, paymentcontract.ErrGatewayNotSubmitted) || !errors.Is(err, cause) {
			t.Fatalf("local: %v", err)
		}
		child, cancel := context.WithCancel(ctx)
		common.MarkCreationDispatched(child)
		cancel()
		if err := classify(cause); errors.Is(err, paymentcontract.ErrGatewayNotSubmitted) || !errors.Is(err, cause) {
			t.Fatalf("dispatched: %v", err)
		}
		if classify(nil) != nil {
			t.Fatal("success classified as failure")
		}
	}
}

func TestAllBuiltInProvidersCertifyLocalConfigErrors(t *testing.T) {
	for _, p := range []paymentcontract.GatewayProvider{alipay.NewAlipayAdapter(), bepusdt.NewBepusdtAdapter(), dujiaopay.NewDujiaoPayAdapter(), epay.NewEpayAdapter(), epusdt.NewEpusdtAdapter(), okpay.NewOkpayAdapter(), paypal.NewPaypalAdapter(), stripe.NewStripeAdapter(), tokenpay.NewTokenpayAdapter(), wechatpay.NewWechatpayAdapter()} {
		t.Run(p.Type(), func(t *testing.T) {
			_, err := p.CreatePayment(context.Background(), jsonmap.JSON{}, paymentcontract.GatewayCreateInput{OrderNo: "local-config-test", ChannelType: "alipay"})
			if !errors.Is(err, paymentcontract.ErrGatewayNotSubmitted) {
				t.Fatalf("local configuration error not classified: %v", err)
			}
		})
	}
}
