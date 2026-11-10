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
	ClientIP         string // Compatibility input; the gateway boundary replaces it with a privacy placeholder.
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

var ErrPaymentCreationUncertain = paymentcontract.ErrPaymentCreationUncertain
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

	if err := s.prepareMembershipPayment(input.Context, input.OrderID); err != nil {
		return nil, err
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
				if previous.Status != constants.PaymentStatusSuccess {
					if err := s.checkPaymentSnapshot(tx, &lockedOrder); err != nil {
						return err
					}
					if err := s.checkOrderPublicSale(tx.Products(), &lockedOrder); err != nil {
						return err
					}
				}
				resolved, err := paymentcontract.ResolvePaymentReplay(&lockedOrder, previous, existingPayments, time.Now())
				if err != nil {
					if errors.Is(err, paymentcontract.ErrReplayOrderInvalid) {
						return orderapp.ErrOrderStatusInvalid
					}
					return err
				}
				previous = resolved
				payment = previous
				order = &lockedOrder
				replayedOperation = true
				if previous.ChannelID != 0 {
					channel, err = tx.PaymentChannels().GetByID(previous.ChannelID)
					if err != nil {
						return err
					}
				}
				return s.appendMembershipPaid(tx, lockedOrder.ID, previous.ID)
			}
		}
		if err := s.checkPaymentSnapshot(tx, &lockedOrder); err != nil {
			return err
		}
		if err := s.checkOrderPublicSale(tx.Products(), &lockedOrder); err != nil {
			return err
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
					// 在持有订单锁的事务内作废其他未完成尝试，避免与并发创建交错（同步上游 cb61a4d）。
					if _, err := paymentRepo.SupersedePendingByOrderID(lockedOrder.ID, existing.ID, time.Now()); err != nil {
						return ErrPaymentCreateFailed
					}
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
			if err := s.appendMembershipPaid(tx, lockedOrder.ID, payment.ID); err != nil {
				return err
			}
			orderPaidByWallet = true
			order = &lockedOrder
			return nil
		}
		if channel == nil {
			// Wallet-only policy takes precedence over the implied UseBalance flag.
			if walletOnly {
				return walletcontract.ErrOnlyPaymentRequired
			}
			if input.UseBalance {
				return walletcontract.ErrInsufficientBalance
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
		result := &CreatePaymentResult{Payment: payment, Channel: channel, OrderPaid: payment.Status == constants.PaymentStatusSuccess && order.PaidAt != nil, WalletPaidAmount: order.WalletPaidAmount, OnlinePayAmount: order.OnlinePaidAmount}
		if payment.ProviderType == constants.PaymentProviderWallet {
			result.Payment = nil
			result.Channel = nil
		}
		return result, nil
	}
	if reusedPending {
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
		terminalFailure := false
		markErr := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
			// 与回调、创建同序：先订单后支付，避免 PostgreSQL/MySQL 死锁（同步上游 cb61a4d）。
			// 不退还被回调消费的资金，也不覆盖回调写入的权威状态。
			lockedOrder, locked, newerID, lockErr := lockPaymentAttempt(tx, order.ID, payment.ID)
			if lockErr != nil {
				return lockErr
			}
			if locked.Status == constants.PaymentStatusSuccess || locked.CallbackAt != nil {
				notSubmitted = false // callback evidence overrides a provider's local classification
				terminalFailure = locked.CreationState == "ready" && (locked.Status == constants.PaymentStatusFailed || locked.Status == constants.PaymentStatusExpired)
				return nil
			}
			locked.CreationState = "unknown"
			if notSubmitted {
				locked.CreationState = "not_submitted"
				// 只改写仍属本尝试的状态；已被替代/过期的记录保留原状态。
				if locked.Status == constants.PaymentStatusInitiated || locked.Status == constants.PaymentStatusPending {
					locked.Status = constants.PaymentStatusFailed
				}
				// 已有更新的有效尝试时，钱包占款归新尝试所有，不能退还。
				if newerID == 0 && lockedOrder.Status == constants.OrderStatusPendingPayment && lockedOrder.PaidAt == nil {
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
		if terminalFailure {
			return nil, paymentcontract.PaymentAttemptRetry(payment)
		}
		if notSubmitted {
			if payment.OperationKey != nil {
				return nil, errors.Join(err, paymentcontract.PaymentAttemptRetry(payment))
			}
			return nil, err
		}
		return nil, ErrPaymentCreationUncertain
	}
	if payment.Status == constants.PaymentStatusFailed {
		return nil, paymentcontract.PaymentAttemptRetry(payment)
	}
	if payment.Status == constants.PaymentStatusExpired || payment.SupersededAt != nil {
		currentOrder, err := s.orderRepo.GetByID(order.ID)
		if err != nil {
			return nil, orderapp.ErrOrderFetchFailed
		}
		if currentOrder == nil {
			return nil, orderapp.ErrOrderNotFound
		}
		log.Warnw("payment_create_stale_provider_success", "payment_id", payment.ID, "order_id", order.ID)
		return takenOverResult(currentOrder)
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

// lockPaymentAttempt 网关返回后按“先订单后支付”重新加锁读取（与创建、钱包支付、回调同序）。
// 参数 orderID/paymentID：要锁定的订单与支付尝试。
// 返回：锁定的订单、支付，以及同订单上比该尝试更新且仍有效的支付 ID（非零说明钱包占款已归新尝试）。
func lockPaymentAttempt(tx paymentcontract.Transaction, orderID, paymentID uint) (*orderdomain.Order, *paymentdomain.Payment, uint, error) {
	order, err := tx.Orders().GetByIDForUpdateWithChildren(orderID)
	if err != nil {
		return nil, nil, 0, orderapp.ErrOrderFetchFailed
	}
	if order == nil {
		return nil, nil, 0, orderapp.ErrOrderNotFound
	}
	current, err := tx.Payments().GetByIDForUpdate(paymentID)
	if err != nil {
		return nil, nil, 0, ErrPaymentUpdateFailed
	}
	if current == nil {
		return nil, nil, 0, ErrPaymentNotFound
	}
	payments, err := tx.Payments().ListByOrderID(orderID)
	if err != nil {
		return nil, nil, 0, ErrPaymentUpdateFailed
	}
	return order, current, newerActivePaymentID(payments, paymentID), nil
}

// newerActivePaymentID 返回同订单上比 paymentID 更新且仍有效的支付 ID；没有则返回 0。
func newerActivePaymentID(payments []paymentdomain.Payment, paymentID uint) uint {
	for _, p := range payments {
		if p.ID > paymentID && (p.Status == constants.PaymentStatusInitiated || p.Status == constants.PaymentStatusPending || p.Status == constants.PaymentStatusSuccess) {
			return p.ID
		}
	}
	return 0
}

// takenOverResult 旧尝试被并发支付接管：订单已付清则如实返回已支付，否则返回订单状态错误提示刷新。
func takenOverResult(order *orderdomain.Order) (*CreatePaymentResult, error) {
	if order.PaidAt != nil {
		return &CreatePaymentResult{OrderPaid: true, WalletPaidAmount: order.WalletPaidAmount, OnlinePayAmount: order.OnlinePaidAmount}, nil
	}
	return nil, orderapp.ErrOrderStatusInvalid
}
