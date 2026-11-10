package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	identitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	paymentgormstore "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gormstore"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
)

func TestExpireWalletRechargePaymentPendingToExpired(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected updated payment")
	}
	if updated.Status != constants.PaymentStatusExpired {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusExpired, updated.Status)
	}
	if updated.ExpiredAt == nil {
		t.Fatalf("expected payment expired_at set")
	}

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusExpired {
		t.Fatalf("reloaded payment status want %s got %s", constants.PaymentStatusExpired, refreshedPayment.Status)
	}
	if refreshedPayment.ExpiredAt == nil {
		t.Fatalf("reloaded payment expected expired_at set")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusExpired {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusExpired, refreshedRecharge.Status)
	}
}

func TestExpireWalletRechargePaymentDoesNotOverrideSuccess(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusSuccess, constants.WalletRechargeStatusSuccess)

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected updated payment")
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("reloaded payment status want %s got %s", constants.PaymentStatusSuccess, refreshedPayment.Status)
	}
	if refreshedPayment.PaidAt == nil {
		t.Fatalf("success payment should keep paid_at")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusSuccess, refreshedRecharge.Status)
	}
}

func TestExpireWalletRechargePaymentSkipsOrderPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	payment := &paymentdomain.Payment{
		OrderID:         99,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(100)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected payment result")
	}
	if updated.Status != constants.PaymentStatusPending {
		t.Fatalf("order payment should remain pending, got %s", updated.Status)
	}
}

func TestWalletRechargeCallbackSuccessAfterExpireCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.ExpireWalletRechargePayment(payment.ID); err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}

	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-1"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
}

func TestWalletRechargeCallbackSuccessThenExpireKeepsSuccess(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-2")); err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
}

func TestLegacyRechargeCurrentContactAndTransactionalRollback(t *testing.T) {
	for _, scenario := range []string{"current", "no_contact", "ambiguous", "revoked", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { sqlDB.Close() })
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(db.AutoMigrate(&delivery.Event{}, &identitydomain.Identity{}, &runtimedomain.Contact{}, &runtimedomain.Instance{}))
			svc.paymentRepo.(*paymentgormstore.Store).EnableBusinessEvents()
			payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
			if payment.SourceBotID != 0 {
				t.Fatal("fixture must be legacy")
			}
			must(db.Create(&userdomain.User{ID: recharge.UserID, Status: "active"}).Error)
			must(db.Create(&identitydomain.Identity{UserID: recharge.UserID, Provider: "telegram", ProviderUserID: "8"}).Error)
			must(db.Create(&runtimedomain.Instance{ID: 7, TelegramID: 77, Enabled: true, Mode: "internal"}).Error)
			if scenario != "no_contact" {
				must(db.Create(&runtimedomain.Contact{UserID: recharge.UserID, BotID: 7, TelegramUserID: 8, ChatID: 8, Reachable: true}).Error)
			}
			if scenario == "ambiguous" {
				must(db.Create(&runtimedomain.Instance{ID: 9, TelegramID: 99, Enabled: true, Mode: "internal"}).Error)
				must(db.Create(&runtimedomain.Contact{UserID: recharge.UserID, BotID: 9, TelegramUserID: 8, ChatID: 8, Reachable: true}).Error)
			}
			if scenario == "revoked" {
				must(db.Where("user_id = ?", recharge.UserID).Delete(&identitydomain.Identity{}).Error)
			}
			if scenario == "rollback" {
				must(db.Callback().Update().Before("gorm:update").Register("fail_recharge_final_update", func(tx *gorm.DB) {
					if tx.Statement.Table == "wallet_recharge_orders" {
						tx.AddError(errors.New("forced final update failure"))
					}
				}))
			}
			_, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "LEGACY-CURRENT"))
			if scenario == "rollback" {
				if err == nil {
					t.Fatal("expected rollback")
				}
				var saved paymentdomain.Payment
				must(db.First(&saved, payment.ID).Error)
				if saved.Status != constants.PaymentStatusPending {
					t.Fatal("payment escaped rollback")
				}
				var ledgerCount int64
				must(db.Model(&walletdomain.Transaction{}).Count(&ledgerCount).Error)
				if ledgerCount != 0 {
					t.Fatal("wallet ledger escaped rollback")
				}
			} else {
				must(err)
				assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
				_, err = svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "LEGACY-DUPLICATE"))
				must(err)
			}
			var rows []delivery.Event
			must(db.Find(&rows).Error)
			if scenario == "current" {
				if len(rows) != 1 || rows[0].BotID != 7 || rows[0].UserID != recharge.UserID || rows[0].RechargeID != recharge.ID {
					t.Fatalf("events=%+v", rows)
				}
			} else if len(rows) != 0 {
				t.Fatalf("unexpected events=%+v", rows)
			}
		})
	}
}

