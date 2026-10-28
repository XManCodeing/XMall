package application

import (
	"context"
	"errors"
	"strings"
	"time"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"

	"github.com/XManCodeing/XMall/internal/constants"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/shopspring/decimal"
)

// CreatePaymentInput 创建支付请求
type CreatePaymentInput struct {
	OperationKey     string // optional, scoped to order; empty remains compatible
	OrderID          uint
	ChannelID        uint
	UseBalance       bool
	ClientIP         string
	Context          context.Context
	Source           *delivery.Source // optional transaction-bound identity guard
	UserID           uint             // expected order owner when Source is supplied
	ReturnBizType    string
	ReturnBusinessNo string
	ReturnGuest      bool
	// RequestScheme 当前请求的 scheme（http/https），用于分销站动态生成 return_url；空值默认 https。
	RequestScheme string
}

// CreatePaymentResult 创建支付结果
type CreatePaymentResult struct {
	Payment          *paymentdomain.Payment
	Channel          *paymentdomain.PaymentChannel
	OrderPaid        bool
	WalletPaidAmount money.Amount
	OnlinePayAmount  money.Amount
}

func hasProviderResult(payment *paymentdomain.Payment) bool {
	if payment == nil {
		return false
	}
	return strings.TrimSpace(payment.PayURL) != "" || strings.TrimSpace(payment.QRCode) != ""
}

var ErrPaymentCreationUncertain = errors.New("payment creation in progress or outcome unknown; query existing payment before retry")
var ErrPaymentOperationConflict = errors.New("payment operation key reused with different request")

