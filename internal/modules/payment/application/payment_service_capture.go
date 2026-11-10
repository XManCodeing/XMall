package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	"strings"
	"time"

	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/logger"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// SetMembershipPreflight 启动期注入事务外能力补查；原有事务最终复核不变。
func (s *PaymentService) SetMembershipPreflight(preflight membership.CapabilityPreflight) {
	if s != nil {
		s.membershipPreflight = preflight
	}
}

// prepareMembershipPayment 在任何支付事务开始前补查历史会员目标。
// ctx 传递取消；id 为已授权订单引用，成功付款重放不触发探测。
func (s *PaymentService) prepareMembershipPayment(ctx context.Context, id uint) error {
	if !s.enforcePublicSale || s.membershipPreflight == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reader, ok := s.orderRepo.(interface {
		GetByIDContext(context.Context, uint) (*od.Order, error)
	})
	if !ok {
		return membership.ErrCapability
	}
	order, err := reader.GetByIDContext(ctx, id)
	if err != nil {
		return err
	}
	if order == nil {
		return ErrPaymentInvalid
	}
	if order.PaidAt != nil || order.Status == constants.OrderStatusPaid {
		return nil
	}
	return s.membershipPreflight.Payment(ctx, order)
}

// checkPaymentSnapshot 在主动付款事务中复核历史快照，不能用于已收款回调。
// tx/order 必须来自当前支付事务；普通商品不查询绑定，会员须通过当前销售与历史身份复核。
func (s *PaymentService) checkPaymentSnapshot(tx paymentcontract.Transaction, order *od.Order) error {
	if !s.enforcePublicSale {
		return nil
	}
	if order == nil {
		return ErrPaymentInvalid
	}
	provider, ok := tx.(membership.PaymentPreflightProvider)
	if !ok {
		return membership.ErrTransactionRequired
	}
	preflight, err := provider.MembershipPaymentPreflight()
	if err != nil {
		return err
	}
	if preflight == nil {
		return membership.ErrTransactionRequired
	}
	_, err = preflight.CheckPayment(context.Background(), order, time.Now())
	if err != nil {
		paymentLogger("order_id", order.ID).Warnw("payment_membership_preflight_rejected", "error", err)
		return err
	}
	return nil
}

// checkReceiptSale 保护已收款回调：检查快照完整性，不执行主动支付身份/能力复核。
// tx/order 来自当前付款事务；异常会员返回停售标记以保留收款事实并停止发货。
func (s *PaymentService) checkReceiptSale(tx paymentcontract.Transaction, order *od.Order) error {
	if !s.enforcePublicSale {
		return nil
	}
	provider, ok := tx.(membership.PaymentPreflightProvider)
	if !ok {
		return membership.ErrTransactionRequired
	}
	preflight, err := provider.MembershipPaymentPreflight()
	if err != nil {
		return err
	}
	checker, ok := preflight.(interface {
		CheckReceipt(context.Context, *od.Order) error
	})
	if !ok {
		return membership.ErrTransactionRequired
	}
	if err := checker.CheckReceipt(context.Background(), order); err != nil {
		if errors.Is(err, membership.ErrSalesClosed) {
			return product.ErrMembershipSaleClosed
		}
		return err
	}
	return s.checkOrderPublicSale(tx.Products(), order)
}

// appendMembershipPaid 在可信支付事务中同步追加权益 outbox，不复用通知事件或联系人。
// tx 必须绑定本地事务；生产门控启用时缺失 provider 拒绝提交，普通历史测试桩保持兼容。
func (s *PaymentService) appendMembershipPaid(tx paymentcontract.Transaction, orderID, paymentID uint) error {
	provider, ok := tx.(membership.PaidEventProvider)
	if !ok {
		if s.enforcePublicSale {
			return membership.ErrTransactionRequired
		}
		return nil
	}
	appender, err := provider.MembershipPaidEvents()
	if err != nil {
		return err
	}
	if appender == nil {
		return membership.ErrTransactionRequired
	}
	if err := appender.AppendPaid(context.Background(), orderID, paymentID); err != nil {
		paymentLogger("order_id", orderID, "payment_id", paymentID).Errorw("payment_membership_event_append_failed")
		return err
	}
	return nil
}

