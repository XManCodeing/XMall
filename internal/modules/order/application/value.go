package application

import (
	"context"
	"errors"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	externalidentity "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/shared/serial"
	"log/slog"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
)

// SetMembershipPreflight 在启动期注入事务外补查，不更改销售门控；nil 接收者用于隔离装配。
func (s *OrderService) SetMembershipPreflight(preflight membership.CapabilityPreflight) {
	if s != nil {
		s.membershipPreflight = preflight
	}
}

// membershipCheckoutError 识别订单事务内可直接返回的稳定会员错误，并记录不含身份的失败类别。
func membershipCheckoutError(err error) bool {
	for _, expected := range []error{membership.ErrInvalid, membership.ErrNotFound, membership.ErrConflict, membership.ErrCapability, membership.ErrSalesClosed, membership.ErrUntrustedIdentity, membership.ErrTransactionRequired, membership.ErrOverlapUnavailable, membership.ErrPermanentFullyCovered, membership.ErrPermanentPartiallyCovered, externalidentity.ErrTelegramIdentityChanged} {
		if errors.Is(err, expected) {
			slog.Warn("membership checkout rejected", "reason", expected.Error())
			return true
		}
	}
	return false
}

// generateOrderNo 无输入，返回 XMALL 前缀、秒级时间戳及六位随机数字组成的新订单号。
func generateOrderNo() string { return serial.Generate("XMALL") }

// guestPasswordMinLength 游客订单密码最小长度。
const guestPasswordMinLength = 6

// validateGuestPassword 校验输入密码的存在性和最短长度，返回稳定业务错误。
func validateGuestPassword(password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return ErrGuestPasswordRequired
	}
	if len([]rune(password)) < guestPasswordMinLength {
		return ErrGuestPasswordTooShort
	}
	return nil
}

func resolveSiteCurrency(settingService *settingsapp.Service) string {
	if settingService == nil {
		return constants.SiteCurrencyDefault
	}
	currency, err := settingService.GetSiteCurrency(constants.SiteCurrencyDefault)
	if err != nil {
		return constants.SiteCurrencyDefault
	}
	return currency
}

// ResolvePaymentExpireMinutes 解析订单与支付共同使用的付款超时时间。
func ResolvePaymentExpireMinutes(settingService *settingsapp.Service, defaultMinutes int) int {
	if defaultMinutes <= 0 {
		defaultMinutes = 15
	}
	if settingService == nil {
		return defaultMinutes
	}
	minutes, err := settingService.GetOrderPaymentExpireMinutes(defaultMinutes)
	if err != nil || minutes <= 0 {
		return defaultMinutes
	}
	return minutes
}

func resolveProductOrderSKU(productSKURepo productcontract.SKURepository, product *productdomain.Product, rawSKUID uint) (*productdomain.ProductSKU, error) {
	if product == nil || product.ID == 0 {
		return nil, ErrProductNotAvailable
	}
	if productSKURepo == nil {
		return nil, ErrProductSKUInvalid
	}
	if rawSKUID > 0 {
		sku, err := productSKURepo.GetByID(rawSKUID)
		if err != nil {
			return nil, err
		}
		if sku == nil || sku.ProductID != product.ID || !sku.IsActive {
			return nil, ErrProductSKUInvalid
		}
		return sku, nil
	}

	activeSKUs, err := productSKURepo.ListByProduct(product.ID, true)
	if err != nil {
		return nil, err
	}
	if len(activeSKUs) == 1 {
		return &activeSKUs[0], nil
	}
	if len(activeSKUs) == 0 {
		return nil, ErrProductSKUInvalid
	}
	return nil, ErrProductSKURequired
}

// prepareMembership 复用订单事务端口进行服务端预检；仅在公共销售门控之后调用。
// 旧隔离 fixture 未启用门控时仍保持其无会员 schema 的普通交易测试行为。
func (s *OrderService) prepareMembership(input orderCreateParams, result *orderBuildResult) (*membership.CheckoutPlan, error) {
	if !s.enforcePublicSale {
		return nil, nil
	}
	lines := make([]membership.CheckoutLine, 0, len(result.Plans))
	for _, plan := range result.Plans {
		lines = append(lines, membership.CheckoutLine{ProductID: plan.Item.ProductID, SKUID: plan.Item.SKUID, Quantity: plan.Item.Quantity})
	}
	var proof *membership.CapabilityProof
	if s.membershipPreflight != nil {
		var err error
		proof, err = s.membershipPreflight.Checkout(context.Background(), lines)
		if err != nil {
			return nil, err
		}
	}
	var plan *membership.CheckoutPlan
	err := s.orderStore.WithinTransaction(func(tx ordercontract.Transaction) error {
		provider, ok := tx.(membership.CheckoutProvider)
		if !ok {
			return membership.ErrTransactionRequired
		}
		checkout, err := provider.MembershipCheckout()
		if err != nil {
			return err
		}
		plan, err = checkout.Prepare(context.Background(), input.UserID, lines, time.Now())
		if err != nil {
			return err
		}
		return proof.Validate(plan)
	})
	return plan, err
}

// membershipInTransaction 仅在已识别会员计划时取得同一 tx 端口并再次校验；普通商品零身份/群查询。
func membershipInTransaction(tx ordercontract.Transaction, plan *membership.CheckoutPlan) (membership.Checkout, error) {
	if plan == nil {
		return nil, nil
	}
	provider, ok := tx.(membership.CheckoutProvider)
	if !ok {
		return nil, membership.ErrTransactionRequired
	}
	checkout, err := provider.MembershipCheckout()
	if err != nil {
		return nil, err
	}
	if err := checkout.Revalidate(context.Background(), plan, time.Now()); err != nil {
		return nil, err
	}
	return checkout, nil
}

// saveMembershipItems 把全部实际子单项交给同事务快照端口，父单汇总不重复写入。
func saveMembershipItems(checkout membership.Checkout, plan *membership.CheckoutPlan, result *orderBuildResult) error {
	if checkout == nil {
		return nil
	}
	items := make([]orderdomain.OrderItem, 0, len(result.Plans))
	for _, child := range result.Plans {
		items = append(items, child.Item)
	}
	return checkout.SaveItems(context.Background(), plan, items, time.Now())
}