func TestWalletRechargeCallbackDuplicateSuccessDoesNotDuplicateCredit(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	if err := db.AutoMigrate(&delivery.Event{}); err != nil {
		t.Fatal(err)
	}
	svc.paymentRepo.(*paymentgormstore.Store).EnableBusinessEvents()
	payment.SourceBotID = 7
	payment.SourceTelegramID = 123
	payment.SourcePrivateChatID = 123
	if err := db.Save(payment).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var count int64
		db.Model(&delivery.Event{}).Where("recharge_id = ?", recharge.ID).Count(&count)
		if count != 1 {
			t.Errorf("recharge events: %d", count)
		}
	})

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-FIRST")); err != nil {
		t.Fatalf("handle first wallet recharge callback failed: %v", err)
	}
	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-SECOND")); err != nil {
		t.Fatalf("handle second wallet recharge callback failed: %v", err)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)

	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var txnCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&txnCount).Error; err != nil {
		t.Fatalf("count wallet transaction failed: %v", err)
	}
	if txnCount != 1 {
		t.Fatalf("wallet recharge success transaction count want 1 got %d", txnCount)
	}
}

func TestWalletRechargeCallbackConcurrentSuccessCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	const callbacks = 2
	var wait sync.WaitGroup
	errorsCh := make(chan error, callbacks)
	for index := 0; index < callbacks; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			input := buildWalletRechargeCallbackInput(
				payment,
				recharge,
				constants.PaymentStatusSuccess,
				fmt.Sprintf("CALLBACK-CONCURRENT-%d", index),
			)
			if _, callbackErr := svc.HandleCallback(input); callbackErr != nil {
				errorsCh <- callbackErr
			}
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for callbackErr := range errorsCh {
		t.Errorf("concurrent callback failed: %v", callbackErr)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var transactionCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&transactionCount).Error; err != nil {
		t.Fatalf("count wallet transaction: %v", err)
	}
	if transactionCount != 1 {
		t.Fatalf("concurrent success callbacks created %d credits, want 1", transactionCount)
	}
}

func TestWalletRechargeCallbackPendingAfterExpireDoesNotReopen(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.ExpireWalletRechargePayment(payment.ID); err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusPending, "CALLBACK-PENDING-LATE"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusExpired {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusExpired, updated.Status)
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusExpired {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusExpired, refreshedRecharge.Status)
	}
}

