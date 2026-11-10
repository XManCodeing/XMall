package identitymigration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// fixture creates actual SQLite tables and synthetic records on a single connection.
// It returns the reader, database, trusted observation time and normalized request fixture.
func fixture(t *testing.T) (*Store, *gorm.DB, time.Time, c.IdentityMigrationInput) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&d.LedgerSource{}, &d.LedgerRevocation{}, &d.LedgerProjection{}, &d.AccessMember{}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, deleted_at DATETIME)",
		"CREATE TABLE user_oauth_identities (id INTEGER PRIMARY KEY, user_id INTEGER, provider TEXT, provider_user_id TEXT)",
		"CREATE TABLE telegram_group_targets (id INTEGER PRIMARY KEY, purpose TEXT, enabled BOOLEAN, recovery_task_id INTEGER, version INTEGER, route_version INTEGER, bot_instance_id INTEGER, telegram_chat_id INTEGER)",
		"INSERT INTO users VALUES (7, 'active', NULL)",
		"INSERT INTO user_oauth_identities VALUES (1, 7, 'telegram', '9007199254740995')",
		"INSERT INTO telegram_group_targets VALUES (1, 'membership', true, 0, 9, 11, 5, -10001)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	mustCreate(t, db, &d.LedgerSource{ID: 9007199254740993, OrderItemID: 8, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, PaidAt: now.AddDate(0, 0, -1), Kind: d.TermMonths, Months: 1, SnapshotHash: "synthetic-secret-must-not-leak"})
	mustCreate(t, db, &d.LedgerProjection{BeneficiaryTelegramID: 9007199254740993, TargetID: 1, Version: 9007199254740993, Active: false})
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return store, db, now, c.IdentityMigrationInput{AccountID: 7, OldTelegramID: "9007199254740993", NewTelegramID: "9007199254740995", TargetIDs: []string{"1"}, ExpectedVersions: map[string]string{"1": "9007199254740993"}}
}

// mustCreate inserts synthetic value into db, failing t on any error.
func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatal(err)
	}
}

// preview calls the real reader with a deadline, detecting single-connection deadlocks.
func preview(t *testing.T, store *Store, input c.IdentityMigrationInput, at time.Time) (*c.IdentityMigrationPreview, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return store.PreviewIdentityMigration(ctx, input, at)
}

// TestPreviewReadOnlySingleConnection proves repeated previews cannot write even projections.
// SQLite query_only rejects all mutations; total_changes independently stays constant.
func TestPreviewReadOnlySingleConnection(t *testing.T) {
	store, db, now, input := fixture(t)
	input.TargetIDs = []string{" 01 ", "1"}
	input.OldTelegramID = " 09007199254740993 "
	var before, after int64
	if err := db.Raw("SELECT total_changes()").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA query_only=ON").Error; err != nil {
		t.Fatal(err)
	}
	first, err := preview(t, store, input, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := preview(t, store, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first.Targets) != 1 || !first.BindingMatchesNewID || len(first.Blockers) != 0 {
		t.Fatalf("unexpected snapshot: %+v", first)
	}
	target := first.Targets[0]
	expected := now.AddDate(0, 0, -1).AddDate(0, 1, 0)
	if target.ExpiresAt == nil || !target.ExpiresAt.Equal(expected) || target.EntitlementVersion != "9007199254740993" || target.Sources[0].ID != "9007199254740993" || target.GroupVersion != "9" || target.RouteVersion != "11" {
		t.Fatalf("lost original term/version: %+v", target)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "hash") {
		t.Fatalf("secret output: %s", encoded)
	}
	if err := db.Raw("SELECT total_changes()").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("preview wrote data: %d -> %d", before, after)
	}
}

// TestPreviewRejectsInvalidEvidence covers identity, refund, expiry, route and version failures.
func TestPreviewRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name, sql string
		want      error
	}{
		{"wrong_account", "UPDATE telegram_membership_ledger_sources SET account_id=9", c.ErrUntrustedIdentity},
		{"wrong_old_id", "UPDATE telegram_membership_ledger_sources SET beneficiary_telegram_id=123", c.ErrUntrustedIdentity},
		{"disabled", "UPDATE users SET status='disabled'", c.ErrUntrustedIdentity},
		{"deleted", "UPDATE users SET deleted_at='2026-01-01'", c.ErrUntrustedIdentity},
		{"missing_account", "DELETE FROM users", c.ErrUntrustedIdentity},
		{"missing_target", "DELETE FROM telegram_group_targets", c.ErrNotFound},
		{"disabled_target", "UPDATE telegram_group_targets SET enabled=false", c.ErrCapability},
		{"recovery", "UPDATE telegram_group_targets SET recovery_task_id=5", c.ErrCapability},
		{"stale_version", "UPDATE telegram_membership_ledger_projections SET version=4", c.ErrConflict},
		{"missing_projection", "DELETE FROM telegram_membership_ledger_projections", c.ErrConflict},
		{"expired", "UPDATE telegram_membership_ledger_sources SET paid_at='2020-01-01 00:00:00+00:00'", c.ErrConflict},
		{"future", "UPDATE telegram_membership_ledger_sources SET paid_at='2030-01-01 00:00:00+00:00'", c.ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, db, now, input := fixture(t)
			if err := db.Exec(test.sql).Error; err != nil {
				t.Fatal(err)
			}
			result, err := preview(t, store, input, now)
			if result != nil || !errors.Is(err, test.want) {
				t.Fatalf("got %+v / %v, want %v", result, err, test.want)
			}
		})
	}
}