// persistProviderAttempt atomically publishes a gateway result and retires old local attempts.
// payment contains the gateway response; failures roll back publication and leave the durable
// processing intent blocked for reconciliation. No gateway close or new debit is performed here.
func (s *PaymentService) persistProviderAttempt(payment *paymentdomain.Payment) error {
	return s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		orderOpen := true
		var newerID uint
		var locked *paymentdomain.Payment
		var err error
		if payment.OrderID != 0 {
			order, current, newer, lockErr := lockPaymentAttempt(tx, payment.OrderID, payment.ID)
			if lockErr != nil {
				return lockErr
			}
			locked, newerID = current, newer
			orderOpen = order.Status == constants.OrderStatusPendingPayment && order.PaidAt == nil
		} else {
			locked, err = tx.Payments().GetByIDForUpdate(payment.ID)
			if err != nil {
				return err
			}
			if locked == nil {
				return ErrPaymentNotFound
			}
		}
		// A callback may have completed while the gateway response was in flight.
		if locked.Status != constants.PaymentStatusInitiated && locked.Status != constants.PaymentStatusPending {
			payment.Status = locked.Status
			payment.PaidAt = locked.PaidAt
			payment.CallbackAt = locked.CallbackAt
			payment.ExceptionCode = locked.ExceptionCode
			payment.ExpiredAt = locked.ExpiredAt
		}
		payment.ProviderPayload = mergeProviderPayload(payment.ProviderPayload, locked.ProviderPayload)
		payment.SupersededAt = locked.SupersededAt
		payment.SupersededByPaymentID = locked.SupersededByPaymentID
		now := time.Now()
		if payment.Status == constants.PaymentStatusPending && (!orderOpen || newerID != 0) {
			payment.Status, payment.ExpiredAt = constants.PaymentStatusExpired, &now
			if newerID != 0 {
				payment.SupersededAt, payment.SupersededByPaymentID = &now, &newerID
			}
		}
		if err := tx.Payments().Update(payment); err != nil {
			return err
		}
		if payment.OrderID != 0 && payment.Status == constants.PaymentStatusPending {
			_, err = tx.Payments().SupersedePendingByOrderID(payment.OrderID, payment.ID, now)
		}
		return err
	})
}

