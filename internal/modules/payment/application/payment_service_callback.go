package application

import (
	"errors"
	"fmt"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	"strings"
	"time"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/shopspring/decimal"
)

// GetManualConfirmation 获取安全财务复核数据，不触发网关查询或泄露支付载荷。
func (s *PaymentService) GetManualConfirmation(paymentID uint) (*paymentcontract.ManualConfirmationReview, error) {
	payment, err := s.paymentRepo.GetByID(paymentID)
	if err != nil {
		return nil, err
	}
	if payment == nil {
		return nil, ErrPaymentNotFound
	}
	if payment.OrderID == 0 {
		return nil, paymentcontract.ErrManualConfirmationInvalid
	}
	order, err := s.orderRepo.GetByID(payment.OrderID)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, paymentcontract.ErrManualConfirmationConflict
	}
	store, ok := s.paymentRepo.(paymentcontract.ManualConfirmationStore)
	if !ok {
		return nil, paymentcontract.ErrManualConfirmationUnavailable
	}
	record, err := store.FindManualConfirmation(payment.ID)
	if err != nil {
		return nil, err
	}
	return manualReview(payment, order, record), nil
}

// manualReview 仅投影允许财务查看的字段；CanConfirm 是提示，写入时再次锁内复核。
func manualReview(payment *paymentdomain.Payment, order *orderdomain.Order, record *paymentdomain.ManualConfirmation) *paymentcontract.ManualConfirmationReview {
	now := time.Now()
	canConfirm := order.ParentID == nil && manualOrderRecoverable(order, now) && manualAttemptRecoverable(payment, now)
	// Wallet cancellation refunds reset the stored wallet contribution; an old partial attempt cannot cover the restored order.
	canConfirm = canConfirm && paymentCoveredOrderAmount(payment, payment.Amount.Decimal).Equal(normalizeOrderAmount(order.TotalAmount.Decimal.Sub(order.WalletPaidAmount.Decimal)))
	blocked := ""
	if !canConfirm {
		blocked = "state_not_confirmable"
	}
	return &paymentcontract.ManualConfirmationReview{PaymentID: payment.ID, OrderID: order.ID, OrderNo: order.OrderNo, ChannelID: payment.ChannelID, ProviderType: payment.ProviderType, PaymentStatus: payment.Status, OrderStatus: order.Status, Amount: payment.Amount.String(), Currency: payment.Currency, OrderAmount: order.TotalAmount.String(), WalletPaidAmount: order.WalletPaidAmount.String(), PaidAt: payment.PaidAt, CreatedAt: payment.CreatedAt, ExpiresAt: payment.ExpiredAt, TransactionID: payment.ProviderRef, ExceptionCode: payment.ExceptionCode, RequiresTimeoutConfirmation: manualTimedOut(payment, order, now), CanConfirm: canConfirm, BlockedReason: blocked, Confirmation: record}
}

// ConfirmManually 将人工复核事实与审计提交到原支付事务，提交后恢复既有履约队列。
// paymentID 精确绑定尝试，input.AdminID 由认证入口注入；错误时全部回滚。
func (s *PaymentService) ConfirmManually(paymentID uint, input paymentcontract.ManualConfirmationInput) (*paymentcontract.ManualConfirmationReview, error) {
	now := time.Now()
	input, digest, err := normalizeManualInput(input, now)
	if err != nil {
		return nil, err
	}
	initial, err := s.paymentRepo.GetByID(paymentID)
	if err != nil {
		return nil, err
	}
	if initial == nil {
		return nil, ErrPaymentNotFound
	}
	if initial.OrderID == 0 {
		return nil, paymentcontract.ErrManualConfirmationInvalid
	}
	var result *paymentcontract.ManualConfirmationReview
	var completedPayment *paymentdomain.Payment
	var completedOrder *orderdomain.Order
	err = s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		repo := tx.Payments()
		if locker, ok := repo.(paymentcontract.CreationLocker); ok {
			if err := locker.LockCreation(initial.OrderID); err != nil {
				return err
			}
		}
		order, err := tx.Orders().GetByIDForUpdateWithChildren(initial.OrderID)
		if err != nil {
			return err
		}
		payment, err := repo.GetByIDForUpdate(paymentID)
		if err != nil {
			return err
		}
		if order == nil || payment == nil || payment.OrderID != order.ID {
			return paymentcontract.ErrManualConfirmationConflict
		}
		// Re-evaluate deadlines after waiting for the root lock; a pre-lock timestamp can bypass timeout consent.
		now = time.Now()
		result, err = s.confirmLockedManual(tx, payment, order, input, digest, now)
		completedPayment, completedOrder = payment, order
		return err
	})
	if err != nil {
		paymentLogger("payment_id", paymentID, "admin_id", input.AdminID).Warnw("payment_manual_confirmation_failed", "error", err)
		return nil, err
	}
	// 已成功的重放只补可靠 outbox；常规调度还包含非幂等累计消费，不能重复调用。
	// 提交后丢失的履约由已有 RecoverPaidFulfillment 扫描恢复。
	if !result.Replayed {
		s.enqueueOrderPaidAsync(completedOrder, completedPayment, paymentLogger("payment_id", paymentID))
	}
	paymentLogger("payment_id", paymentID, "admin_id", input.AdminID).Infow("payment_manual_confirmation_committed", "replayed", result.Replayed)
	return result, nil
}

