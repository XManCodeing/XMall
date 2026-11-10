package refund_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	refund "github.com/XManCodeing/XMall/internal/modules/order/application/refund"
	c "github.com/XManCodeing/XMall/internal/modules/order/contract"
	d "github.com/XManCodeing/XMall/internal/modules/order/domain"
	store "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	wallet "github.com/XManCodeing/XMall/internal/modules/wallet/application"
	walletstore "github.com/XManCodeing/XMall/internal/modules/wallet/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// combinedFixture creates actual money/command tables on a single SQLite connection.
func combinedFixture(t *testing.T) (*refund.Service, *gorm.DB, c.MembershipRefundInput) {
	t.Helper()
	_, db := setupOrderRefundServiceTest(t)
	if err := db.AutoMigrate(&d.MembershipRefundRequest{}, &membership.OrderItemSnapshot{}, &membership.RefundNotice{}); err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	now := time.Now().UTC()
	order := d.Order{ID: 900, OrderNo: "COMBINED-900", UserID: 99, Status: constants.OrderStatusCompleted, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), PaidAt: &now, CreatedAt: now}
	item := d.OrderItem{TitleJSON: jsonmap.JSON{"zh-CN": "会员"}, ID: 901, OrderID: 900, ProductID: 22, SKUID: 23, Quantity: 1}
	snapshot := membership.OrderItemSnapshot{OrderItemID: 901, ProductID: 22, SKUID: 23, AccountID: 99, BeneficiaryTelegramID: 123, Targets: []membership.SnapshotTarget{{TargetID: 4}}, Quantity: 1, Kind: "months", Months: 1}
	for _, value := range []any{&order, &item, &snapshot} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	wallets := walletstore.New(db)
	service := refund.New(store.New(db, "test-guest-credential-secret-with-32-bytes"), nil, nil, nil, wallet.NewService(wallet.Options{Repository: wallets, Transactions: wallets}), nil)
	return service, db, c.MembershipRefundInput{RequestKey: "combined-request-900", OrderID: 900, ActorAdminID: 8, Method: "manual", Amount: "10.00", ItemIDs: []uint{901}}
}

// TestCombinedRefundAtomicReplay verifies real manual/wallet mutations, persisted intent and idempotent replay.
func TestCombinedRefundAtomicReplay(t *testing.T) {
	for _, method := range []string{"manual", "wallet"} {
		t.Run(method, func(t *testing.T) {
			service, db, input := combinedFixture(t)
			input.Method = method
			first, err := service.CommitMembershipRefund(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := service.CommitMembershipRefund(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if first.RefundID == 0 || first.RefundID != second.RefundID || second.RightsState != "pending" {
				t.Fatalf("bad result: %+v %+v", first, second)
			}
			var count int64
			if err := db.Model(&d.OrderRefundRecord{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("refund count=%d err=%v", count, err)
			}
			input.RequestKey = "different-request-900"
			if _, err := service.CommitMembershipRefund(context.Background(), input); !errors.Is(err, c.ErrRefundRequestConflict) {
				t.Fatalf("new key must not refund same purchase twice: %v", err)
			}
			input.RequestKey = "combined-request-900"
			input.Amount = "11.00"
			if _, err := service.CommitMembershipRefund(context.Background(), input); !errors.Is(err, c.ErrRefundRequestConflict) {
				t.Fatalf("expected conflict: %v", err)
			}
		})
	}
}

// TestCombinedRefundFailureRollsBack ensures malformed selection and missing command linkage never leak money.
func TestCombinedRefundFailureRollsBack(t *testing.T) {
	for _, invalid := range []string{"item", "amount", "bind"} {
		t.Run(invalid, func(t *testing.T) {
			service, db, input := combinedFixture(t)
			switch invalid {
			case "item":
				input.ItemIDs = []uint{999}
			case "amount":
				input.Amount = "100.01"
			case "bind":
				if err := db.Exec("CREATE TRIGGER reject_command_binding BEFORE UPDATE OF refund_id ON membership_refund_requests BEGIN SELECT RAISE(ABORT, 'test binding failure'); END").Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.CommitMembershipRefund(context.Background(), input); err == nil {
				t.Fatal("expected failure")
			}
			var records, commands int64
			if err := db.Model(&d.OrderRefundRecord{}).Count(&records).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&d.MembershipRefundRequest{}).Count(&commands).Error; err != nil {
				t.Fatal(err)
			}
			if records != 0 || commands != 0 {
				t.Fatalf("partial commit records=%d commands=%d", records, commands)
			}
		})
	}
}

// TestCombinedRefundExistingOnlyLinks verifies a real prior record is reused without a second refund.
func TestCombinedRefundExistingOnlyLinks(t *testing.T) {
	service, db, input := combinedFixture(t)
	_, record, err := service.AdminManualRefund(refund.AdminManualRefundInput{OrderID: input.OrderID, Amount: money.FromDecimal(decimal.NewFromInt(10))})
	if err != nil {
		t.Fatal(err)
	}
	input.Method = "existing"
	input.Amount = ""
	input.ExistingRefundID = record.ID
	result, err := service.CommitMembershipRefund(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RefundID != record.ID {
		t.Fatal("refund not linked")
	}
	var count int64
	if err := db.Model(&d.OrderRefundRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