func paymentOperationHash(input CreatePaymentInput) string {
	raw, _ := json.Marshal(struct {
		ChannelID                       uint
		UseBalance                      bool
		ReturnBizType, ReturnBusinessNo string
		ReturnGuest                     bool
	}{input.ChannelID, input.UseBalance, input.ReturnBizType, input.ReturnBusinessNo, input.ReturnGuest})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RecoverPaidFulfillment repairs the commit/enqueue crash window. limit is the
// page size; keyset traversal prevents failing rows starving later orders.
// Run once per reconciliation interval, not once per Bot.
func (s *PaymentService) RecoverPaidFulfillment(ctx context.Context, limit int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	scanner, ok := s.orderRepo.(ordercontract.FulfillmentRecoveryStore)
	if !ok {
		return 0, errors.New("fulfillment recovery store unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var after uint
	recovered := 0
	var firstErr error
	before := time.Now().Add(-time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		ids, err := scanner.ListPaidWithoutFulfillment(ctx, after, before, limit)
		if err != nil {
			return recovered, err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return recovered, err
			}
			if id <= after {
				return recovered, errors.New("fulfillment recovery cursor did not advance")
			}
			after = id
			order, err := scanner.GetByIDContext(ctx, id)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if order == nil || order.Fulfillment != nil || (order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling) {
				continue
			}
			if shouldAutoFulfill(order) {
				if s.queue == nil || !s.queue.Enabled() {
					if firstErr == nil {
						firstErr = orderapp.ErrQueueUnavailable
					}
					continue
				}
				queue, ok := s.queue.(interface {
					EnqueueOrderAutoFulfillContext(context.Context, uint) error
				})
				if !ok {
					return recovered, errors.New("context-aware fulfillment queue unavailable")
				}
				if err := ctx.Err(); err != nil {
					return recovered, err
				}
				if err := queue.EnqueueOrderAutoFulfillContext(ctx, id); err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					recovered++
				}
				continue
			}
			upstream := false
			for _, item := range order.Items {
				if item.FulfillmentType == constants.FulfillmentTypeUpstream {
					upstream = true
				}
			}
			if upstream && s.procurementSvc != nil {
				creator, ok := s.procurementSvc.(interface {
					CreateForOrderContext(context.Context, uint) error
				})
				if !ok {
					return recovered, errors.New("context-aware procurement unavailable")
				}
				if err := ctx.Err(); err != nil {
					return recovered, err
				}
				err := creator.CreateForOrderContext(ctx, id)
				if err != nil && !errors.Is(err, procurementcontract.ErrExists) {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					recovered++
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		if len(ids) < limit {
			return recovered, firstErr
		}
	}
}

// CapturePaymentInput 捕获支付输入。
type CapturePaymentInput struct {
	PaymentID uint
	Context   context.Context
}

func (s *PaymentService) CapturePayment(input CapturePaymentInput) (*paymentdomain.Payment, error) {
	if input.PaymentID == 0 {
		return nil, ErrPaymentInvalid
	}
	payment, err := s.paymentRepo.GetByID(input.PaymentID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if payment == nil {
		return nil, ErrPaymentNotFound
	}
	if payment.Status == constants.PaymentStatusSuccess {
		return payment, nil
	}

	channel, err := s.channelRepo.GetByID(payment.ChannelID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if channel == nil {
		return nil, ErrPaymentChannelNotFound
	}

	providerType := strings.ToLower(strings.TrimSpace(channel.ProviderType))
	if providerType != constants.PaymentProviderOfficial {
		return nil, ErrPaymentProviderNotSupported
	}
	if strings.TrimSpace(payment.ProviderRef) == "" {
		return nil, ErrPaymentInvalid
	}

	// 统一通过 Registry 路由。Registry.Lookup 会返回 channel 对应的 adapter,
	// 如果 adapter 不实现 Capturer,type assertion 失败,返回 ErrPaymentProviderNotSupported。
	// 因此无需在此显式检查 channel 是否支持 capture。
	return s.captureViaRegistry(input, payment, channel)
}

// captureViaRegistry 通过 PaymentProviderRegistry 路由调用 QueryPayment。
// stripe + paypal + wechat 实现了 paymentcontract.GatewayCapturer 接口,其它 channel
// (alipay / epay / epusdt / bepusdt / tokenpay / okpay) 仅实现 webhook 回调,
// type assertion 失败时返回 ErrPaymentProviderNotSupported。
func (s *PaymentService) captureViaRegistry(input CapturePaymentInput, payment *paymentdomain.Payment, channel *paymentdomain.PaymentChannel) (*paymentdomain.Payment, error) {
	if err := s.checkPaymentPublicSale(payment.OrderID); err != nil {
		return nil, err
	}
	logger.Infow("payment_capture_via_registry",
		"payment_id", payment.ID,
		"provider_type", channel.ProviderType,
		"channel_type", channel.ChannelType,
	)
	if s.paymentProviderRegistry == nil {
		return nil, ErrPaymentProviderNotSupported
	}
	p, ok := s.paymentProviderRegistry.Lookup(channel.ProviderType, channel.ChannelType)
	if !ok {
		return nil, ErrPaymentProviderNotSupported
	}
	capturer, ok := p.(paymentcontract.GatewayCapturer)
	if !ok {
		logger.Warnw("payment_provider_capture_not_implemented",
			"provider_type", channel.ProviderType,
			"channel_type", channel.ChannelType,
		)
		return nil, ErrPaymentProviderNotSupported
	}

	// 第二参数是 interactionMode,不是 channelType。stripe/wechat adapter
	// 会拒绝任何非法 mode,传 channelType 会导致  一律 ErrConfigInvalid。
	if err := capturer.ValidateConfig(channel.ConfigJSON, channel.InteractionMode); err != nil {
		return nil, mapProviderErrorToService(err)
	}

	ctx, cancel := detachOutboundRequestContext(input.Context)
	defer cancel()

	queryResult, err := capturer.QueryPayment(ctx, channel.ConfigJSON, payment.ProviderRef)
	if err != nil {
		return nil, mapProviderErrorToService(err)
	}

	payload := jsonmap.JSON{}
	if queryResult.Payload != nil {
		payload = queryResult.Payload
	}
	status := strings.TrimSpace(queryResult.Status)
	if status == "" {
		status = constants.PaymentStatusPending
	}

	callbackInput := PaymentCallbackInput{
		PaymentID:   payment.ID,
		ChannelID:   channel.ID,
		Status:      status,
		ProviderRef: pickFirstNonEmpty(queryResult.ProviderRef, payment.ProviderRef),
		Amount:      queryResult.Amount,
		Currency:    strings.ToUpper(strings.TrimSpace(queryResult.Currency)),
		PaidAt:      queryResult.PaidAt,
		Payload:     payload,
	}
	return s.HandleCallback(callbackInput)
}

// mapProviderErrorToService 把 provider.ErrXxx 转换为 service 层的 ErrPaymentXxx。
func mapProviderErrorToService(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, paymentcontract.ErrGatewayConfigInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentChannelConfigInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayRequestFailed), errors.Is(err, paymentcontract.ErrGatewayAuthFailed):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	case errors.Is(err, paymentcontract.ErrGatewayResponseInvalid), errors.Is(err, paymentcontract.ErrGatewaySignatureInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayResponseInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayUnsupportedChannel), errors.Is(err, paymentcontract.ErrGatewayProviderNotFound):
		return ErrPaymentProviderNotSupported
	default:
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	}
}