// TestPreviewRefundAndPermanent uses real tombstones and original terms, not cached active flags.
func TestPreviewRefundAndPermanent(t *testing.T) {
	store, db, now, input := fixture(t)
	mustCreate(t, db, &d.LedgerRevocation{OrderItemID: 8, TargetID: 1, BeneficiaryTelegramID: 9007199254740993, RevokedAt: now})
	if _, err := preview(t, store, input, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("refund still active: %v", err)
	}
	mustCreate(t, db, &d.LedgerSource{OrderItemID: 9, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, PaidAt: now.AddDate(-1, 0, 0), Kind: d.TermPermanent})
	result, err := preview(t, store, input, now)
	if err != nil {
		t.Fatal(err)
	}
	target := result.Targets[0]
	if target.Kind != d.TermPermanent || target.ExpiresAt != nil || len(target.Sources) != 2 || !target.Sources[1].Revoked {
		t.Fatalf("permanent snapshot: %+v", target)
	}
	// A conflicting identity tombstone cannot be silently ignored.
	if err := db.Model(&d.LedgerRevocation{}).Where("order_item_id=8").Update("beneficiary_telegram_id", 99).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := preview(t, store, input, now); !errors.Is(err, d.ErrEntitlementScope) {
		t.Fatalf("mismatched tombstone: %v", err)
	}
}

// TestPreviewAllTargetsAndRenewal verifies every selected target and recalculates refunds without resetting time.
func TestPreviewAllTargetsAndRenewal(t *testing.T) {
	store, db, now, input := fixture(t)
	if err := db.Exec("INSERT INTO telegram_group_targets VALUES (2, 'membership', true, 0, 2, 3, 5, -10002)").Error; err != nil {
		t.Fatal(err)
	}
	mustCreate(t, db, &d.LedgerProjection{BeneficiaryTelegramID: 9007199254740993, TargetID: 2, Version: 1})
	mustCreate(t, db, &d.LedgerSource{OrderItemID: 22, TargetID: 2, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, PaidAt: now.AddDate(-2, 0, 0), Kind: d.TermPermanent})
	input.TargetIDs = []string{"2", "1", "02"}
	input.ExpectedVersions["2"] = "1"
	result, err := preview(t, store, input, now)
	if err != nil || len(result.Targets) != 2 || result.Targets[0].TargetID != "1" || result.Targets[1].Kind != d.TermPermanent {
		t.Fatalf("batch snapshot: %+v / %v", result, err)
	}
	mustCreate(t, db, &d.LedgerSource{OrderItemID: 9, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, PaidAt: now, Kind: d.TermMonths, Months: 1})
	mustCreate(t, db, &d.LedgerRevocation{OrderItemID: 8, TargetID: 1, BeneficiaryTelegramID: 9007199254740993, RevokedAt: now})
	result, err = preview(t, store, input, now)
	if err != nil {
		t.Fatal(err)
	}
	expected := now.AddDate(0, 1, 0)
	if result.Targets[0].ExpiresAt == nil || !result.Targets[0].ExpiresAt.Equal(expected) {
		t.Fatalf("refund stacked revoked contribution: %+v", result.Targets[0])
	}
	if err := db.Model(&d.LedgerSource{}).Where("target_id=2").Update("account_id", 99).Error; err != nil {
		t.Fatal(err)
	}
	if result, err := preview(t, store, input, now); result != nil || !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("partial batch accepted: %+v / %v", result, err)
	}
}

// TestPreviewBindingMismatchIsExplicit never changes the user's login identity.
func TestPreviewBindingMismatchIsExplicit(t *testing.T) {
	store, db, now, input := fixture(t)
	if err := db.Exec("UPDATE user_oauth_identities SET provider_user_id='9007199254740993'").Error; err != nil {
		t.Fatal(err)
	}
	result, err := preview(t, store, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingMatchesNewID || !reflect.DeepEqual(result.Blockers, []string{"account_binding_mismatch"}) {
		t.Fatalf("missing binding blocker: %+v", result)
	}
	var binding string
	if err := db.Table("user_oauth_identities").Select("provider_user_id").Scan(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding != "9007199254740993" {
		t.Fatal("binding changed")
	}
}
