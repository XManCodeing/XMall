package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// retryFixture creates only local payment schema for h; no real database or provider is used.
func retryFixture(t *testing.T, h *lifecycleHarness, ctx context.Context) {
	t.Helper()
	if err := h.db.AutoMigrate(&oc.FulfillmentOrder{}, &oc.FulfillmentItem{}, &d.OrderItemSnapshot{}, &d.PaidEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exec("CREATE TABLE payments (id integer PRIMARY KEY,order_id integer,status text,paid_at datetime,deleted_at datetime,exception_code text)").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Scan(ctx, 9, 100); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&d.AccessOperation{}).Where("kind = ?", "lifecycle").Updates(map[string]any{"state": "needs_review", "lifecycle_phase": "ban"}).Error; err != nil {
		t.Fatal(err)
	}
}

// retryCommand invokes authenticated retry with the current durable version of h's task.
func retryCommand(t *testing.T, h *lifecycleHarness, ctx context.Context) error {
	t.Helper()
	row := h.task()
	return h.service.Recover(ctx, c.LifecycleRecovery{ID: row.ID, BotID: 9, ExpectedVersion: row.Version, ActorAdminID: 3, Action: "retry"})
}

// pendingRenewal creates an immutable same-target purchase; paid controls confirmed outbox evidence.
func pendingRenewal(t *testing.T, h *lifecycleHarness, paid bool, identity int64, target uint) {
	t.Helper()
	status := constants.OrderStatusPendingPayment
	var paidAt *time.Time
	if paid {
		status, paidAt = constants.OrderStatusPaid, &h.now
	}
	rows := []any{
		&oc.FulfillmentOrder{ID: 99, Status: status, PaidAt: paidAt},
		&oc.FulfillmentItem{ID: 99, OrderID: 99, Quantity: 1, TitleJSON: jsonmap.JSON{}},
		&d.OrderItemSnapshot{OrderItemID: 99, AccountID: 7, BeneficiaryTelegramID: identity, Targets: []d.SnapshotTarget{{TargetID: target}}},
	}
	for _, row := range rows {
		if err := h.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !paid {
		return
	}
	if err := h.db.Exec("INSERT INTO payments (id, order_id, status, paid_at, exception_code) VALUES (?, ?, ?, ?, '')", 99, 99, constants.PaymentStatusSuccess, h.now).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&d.PaidEvent{OrderID: 99, PaymentID: 99, PaidAt: h.now, AvailableAt: h.now}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestLifecycleManualRetryRenewal checks request-time cancellation, pending and negative scopes.
func TestLifecycleManualRetryRenewal(t *testing.T) {
	for _, scenario := range []string{"active", "paid_pending", "unpaid", "other_identity", "other_target", "revoked", "query_error", "untrusted"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			retryFixture(t, h, ctx)
			identity, target := int64(9007199254740993), uint(1)
			if scenario == "other_identity" {
				identity++
			}
			if scenario == "other_target" {
				target++
			}
			if scenario == "active" {
				h.renew(d.TermPermanent)
			} else {
				pendingRenewal(t, h, scenario != "unpaid", identity, target)
			}
			if scenario == "revoked" {
				if err := h.db.Create(&d.LedgerRevocation{OrderItemID: 99, TargetID: 1, BeneficiaryTelegramID: identity, RevokedAt: h.now}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "query_error" {
				if err := h.db.Migrator().DropTable(&d.PaidEvent{}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "untrusted" {
				if err := h.db.Model(&d.AccessMember{}).Where("target_id = 1").Update("source_id", 999).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := retryCommand(t, h, ctx)
			if scenario == "query_error" || scenario == "untrusted" {
				if err == nil || h.task().State != "needs_review" {
					t.Fatalf("unsafe retry: %v %+v", err, h.task())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			row := h.task()
			if scenario == "active" {
				if row.State != "succeeded" || row.ReviewReason != "entitlement_active" {
					t.Fatalf("not canceled: %+v", row)
				}
			} else if scenario == "paid_pending" {
				if row.State != "retryable" || row.ReviewReason != "payment_pending" {
					t.Fatalf("not deferred: %+v", row)
				}
			} else if row.State != "pending" {
				t.Fatalf("wrong scope blocked: %+v", row)
			}
			if h.bans != 0 {
				t.Fatal("recovery called Telegram")
			}
		})
	}
}

// TestLifecycleManualRetryExecutionBoundary adds renewal after retry and immediately before ban.
func TestLifecycleManualRetryExecutionBoundary(t *testing.T) {
	for _, scenario := range []string{"active", "paid_pending", "query_error"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			retryFixture(t, h, ctx)
			if err := retryCommand(t, h, ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.Run(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			h.onMember = func() {
				h.onMember = nil
				switch scenario {
				case "active":
					h.renew(d.TermMonths)
				case "paid_pending":
					pendingRenewal(t, h, true, 9007199254740993, 1)
				case "query_error":
					if err := h.db.Migrator().DropTable(&d.PaidEvent{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := h.service.Run(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			row := h.task()
			if h.bans != 0 {
				t.Fatal("renewal/query error allowed ban")
			}
			if scenario == "active" && row.ReviewReason != "entitlement_active" {
				t.Fatalf("not canceled: %+v", row)
			}
			if scenario == "paid_pending" && (row.State != "retryable" || row.ReviewReason != "payment_pending") {
				t.Fatalf("not deferred: %+v", row)
			}
		})
	}
}

// TestLifecycleManualRetryClaimGuard proves audit survives claiming and paid work later resumes.
func TestLifecycleManualRetryClaimGuard(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	retryFixture(t, h, ctx)
	if err := retryCommand(t, h, ctx); err != nil {
		t.Fatal(err)
	}
	pendingRenewal(t, h, true, 9007199254740993, 1)
	if _, err := h.service.Run(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	if row := h.task(); row.State != "retryable" || row.ReviewReason != "payment_pending" || h.bans != 0 {
		t.Fatalf("claim bypassed payment guard: %+v", row)
	}
	h.renew(d.TermMonths)
	h.now = h.now.Add(time.Hour)
	if _, err := h.service.Run(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	if row := h.task(); row.State != "succeeded" || row.ReviewReason != "entitlement_active" || h.bans != 0 {
		t.Fatalf("posted renewal not canceled: %+v", row)
	}
}

// TestLifecycleManualRetryIdentityCleanup preserves explicit migrated old-member cleanup.
func TestLifecycleManualRetryIdentityCleanup(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	retryFixture(t, h, ctx)
	pendingRenewal(t, h, true, 9007199254740994, 1)
	h.renew(d.TermPermanent)
	mapping := d.IdentityMigration{AccountID: 7, TargetID: 1, OldTelegramID: 9007199254740993, NewTelegramID: 9007199254740994, BotID: 9, ChatID: -1001, RouteVersion: 1, State: "cleanup_pending", AdmittedAt: &h.now}
	if err := h.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	if err := retryCommand(t, h, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 8); err != nil {
		t.Fatal(err)
	}
	if row := h.task(); row.State != "succeeded" || h.bans != 1 || h.unbans != 1 {
		t.Fatalf("destination rights blocked old cleanup: %+v", row)
	}
}

// TestLifecycleManualRetryTaskOwnedCleanup keeps explicit old-route cleanup pending despite active rights.
func TestLifecycleManualRetryTaskOwnedCleanup(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	retryFixture(t, h, ctx)
	h.renew(d.TermPermanent)
	if err := h.db.Model(&d.AccessOperation{}).Where("kind = ?", "lifecycle").Update("business_key", "route-recovery:12:1:9007199254740993").Error; err != nil {
		t.Fatal(err)
	}
	if err := retryCommand(t, h, ctx); err != nil {
		t.Fatal(err)
	}
	if row := h.task(); row.State != "pending" || row.LifecyclePhase != "check" || h.bans != 0 {
		t.Fatalf("task-owned cleanup canceled: %+v", row)
	}
}

// TestLifecycleManualRetryUnknownNeverOwned rejects unknown bans even after renewal.
func TestLifecycleManualRetryUnknownNeverOwned(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	retryFixture(t, h, ctx)
	h.renew(d.TermPermanent)
	if err := h.db.Model(&d.AccessOperation{}).Where("kind = ?", "lifecycle").Updates(map[string]any{"lifecycle_phase": "unknown_ban", "ban_outcome": "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := retryCommand(t, h, ctx); err == nil {
		t.Fatal("unknown ban accepted")
	}
	if row := h.task(); row.BanConfirmed || row.State != "needs_review" || h.unbans != 0 {
		t.Fatalf("unknown ban attributed: %+v", row)
	}
}
