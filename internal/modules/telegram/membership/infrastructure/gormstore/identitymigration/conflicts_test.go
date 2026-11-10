package identitymigration

import (
	"context"
	"errors"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// TestDestinationConflicts rejects binding aliases and local facts including stale member rows.
func TestDestinationConflicts(t *testing.T) {
	for _, kind := range []string{"binding", "binding_alias", "source", "projection", "revocation", "member", "physical_member"} {
		t.Run(kind, func(t *testing.T) {
			store, db, now, input := fixture(t)
			switch kind {
			case "binding", "binding_alias":
				raw := "9007199254740995"
				if kind == "binding_alias" {
					raw = "09007199254740995"
				}
				if err := db.Exec("INSERT INTO user_oauth_identities VALUES (2, 999, 'telegram', ?)", raw).Error; err != nil {
					t.Fatal(err)
				}
			case "source":
				mustCreate(t, db, &d.LedgerSource{OrderItemID: 77, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740995, AccountID: 7, PaidAt: now.AddDate(-5, 0, 0), Kind: d.TermMonths, Months: 1})
			case "projection":
				mustCreate(t, db, &d.LedgerProjection{BeneficiaryTelegramID: 9007199254740995, TargetID: 1, Version: 1, Active: false, Kind: d.TermMonths})
			case "revocation":
				mustCreate(t, db, &d.LedgerRevocation{OrderItemID: 77, TargetID: 1, BeneficiaryTelegramID: 9007199254740995, RevokedAt: now})
			case "member":
				mustCreate(t, db, &d.AccessMember{TargetID: 1, TelegramID: 9007199254740995, ChatID: -999, Status: "left"})
			case "physical_member":
				mustCreate(t, db, &d.AccessMember{TargetID: 999, TelegramID: 9007199254740995, ChatID: -10001, Status: "member"})
			}
			result, err := preview(t, store, input, now)
			if result != nil || !errors.Is(err, c.ErrConflict) {
				t.Fatalf("destination silently merged: %+v / %v", result, err)
			}
		})
	}
}

// TestPreviewOnlyProjectionDoesNotConflict preserves serialization roots without
// treating a quotation as purchased entitlement. Real term/history tests remain above.
func TestPreviewOnlyProjectionDoesNotConflict(t *testing.T) {
	store, db, at, input := commitFixture(t)
	mustCreate(t, db, &d.LedgerProjection{BeneficiaryTelegramID: 9007199254740995, TargetID: 1, Version: 7, CalculatedAt: at})
	if _, err := store.PreviewIdentityMigration(context.Background(), input.IdentityMigrationInput, at); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitIdentityMigration(context.Background(), input, at); err != nil {
		t.Fatal(err)
	}
	var root d.LedgerProjection
	if err := db.Where("beneficiary_telegram_id = ?", 9007199254740995).First(&root).Error; err != nil || root.Version != 7 {
		t.Fatalf("preview root rewritten: %+v %v", root, err)
	}
}

// TestInputBoundaries rejects numeric coercion/overflow and incomplete version snapshots.
func TestInputBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		change func(*c.IdentityMigrationInput)
	}{
		{"zero", func(input *c.IdentityMigrationInput) { input.OldTelegramID = "0" }},
		{"negative", func(input *c.IdentityMigrationInput) { input.NewTelegramID = "-1" }},
		{"signed", func(input *c.IdentityMigrationInput) { input.NewTelegramID = "+123" }},
		{"exponent", func(input *c.IdentityMigrationInput) { input.NewTelegramID = "1e3" }},
		{"overflow", func(input *c.IdentityMigrationInput) { input.NewTelegramID = "9223372036854775808" }},
		{"same", func(input *c.IdentityMigrationInput) { input.NewTelegramID = input.OldTelegramID }},
		{"no_targets", func(input *c.IdentityMigrationInput) { input.TargetIDs = nil }},
		{"target_overflow", func(input *c.IdentityMigrationInput) { input.TargetIDs = []string{"18446744073709551616"} }},
		{"too_many", func(input *c.IdentityMigrationInput) {
			input.TargetIDs = make([]string, 101)
			for index := range input.TargetIDs {
				input.TargetIDs[index] = "1"
			}
		}},
		{"version_overflow", func(input *c.IdentityMigrationInput) { input.ExpectedVersions["1"] = "18446744073709551616" }},
		{"version_conflict", func(input *c.IdentityMigrationInput) { input.ExpectedVersions["01"] = "2" }},
		{"unknown_version", func(input *c.IdentityMigrationInput) { input.ExpectedVersions["2"] = "2" }},
		{"missing_version", func(input *c.IdentityMigrationInput) { input.TargetIDs = []string{"1", "2"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _, now, input := fixture(t)
			test.change(&input)
			if result, err := preview(t, store, input, now); result != nil || !errors.Is(err, c.ErrInvalid) {
				t.Fatalf("invalid input accepted: %+v / %v", result, err)
			}
		})
	}
}

// TestVersionsSnapshotAndLargeInteger confirms optional snapshot and full uint64 comparison.
func TestVersionsSnapshotAndLargeInteger(t *testing.T) {
	store, _, now, input := fixture(t)
	input.ExpectedVersions = nil
	result, err := preview(t, store, input, now)
	if err != nil || result.Targets[0].EntitlementVersion != "9007199254740993" {
		t.Fatalf("snapshot: %+v / %v", result, err)
	}
	input.ExpectedVersions = map[string]string{"1": "18446744073709551615"}
	if _, err := preview(t, store, input, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("uint64 version not compared safely: %v", err)
	}
}
