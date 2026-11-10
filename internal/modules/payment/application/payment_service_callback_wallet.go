package application

import (
	"fmt"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	"strings"
	"time"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
)

func (s *PaymentService) handleWalletRechargeCallback(payment *paymentdomain.Payment, status string, input PaymentCallbackInput) (*paymentdomain.Payment, error) {
	log := paymentLogger(
		"payment_id", payment.ID,
		"recharge_no", strings.TrimSpace(input.OrderNo),
		"target_status", status,
		"callback_channel_id", input.ChannelID,
		"callback_provider_ref", strings.TrimSpace(input.ProviderRef),
		"callback_currency", strings.ToUpper(strings.TrimSpace(input.Currency)),
		"callback_amount", input.Amount.String(),
	)
	if s.walletRepo == nil {
		log.Errorw("wallet_recharge_callback_wallet_repo_nil")
		return nil, ErrPaymentUpdateFailed
	}
	recharge, err := s.walletRepo.GetRechargeOrderByPaymentID(payment.ID)
	if err != nil {
		log.Errorw("wallet_recharge_callback_recharge_fetch_failed", "error", err)
		return nil, ErrPaymentUpdateFailed
	}
	if recharge == nil {
		log.Warnw("wallet_recharge_callback_recharge_not_found")
		return nil, walletcontract.ErrRechargeNotFound
	}

	if err := validateWalletCallbackPaymentFacts(payment, recharge.RechargeNo, status, input); err != nil {
		return nil, err
	}

	// 所有通知均在同一事务重读，不能用锁外快照绕过流水占位或覆盖已成功付款。

	now := time.Now()
	updated, newlySucceeded, err := s.applyWalletRechargePaymentUpdate(payment, status, input, now)
	if err != nil {
		log.Errorw("wallet_recharge_callback_apply_failed", "error", err)
		return nil, err
	}
	log.Infow("wallet_recharge_callback_processed",
		"new_status", updated.Status,
	)
	if newlySucceeded {
		reloadedRecharge, reloadErr := s.walletRepo.GetRechargeOrderByPaymentID(payment.ID)
		if reloadErr != nil {
			log.Warnw("wallet_recharge_callback_reload_failed", "error", reloadErr)
		} else if reloadedRecharge != nil {
			recharge = reloadedRecharge
		}
		if recharge != nil {
			recharge.Status = constants.WalletRechargeStatusSuccess
			recharge.PaidAt = updated.PaidAt
			recharge.UpdatedAt = updated.UpdatedAt
		}
		if s.memberLevelSvc != nil && recharge != nil && recharge.UserID > 0 {
			if err := s.memberLevelSvc.OnRechargeCompleted(recharge.UserID, recharge.Amount.Decimal); err != nil {
				paymentLogger().Warnw("member_level_recharge_completed_failed",
					"payment_id", payment.ID,
					"user_id", recharge.UserID,
					"amount", recharge.Amount.Decimal.String(),
					"error", err,
				)
			}
		}
		s.enqueueWalletRechargeSuccessAsync(recharge, updated, log)
		// Telegram delivery is emitted only from the payment transaction below.
		// Never fall back to login identity/global bot routing after commit.
	}
	return updated, nil
}

func validateWalletCallbackPaymentFacts(payment *paymentdomain.Payment, rechargeNo, status string, input PaymentCallbackInput) error {
	return validateCallbackPaymentFacts(payment, rechargeNo, status, input)
}