func TestWalletRechargeCallbackTerminalStateMatrixDoesNotReopen(t *testing.T) {
	cases := []struct {
		name              string
		paymentStatus     string
		rechargeStatus    string
		callbackStatus    string
		wantPaymentStatus string
		wantRechargeState string
	}{
		{
			name:              "expired_to_pending",
			paymentStatus:     constants.PaymentStatusExpired,
			rechargeStatus:    constants.WalletRechargeStatusExpired,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusExpired,
			wantRechargeState: constants.WalletRechargeStatusExpired,
		},
		{
			name:              "failed_to_pending",
			paymentStatus:     constants.PaymentStatusFailed,
			rechargeStatus:    constants.WalletRechargeStatusFailed,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusFailed,
			wantRechargeState: constants.WalletRechargeStatusFailed,
		},
		{
			name:              "failed_to_expired",
			paymentStatus:     constants.PaymentStatusFailed,
			rechargeStatus:    constants.WalletRechargeStatusFailed,
			callbackStatus:    constants.PaymentStatusExpired,
			wantPaymentStatus: constants.PaymentStatusFailed,
			wantRechargeState: constants.WalletRechargeStatusFailed,
		},
		{
			name:              "expired_to_failed",
			paymentStatus:     constants.PaymentStatusExpired,
			rechargeStatus:    constants.WalletRechargeStatusExpired,
			callbackStatus:    constants.PaymentStatusFailed,
			wantPaymentStatus: constants.PaymentStatusExpired,
			wantRechargeState: constants.WalletRechargeStatusExpired,
		},
		{
			name:              "success_to_pending",
			paymentStatus:     constants.PaymentStatusSuccess,
			rechargeStatus:    constants.WalletRechargeStatusSuccess,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusSuccess,
			wantRechargeState: constants.WalletRechargeStatusSuccess,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			payment, recharge := createWalletRechargeFixture(t, db, tc.paymentStatus, tc.rechargeStatus)

			providerRef := fmt.Sprintf("CALLBACK-%s-%d", tc.name, time.Now().UnixNano())
			updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, tc.callbackStatus, providerRef))
			if err != nil {
				t.Fatalf("handle wallet recharge callback failed: %v", err)
			}
			if updated.Status != tc.wantPaymentStatus {
				t.Fatalf("payment status want %s got %s", tc.wantPaymentStatus, updated.Status)
			}

			var refreshedPayment paymentdomain.Payment
			if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
				t.Fatalf("reload payment failed: %v", err)
			}
			if refreshedPayment.Status != tc.wantPaymentStatus {
				t.Fatalf("reloaded payment status want %s got %s", tc.wantPaymentStatus, refreshedPayment.Status)
			}

			var refreshedRecharge walletdomain.RechargeOrder
			if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
				t.Fatalf("reload recharge failed: %v", err)
			}
			if refreshedRecharge.Status != tc.wantRechargeState {
				t.Fatalf("recharge status want %s got %s", tc.wantRechargeState, refreshedRecharge.Status)
			}
		})
	}
}

func TestWalletRechargeCallbackSuccessAfterFailedCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusFailed, constants.WalletRechargeStatusFailed)

	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-AFTER-FAILED"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)

	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var txnCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&txnCount).Error; err != nil {
		t.Fatalf("count wallet transaction failed: %v", err)
	}
	if txnCount != 1 {
		t.Fatalf("wallet recharge success transaction count want 1 got %d", txnCount)
	}
}

type failWalletRechargeReloadRepository struct {
	walletcontract.Repository
	getCalls int
}

func (r *failWalletRechargeReloadRepository) GetRechargeOrderByPaymentID(paymentID uint) (*walletdomain.RechargeOrder, error) {
	r.getCalls++
	if r.getCalls > 1 {
		return nil, fmt.Errorf("forced recharge reload failure")
	}
	return r.Repository.GetRechargeOrderByPaymentID(paymentID)
}

type recordingMemberLevelProgressor struct {
	rechargeUserID uint
	rechargeAmount decimal.Decimal
}

func (r *recordingMemberLevelProgressor) OnOrderPaid(uint, decimal.Decimal) error {
	return nil
}

func (r *recordingMemberLevelProgressor) OnRechargeCompleted(userID uint, amount decimal.Decimal) error {
	r.rechargeUserID = userID
	r.rechargeAmount = amount
	return nil
}

func TestWalletRechargeCallbackReloadFailureKeepsNotificationContext(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	svc.walletRepo = &failWalletRechargeReloadRepository{Repository: svc.walletRepo}
	progressor := &recordingMemberLevelProgressor{}
	svc.SetMemberLevelService(progressor)

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-RELOAD-FAILURE")); err != nil {
		t.Fatalf("HandleCallback failed: %v", err)
	}
	if progressor.rechargeUserID != recharge.UserID {
		t.Fatalf("fallback recharge user id = %d, want %d", progressor.rechargeUserID, recharge.UserID)
	}
	if !progressor.rechargeAmount.Equal(recharge.Amount.Decimal) {
		t.Fatalf("fallback recharge amount = %s, want %s", progressor.rechargeAmount, recharge.Amount.String())
	}
}

