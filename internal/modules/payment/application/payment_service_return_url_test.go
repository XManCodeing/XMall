package application

import (
	"context"
	"errors"
	"github.com/XManCodeing/XMall/internal/constants"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"testing"
	"time"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	resellercontract "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

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

// TestBuildPaymentReturnQueryForOrder verifies the order return parameters using a guest order.
func TestBuildPaymentReturnQueryForOrder(t *testing.T) {
	order := &orderdomain.Order{OrderNo: "DJ202603230001", UserID: 0}
	params := buildPaymentReturnQuery(CreatePaymentInput{}, order, "epay_return", "SESSION-ORDER-1")
	for key, expected := range map[string]string{"biz_type": "order", "order_no": "DJ202603230001", "guest": "1", "epay_return": "1", "session_id": "SESSION-ORDER-1"} {
		if got := params[key]; got != expected {
			t.Fatalf("%s want %q got %q", key, expected, got)
		}
	}
	if _, exists := params["recharge_no"]; exists {
		t.Fatalf("recharge_no should be absent: %#v", params)
	}
}

// TestBuildPaymentReturnQueryForRecharge verifies recharge return parameters exclude order/guest fields.
func TestBuildPaymentReturnQueryForRecharge(t *testing.T) {
	order := &orderdomain.Order{OrderNo: "DJRECHARGE0001", UserID: 100}
	input := CreatePaymentInput{ReturnBizType: "recharge", ReturnBusinessNo: "DJRECHARGE0001"}
	params := buildPaymentReturnQuery(input, order, "stripe_return", "SESSION-RECHARGE-1")
	for key, expected := range map[string]string{"biz_type": "recharge", "recharge_no": "DJRECHARGE0001", "stripe_return": "1", "session_id": "SESSION-RECHARGE-1"} {
		if got := params[key]; got != expected {
			t.Fatalf("%s want %q got %q", key, expected, got)
		}
	}
	for _, key := range []string{"order_no", "guest"} {
		if _, exists := params[key]; exists {
			t.Fatalf("%s should be absent: %#v", key, params)
		}
	}
}

func TestResolveTenantReturnURLMainTenantKeepsConfigFallback(t *testing.T) {
	ctx := resellercontract.WithTenantContext(context.Background(), resellercontract.MainTenantContext("main.example.com"))
	channel := &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{"return_url": "https://main.example.com/pay"}}

	if got := resolveTenantReturnURL(ctx, "https", channel); got != "" {
		t.Fatalf("main tenant want empty got %q", got)
	}
	if got := resolveTenantReturnURL(context.Background(), "https", channel); got != "" {
		t.Fatalf("no tenant want empty got %q", got)
	}
	//lint:ignore SA1012 This assertion deliberately verifies the nil-context fallback.
	if got := resolveTenantReturnURL(nil, "https", channel); got != "" {
		t.Fatalf("nil ctx want empty got %q", got)
	}
}

func TestResolveTenantReturnURLResellerTenantUsesRequestHost(t *testing.T) {
	ctx := resellercontract.WithTenantContext(context.Background(), resellercontract.ResellerTenantContext("shop.example.com", 7, 3, "primary.example.com"))
	channel := &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{"return_url": "https://main.example.com/pay"}}

	if got := resolveTenantReturnURL(ctx, "https", channel); got != "https://shop.example.com/pay" {
		t.Fatalf("want https://shop.example.com/pay got %q", got)
	}
}

func TestResolveTenantReturnURLResellerTenantFallsBackToPrimaryDomain(t *testing.T) {
	ctx := resellercontract.WithTenantContext(context.Background(), resellercontract.ResellerTenantContext("", 7, 3, "primary.example.com"))

	if got := resolveTenantReturnURL(ctx, "https", nil); got != "https://primary.example.com/pay" {
		t.Fatalf("want https://primary.example.com/pay got %q", got)
	}
}

func TestResolveTenantReturnURLSchemeHandling(t *testing.T) {
	ctx := resellercontract.WithTenantContext(context.Background(), resellercontract.ResellerTenantContext("shop.example.com", 7, 3, "primary.example.com"))

	if got := resolveTenantReturnURL(ctx, "http", nil); got != "http://shop.example.com/pay" {
		t.Fatalf("http scheme want http://shop.example.com/pay got %q", got)
	}
	// 空值 / 非法值默认 https
	if got := resolveTenantReturnURL(ctx, "", nil); got != "https://shop.example.com/pay" {
		t.Fatalf("empty scheme want https got %q", got)
	}
	if got := resolveTenantReturnURL(ctx, "ftp", nil); got != "https://shop.example.com/pay" {
		t.Fatalf("invalid scheme want https got %q", got)
	}
}

func TestResolveTenantReturnURLUnavailableTenantKeepsConfigFallback(t *testing.T) {
	ctx := resellercontract.WithTenantContext(context.Background(), resellercontract.UnavailableTenantContext("gone.example.com", "disabled"))

	if got := resolveTenantReturnURL(ctx, "https", nil); got != "" {
		t.Fatalf("unavailable tenant want empty got %q", got)
	}
}

func TestTenantReturnPathReusesConfiguredPath(t *testing.T) {
	channel := &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{"return_url": "https://main.example.com/checkout/result?from=gateway"}}
	if got := tenantReturnPath(channel); got != "/checkout/result?from=gateway" {
		t.Fatalf("want /checkout/result?from=gateway got %q", got)
	}

	// stripe/dujiaopay 使用 success_url
	channel = &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{"success_url": "https://main.example.com/pay/success"}}
	if got := tenantReturnPath(channel); got != "/pay/success" {
		t.Fatalf("want /pay/success got %q", got)
	}

	// return_url 优先于 success_url
	channel = &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{
		"return_url":  "https://main.example.com/pay/return",
		"success_url": "https://main.example.com/pay/success",
	}}
	if got := tenantReturnPath(channel); got != "/pay/return" {
		t.Fatalf("want /pay/return got %q", got)
	}
}

func TestTenantReturnPathDefaults(t *testing.T) {
	if got := tenantReturnPath(nil); got != "/pay" {
		t.Fatalf("nil channel want /pay got %q", got)
	}
	if got := tenantReturnPath(&paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{}}); got != "/pay" {
		t.Fatalf("empty config want /pay got %q", got)
	}
	// 配置只有域名没有路径时也回落 /pay
	channel := &paymentdomain.PaymentChannel{ConfigJSON: jsonmap.JSON{"return_url": "https://main.example.com/"}}
	if got := tenantReturnPath(channel); got != "/pay" {
		t.Fatalf("root path want /pay got %q", got)
	}
}