// CreatePayment 创建支付单
func (s *PaymentService) CreatePayment(input CreatePaymentInput) (*CreatePaymentResult, error) {
	input.OperationKey = strings.TrimSpace(input.OperationKey)
	if len(input.OperationKey) > 160 {
		return nil, ErrPaymentInvalid
	}
	operationHash := paymentOperationHash(input)
	if input.OrderID == 0 {
		return nil, ErrPaymentInvalid
	}

	log := paymentLogger(
		"order_id", input.OrderID,
		"channel_id", input.ChannelID,
	)

	var payment *paymentdomain.Payment
	var order *orderdomain.Order
	var channel *paymentdomain.PaymentChannel
	feeRate := decimal.Zero
	reusedPending := false
	replayedOperation := false
	orderPaidByWallet := false
	now := time.Now()
	feeConfig := settingsapp.DefaultPaymentFeeConfig()
	if s.settingService != nil {
		feeConfig = s.settingService.GetPaymentFeeConfig()
	}

	// 在事务外查询设置，避免 SQLite 单连接池下自锁
	walletOnly := s.settingService != nil && s.settingService.GetWalletOnlyPayment()
	if walletOnly {
		input.UseBalance = true
		if input.ChannelID != 0 {
			return nil, walletcontract.ErrOnlyPaymentRequired
		}
	}

	err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		if locker, ok := tx.Payments().(paymentcontract.CreationLocker); ok {
			if err := locker.LockCreation(input.OrderID); err != nil {
				return err
			}
		}
		preloaded, err := tx.Orders().GetByIDForUpdateWithChildren(input.OrderID)
		if err != nil {
			return orderapp.ErrOrderFetchFailed
		}
		if preloaded == nil {
			return orderapp.ErrOrderNotFound
		}
		lockedOrder := *preloaded
		if input.Source != nil {
			if input.Source.ExpectedIdentityID == 0 || input.UserID == 0 || lockedOrder.UserID != input.UserID {
				return delivery.ErrIdentityChanged
			}
			if err := delivery.GuardIdentity(tx.Orders(), input.UserID, input.Source); err != nil {
				return err
			}
		}
		// The order row lock serializes durable intent creation across processes.
		// Any unresolved gateway attempt blocks ALL channels, including wallet.
		existingPayments, err := tx.Payments().ListByOrderID(input.OrderID)
		if err != nil {
			return err
		}
		for i := range existingPayments {
			previous := &existingPayments[i]
			if input.OperationKey != "" && previous.OperationKey != nil && *previous.OperationKey == input.OperationKey {
				if previous.OperationHash != operationHash {
					return ErrPaymentOperationConflict
				}
				// A proven local failure is terminal for this operation key. A new
				// key may retry/switch channels; replay never dispatches twice.
				if previous.CreationState == "not_submitted" && previous.Status != constants.PaymentStatusSuccess {
					return ErrPaymentCreateFailed
				}
				if previous.CreationState != "ready" && previous.Status != constants.PaymentStatusSuccess {
					return ErrPaymentCreationUncertain
				}
				payment = previous
				order = &lockedOrder
				replayedOperation = true
				if previous.ChannelID != 0 {
					channel, err = tx.PaymentChannels().GetByID(previous.ChannelID)
					if err != nil {
						return err
					}
				}
				return nil
			}
		}
		for _, previous := range existingPayments {
			if (previous.CreationState == "processing" || previous.CreationState == "unknown") && previous.Status != constants.PaymentStatusSuccess {
				return ErrPaymentCreationUncertain
			}
		}
		if lockedOrder.ParentID != nil {
			return ErrPaymentInvalid
		}
		if lockedOrder.Status != constants.OrderStatusPendingPayment {
			return orderapp.ErrOrderStatusInvalid
		}
		if lockedOrder.ExpiresAt != nil && !lockedOrder.ExpiresAt.After(time.Now()) {
			return orderapp.ErrOrderStatusInvalid
		}

		paymentRepo := tx.Payments()
		channelRepo := tx.PaymentChannels()
		if input.ChannelID != 0 {
			if channel == nil {
				// 事务内必须使用 tx 绑定仓储，避免在单连接池下发生自锁等待。
				resolvedChannel, err := channelRepo.GetByID(input.ChannelID)
				if err != nil {
					return err
				}
				if resolvedChannel == nil {
					return ErrPaymentChannelNotFound
				}
				if !resolvedChannel.IsActive {
					return ErrPaymentChannelInactive
				}
				resolvedFeeRate := resolvedChannel.FeeRate.Decimal.Round(2)
				if resolvedFeeRate.LessThan(decimal.Zero) || resolvedFeeRate.GreaterThan(decimal.NewFromInt(100)) {
					return ErrPaymentChannelConfigInvalid
				}
				channel = resolvedChannel
				feeRate = resolvedFeeRate
			}
			if err := validateOrderChannelEligibility(*channel, &lockedOrder); err != nil {
				return err
			}

			// 校验商品是否允许该支付渠道（传入 tx 避免 SQLite 自锁）
			allItems := lockedOrder.Items
			for _, child := range lockedOrder.Children {
				allItems = append(allItems, child.Items...)
			}
			if err := s.validateProductPaymentChannel(allItems, channel.ID, tx.Products()); err != nil {
				return err
			}

			existing, err := paymentRepo.GetLatestPendingByOrderChannel(lockedOrder.ID, channel.ID, time.Now())
			if err != nil {
				return ErrPaymentCreateFailed
			}
			if existing != nil && hasProviderResult(existing) && input.OperationKey == "" {
				legacyFeePayment := existing.FeeAmount.Decimal.IsPositive() &&
					(existing.FeePolicy == "" || existing.FeePolicy == constants.PaymentFeePolicyLegacyCustomerSurcharge)
				if !legacyFeePayment || feeConfig.ReuseLegacyOrderFeePayment {
					reusedPending = true
					payment = existing
					order = &lockedOrder
					return nil
				}
			}
		}

		if s.walletSvc != nil {
			if input.UseBalance {
				if _, err := orderapp.ApplyWalletBalance(s.walletSvc, tx, &lockedOrder, true); err != nil {
					return err
				}
			} else if lockedOrder.WalletPaidAmount.Decimal.GreaterThan(decimal.Zero) {
				if _, err := orderapp.ReleaseWalletBalance(s.walletSvc, tx, &lockedOrder, constants.WalletTxnTypeOrderRefund, "用户改为在线支付，退回余额"); err != nil {
					return err
				}
			}
		}

		onlineAmount := normalizeOrderAmount(lockedOrder.TotalAmount.Decimal.Sub(lockedOrder.WalletPaidAmount.Decimal))
		if onlineAmount.LessThanOrEqual(decimal.Zero) {
			walletPaidAmount := normalizeOrderAmount(lockedOrder.WalletPaidAmount.Decimal)
			paidAt := time.Now()
			payment = &paymentdomain.Payment{
				OrderID:         lockedOrder.ID,
				ChannelID:       0,
				ProviderType:    constants.PaymentProviderWallet,
				ChannelType:     constants.PaymentChannelTypeBalance,
				InteractionMode: constants.PaymentInteractionBalance,
				Amount:          money.FromDecimal(walletPaidAmount),
				FeeRate:         money.FromDecimal(decimal.Zero),
				FixedFee:        money.FromDecimal(decimal.Zero),
				FeeAmount:       money.FromDecimal(decimal.Zero),
				FeePolicy:       constants.PaymentFeePolicyNone,
				Currency:        lockedOrder.Currency,
				Status:          constants.PaymentStatusSuccess,
				CreatedAt:       paidAt,
				UpdatedAt:       paidAt,
				PaidAt:          &paidAt,
			}
			payment.CreationState = "ready"
			if input.OperationKey != "" {
				key := input.OperationKey
				payment.OperationKey = &key
				payment.OperationHash = operationHash
			}
			if err := paymentRepo.Create(payment); err != nil {
				return ErrPaymentCreateFailed
			}
			// 余额已覆盖全额，订单遗留的在线链接必须同时作废，避免用户再付一次。
			if _, err := paymentRepo.SupersedePendingByOrderID(lockedOrder.ID, payment.ID, paidAt); err != nil {
				return ErrPaymentCreateFailed
			}
			if err := s.markOrderPaid(tx, &lockedOrder, payment.ID, paidAt); err != nil {
				return err
			}
			// 与在线支付回调一致：分销利润与支付在同一事务入账
			if s.resellerAccounting != nil {
				if err := s.resellerAccounting.PostOrderProfit(tx.ResellerAccounting(), &lockedOrder, payment); err != nil {
					return err
				}
			}
			orderPaidByWallet = true
			order = &lockedOrder
			return nil
		}
		if channel == nil {
			if walletOnly {
				return walletcontract.ErrOnlyPaymentRequired
			}
			return ErrPaymentInvalid
		}
		if err := validatePaymentCurrencyForChannel(lockedOrder.Currency, channel); err != nil {
			return err
		}
		if err := validatePaymentAmountForChannel(onlineAmount, channel); err != nil {
			return err
		}

		fixedFee := decimal.Zero
		if channel.FixedFee.Decimal.GreaterThan(decimal.Zero) {
			fixedFee = channel.FixedFee.Decimal.Round(2)
		}

		paymentAmount, feeAmount, feePolicy := calculatePaymentAmounts(onlineAmount, feeRate, fixedFee, feeConfig.CustomerFeeEnabled)
		payment = &paymentdomain.Payment{
			OrderID:         lockedOrder.ID,
			ChannelID:       channel.ID,
			ProviderType:    channel.ProviderType,
			ChannelType:     channel.ChannelType,
			InteractionMode: channel.InteractionMode,
			Amount:          money.FromDecimal(paymentAmount),
			FeeRate:         money.FromDecimal(feeRate),
			FixedFee:        money.FromDecimal(fixedFee),
			FeeAmount:       money.FromDecimal(feeAmount),
			FeePolicy:       feePolicy,
			Currency:        lockedOrder.Currency,
			Status:          constants.PaymentStatusInitiated,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if shouldUseCNYPaymentCurrency(channel) {
			payment.Currency = "CNY"
		}

		payment.CreationState = "processing"
		if input.OperationKey != "" {
			key := input.OperationKey
			payment.OperationKey = &key
			payment.OperationHash = operationHash
		}
		if err := paymentRepo.Create(payment); err != nil {
			return ErrPaymentCreateFailed
		}
		if err := tx.Orders().UpdateFields(lockedOrder.ID, map[string]interface{}{
			"online_paid_amount": money.FromDecimal(onlineAmount),
			"updated_at":         time.Now(),
		}); err != nil {
			return orderapp.ErrOrderUpdateFailed
		}
		lockedOrder.OnlinePaidAmount = money.FromDecimal(onlineAmount)
		lockedOrder.UpdatedAt = time.Now()
		order = &lockedOrder
		return nil
	})
	if err != nil {
		return nil, err
	}

	if order == nil {
		return nil, orderapp.ErrOrderFetchFailed
	}

	if replayedOperation {
		result := &CreatePaymentResult{Payment: payment, Channel: channel, OrderPaid: payment.Status == constants.PaymentStatusSuccess, WalletPaidAmount: order.WalletPaidAmount, OnlinePayAmount: order.OnlinePaidAmount}
		if payment.ProviderType == constants.PaymentProviderWallet {
			result.Payment = nil
			result.Channel = nil
		}
		return result, nil
	}
	if reusedPending {
		if _, err := s.paymentRepo.SupersedePendingByOrderID(order.ID, payment.ID, time.Now()); err != nil {
			return nil, ErrPaymentCreateFailed
		}
		log.Infow("payment_create_reuse_pending",
			"payment_id", payment.ID,
			"provider_type", payment.ProviderType,
			"channel_type", payment.ChannelType,
		)
		return &CreatePaymentResult{
			Payment:          payment,
			Channel:          channel,
			WalletPaidAmount: order.WalletPaidAmount,
			OnlinePayAmount:  order.OnlinePaidAmount,
		}, nil
	}

	if orderPaidByWallet {
		log.Infow("payment_create_wallet_success",
			"payment_id", payment.ID,
			"provider_type", payment.ProviderType,
			"channel_type", payment.ChannelType,
			"interaction_mode", payment.InteractionMode,
			"currency", payment.Currency,
			"amount", payment.Amount.String(),
			"wallet_paid_amount", order.WalletPaidAmount.String(),
			"online_pay_amount", order.OnlinePaidAmount.String(),
		)
		s.enqueueOrderPaidAsync(order, payment, log)
		return &CreatePaymentResult{
			Payment:          nil,
			Channel:          nil,
			OrderPaid:        true,
			WalletPaidAmount: order.WalletPaidAmount,
			OnlinePayAmount:  money.FromDecimal(decimal.Zero),
		}, nil
	}

	if payment == nil {
		return nil, ErrPaymentCreateFailed
	}

	if err := s.applyProviderPayment(input, order, channel, payment); err != nil {
		// Only explicit execution-boundary evidence permits releasing the intent.
		// A category such as config/auth/request alone is never sufficient.
		notSubmitted := errors.Is(err, paymentcontract.ErrGatewayNotSubmitted)
		markErr := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
			locked, lockErr := tx.Payments().GetByIDForUpdate(payment.ID)
			if lockErr != nil {
				return lockErr
			}
			if locked == nil {
				return ErrPaymentNotFound
			}
			// Match callback lock order (payment then order). Never refund funds
			// consumed by a callback, nor overwrite its authoritative state.
			lockedOrder, lockErr := tx.Orders().GetByIDForUpdateWithChildren(order.ID)
			if lockErr != nil {
				return lockErr
			}
			if lockedOrder == nil {
				return orderapp.ErrOrderNotFound
			}
			if locked.Status == constants.PaymentStatusSuccess || locked.CallbackAt != nil {
				notSubmitted = false // callback evidence overrides a provider's local classification
				return nil
			}
			locked.CreationState = "unknown"
			if notSubmitted {
				locked.CreationState = "not_submitted"
				locked.Status = constants.PaymentStatusFailed
				if lockedOrder.Status == constants.OrderStatusPendingPayment && lockedOrder.PaidAt == nil {
					if _, releaseErr := orderapp.ReleaseWalletBalance(s.walletSvc, tx, lockedOrder, constants.WalletTxnTypeOrderRefund, "支付请求未提交，退回余额"); releaseErr != nil {
						return releaseErr
					}
					if updateErr := tx.Orders().UpdateFields(order.ID, map[string]interface{}{
						"online_paid_amount": money.FromDecimal(normalizeOrderAmount(lockedOrder.TotalAmount.Decimal.Sub(lockedOrder.WalletPaidAmount.Decimal))),
						"updated_at":         time.Now(),
					}); updateErr != nil {
						return updateErr
					}
				}
			}
			locked.UpdatedAt = time.Now()
			return tx.Payments().Update(locked)
		})
		if markErr != nil {
			log.Errorw("payment_creation_failure_persist_failed", "payment_id", payment.ID, "error", markErr)
			return nil, ErrPaymentCreationUncertain
		}
		if notSubmitted {
			return nil, err
		}
		return nil, ErrPaymentCreationUncertain
	}
	if _, err := s.paymentRepo.SupersedePendingByOrderID(order.ID, payment.ID, time.Now()); err != nil {
		log.Errorw("payment_create_supersede_previous_failed",
			"payment_id", payment.ID,
			"order_id", order.ID,
			"error", err,
		)
		return nil, ErrPaymentCreateFailed
	}

	log.Infow("payment_create_success",
		"payment_id", payment.ID,
		"provider_type", payment.ProviderType,
		"channel_type", payment.ChannelType,
		"interaction_mode", payment.InteractionMode,
		"currency", payment.Currency,
		"amount", payment.Amount.String(),
		"wallet_paid_amount", order.WalletPaidAmount.String(),
		"online_pay_amount", order.OnlinePaidAmount.String(),
	)

	return &CreatePaymentResult{
		Payment:          payment,
		Channel:          channel,
		WalletPaidAmount: order.WalletPaidAmount,
		OnlinePayAmount:  order.OnlinePaidAmount,
	}, nil
}