func createWalletRechargeFixture(t *testing.T, db *gorm.DB, paymentStatus string, rechargeStatus string) (*paymentdomain.Payment, *walletdomain.RechargeOrder) {
	t.Helper()
	now := time.Now()
	payment := &paymentdomain.Payment{
		OrderID:         0,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          paymentStatus,
		ProviderRef:     fmt.Sprintf("RECHARGE-PAY-%d", now.UnixNano()),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if paymentStatus == constants.PaymentStatusSuccess {
		payment.PaidAt = &now
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	recharge := &walletdomain.RechargeOrder{
		RechargeNo:      fmt.Sprintf("WRTEST%d", now.UnixNano()),
		UserID:          1,
		PaymentID:       payment.ID,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		PayableAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          rechargeStatus,
		Remark:          "test",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if rechargeStatus == constants.WalletRechargeStatusSuccess {
		recharge.PaidAt = &now
	}
	if err := db.Create(recharge).Error; err != nil {
		t.Fatalf("create recharge failed: %v", err)
	}
	return payment, recharge
}

func buildWalletRechargeCallbackInput(payment *paymentdomain.Payment, recharge *walletdomain.RechargeOrder, status string, providerRef string) PaymentCallbackInput {
	return PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     recharge.RechargeNo,
		ChannelID:   payment.ChannelID,
		Status:      status,
		ProviderRef: providerRef,
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      ptrTime(time.Now()),
		Payload: jsonmap.JSON{
			"provider_ref": providerRef,
			"status":       status,
		},
	}
}

func assertWalletRechargeSuccessState(t *testing.T, db *gorm.DB, paymentID uint, rechargeID uint, userID uint, rechargeAmount decimal.Decimal) {
	t.Helper()

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, paymentID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, refreshedPayment.Status)
	}
	if refreshedPayment.PaidAt == nil {
		t.Fatalf("payment should set paid_at")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, rechargeID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusSuccess, refreshedRecharge.Status)
	}
	if refreshedRecharge.PaidAt == nil {
		t.Fatalf("recharge should set paid_at")
	}

	var account walletdomain.Account
	if err := db.Where("user_id = ?", userID).First(&account).Error; err != nil {
		t.Fatalf("reload wallet account failed: %v", err)
	}
	if !account.Balance.Decimal.Equal(rechargeAmount) {
		t.Fatalf("wallet balance want %s got %s", rechargeAmount.String(), account.Balance.String())
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func signDujiaoPayWebhook(body []byte, secret string) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return map[string]string{
		"DJP-Webhook-Timestamp": timestamp,
		"DJP-Webhook-Signature": hex.EncodeToString(mac.Sum(nil)),
	}
}

func createDujiaoPayChannelFixture(t *testing.T, db *gorm.DB) *paymentdomain.PaymentChannel {
	t.Helper()
	now := time.Now()
	channel := &paymentdomain.PaymentChannel{
		Name:            "DujiaoPay test",
		ProviderType:    constants.PaymentProviderDujiaoPay,
		ChannelType:     "tron-usdt",
		InteractionMode: constants.PaymentInteractionRedirect,
		ConfigJSON: jsonmap.JSON{
			"api_base_url":   "https://pay.example.test",
			"api_key_id":     "test-key",
			"api_secret":     "test-api-secret",
			"webhook_secret": "test-dujiaopay-webhook-secret",
			"order_mode":     constants.PaymentDujiaoPayOrderModeTransaction,
			"chain":          "tron",
			"token_id":       "tron-usdt",
			"fiat_currency":  "CNY",
		},
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create DujiaoPay channel failed: %v", err)
	}
	return channel
}

