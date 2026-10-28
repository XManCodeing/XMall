package gormstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/homles698698-lab/XMall/internal/constants"
	affiliatecontract "github.com/homles698698-lab/XMall/internal/modules/affiliate/contract"
	affiliatedomain "github.com/homles698698-lab/XMall/internal/modules/affiliate/domain"
	userdomain "github.com/homles698698-lab/XMall/internal/modules/identity/user/domain"
	orderdomain "github.com/homles698698-lab/XMall/internal/modules/order/domain"
	"github.com/homles698698-lab/XMall/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Exercise the real model with each dialect without connecting to a server.
// MySQL rejects DATETIME(3) DEFAULT CURRENT_TIMESTAMP (precision mismatch).
func TestClickCreationTimeAcrossDialects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect gorm.Dialector
	}{
		{"mysql", mysql.New(mysql.Config{DSN: "unused@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true})},
		{"postgres", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"})},
		{"sqlite", sqlite.Open(":memory:")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 26, 3, 0, 0, 123000000, time.UTC)
			db, err := gorm.Open(tc.dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, NowFunc: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			stmt := &gorm.Statement{DB: db}
			if err := stmt.Parse(&affiliatedomain.Click{}); err != nil {
				t.Fatal(err)
			}
			field := stmt.Schema.LookUpField("CreatedAt")
			ddl := db.Migrator().FullDataTypeOf(field).SQL
			if strings.Contains(strings.ToUpper(ddl), "DEFAULT") || field.HasDefaultValue {
				t.Fatalf("unexpected database timestamp default: %s", ddl)
			}
			if tc.name == "mysql" && !strings.Contains(ddl, "datetime(3)") {
				t.Fatalf("timestamp precision changed: %s", ddl)
			}
			for _, explicit := range []bool{false, true} {
				click := &affiliatedomain.Click{AffiliateProfileID: 1}
				want := now
				if explicit {
					want = now.Add(-time.Hour)
					click.CreatedAt = want
				}
				result := db.Create(click)
				if result.Error != nil {
					t.Fatal(result.Error)
				}
				if !click.CreatedAt.Equal(want) {
					t.Fatalf("creation time: got %v want %v", click.CreatedAt, want)
				}
				if !strings.Contains(result.Statement.SQL.String(), "created_at") {
					t.Fatal("creation time missing from INSERT")
				}
			}
		})
	}
}

func openTestStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:affiliate_store_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&userdomain.User{},
		&orderdomain.Order{},
		&affiliatedomain.Profile{},
		&affiliatedomain.Click{},
		&affiliatedomain.Commission{},
		&affiliatedomain.WithdrawRequest{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db), db
}

func createTestProfile(t *testing.T, store *Store, userID uint, code string) *affiliatedomain.Profile {
	t.Helper()
	profile := &affiliatedomain.Profile{
		UserID: userID, AffiliateCode: code, Status: constants.AffiliateProfileStatusActive,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateProfile(profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return profile
}

func TestStoreExcludesSoftDeletedAffiliateRows(t *testing.T) {
	store, db := openTestStore(t)
	user := &userdomain.User{Email: "affiliate-store@example.com", PasswordHash: "x", DisplayName: "affiliate", Status: constants.UserStatusActive}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	profile := createTestProfile(t, store, user.ID, "AFFSTORE")
	commission := &affiliatedomain.Commission{
		AffiliateProfileID: profile.ID,
		OrderID:            10,
		CommissionType:     constants.AffiliateCommissionTypeOrder,
		CommissionAmount:   money.FromDecimal(decimal.NewFromInt(12)),
		Status:             constants.AffiliateCommissionStatusAvailable,
	}
	if err := store.CreateCommission(commission); err != nil {
		t.Fatalf("create commission: %v", err)
	}
	request := &affiliatedomain.WithdrawRequest{
		AffiliateProfileID: profile.ID,
		Amount:             money.FromDecimal(decimal.NewFromInt(5)),
		Channel:            "usdt",
		Account:            "T-address",
		Status:             constants.AffiliateWithdrawStatusPendingReview,
	}
	if err := store.CreateWithdraw(request); err != nil {
		t.Fatalf("create withdraw: %v", err)
	}

	deletedAt := time.Now()
	for _, target := range []struct {
		model interface{}
		id    uint
	}{
		{model: &affiliatedomain.Profile{}, id: profile.ID},
		{model: &affiliatedomain.Commission{}, id: commission.ID},
		{model: &affiliatedomain.WithdrawRequest{}, id: request.ID},
	} {
		if err := db.Model(target.model).Where("id = ?", target.id).Update("deleted_at", deletedAt).Error; err != nil {
			t.Fatalf("mark deleted: %v", err)
		}
	}

	if row, err := store.GetProfileByID(profile.ID); err != nil || row != nil {
		t.Fatalf("deleted profile leaked through GetProfileByID: row=%+v err=%v", row, err)
	}
	profiles, total, err := store.ListProfiles(affiliatecontract.ProfileListFilter{Page: 1, PageSize: 20})
	if err != nil || total != 0 || len(profiles) != 0 {
		t.Fatalf("deleted profile leaked through ListProfiles: total=%d rows=%d err=%v", total, len(profiles), err)
	}
	if row, err := store.GetCommissionByOrderAndProfile(commission.OrderID, profile.ID, commission.CommissionType); err != nil || row != nil {
		t.Fatalf("deleted commission leaked: row=%+v err=%v", row, err)
	}
	commissions, total, err := store.ListCommissions(affiliatecontract.CommissionListFilter{Page: 1, PageSize: 20})
	if err != nil || total != 0 || len(commissions) != 0 {
		t.Fatalf("deleted commission leaked through list: total=%d rows=%d err=%v", total, len(commissions), err)
	}
	if row, err := store.GetWithdrawByID(request.ID); err != nil || row != nil {
		t.Fatalf("deleted withdraw leaked: row=%+v err=%v", row, err)
	}
}

func TestStoreDoesNotPreloadSoftDeletedCommissionOrder(t *testing.T) {
	store, db := openTestStore(t)
	user := &userdomain.User{Email: "affiliate-deleted-order@example.com", PasswordHash: "x", Status: constants.UserStatusActive}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	profile := createTestProfile(t, store, user.ID, "AFFDELETEDORDER")
	order := &orderdomain.Order{
		OrderNo:  "AFF-DELETED-ORDER",
		Status:   constants.OrderStatusPaid,
		Currency: "USD",
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	commission := &affiliatedomain.Commission{
		AffiliateProfileID: profile.ID,
		OrderID:            order.ID,
		CommissionType:     constants.AffiliateCommissionTypeOrder,
		CommissionAmount:   money.FromDecimal(decimal.NewFromInt(12)),
		Status:             constants.AffiliateCommissionStatusAvailable,
	}
	if err := store.CreateCommission(commission); err != nil {
		t.Fatalf("create commission: %v", err)
	}
	deletedAt := time.Now()
	if err := db.Model(order).Update("deleted_at", &deletedAt).Error; err != nil {
		t.Fatalf("soft delete order: %v", err)
	}

	rows, total, err := store.ListCommissions(affiliatecontract.CommissionListFilter{
		Page:     1,
		PageSize: 20,
		OrderID:  order.ID,
	})
	if err != nil {
		t.Fatalf("list commissions: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected commission to remain visible, total=%d rows=%d", total, len(rows))
	}
	if rows[0].Order.ID != 0 {
		t.Fatalf("soft-deleted order leaked through preload: %+v", rows[0].Order)
	}
}

func TestStoreWithinTransactionRollsBack(t *testing.T) {
	store, db := openTestStore(t)
	wantErr := errors.New("rollback")
	err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
		if err := tx.CreateProfile(&affiliatedomain.Profile{
			UserID: 99, AffiliateCode: "AFFROLLBACK", Status: constants.AffiliateProfileStatusActive,
		}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected rollback error, got %v", err)
	}
	var count int64
	if err := db.Model(&affiliatedomain.Profile{}).Where("affiliate_code = ?", "AFFROLLBACK").Count(&count).Error; err != nil {
		t.Fatalf("count profiles: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction committed %d profile rows", count)
	}
}
