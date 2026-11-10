package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"

	"github.com/shopspring/decimal"
)

// manualTimedOut reports deadline expiry for the attempt or any order in its tree.
// Canceled orders require explicit confirmation even when their historical provenance is unknown.
func manualTimedOut(payment *paymentdomain.Payment, order *orderdomain.Order, now time.Time) bool {
	if payment.Status == constants.PaymentStatusExpired || (payment.ExpiredAt != nil && !payment.ExpiredAt.After(now)) {
		return true
	}
	return manualOrderTimedOut(order, now)
}

// manualOrderTimedOut checks order-tree deadlines without changing lifecycle state.
func manualOrderTimedOut(order *orderdomain.Order, now time.Time) bool {
	if order.Status == constants.OrderStatusCanceled || (order.ExpiresAt != nil && !order.ExpiresAt.After(now)) {
		return true
	}
	for index := range order.Children {
		if manualOrderTimedOut(&order.Children[index], now) {
			return true
		}
	}
	return false
}

// manualOrderRecoverable validates unpaid state and durable timeout provenance recursively.
// Historical canceled orders without a reason, coupons and already fulfilled orders fail closed.
func manualOrderRecoverable(order *orderdomain.Order, now time.Time) bool {
	if order == nil || order.PaidAt != nil || order.DeletedAt != nil || order.RefundedAmount.Decimal.IsPositive() || order.Fulfillment != nil {
		return false
	}
	switch order.Status {
	case constants.OrderStatusPendingPayment:
		if order.CanceledAt != nil {
			return false
		}
	case constants.OrderStatusCanceled:
		if order.CancellationReason != "timeout" || order.CanceledAt == nil || order.ExpiresAt == nil || order.ExpiresAt.After(*order.CanceledAt) || order.CanceledAt.After(now) || order.CouponID != nil || order.WalletPaidAmount.Decimal.IsPositive() {
			return false
		}
		for _, item := range order.Items {
			if item.FulfillmentType != constants.FulfillmentTypeManual && item.FulfillmentType != constants.FulfillmentTypeAuto {
				return false
			}
		}
	default:
		return false
	}
	for index := range order.Children {
		child := &order.Children[index]
		if child.Status != order.Status || !manualOrderRecoverable(child, now) {
			return false
		}
	}
	return true
}

// manualAttemptRecoverable permits only active attempts and genuinely expired attempts.
// Superseded, failed, successful and anomalous attempts must never be reactivated.
func manualAttemptRecoverable(payment *paymentdomain.Payment, now time.Time) bool {
	if payment.SupersededAt != nil || payment.ExceptionCode != "" || payment.ChannelID == 0 || payment.PaidAt != nil {
		return false
	}
	switch payment.Status {
	case constants.PaymentStatusPending, constants.PaymentStatusInitiated:
		return true
	case constants.PaymentStatusExpired:
		return payment.ExpiredAt != nil && !payment.ExpiredAt.After(now)
	default:
		return false
	}
}

// rejectOtherManualReceipts checks the locked order tree for previous external receipts.
// confirmingPaymentID may itself be unknown: evidence-based manual recovery must remain possible.
// Other unknown attempts fail closed; wallet contributions remain compatible.
func rejectOtherManualReceipts(tx paymentcontract.Transaction, order *orderdomain.Order, confirmingPaymentID uint) error {
	payments, err := tx.Payments().ListByOrderID(order.ID)
	if err != nil {
		return err
	}
	for _, payment := range payments {
		if payment.ChannelID != 0 && (payment.Status == constants.PaymentStatusSuccess ||
			(payment.ID != confirmingPaymentID && (payment.CreationState == "processing" || payment.CreationState == "unknown"))) {
			return paymentcontract.ErrManualConfirmationConflict
		}
	}
	for index := range order.Children {
		if err := rejectOtherManualReceipts(tx, &order.Children[index], confirmingPaymentID); err != nil {
			return err
		}
	}
	return nil
}