// confirmLockedManual 在付款根和尝试锁下执行幂等核验，持久事实或恢复既有 outbox。
func (s *PaymentService) confirmLockedManual(tx paymentcontract.Transaction, payment *paymentdomain.Payment, order *orderdomain.Order, input paymentcontract.ManualConfirmationInput, digest string, now time.Time) (*paymentcontract.ManualConfirmationReview, error) {
	repo := tx.Payments()
	store, ok := repo.(paymentcontract.ManualConfirmationStore)
	if !ok {
		return nil, paymentcontract.ErrManualConfirmationUnavailable
	}
	existing, err := store.FindManualRequest(input.RequestID)
	if err != nil {
		return nil, err
	}
	if existing != nil && (existing.PaymentID != payment.ID || existing.RequestHash != digest) {
		return nil, paymentcontract.ErrManualConfirmationConflict
	}
	record, err := store.FindManualConfirmation(payment.ID)
	if err != nil {
		return nil, err
	}
	if payment.Status == constants.PaymentStatusSuccess {
		if manualTimedOut(payment, order, now) && !input.ConfirmTimeout {
			return nil, paymentcontract.ErrManualConfirmationConflict
		}
		if err := validateManualReplay(payment, order, input, record, existing); err != nil {
			return nil, err
		}
		if err := claimManualEvidence(repo, store, payment, input); err != nil {
			return nil, err
		}
		if err := s.appendMembershipPaid(tx, order.ID, payment.ID); err != nil {
			return nil, err
		}
		result := manualReview(payment, order, record)
		result.Replayed = true
		return result, nil
	}
	if record != nil || existing != nil {
		return nil, paymentcontract.ErrManualConfirmationConflict
	}
	if err := validateManualPayment(payment, order, input, now); err != nil {
		return nil, err
	}
	if err := rejectOtherManualReceipts(tx, order, payment.ID); err != nil {
		return nil, err
	}
	// This endpoint records money already received; do not run active-payment capability checks or initiate a debit.
	if err := s.checkReceiptSale(tx, order); err != nil {
		return nil, err
	}
	if err := claimManualEvidence(repo, store, payment, input); err != nil {
		return nil, err
	}
	record, err = s.persistManualConfirmation(tx, store, payment, order, input, digest, now)
	if err != nil {
		return nil, err
	}
	return manualReview(payment, order, record), nil
}

// validateManualReplay 仅恢复已成功事实，拒绝改写原证据和退款后的再次推进。
func validateManualReplay(payment *paymentdomain.Payment, order *orderdomain.Order, input paymentcontract.ManualConfirmationInput, record, request *paymentdomain.ManualConfirmation) error {
	amount, err := decimal.NewFromString(input.Amount)
	if err != nil || !amount.Equal(payment.Amount.Decimal) || input.Currency != payment.Currency || payment.ExceptionCode != "" || order.PaidAt == nil || order.RefundedAmount.Decimal.IsPositive() {
		return paymentcontract.ErrManualConfirmationConflict
	}
	if order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling && order.Status != constants.OrderStatusCompleted {
		return paymentcontract.ErrManualConfirmationConflict
	}
	if request != nil {
		return nil
	}
	if record != nil {
		if record.TransactionID != input.TransactionID || record.EvidenceReference != input.EvidenceReference || !record.PaidAt.Equal(input.PaidAt) {
			return paymentcontract.ErrManualConfirmationConflict
		}
		return nil
	}
	if input.TransactionID == "" || payment.ProviderRef != input.TransactionID || payment.PaidAt == nil || !payment.PaidAt.Equal(input.PaidAt) {
		return paymentcontract.ErrManualConfirmationConflict
	}
	return nil
}