func TestSupportsBlindWebhookCandidateMatching(t *testing.T) {
	cases := []struct {
		channelType string
		want        bool
	}{
		{constants.PaymentChannelTypeWechat, true},
		{constants.PaymentChannelTypeStripe, true},
		{constants.PaymentChannelTypePaypal, false},
		{constants.PaymentChannelTypeAlipay, false},
		{"", false},
		{"  WECHAT  ", true},
	}
	for _, tc := range cases {
		if got := supportsBlindWebhookCandidateMatching(tc.channelType); got != tc.want {
			t.Fatalf("channelType=%q: got %v, want %v", tc.channelType, got, tc.want)
		}
	}
}

// TestHandleWechatWebhookFallbackNoCandidate 验证 channel_id 为 0 且没有任何 active wechat
// channel 时,wechat webhook 返回 ErrPaymentChannelNotFound(而非 ErrPaymentInvalid);
// 这是相对 67bbc47 重构后回归行为(硬性拒绝 channel_id==0)的关键修复。
func TestHandleWechatWebhookFallbackNoCandidate(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)

	_, _, err := svc.HandleWechatWebhook(WebhookCallbackInput{
		ChannelID: 0,
		Body:      []byte(`{"resource":{}}`),
		Headers:   map[string]string{},
	})
	if !errors.Is(err, ErrPaymentChannelNotFound) {
		t.Fatalf("expected ErrPaymentChannelNotFound for empty wechat candidate list, got: %v", err)
	}
}

// TestHandlePaypalWebhookRequiresChannelID 验证 paypal webhook 必须带 channel_id,
// 不具备盲匹配能力;channel_id==0 直接走 ErrPaymentInvalid。
func TestHandlePaypalWebhookRequiresChannelID(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)

	_, _, err := svc.HandlePaypalWebhook(WebhookCallbackInput{
		ChannelID: 0,
		Body:      []byte(`{}`),
		Headers:   map[string]string{},
	})
	if !errors.Is(err, ErrPaymentInvalid) {
		t.Fatalf("expected ErrPaymentInvalid for paypal without channel_id, got: %v", err)
	}
}

// scriptedGateway 第 N 次网关调用执行 steps[N]，用来在旧请求等待网关期间插入并发操作。
type scriptedGateway struct {
	steps []func(paymentcontract.GatewayCreateInput) error
	calls *int
}

func (g scriptedGateway) Type() string                              { return "scripted" }
func (g scriptedGateway) ValidateConfig(jsonmap.JSON, string) error { return nil }
func (g scriptedGateway) CreatePayment(_ context.Context, _ jsonmap.JSON, input paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	n := *g.calls
	*g.calls++
	if n < len(g.steps) && g.steps[n] != nil {
		if err := g.steps[n](input); err != nil {
			return nil, err
		}
	}
	return &paymentcontract.GatewayCreateResult{QRCodeURL: "weixin://wxpay/bizpayurl?pr=scripted"}, nil
}

var errScriptedGatewayDown = errors.New("gateway 502")

func setupCreateRaceFixture(t *testing.T, balance int64, steps ...func(paymentcontract.GatewayCreateInput) error) (*PaymentService, *gorm.DB, *orderdomain.Order, *paymentdomain.PaymentChannel) {
	t.Helper()
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	user := &userdomain.User{Email: "race@example.com", PasswordHash: "hash", Status: constants.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(balance)), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create wallet account failed: %v", err)
	}
	channel, order := createFeePolicyOrderFixture(t, db, "Race Gateway", "DJ-CREATE-RACE")
	if err := db.Model(order).Updates(map[string]interface{}{
		"user_id": user.ID, "total_amount": "50.00", "original_amount": "50.00", "online_paid_amount": "50.00",
	}).Error; err != nil {
		t.Fatalf("update order failed: %v", err)
	}
	if err := db.Model(channel).Updates(map[string]interface{}{"fee_rate": "0", "fixed_fee": "0"}).Error; err != nil {
		t.Fatalf("update channel failed: %v", err)
	}
	calls := 0
	registerTestGateway(t, svc, channel.ProviderType, channel.ChannelType, scriptedGateway{steps: steps, calls: &calls})
	return svc, db, order, channel
}

