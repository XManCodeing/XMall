package orderwiring

import (
	"context"
	"strings"
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
)

// TestAdminLedgerReadFacts exercises real storage, HTTP authorization, exact IDs and account-safe DTOs.
func TestAdminLedgerReadFacts(t *testing.T) {
	router, db := membershipDetailFixture(t)
	paidAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, model := range []any{
		&d.LedgerSource{OrderItemID: 11, TargetID: 21, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, Kind: "months", Months: 18, PaidAt: paidAt},
		&d.LedgerSource{OrderItemID: 14, TargetID: 21, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, Kind: "permanent", PaidAt: paidAt},
		&d.LedgerProjection{BeneficiaryTelegramID: 9007199254740993, TargetID: 21, Active: true, Kind: "permanent", CalculatedAt: paidAt},
		&d.LedgerOutbox{OrderItemID: 11, TargetID: 21, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, State: "pending"},
	} {
		if err := db.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	reader := membershipstore.New(db)
	rows, err := reader.OrderLedger(context.Background(), []uint{11, 11})
	if err != nil || len(rows) != 1 || len(rows[0].Sources) != 1 || rows[0].Sources[0].BeneficiaryTelegramID != "9007199254740993" {
		t.Fatalf("sources: %+v %v", rows, err)
	}
	if len(rows[0].Effective) != 1 || !rows[0].Effective[0].Permanent || rows[0].Effective[0].ExpiresAt != nil || !rows[0].Effective[0].PendingRestore {
		t.Fatalf("projection: %+v", rows[0])
	}
	body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders/2", true).Body.String()
	if !strings.Contains(body, `"membership_ledger"`) || !strings.Contains(body, `"state":"recorded"`) {
		t.Fatalf("missing enrichment: %s", body)
	}
	if body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders/2", false).Body.String(); strings.Contains(body, "membership_ledger") {
		t.Fatal("unauthorized disclosure")
	}
	if body := membershipDetailRequest(t, router, "/admin/orders/2", true).Body.String(); strings.Contains(body, "membership_ledger") {
		t.Fatal("ordinary API changed")
	}
	if err := db.Model(&d.LedgerSource{}).Where("order_item_id = ?", 11).Update("beneficiary_telegram_id", 42).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := reader.OrderLedger(context.Background(), []uint{11}); err == nil {
		t.Fatal("inconsistent identity accepted")
	}
}

// TestAdminLedgerPendingFailureAndEmpty separates absent purchases from persisted failed attempts.
func TestAdminLedgerPendingFailureAndEmpty(t *testing.T) {
	router, db := membershipDetailFixture(t)
	reader := membershipstore.New(db)
	rows, err := reader.OrderLedger(context.Background(), []uint{11, 13})
	if err != nil || len(rows) != 1 || rows[0].State != "pending" {
		t.Fatalf("pending: %+v %v", rows, err)
	}
	if err := db.Create(&d.PaidEvent{OrderID: 1, PaymentID: 8, PaidAt: time.Now(), Attempts: 1}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = reader.OrderLedger(context.Background(), []uint{11})
	if err != nil || len(rows) != 1 || rows[0].State != "failed" {
		t.Fatalf("failed: %+v %v", rows, err)
	}
	rows, err = reader.OrderLedger(context.Background(), nil)
	if err != nil || len(rows) != 0 {
		t.Fatal("empty scope read facts")
	}
	if err := db.Migrator().DropTable(&d.LedgerSource{}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.OrderLedger(context.Background(), []uint{11}); err == nil || err.Error() != "membership ledger read failed" {
		t.Fatalf("unsafe query failure: %v", err)
	}
	body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders/2", true).Body.String()
	if !strings.Contains(body, `"status_code":500`) || strings.Contains(body, "no such table") || strings.Contains(body, "telegram_membership_ledger_sources") {
		t.Fatalf("unsafe HTTP error: %s", body)
	}
}