// persistManualConfirmation 复用支付成功与订单完成函数；审计、库存、会员 outbox 在同一事务提交。
func (s *PaymentService) persistManualConfirmation(tx paymentcontract.Transaction, store paymentcontract.ManualConfirmationStore, payment *paymentdomain.Payment, order *orderdomain.Order, input paymentcontract.ManualConfirmationInput, digest string, now time.Time) (*paymentdomain.ManualConfirmation, error) {
	record := &paymentdomain.ManualConfirmation{PaymentID: payment.ID, OrderID: order.ID, AdminID: input.AdminID, RequestID: input.RequestID, RequestHash: digest, Amount: input.Amount, Currency: input.Currency, TransactionID: input.TransactionID, EvidenceReference: input.EvidenceReference, Reason: input.Reason, PreviousPaymentStatus: payment.Status, PreviousOrderStatus: order.Status, PaidAt: input.PaidAt, CreatedAt: now}
	amount, err := decimal.NewFromString(input.Amount)
	if err != nil {
		return nil, err
	}
	if err := restoreManualTimeout(tx, order, now); err != nil {
		return nil, err
	}
	callbackAt := payment.CallbackAt
	facts := PaymentCallbackInput{Amount: money.FromDecimal(amount), Currency: input.Currency, ProviderRef: input.TransactionID, PaidAt: &input.PaidAt}
	outcome, err := s.persistCallbackPaymentFacts(tx, payment, order, constants.PaymentStatusSuccess, facts, now)
	if err != nil {
		return nil, err
	}
	if !outcome.canFulfill {
		return nil, paymentcontract.ErrManualConfirmationConflict
	}
	// 人工审核不是网关回调，绝不写伪造 ProviderPayload/CallbackAt。
	payment.CallbackAt = callbackAt
	if err := tx.Payments().Update(payment); err != nil {
		return nil, err
	}
	if _, err := tx.Payments().ExpirePendingByOrderIDs([]uint{order.ID}, now); err != nil {
		return nil, err
	}
	if err := s.completePaidOrder(tx, order, payment, now); err != nil {
		return nil, err
	}
	record.PaymentStatus, record.OrderStatus = payment.Status, order.Status
	if err := store.CreateManualConfirmation(record); err != nil {
		return nil, err
	}
	return record, nil
}

// PaymentCallbackInput 支付回调输入
type PaymentCallbackInput struct {
	PaymentID   uint
	OrderNo     string
	ChannelID   uint
	Status      string
	ProviderRef string
	Amount      money.Amount
	Currency    string
	PaidAt      *time.Time
	Payload     jsonmap.JSON

	// verifiedLegacyDujiaoPayCurrency 只能由已验签的 DujiaoPay webhook 入口设置。
	// 它允许升级前创建且没有法币快照标记的在途支付采纳网关签名币种；
	// 普通调用方无法开启该兼容分支。
	verifiedLegacyDujiaoPayCurrency string
}

func (s *PaymentService) HandleCallback(input PaymentCallbackInput) (*paymentdomain.Payment, error) {
	if input.PaymentID == 0 {
		return nil, ErrPaymentInvalid
	}
	status := normalizePaymentStatus(input.Status)
	if !isPaymentStatusValid(status) {
		return nil, ErrPaymentStatusInvalid
	}

	log := paymentLogger(
		"payment_id", input.PaymentID,
		"target_status", status,
		"callback_channel_id", input.ChannelID,
		"callback_order_no", strings.TrimSpace(input.OrderNo),
		"callback_provider_ref", strings.TrimSpace(input.ProviderRef),
		"callback_currency", strings.ToUpper(strings.TrimSpace(input.Currency)),
		"callback_amount", input.Amount.String(),
	)
	log.Infow("payment_callback_received")

	payment, err := s.paymentRepo.GetByID(input.PaymentID)
	if err != nil {
		log.Errorw("payment_callback_payment_fetch_failed", "error", err)
		return nil, ErrPaymentUpdateFailed
	}
	if payment == nil {
		log.Warnw("payment_callback_payment_not_found")
		return nil, ErrPaymentNotFound
	}
	if payment.OrderID == 0 {
		log.Infow("payment_callback_wallet_recharge_flow")
		return s.handleWalletRechargeCallback(payment, status, input)
	}

	order, err := s.orderRepo.GetByID(payment.OrderID)
	if err != nil {
		log.Errorw("payment_callback_order_fetch_failed", "order_id", payment.OrderID, "error", err)
		return nil, orderapp.ErrOrderFetchFailed
	}
	if order == nil {
		log.Warnw("payment_callback_order_not_found", "order_id", payment.OrderID)
		return nil, orderapp.ErrOrderNotFound
	}

	if input.ChannelID != 0 && input.ChannelID != payment.ChannelID {
		log.Warnw("payment_callback_channel_mismatch",
			"stored_channel_id", payment.ChannelID,
			"callback_channel_id", input.ChannelID,
		)
		return nil, ErrPaymentInvalid
	}
	if !matchesBusinessOrderNo(input.OrderNo, order.OrderNo, payment) {
		log.Warnw("payment_callback_order_no_mismatch",
			"stored_order_no", order.OrderNo,
			"stored_gateway_order_no", payment.GatewayOrderNo,
			"callback_order_no", input.OrderNo,
		)
		return nil, ErrPaymentInvalid
	}
	if err := validateCallbackPaymentFacts(payment, order.OrderNo, status, input); err != nil {
		return nil, err
	}

	// 幂等处理：已成功的不再回退状态
	if payment.Status == constants.PaymentStatusSuccess {
		log.Infow("payment_callback_idempotent_success",
			"current_status", payment.Status,
		)
		updated, _, _, err := s.applyPaymentUpdate(payment, order, status, input, time.Now())
		return updated, err
	}
	if payment.Status == status {
		log.Infow("payment_callback_idempotent_same_status",
			"current_status", payment.Status,
		)
		// 同状态通知也必须锁付款根并重新读取，避免旧 pending/failed 快照覆盖并发补单。
		updated, _, _, err := s.applyPaymentUpdate(payment, order, status, input, time.Now())
		return updated, err
	}

	previousStatus := payment.Status
	now := time.Now()
	updated, processedOrder, orderPaid, err := s.applyPaymentUpdate(payment, order, status, input, now)
	if err != nil {
		log.Errorw("payment_callback_apply_failed",
			"order_id", order.ID,
			"order_no", order.OrderNo,
			"current_status", payment.Status,
			"error", err,
		)
		return nil, err
	}
	if orderPaid {
		s.enqueueOrderPaidAsync(processedOrder, updated, log)
	}
	log.Infow("payment_callback_processed",
		"order_id", processedOrder.ID,
		"order_no", processedOrder.OrderNo,
		"previous_status", previousStatus,
		"new_status", updated.Status,
		"order_paid", orderPaid,
		"exception_code", updated.ExceptionCode,
	)
	return updated, nil
}