// restoreManualTimeout restores canceled resources and states inside the original payment transaction.
// order must pass provenance and amount validation first; no wallet debit or gateway call occurs.
func restoreManualTimeout(tx paymentcontract.Transaction, order *orderdomain.Order, now time.Time) error {
	if order.Status != constants.OrderStatusCanceled {
		return nil
	}
	if len(order.Children) == 0 {
		if err := orderapp.RestoreCanceledOrderResources(tx, order, now); err != nil {
			return err
		}
	}
	for index := range order.Children {
		if err := restoreManualTimeout(tx, &order.Children[index], now); err != nil {
			return err
		}
	}
	if err := tx.Orders().UpdateStatus(order.ID, constants.OrderStatusPendingPayment, map[string]interface{}{"canceled_at": nil, "updated_at": now}); err != nil {
		return err
	}
	order.Status, order.CanceledAt = constants.OrderStatusPendingPayment, nil
	return nil
}

var manualRequestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{7,79}$`)
var manualAmountPattern = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,2})?$`)

// normalizeManualInput 校验外部实收边界，返回规范化输入及幂等摘要；不记录敏感正文到日志。
func normalizeManualInput(input paymentcontract.ManualConfirmationInput, now time.Time) (paymentcontract.ManualConfirmationInput, string, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.TransactionID = strings.TrimSpace(input.TransactionID)
	input.EvidenceReference = strings.TrimSpace(input.EvidenceReference)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Amount = strings.TrimSpace(input.Amount)
	input.PaidAt = input.PaidAt.UTC()
	if input.AdminID == 0 || !manualRequestPattern.MatchString(input.RequestID) || !manualAmountPattern.MatchString(input.Amount) || input.ExpectedStatus == "" || len(input.Currency) < 3 || len(input.Currency) > 12 || input.PaidAt.IsZero() || input.PaidAt.After(now) {
		return input, "", paymentcontract.ErrManualConfirmationInvalid
	}
	amount, err := decimal.NewFromString(input.Amount)
	if err != nil || !amount.IsPositive() {
		return input, "", paymentcontract.ErrManualConfirmationInvalid
	}
	input.Amount = amount.StringFixed(2)
	if input.TransactionID == "" && input.EvidenceReference == "" {
		return input, "", paymentcontract.ErrManualConfirmationInvalid
	}
	for _, field := range []struct {
		value string
		max   int
	}{{input.TransactionID, 160}, {input.EvidenceReference, 240}, {input.Reason, 1000}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return input, "", paymentcontract.ErrManualConfirmationInvalid
		}
	}
	if input.Reason == "" {
		return input, "", paymentcontract.ErrManualConfirmationInvalid
	}
	canonical := input
	canonical.AdminID = 0
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return input, "", err
	}
	digest := sha256.Sum256(encoded)
	return input, hex.EncodeToString(digest[:]), nil
}

// validateManualPayment 在锁内复核精确支付尝试、外部金额及可信付款时刻，错误时不写状态。
func validateManualPayment(payment *paymentdomain.Payment, order *orderdomain.Order, input paymentcontract.ManualConfirmationInput, now time.Time) error {
	if order.ParentID != nil || !manualOrderRecoverable(order, now) || payment.Status != input.ExpectedStatus || !manualAttemptRecoverable(payment, now) || (manualTimedOut(payment, order, now) && !input.ConfirmTimeout) {
		return paymentcontract.ErrManualConfirmationConflict
	}
	if input.PaidAt.Before(payment.CreatedAt) || input.PaidAt.Before(order.CreatedAt) {
		return paymentcontract.ErrManualConfirmationInvalid
	}
	amount, err := decimal.NewFromString(input.Amount)
	if err != nil || !amount.Equal(payment.Amount.Decimal) {
		return ErrPaymentAmountMismatch
	}
	if input.Currency != strings.ToUpper(payment.Currency) {
		return ErrPaymentCurrencyMismatch
	}
	if payment.ProviderRef != "" && input.TransactionID != payment.ProviderRef {
		return paymentcontract.ErrManualConfirmationConflict
	}
	required := normalizeOrderAmount(order.TotalAmount.Decimal.Sub(order.WalletPaidAmount.Decimal))
	if !required.IsPositive() || !paymentCoveredOrderAmount(payment, amount).Equal(required) {
		return ErrPaymentAmountMismatch
	}
	return nil
}