func loadRaceState(t *testing.T, db *gorm.DB, orderID uint) (orderdomain.Order, walletdomain.Account, []paymentdomain.Payment, int64) {
	t.Helper()
	var order orderdomain.Order
	if err := db.First(&order, orderID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	var account walletdomain.Account
	if err := db.Where("user_id = ?", order.UserID).First(&account).Error; err != nil {
		t.Fatalf("reload account failed: %v", err)
	}
	var payments []paymentdomain.Payment
	if err := db.Where("order_id = ?", orderID).Order("id asc").Find(&payments).Error; err != nil {
		t.Fatalf("reload payments failed: %v", err)
	}
	var refunds int64
	if err := db.Model(&walletdomain.Transaction{}).Where("type = ?", constants.WalletTxnTypeOrderRefund).Count(&refunds).Error; err != nil {
		t.Fatalf("count refunds failed: %v", err)
	}
	return order, account, payments, refunds
}

func assertSupersededBy(t *testing.T, p paymentdomain.Payment, replacementID uint) {
	t.Helper()
	if p.Status != constants.PaymentStatusExpired || p.SupersededByPaymentID == nil || *p.SupersededByPaymentID != replacementID {
		t.Fatalf("payment %d want expired superseded by %d, got status=%s superseded_by=%v", p.ID, replacementID, p.Status, p.SupersededByPaymentID)
	}
}

func TestCreatePaymentLateSuccessDoesNotSupersedeNewerAttempt(t *testing.T) {
	var db *gorm.DB
	var newer paymentdomain.Payment
	svc, db, order, channel := setupCreateRaceFixture(t, 30, func(input paymentcontract.GatewayCreateInput) error {
		// 模拟一条仍在等待网关的更新尝试。
		if err := db.First(&newer, input.PaymentID).Error; err != nil {
			t.Fatalf("load attempt failed: %v", err)
		}
		newer.ID, newer.GatewayOrderNo, newer.Status = 0, "", constants.PaymentStatusInitiated
		if err := db.Create(&newer).Error; err != nil {
			t.Fatalf("create newer attempt failed: %v", err)
		}
		return nil
	})

	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()}); !errors.Is(err, orderapp.ErrOrderStatusInvalid) {
		t.Fatalf("older attempt must not hand out its link after a newer attempt, got %v", err)
	}

	_, _, payments, _ := loadRaceState(t, db, order.ID)
	assertSupersededBy(t, payments[0], newer.ID)
	if payments[1].Status != constants.PaymentStatusInitiated {
		t.Fatalf("newer attempt want untouched initiated got %s", payments[1].Status)
	}
}

func TestStalePaymentFailedCallbackKeepsNewerAttemptWallet(t *testing.T) {
	var svc *PaymentService
	var db *gorm.DB
	var oldPaymentID uint
	svc, db, order, channel := setupCreateRaceFixture(t, 30, nil, func(paymentcontract.GatewayCreateInput) error {
		// 新的混合支付还在等网关时，网关推来旧链接的失败通知。
		var old paymentdomain.Payment
		if err := db.First(&old, oldPaymentID).Error; err != nil {
			t.Fatalf("load old payment failed: %v", err)
		}
		if _, err := svc.HandleCallback(PaymentCallbackInput{
			PaymentID: old.ID, OrderNo: old.GatewayOrderNo, ChannelID: old.ChannelID,
			Status: constants.PaymentStatusFailed, Amount: old.Amount, Currency: old.Currency,
		}); err != nil {
			t.Fatalf("old payment failed callback: %v", err)
		}
		return nil
	})
	first, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
	if err != nil {
		t.Fatalf("create old online payment failed: %v", err)
	}
	oldPaymentID = first.Payment.ID
	second := *channel
	second.ID, second.Name = 0, "Race Gateway 2"
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second channel failed: %v", err)
	}

	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: second.ID, UseBalance: true, Context: context.Background()}); err != nil {
		t.Fatalf("newer mixed payment failed: %v", err)
	}

	stored, account, _, refunds := loadRaceState(t, db, order.ID)
	if got := account.Balance.StringFixed(2); got != "0.00" || refunds != 0 {
		t.Fatalf("stale failed callback released newer attempt's wallet: balance=%s refunds=%d", got, refunds)
	}
	if stored.WalletPaidAmount.StringFixed(2) != "30.00" {
		t.Fatalf("order wallet allocation want 30.00 got %s", stored.WalletPaidAmount.String())
	}
}