func validateCallbackPaymentFacts(payment *paymentdomain.Payment, businessOrderNo, status string, input PaymentCallbackInput) error {
	if payment == nil {
		return ErrPaymentNotFound
	}
	if input.ChannelID != 0 && input.ChannelID != payment.ChannelID {
		return ErrPaymentInvalid
	}
	if !matchesBusinessOrderNo(input.OrderNo, businessOrderNo, payment) {
		return ErrPaymentInvalid
	}
	currency := strings.TrimSpace(input.Currency)
	if status == constants.PaymentStatusSuccess {
		if currency == "" {
			return ErrPaymentCurrencyMismatch
		}
		if !input.Amount.Decimal.IsPositive() {
			return ErrPaymentAmountMismatch
		}
	}
	if currency != "" &&
		!strings.EqualFold(currency, strings.TrimSpace(payment.Currency)) &&
		!canAdoptVerifiedLegacyDujiaoPayCurrency(payment, status, input) {
		return ErrPaymentCurrencyMismatch
	}
	if !input.Amount.Decimal.IsZero() && input.Amount.Decimal.Cmp(payment.Amount.Decimal) != 0 {
		return ErrPaymentAmountMismatch
	}
	return nil
}

// canAdoptVerifiedLegacyDujiaoPayCurrency 只接受已经通过 DujiaoPay webhook
// 验签入口标记的升级前支付。新版支付带有创建时法币快照，仍执行严格币种一致性校验。
func canAdoptVerifiedLegacyDujiaoPayCurrency(payment *paymentdomain.Payment, status string, input PaymentCallbackInput) bool {
	if payment == nil ||
		payment.ProviderType != constants.PaymentProviderDujiaoPay ||
		payment.Status == constants.PaymentStatusSuccess ||
		status != constants.PaymentStatusSuccess {
		return false
	}
	verifiedCurrency := strings.ToUpper(strings.TrimSpace(input.verifiedLegacyDujiaoPayCurrency))
	callbackCurrency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if verifiedCurrency == "" || verifiedCurrency != callbackCurrency {
		return false
	}
	if !input.Amount.Decimal.IsPositive() || input.Amount.Decimal.Cmp(payment.Amount.Decimal) != 0 {
		return false
	}
	if payment.ProviderPayload != nil {
		if _, hasSnapshot := payment.ProviderPayload[paymentcontract.GatewayPayloadFiatCurrencySent]; hasSnapshot {
			return false
		}
	}
	return true
}

func adoptVerifiedLegacyDujiaoPayCurrency(payment *paymentdomain.Payment, status string, input PaymentCallbackInput) bool {
	if !canAdoptVerifiedLegacyDujiaoPayCurrency(payment, status, input) {
		return false
	}
	currency := strings.ToUpper(strings.TrimSpace(input.verifiedLegacyDujiaoPayCurrency))
	payment.Currency = currency
	if payment.ProviderPayload == nil {
		payment.ProviderPayload = jsonmap.JSON{}
	}
	payment.ProviderPayload[paymentcontract.GatewayPayloadFiatCurrencySent] = currency
	return true
}