// claimManualEvidence 占用渠道内交易号/凭据唯一槽位；输入必须已校验并处于付款根事务锁内。
func claimManualEvidence(repo paymentcontract.Store, store paymentcontract.ManualConfirmationStore, payment *paymentdomain.Payment, input paymentcontract.ManualConfirmationInput) error {
	if input.TransactionID != "" {
		existing, err := repo.GetLatestByProviderRef(input.TransactionID)
		if err != nil {
			return err
		}
		if existing != nil && existing.ID != payment.ID && existing.ChannelID == payment.ChannelID {
			return paymentcontract.ErrManualConfirmationConflict
		}
		if err := store.ClaimEvidence(payment.ChannelID, "transaction:"+input.TransactionID, payment.ID); err != nil {
			return err
		}
	}
	if input.EvidenceReference != "" {
		return store.ClaimEvidence(payment.ChannelID, "evidence:"+input.EvidenceReference, payment.ID)
	}
	return nil
}

// calculatePaymentAmounts 按创建时配置计算用户实付金额、渠道手续费及不可变策略快照。
func calculatePaymentAmounts(baseAmount, feeRate, fixedFee decimal.Decimal, customerFeeEnabled bool) (paymentAmount, feeAmount decimal.Decimal, feePolicy string) {
	baseAmount = baseAmount.Round(2)
	feeAmount = fixedFee.Round(2)
	if feeRate.GreaterThan(decimal.Zero) {
		feeAmount = feeAmount.Add(baseAmount.Mul(feeRate).Div(decimal.NewFromInt(100))).Round(2)
	}
	if feeAmount.IsZero() {
		return baseAmount, feeAmount, constants.PaymentFeePolicyNone
	}
	if customerFeeEnabled {
		return baseAmount.Add(feeAmount).Round(2), feeAmount, constants.PaymentFeePolicyCustomerSurcharge
	}
	return baseAmount, feeAmount, constants.PaymentFeePolicyMerchantAbsorbed
}

// paymentExchangeRate 读取创建支付时存下的渠道换汇汇率快照
// （ProviderPayload["exchange_rate"]）。存在即说明这笔支付发生过法币换汇（渠道配置了
// target_currency + exchange_rate，见各 adapter 的 cfg.NeedsCurrencyConversion 分支），
// applyProviderPayment 回写后 payment.Amount/Currency 已经不是订单原始币种。返回零值
// 和 false 表示未换汇或快照缺失/无效。
func paymentExchangeRate(payment *paymentdomain.Payment) (decimal.Decimal, bool) {
	if payment == nil || payment.ProviderPayload == nil {
		return decimal.Zero, false
	}
	rateRaw, ok := payment.ProviderPayload["exchange_rate"]
	if !ok {
		return decimal.Zero, false
	}
	rateStr := strings.TrimSpace(fmt.Sprint(rateRaw))
	if rateStr == "" {
		return decimal.Zero, false
	}
	rate, err := decimal.NewFromString(rateStr)
	if err != nil || !rate.IsPositive() {
		return decimal.Zero, false
	}
	return rate, true
}

// paymentCoveredOrderAmount 返回支付覆盖的订单币种金额。
// callbackAmount 必须与建单外币金额精确相等，才可采用原价快照；不扩大金额容差。
// 缺失快照的旧记录保留汇率反算，手续费仍按创建时策略从原币金额中扣除。
func paymentCoveredOrderAmount(payment *paymentdomain.Payment, callbackAmount decimal.Decimal) decimal.Decimal {
	if payment == nil {
		return decimal.Zero
	}
	covered := payment.Amount.Decimal
	if !callbackAmount.IsZero() {
		covered = callbackAmount
	}
	original := decimal.Zero
	if payment.ProviderPayload != nil && callbackAmount.Equal(payment.Amount.Decimal) {
		if raw, ok := payment.ProviderPayload["original_amount"]; ok {
			if amount, err := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(raw))); err == nil && amount.IsPositive() {
				original = amount
			}
		}
	}
	if original.IsPositive() {
		covered = original
	} else if rate, ok := paymentExchangeRate(payment); ok {
		covered = covered.Div(rate)
	}
	fee := payment.FeeAmount.Decimal
	switch payment.FeePolicy {
	case constants.PaymentFeePolicyCustomerSurcharge, constants.PaymentFeePolicyLegacyCustomerSurcharge:
		covered = covered.Sub(fee)
	case "":
		if fee.IsPositive() {
			covered = covered.Sub(fee)
		}
	}
	return normalizeOrderAmount(covered)
}

