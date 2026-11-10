package routerecovery

import (
	"context"
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// TestUnattributedWorkAndMigrationStayBlocked verifies no shortcut around identity sequencing.
func TestUnattributedWorkAndMigrationStayBlocked(t *testing.T) {
	store, db, request := fixture(t)
	must(t, db.Create(&d.IdentityMigration{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, OldTelegramID: 42, NewTelegramID: 43, State: "awaiting_member"}).Error)
	must(t, db.Create(&d.AccessJoin{ID: 1, BotID: 1, ChatID: -10, UpdateID: 8, TelegramID: 42, State: "needs_review"}).Error)
	must(t, db.Exec("INSERT INTO telegram_bot_deliveries VALUES (1, 1, 'panel', 'processing')").Error)
	result, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if result.Verified || result.Unknown != 3 {
		t.Fatalf("unresolved responsibility passed: %+v", result)
	}
	var mapping d.IdentityMigration
	must(t, db.First(&mapping, 1).Error)
	if mapping.State != "awaiting_member" || mapping.ChatID != -10 {
		t.Fatal("identity sequence bypassed")
	}
	var operations int64
	must(t, db.Model(&d.AccessOperation{}).Count(&operations).Error)
	if operations != 0 {
		t.Fatal("unexpected platform operation enqueued")
	}
}

// TestDeletedEvidenceRemainsUnknown prevents missing local history from manufacturing closure.
func TestDeletedEvidenceRemainsUnknown(t *testing.T) {
	store, db, request := fixture(t)
	invite := d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "unknown"}
	must(t, db.Create(&invite).Error)
	_, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	must(t, db.Delete(&invite).Error)
	result, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if result.Verified || result.Unknown != 1 {
		t.Fatalf("deleted evidence passed: %+v", result)
	}
}

// TestAliasAndUntrustedExpiryStayUnknown includes physical aliases and unsafe old-link expiry.
func TestAliasAndUntrustedExpiryStayUnknown(t *testing.T) {
	store, db, request := fixture(t)
	for _, row := range []d.AccessInvite{
		{ID: 1, TargetID: 99, BotID: 8, ChatID: -10, RouteVersion: 4, State: "revoked"},
		{ID: 2, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "expired", ExpiresAt: time.Now().Add(-time.Hour)},
	} {
		must(t, db.Create(&row).Error)
	}
	result, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	if result.Verified || result.Unknown != 1 || result.Remaining != 1 {
		t.Fatalf("unsafe scope passed: %+v", result)
	}
}

// TestManagementAuditDoesNotBecomeExternalUnknown distinguishes local recover
// decisions from external receipts while preserving unknown original operations.
func TestManagementAuditDoesNotBecomeExternalUnknown(t *testing.T) {
	for _, outcome := range []string{"accepted", "blocked"} {
		t.Run(outcome, func(t *testing.T) {
			store, db, request := fixture(t)
			operation := d.AccessOperation{ID: 1, BusinessKey: "audit", Kind: "create", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "failed"}
			must(t, db.Create(&operation).Error)
			must(t, db.Create(&d.AccessResult{OperationID: 1, LeaseVersion: 2, Outcome: outcome, ActorAdminID: 7, Action: "review", Reason: "operator_verified"}).Error)
			result, err := store.RecoverRoute(context.Background(), request)
			must(t, err)
			if !result.Verified || result.Unknown != 0 {
				t.Fatalf("local audit became external uncertainty: %+v", result)
			}
			must(t, db.Create(&d.AccessResult{OperationID: 1, LeaseVersion: 1, Outcome: "unknown", Action: "create"}).Error)
			result, err = store.RecoverRoute(context.Background(), request)
			must(t, err)
			if result.Verified || result.Unknown != 1 {
				t.Fatalf("audit erased original external uncertainty: %+v", result)
			}
		})
	}
}

// TestManagementAuditRequiresWriterSignature rejects unrecognized local-looking
// outcomes; only the existing recover writer's complete signature is classified.
func TestManagementAuditRequiresWriterSignature(t *testing.T) {
	valid := d.AccessResult{Outcome: "accepted", ActorAdminID: 7, Action: "review", Reason: "operator_verified"}
	for _, mutate := range []func(*d.AccessResult){
		func(row *d.AccessResult) { row.ActorAdminID = 0 },
		func(row *d.AccessResult) { row.Action = "approve" },
		func(row *d.AccessResult) { row.Reason = "" },
		func(row *d.AccessResult) { row.Outcome = "unknown" },
	} {
		row := valid
		mutate(&row)
		if localRecoveryAudit(row) {
			t.Fatalf("invalid audit accepted: %+v", row)
		}
	}
}

// TestTaskCannotRebindGeneration confirms persistent idempotency even after runtime changes.
func TestTaskCannotRebindGeneration(t *testing.T) {
	store, db, request := fixture(t)
	_, err := store.RecoverRoute(context.Background(), request)
	must(t, err)
	must(t, db.Exec("UPDATE telegram_bot_instances SET generation = 8").Error)
	request.Generation = 8
	result, err := store.RecoverRoute(context.Background(), request)
	if err == nil || result.Verified {
		t.Fatal("task rebound to different generation")
	}
}