func (s *PaymentService) updateCallbackMeta(payment *paymentdomain.Payment, status string, input PaymentCallbackInput) (*paymentdomain.Payment, error) {
	return updateCallbackMetaWithRepo(s.paymentRepo, payment, status, input)
}

func updateCallbackMetaWithRepo(repo paymentcontract.Store, payment *paymentdomain.Payment, status string, input PaymentCallbackInput) (*paymentdomain.Payment, error) {
	updated := adoptVerifiedLegacyDujiaoPayCurrency(payment, status, input)
	if input.ProviderRef != "" && payment.ProviderRef == "" {
		payment.ProviderRef = input.ProviderRef
		updated = true
	}
	if input.Payload != nil {
		payment.ProviderPayload = mergeProviderPayload(payment.ProviderPayload, input.Payload)
		updated = true
	}
	// 只有已验证的终态事实可以解除创建结果未知；pending 不能证明不会继续扣款。
	if status == constants.PaymentStatusSuccess || status == constants.PaymentStatusFailed || status == constants.PaymentStatusExpired {
		if payment.CreationState != "ready" {
			payment.CreationState = "ready"
			updated = true
		}
	}
	if status != "" && payment.Status != status {
		payment.Status = status
		updated = true
	}
	if payment.Status == constants.PaymentStatusSuccess && payment.PaidAt == nil && input.PaidAt != nil {
		payment.PaidAt = input.PaidAt
		updated = true
	}
	if updated {
		now := time.Now()
		payment.CallbackAt = &now
		payment.UpdatedAt = now
		if err := repo.Update(payment); err != nil {
			return nil, ErrPaymentUpdateFailed
		}
	}
	return payment, nil
}