func TestCreateWalletRechargeGatewayFailureAfterEarlyCallbackKeepsSuccess(t *testing.T) {
	var svc *PaymentService
	var db *gorm.DB
	svc, db, _, channel := setupCreateRaceFixture(t, 0, func(input paymentcontract.GatewayCreateInput) error {
		var payment paymentdomain.Payment
		if err := db.First(&payment, input.PaymentID).Error; err != nil {
			t.Fatalf("early callback cannot locate payment: %v", err)
		}
		if payment.GatewayOrderNo == "" || payment.GatewayOrderNo != input.OrderNo {
			t.Fatalf("gateway order no must be persisted before the gateway request, db=%q sent=%q", payment.GatewayOrderNo, input.OrderNo)
		}
		var recharge walletdomain.RechargeOrder
		if err := db.Where("payment_id = ?", payment.ID).First(&recharge).Error; err != nil {
			t.Fatalf("load recharge failed: %v", err)
		}
		if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(&payment, &recharge, constants.PaymentStatusSuccess, "EARLY-1")); err != nil {
			t.Fatalf("early callback failed: %v", err)
		}
		return errScriptedGatewayDown
	})
	var user userdomain.User
	if err := db.Where("email = ?", "race@example.com").First(&user).Error; err != nil {
		t.Fatalf("load user failed: %v", err)
	}

	if _, err := svc.CreateWalletRechargePayment(CreateWalletRechargePaymentInput{
		UserID: user.ID, ChannelID: channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(100)), Currency: "CNY", Context: context.Background(),
	}); err == nil {
		t.Fatal("expected gateway failure")
	}

	var payment paymentdomain.Payment
	if err := db.Where("channel_id = ? AND order_id = 0", channel.ID).First(&payment).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	var recharge walletdomain.RechargeOrder
	if err := db.Where("payment_id = ?", payment.ID).First(&recharge).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if payment.Status != constants.PaymentStatusSuccess || recharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatalf("credited recharge was overwritten: payment=%s recharge=%s", payment.Status, recharge.Status)
	}
}

// TestCreatePaymentRejectsConcurrentPaymentWhileGatewayInFlight 本分支比上游更严格：
// 网关请求进行中（creation_state=processing）时，同订单任何渠道（含余额）都不能并发建单，
// 因此上游 cb61a4d 中"网关期间钱包支付"的竞态在此不会发生；旧尝试失败也不会误退余额。
func TestCreatePaymentRejectsConcurrentPaymentWhileGatewayInFlight(t *testing.T) {
	var svc *PaymentService
	var orderID uint
	svc, db, order, channel := setupCreateRaceFixture(t, 100, func(paymentcontract.GatewayCreateInput) error {
		_, err := svc.CreatePayment(CreatePaymentInput{OrderID: orderID, UseBalance: true, Context: context.Background()})
		if !errors.Is(err, ErrPaymentCreationUncertain) {
			t.Fatalf("concurrent wallet payment during gateway want ErrPaymentCreationUncertain, got %v", err)
		}
		return errScriptedGatewayDown
	})
	orderID = order.ID

	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()}); err == nil {
		t.Fatal("expected gateway failure")
	}
	stored, account, payments, refunds := loadRaceState(t, db, order.ID)
	if got := account.Balance.StringFixed(2); got != "100.00" || refunds != 0 {
		t.Fatalf("wallet must stay untouched: balance=%s refunds=%d", got, refunds)
	}
	if stored.Status != constants.OrderStatusPendingPayment || stored.WalletPaidAmount.StringFixed(2) != "0.00" {
		t.Fatalf("order changed: status=%s wallet=%s", stored.Status, stored.WalletPaidAmount.String())
	}
	if len(payments) != 1 {
		t.Fatalf("want exactly 1 payment attempt, got %d", len(payments))
	}
}