// normalizeOrderAmount 归一化金额精度与下限。
func normalizeOrderAmount(amount decimal.Decimal) decimal.Decimal {
	normalized := amount.Round(2)
	if normalized.LessThan(decimal.Zero) {
		return decimal.Zero
	}
	return normalized
}

func pickFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shouldMarkFulfilling(order *orderdomain.Order) bool {
	if order == nil {
		return false
	}
	if len(order.Items) == 0 {
		return false
	}
	for _, item := range order.Items {
		fulfillmentType := strings.TrimSpace(item.FulfillmentType)
		if fulfillmentType == "" || fulfillmentType == constants.FulfillmentTypeManual || fulfillmentType == constants.FulfillmentTypeUpstream {
			return true
		}
	}
	return false
}

func shouldUseCNYPaymentCurrency(channel *paymentdomain.PaymentChannel) bool {
	if channel == nil {
		return false
	}
	providerType := strings.ToLower(strings.TrimSpace(channel.ProviderType))
	if providerType != constants.PaymentProviderOfficial {
		return false
	}
	channelType := strings.ToLower(strings.TrimSpace(channel.ChannelType))
	return channelType == constants.PaymentChannelTypeWechat || channelType == constants.PaymentChannelTypeAlipay
}

func validatePaymentAmountForChannel(amount decimal.Decimal, channel *paymentdomain.PaymentChannel) error {
	if channel == nil {
		return nil
	}
	amountOverflow20_2 := decimal.NewFromInt(1000000000000000000)
	if amount.GreaterThanOrEqual(amountOverflow20_2) {
		return ErrPaymentAmountTooLarge
	}
	minAmount := channel.MinAmount.Decimal
	maxAmount := channel.MaxAmount.Decimal
	if minAmount.GreaterThan(decimal.Zero) && amount.LessThan(minAmount) {
		return ErrPaymentAmountTooSmall
	}
	if maxAmount.GreaterThan(decimal.Zero) && amount.GreaterThan(maxAmount) {
		return ErrPaymentAmountTooLarge
	}
	return nil
}

func validatePaymentCurrencyForChannel(currency string, channel *paymentdomain.PaymentChannel) error {
	normalized := strings.ToUpper(strings.TrimSpace(currency))
	if !settingsapp.IsCurrencyCode(normalized) {
		return ErrPaymentCurrencyMismatch
	}
	if shouldUseCNYPaymentCurrency(channel) && normalized != constants.SiteCurrencyDefault {
		return ErrPaymentCurrencyMismatch
	}
	return nil
}

func (s *PaymentService) resolveExpireMinutes() int {
	return orderapp.ResolvePaymentExpireMinutes(s.settingService, s.expireMinutes)
}

func normalizePaymentStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func isPaymentStatusValid(status string) bool {
	switch status {
	case constants.PaymentStatusInitiated, constants.PaymentStatusPending, constants.PaymentStatusSuccess, constants.PaymentStatusFailed, constants.PaymentStatusExpired:
		return true
	default:
		return false
	}
}

func shouldAutoFulfill(order *orderdomain.Order) bool {
	if order == nil || len(order.Items) == 0 {
		return false
	}
	for _, item := range order.Items {
		if strings.TrimSpace(item.FulfillmentType) != constants.FulfillmentTypeAuto {
			return false
		}
	}
	return true
}

// isOrderFullyAutoFulfill 判断订单是否完全为自动交付。
// 父订单：所有子订单均满足 shouldAutoFulfill；单订单：自身满足 shouldAutoFulfill。
// 用于支付成功时跳过"已支付"邮件——自动交付会紧接着发送含卡密内容的"已完成"邮件，避免重复打扰。
func isOrderFullyAutoFulfill(order *orderdomain.Order) bool {
	if order == nil {
		return false
	}
	if len(order.Children) > 0 {
		for i := range order.Children {
			if !shouldAutoFulfill(&order.Children[i]) {
				return false
			}
		}
		return true
	}
	return shouldAutoFulfill(order)
}

func buildOrderSubject(order *orderdomain.Order) string {
	if order == nil {
		return ""
	}
	return strings.TrimSpace(order.OrderNo)
}