func (s *PaymentService) applyPaymentUpdate(payment *paymentdomain.Payment, order *orderdomain.Order, status string, input PaymentCallbackInput, now time.Time) (*paymentdomain.Payment, *orderdomain.Order, bool, error) {
	returnVal := payment
	processedOrder := order
	orderPaid := false

	err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		paymentRepo := tx.Payments()
		if locker, ok := paymentRepo.(paymentcontract.CreationLocker); ok {
			if err := locker.LockCreation(order.ID); err != nil {
				return err
			}
		}
		lockedOrder, err := tx.Orders().GetByIDForUpdateWithChildren(order.ID)
		if err != nil {
			return orderapp.ErrOrderFetchFailed
		}
		if lockedOrder == nil {
			return orderapp.ErrOrderNotFound
		}
		lockedPayment, err := paymentRepo.GetByIDForUpdate(payment.ID)
		if err != nil {
			return ErrPaymentUpdateFailed
		}
		if lockedPayment == nil {
			return ErrPaymentNotFound
		}
		if err := validateCallbackPaymentFacts(lockedPayment, lockedOrder.OrderNo, status, input); err != nil {
			return err
		}
		adoptVerifiedLegacyDujiaoPayCurrency(lockedPayment, status, input)
		returnVal = lockedPayment
		processedOrder = lockedOrder

		if blocked, err := guardCallbackEvidence(paymentRepo, lockedPayment, status, input, now); blocked || err != nil {
			return err
		}
		if handled, err := preserveSuccessfulReceipt(paymentRepo, lockedPayment, status, input, now); handled {
			if err != nil {
				return err
			}
			return s.appendMembershipPaid(tx, lockedOrder.ID, lockedPayment.ID)
		}
		if lockedPayment.Status == status {
			_, err := updateCallbackMetaWithRepo(paymentRepo, lockedPayment, status, input)
			return err
		}

		outcome, err := s.persistCallbackPaymentFacts(tx, lockedPayment, lockedOrder, status, input, now)
		if err != nil {
			return err
		}

		if status == constants.PaymentStatusSuccess {
			if _, err := paymentRepo.ExpirePendingByOrderIDs([]uint{lockedOrder.ID}, now); err != nil {
				return ErrPaymentUpdateFailed
			}
		}
		if outcome.canFulfill {
			if err := s.completePaidOrder(tx, lockedOrder, lockedPayment, now); err != nil {
				return err
			}
			orderPaid = true
		}
		if outcome.creditUnderpaid {
			// 订单不履约，但用户的钱不能吞：转入余额，用户可用余额补齐后重新支付。
			if err := s.creditUnderpaidToWallet(tx, lockedOrder, lockedPayment, outcome.covered, outcome.required); err != nil {
				return err
			}
		}
		// 已被替代或已有更新尝试时，钱包占款归更新的尝试所有，旧链接的失败/过期回调不能退还（同步上游 cb61a4d）。
		if (status == constants.PaymentStatusFailed || status == constants.PaymentStatusExpired) && lockedOrder.Status == constants.OrderStatusPendingPayment && s.walletSvc != nil && lockedPayment.SupersededAt == nil {
			payments, err := paymentRepo.ListByOrderID(lockedOrder.ID)
			if err != nil {
				return ErrPaymentUpdateFailed
			}
			if newerActivePaymentID(payments, lockedPayment.ID) == 0 {
				if _, err := orderapp.ReleaseWalletBalance(s.walletSvc, tx, lockedOrder, constants.WalletTxnTypeOrderRefund, "在线支付失败，退回余额"); err != nil {
					return err
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return returnVal, processedOrder, orderPaid, nil
}

// guardCallbackEvidence 在调用方已锁业务根/支付的事务内占用同一渠道流水槽位。
// 返回 blocked=true 表示冲突事实已持久化，禁止本次履约/充值；存储失败必须回滚并要求回调重试。
func guardCallbackEvidence(repo paymentcontract.Store, payment *paymentdomain.Payment, status string, input PaymentCallbackInput, now time.Time) (bool, error) {
	if status != constants.PaymentStatusSuccess {
		return false, nil
	}
	reference := strings.TrimSpace(input.ProviderRef)
	if reference == "" {
		reference = strings.TrimSpace(payment.ProviderRef)
	}
	if reference == "" {
		return false, nil
	} // 无流水网关只能沿 payment ID 幂等，不能猜测跨支付身份。
	store, ok := repo.(paymentcontract.ManualConfirmationStore)
	if !ok {
		return false, paymentcontract.ErrManualConfirmationUnavailable
	}
	err := store.ClaimEvidence(payment.ChannelID, "transaction:"+reference, payment.ID)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, paymentcontract.ErrManualConfirmationConflict) {
		return false, err
	}
	anomaly := &paymentdomain.PaymentReceiptAnomaly{PaymentID: payment.ID, TransactionID: reference, Amount: input.Amount.String(), Currency: input.Currency, PaidAt: input.PaidAt, ObservedAt: now}
	if err := store.RecordReceiptAnomaly(anomaly); err != nil {
		return false, err
	}
	paymentLogger("payment_id", payment.ID, "channel_id", payment.ChannelID).Warnw("payment_receipt_evidence_conflict")
	return true, nil
}

// completePaidOrder 是网关及人工收款共用的库存、分销记账、会员 outbox 链路。
// tx/order/payment 必须已锁定；付款起算沿用可信 PaidAt，失败由调用方整体回滚。
func (s *PaymentService) completePaidOrder(tx paymentcontract.Transaction, order *orderdomain.Order, payment *paymentdomain.Payment, now time.Time) error {
	paidAt := now
	if payment.PaidAt != nil {
		paidAt = *payment.PaidAt
	}
	if err := s.markOrderPaid(tx, order, payment.ID, paidAt); err != nil {
		return err
	}
	if s.resellerAccounting != nil {
		if err := s.resellerAccounting.PostOrderProfit(tx.ResellerAccounting(), order, payment); err != nil {
			return err
		}
	}
	return s.appendMembershipPaid(tx, order.ID, payment.ID)
}

// preserveSuccessfulReceipt 保留已成功支付证据；额外交易流水持久记录异常，不覆盖原流水或付款时刻。
func preserveSuccessfulReceipt(repo paymentcontract.Store, payment *paymentdomain.Payment, status string, input PaymentCallbackInput, now time.Time) (bool, error) {
	if payment.Status != constants.PaymentStatusSuccess {
		return false, nil
	}
	if status != constants.PaymentStatusSuccess {
		return true, nil
	}
	// 兼容历史网关成功未记录流水的情况；人工凭据确认则保守保存为待复核异常，不能猜测同笔。
	if input.ProviderRef != "" && payment.ProviderRef == "" {
		store, ok := repo.(paymentcontract.ManualConfirmationStore)
		if !ok {
			return true, paymentcontract.ErrManualConfirmationUnavailable
		}
		record, err := store.FindManualConfirmation(payment.ID)
		if err != nil {
			return true, err
		}
		if record == nil {
			return true, nil
		}
	}
	if input.ProviderRef != "" && input.ProviderRef != payment.ProviderRef {
		store, ok := repo.(paymentcontract.ManualConfirmationStore)
		if !ok {
			return true, paymentcontract.ErrManualConfirmationUnavailable
		}
		anomaly := &paymentdomain.PaymentReceiptAnomaly{PaymentID: payment.ID, TransactionID: input.ProviderRef, Amount: input.Amount.String(), Currency: input.Currency, PaidAt: input.PaidAt, ObservedAt: now}
		if err := store.RecordReceiptAnomaly(anomaly); err != nil {
			return true, err
		}
		// 额外收款独立留痕，不把原先有效付款改为异常，否则待消费权益会被误阻断。
		return true, nil
	}
	return true, nil
}

// callbackOutcome 将可信收款事实与是否允许履约/余额补偿分开。
type callbackOutcome struct {
	canFulfill, creditUnderpaid bool
	covered, required           decimal.Decimal
}

// persistCallbackPaymentFacts 在调用方事务中保存已验签、金额校验后的支付事实。
// tx/payment/order 必须已锁定；status/input 为可信回调，now 为处理时间。
// 返回允许的后续动作；只有明确会员拒绝转为异常，数据库错误仍回滚待重试。
func (s *PaymentService) persistCallbackPaymentFacts(tx paymentcontract.Transaction, payment *paymentdomain.Payment, order *orderdomain.Order, status string, input PaymentCallbackInput, now time.Time) (callbackOutcome, error) {
	outcome := callbackOutcome{
		required: normalizeOrderAmount(order.TotalAmount.Decimal.Sub(order.WalletPaidAmount.Decimal)),
		covered:  paymentCoveredOrderAmount(payment, input.Amount.Decimal),
	}
	orderOpen := order.Status == constants.OrderStatusPendingPayment && order.PaidAt == nil
	underpaid := status == constants.PaymentStatusSuccess && orderOpen && outcome.covered.LessThan(outcome.required)
	membershipClosed := false
	// 已支付订单属于历史事实，后来转换商品不能重新分类这笔付款。
	if status == constants.PaymentStatusSuccess && order.PaidAt == nil && (orderOpen || order.Status == constants.OrderStatusCanceled) {
		if err := s.checkReceiptSale(tx, order); err != nil {
			if !errors.Is(err, productcontract.ErrMembershipSaleClosed) {
				return outcome, err
			}
			membershipClosed = true
		}
	}
	outcome.canFulfill = status == constants.PaymentStatusSuccess && orderOpen && !underpaid && !membershipClosed
	outcome.creditUnderpaid = underpaid && !membershipClosed
	switch status {
	case constants.PaymentStatusSuccess:
		paidAt := now
		if input.PaidAt != nil {
			paidAt = *input.PaidAt
		}
		payment.PaidAt = &paidAt
		switch {
		case membershipClosed:
			payment.ExceptionCode = constants.PaymentExceptionMembershipSaleClosed
		case underpaid:
			payment.ExceptionCode = constants.PaymentExceptionUnderpaidSucceeded
		case payment.SupersededAt != nil:
			payment.ExceptionCode = constants.PaymentExceptionSupersededSucceeded
		case !outcome.canFulfill:
			if order.PaidAt != nil {
				payment.ExceptionCode = constants.PaymentExceptionDuplicateSucceeded
			} else {
				payment.ExceptionCode = constants.PaymentExceptionClosedOrderSucceeded
			}
		}
	case constants.PaymentStatusExpired:
		payment.ExpiredAt = &now
	}
	payment.Status, payment.CallbackAt, payment.UpdatedAt = status, &now, now
	if status == constants.PaymentStatusSuccess || status == constants.PaymentStatusFailed || status == constants.PaymentStatusExpired {
		payment.CreationState = "ready"
	}
	if input.ProviderRef != "" {
		payment.ProviderRef = input.ProviderRef
	}
	if input.Payload != nil {
		payment.ProviderPayload = mergeProviderPayload(payment.ProviderPayload, input.Payload)
	}
	if err := tx.Payments().Update(payment); err != nil {
		return outcome, ErrPaymentUpdateFailed
	}
	return outcome, nil
}

// creditUnderpaidToWallet 把"支付成功但金额不足以履约订单"的款项转入用户余额。
//
// 订单保持待支付，用户可以用这笔余额补齐后重新发起支付；商家既不发货也不吞款。
// 以支付 ID 为幂等键，重复回调不会重复入账。游客订单没有钱包账户，只留异常码等待人工处理。
func (s *PaymentService) creditUnderpaidToWallet(
	tx paymentcontract.Transaction,
	order *orderdomain.Order,
	payment *paymentdomain.Payment,
	coveredAmount decimal.Decimal,
	requiredAmount decimal.Decimal,
) error {
	if order == nil || payment == nil {
		return nil
	}
	log := paymentLogger(
		"payment_id", payment.ID,
		"order_id", order.ID,
		"order_no", order.OrderNo,
		"covered_amount", coveredAmount.String(),
		"required_amount", requiredAmount.String(),
	)
	if s.walletSvc == nil || order.UserID == 0 || !coveredAmount.IsPositive() {
		log.Warnw("payment_callback_underpaid_credit_skipped", "user_id", order.UserID)
		return nil
	}
	orderID := order.ID
	if _, _, err := s.walletSvc.CreditInTransaction(tx.Wallets(), walletcontract.CreditInput{
		UserID:    order.UserID,
		Amount:    money.FromDecimal(coveredAmount),
		Currency:  order.Currency,
		Type:      constants.WalletTxnTypeOrderUnderpaidCredit,
		Reference: fmt.Sprintf("payment:%d:underpaid_credit", payment.ID),
		Remark:    "支付金额不足以完成订单，款项已转入余额",
		OrderID:   &orderID,
	}); err != nil {
		log.Errorw("payment_callback_underpaid_credit_failed", "user_id", order.UserID, "error", err)
		return err
	}
	log.Warnw("payment_callback_underpaid_credited_to_wallet", "user_id", order.UserID)
	return nil
}

// mergeProviderPayload 合并第三方回调原文，同时保留创建支付阶段写入的展示快照等元数据。
// 回调字段优先覆盖同名旧字段，未出现在回调中的 display_channel_type 等字段不会丢失。
func mergeProviderPayload(existing jsonmap.JSON, incoming jsonmap.JSON) jsonmap.JSON {
	if incoming == nil {
		return existing
	}
	merged := make(jsonmap.JSON, len(existing)+len(incoming))
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range incoming {
		// 创建时的金额/汇率是资金核对依据，回调原文不能覆盖这些快照。
		if key == "original_amount" || key == "original_currency" || key == "exchange_rate" {
			continue
		}
		merged[key] = value
	}
	return merged
}

// markOrderPaid 在事务内将订单更新为已支付并处理库存
func (s *PaymentService) markOrderPaid(tx paymentcontract.Transaction, order *orderdomain.Order, paymentID uint, now time.Time) error {
	if err := s.checkReceiptSale(tx, order); err != nil {
		return err
	}
	if order == nil {
		return orderapp.ErrOrderNotFound
	}
	if !orderapp.IsTransitionAllowed(order.Status, constants.OrderStatusPaid) {
		return orderapp.ErrOrderStatusInvalid
	}
	orderRepo := tx.Orders()
	productRepo := tx.Products()
	var productSKURepo productcontract.SKURepository
	if s.productSKURepo != nil {
		productSKURepo = tx.ProductSKUs()
	}

	onlineAmount := normalizeOrderAmount(order.TotalAmount.Decimal.Sub(order.WalletPaidAmount.Decimal))
	orderUpdates := map[string]interface{}{
		"paid_at":            now,
		"online_paid_amount": money.FromDecimal(onlineAmount),
		"updated_at":         now,
	}
	if err := orderRepo.UpdateStatus(order.ID, constants.OrderStatusPaid, orderUpdates); err != nil {
		return orderapp.ErrOrderUpdateFailed
	}
	order.Status = constants.OrderStatusPaid
	order.PaidAt = &now
	order.OnlinePaidAmount = money.FromDecimal(onlineAmount)
	order.UpdatedAt = now
	if err := delivery.AppendOrder(tx.Orders(), delivery.EventOrderPaid, order.ID, 0, paymentID, 0); err != nil {
		return err
	}

	if len(order.Children) > 0 {
		for idx := range order.Children {
			child := &order.Children[idx]
			childStatus := constants.OrderStatusPaid
			if shouldMarkFulfilling(child) {
				childStatus = constants.OrderStatusFulfilling
			}
			if err := orderRepo.UpdateStatus(child.ID, childStatus, map[string]interface{}{
				"paid_at":    now,
				"updated_at": now,
			}); err != nil {
				return orderapp.ErrOrderUpdateFailed
			}
			if err := orderapp.ConsumeManualStockByItems(productRepo, productSKURepo, child.Items); err != nil {
				return err
			}
			child.Status = childStatus
			child.PaidAt = &now
			child.UpdatedAt = now
		}
		parentStatus := orderapp.CalcParentStatus(order.Children, constants.OrderStatusPaid)
		if parentStatus != "" && parentStatus != constants.OrderStatusPaid {
			if err := orderRepo.UpdateStatus(order.ID, parentStatus, map[string]interface{}{
				"online_paid_amount": money.FromDecimal(onlineAmount),
				"updated_at":         now,
			}); err != nil {
				return orderapp.ErrOrderUpdateFailed
			}
			order.Status = parentStatus
		}
		return nil
	}

	if err := orderapp.ConsumeManualStockByItems(productRepo, productSKURepo, order.Items); err != nil {
		return err
	}
	return nil
}