func (s *PaymentService) applyWalletRechargePaymentUpdate(payment *paymentdomain.Payment, status string, input PaymentCallbackInput, now time.Time) (*paymentdomain.Payment, bool, error) {
	paymentVal := payment
	newlySucceeded := false

	err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		paymentRepo := tx.Payments()
		walletTx := tx.Wallets()
		rechargeRepo := walletTx.Wallets()

		if locker, ok := paymentRepo.(paymentcontract.CreationLocker); ok {
			if err := locker.LockCreation(0); err != nil {
				return err
			}
		}
		lockedPayment, err := paymentRepo.GetByIDForUpdate(payment.ID)
		if err != nil {
			return ErrPaymentUpdateFailed
		}
		recharge, err := rechargeRepo.GetRechargeOrderByPaymentIDForUpdate(payment.ID)
		if err != nil {
			return ErrPaymentUpdateFailed
		}
		if recharge == nil {
			return walletcontract.ErrRechargeNotFound
		}
		if err := validateWalletCallbackPaymentFacts(lockedPayment, recharge.RechargeNo, status, input); err != nil {
			return err
		}
		adoptVerifiedLegacyDujiaoPayCurrency(lockedPayment, status, input)
		paymentVal = lockedPayment
		if blocked, err := guardCallbackEvidence(paymentRepo, lockedPayment, status, input, now); blocked || err != nil {
			return err
		}
		if handled, err := preserveSuccessfulReceipt(paymentRepo, lockedPayment, status, input, now); handled {
			return err
		}
		if lockedPayment.Status == status {
			_, err := updateCallbackMetaWithRepo(paymentRepo, lockedPayment, status, input)
			return err
		}
		if !canApplyWalletRechargeCallback(lockedPayment.Status, recharge.Status, status) {
			_, err := updateCallbackMetaWithRepo(paymentRepo, lockedPayment, lockedPayment.Status, input)
			return err
		}

		switch status {
		case constants.PaymentStatusSuccess:
			paidAt := now
			if input.PaidAt != nil {
				paidAt = *input.PaidAt
			}
			lockedPayment.PaidAt = &paidAt
		case constants.PaymentStatusExpired:
			lockedPayment.ExpiredAt = &now
		}
		lockedPayment.Status = status
		lockedPayment.CallbackAt = &now
		lockedPayment.UpdatedAt = now
		if input.ProviderRef != "" {
			lockedPayment.ProviderRef = input.ProviderRef
		}
		if input.Payload != nil {
			lockedPayment.ProviderPayload = mergeProviderPayload(lockedPayment.ProviderPayload, input.Payload)
		}
		if err := paymentRepo.Update(lockedPayment); err != nil {
			return ErrPaymentUpdateFailed
		}
		if recharge.Status == constants.WalletRechargeStatusSuccess {
			return nil
		}

		switch status {
		case constants.PaymentStatusSuccess:
			if s.walletSvc == nil {
				return walletcontract.ErrAccountNotFound
			}
			if _, err := s.walletSvc.ApplyRechargePayment(walletTx, recharge); err != nil {
				return err
			}
			newlySucceeded = true
			events := delivery.From(tx.Orders())
			source := &delivery.Source{BotID: lockedPayment.SourceBotID, TelegramID: lockedPayment.SourceTelegramID, PrivateChatID: lockedPayment.SourcePrivateChatID}
			if source.BotID == 0 {
				// Legacy pending recharges have no checkout source. Resolve only
				// today's trusted contact, never a historical binding or order.
				source = nil
				if resolver, ok := events.(delivery.CurrentSourceResolver); ok {
					source, err = resolver.ResolveCurrentSource(recharge.UserID, 0)
					if err != nil {
						return err
					}
				}
			}
			if source != nil {
				if events == nil {
					return ErrPaymentUpdateFailed
				}
				if err := events.Append(&delivery.Event{EventKey: fmt.Sprintf("recharge:%d:succeeded", recharge.ID), Kind: delivery.EventRechargeSucceeded, RechargeID: recharge.ID, PaymentID: lockedPayment.ID, UserID: recharge.UserID, BotID: source.BotID, TelegramID: source.TelegramID, PrivateChatID: source.PrivateChatID}); err != nil {
					return err
				}
			}
			recharge.Status = constants.WalletRechargeStatusSuccess
			paidAt := now
			if lockedPayment.PaidAt != nil {
				paidAt = *lockedPayment.PaidAt
			}
			recharge.PaidAt = &paidAt
		case constants.PaymentStatusFailed:
			recharge.Status = constants.WalletRechargeStatusFailed
		case constants.PaymentStatusExpired:
			recharge.Status = constants.WalletRechargeStatusExpired
		default:
			recharge.Status = constants.WalletRechargeStatusPending
		}
		recharge.UpdatedAt = now
		if err := rechargeRepo.UpdateRechargeOrder(recharge); err != nil {
			return ErrPaymentUpdateFailed
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return paymentVal, newlySucceeded, nil
}

func canApplyWalletRechargeCallback(paymentStatus string, rechargeStatus string, targetStatus string) bool {
	// 成功回调允许覆盖终态（支付网关存在延迟成功通知场景）。
	if targetStatus == constants.PaymentStatusSuccess {
		return true
	}
	// 非成功回调不允许改变任何终态，避免 expired/failed/success 被回调串扰重开。
	if paymentStatus == constants.PaymentStatusSuccess || rechargeStatus == constants.WalletRechargeStatusSuccess {
		return false
	}
	if paymentStatus == constants.PaymentStatusFailed || rechargeStatus == constants.WalletRechargeStatusFailed {
		return false
	}
	if paymentStatus == constants.PaymentStatusExpired || rechargeStatus == constants.WalletRechargeStatusExpired {
		return false
	}
	return true
}
