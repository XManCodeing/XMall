package routerecovery

import (
	"context"
	"reflect"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// noEntryPlatform fails if entry switching even constructs a Telegram adapter.
// The supplied test records the unexpected call; neither platform is returned.
type noEntryPlatform struct{ test *testing.T }

// MembershipGroupPlatform must remain unused: entry switching only scans SQL.
func (platform noEntryPlatform) MembershipGroupPlatform(uint, uint, c.GroupRecoveryAuthorization) (c.AccessPlatform, c.LifecyclePlatform) {
	platform.test.Fatal("entry switch requested a Telegram platform")
	return nil, nil
}

// TestSwitchEntryPreservesHistoricalResponsibilities checks real old member/link
// evidence, immutable terms and absence of admission/API work on repeated passes.
func TestSwitchEntryPreservesHistoricalResponsibilities(t *testing.T) {
	worker, db, input, _ := groupFixture(t)
	worker.Platforms = noEntryPlatform{test: t}
	invite := d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, TelegramID: 42, State: "active", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	must(t, db.Create(&invite).Error)
	must(t, db.First(&invite).Error)
	must(t, db.Create(&d.AccessSlot{TargetID: 2, TelegramID: 42, InviteID: invite.ID}).Error)
	var source d.LedgerSource
	var projection d.LedgerProjection
	must(t, db.First(&source).Error)
	must(t, db.First(&projection).Error)
	must(t, db.AutoMigrate(&d.OrderItemSnapshot{}))
	snapshot := d.OrderItemSnapshot{OrderItemID: 5, AccountID: 3, BeneficiaryTelegramID: 42, Kind: d.TermMonths, Months: 2, Quantity: 1}
	must(t, db.Create(&snapshot).Error)
	must(t, db.First(&snapshot).Error)
	for range 2 {
		result, err := worker.SwitchEntry(context.Background(), input)
		must(t, err)
		if !result.EntryReady || result.Verified || !result.Checkpointed || result.Remaining != 2 || result.Unknown != 0 {
			t.Fatalf("entry-only proof lost: %+v", result)
		}
	}
	var run Run
	must(t, db.First(&run).Error)
	if run.Verified || run.CompletionReceipt != "membership-entry:3:2:5" || run.Remaining != 2 || run.Unknown != 0 {
		t.Fatalf("incorrect persisted receipt: %+v", run)
	}
	var savedInvite d.AccessInvite
	must(t, db.First(&savedInvite).Error)
	if !reflect.DeepEqual(invite, savedInvite) {
		t.Fatal("old invite was changed")
	}
	var savedSource d.LedgerSource
	var savedProjection d.LedgerProjection
	must(t, db.First(&savedSource).Error)
	must(t, db.First(&savedProjection).Error)
	if !reflect.DeepEqual(source, savedSource) || !reflect.DeepEqual(projection, savedProjection) {
		t.Fatal("original entitlement changed")
	}
	var savedSnapshot d.OrderItemSnapshot
	must(t, db.First(&savedSnapshot).Error)
	if !reflect.DeepEqual(snapshot, savedSnapshot) {
		t.Fatal("historical order snapshot changed")
	}
	var count int64
	must(t, db.Model(&Evidence{}).Where("kind = ?", "admission").Count(&count).Error)
	if count != 0 {
		t.Fatal("entry switch introduced admission")
	}
	must(t, db.Model(&d.AccessOperation{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("entry switch scheduled platform work")
	}
	must(t, db.Model(&d.AccessSlot{}).Where("invite_id = ?", invite.ID).Count(&count).Error)
	if count != 1 {
		t.Fatal("old slot removed before release")
	}
}

// TestSwitchEntryBlocksUncertainty retains independent API receipts and queued
// panels rather than interpreting local terminal states as cleanup success.
func TestSwitchEntryBlocksUncertainty(t *testing.T) {
	for _, scenario := range []string{"create", "revoke", "approve", "ban", "unban", "bound_unknown", "inflight", "migration", "panel", "destination", "deleted_evidence", "pending_invite"} {
		t.Run(scenario, func(t *testing.T) {
			worker, db, input, _ := groupFixture(t)
			worker.Platforms = noEntryPlatform{test: t}
			seedEntryBlocker(t, db, scenario)
			result, err := worker.SwitchEntry(context.Background(), input)
			if result.EntryReady || result.Verified {
				t.Fatalf("unsafe proof for %s: %+v %v", scenario, result, err)
			}
			if err == nil {
				var run Run
				must(t, db.First(&run).Error)
				if run.CompletionReceipt != "" || scenario != "panel" && result.Unknown == 0 {
					t.Fatalf("blocking evidence erased: %+v %+v", run, result)
				}
			}
		})
	}
}

// seedEntryBlocker inserts one synthetic liability into db for scenario; failures
// stop the test immediately and no external endpoint is involved.
func seedEntryBlocker(t *testing.T, db *gorm.DB, scenario string) {
	t.Helper()
	switch scenario {
	case "migration":
		must(t, db.Create(&d.IdentityMigration{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, State: "pending"}).Error)
	case "panel":
		must(t, db.Exec("INSERT INTO telegram_bot_deliveries VALUES (1,1,'panel','pending')").Error)
	case "destination":
		must(t, db.Create(&d.AccessMember{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, TelegramID: 42, SourceID: 1, Status: "kicked", ObservationSource: "query", ObservedAt: time.Now().UTC()}).Error)
	case "deleted_evidence":
		must(t, db.Create(&Evidence{TaskID: 3, TargetID: 2, Kind: "receipt", Reference: "missing", State: "unknown"}).Error)
	case "pending_invite":
		must(t, db.Create(&d.AccessInvite{TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "pending", ExpiresAt: time.Now().UTC().Add(time.Hour)}).Error)
	default:
		operation := d.AccessOperation{ID: 1, BusinessKey: "entry:test", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, Kind: scenario, State: "failed"}
		if scenario == "bound_unknown" {
			operation.State = "unknown"
			must(t, db.Create(&Action{TaskID: 3, TargetID: 2, Kind: "group_operation", Reference: "1", State: "unknown"}).Error)
		}
		if scenario == "inflight" {
			operation.LeaseToken, operation.LeaseUntil = "test-lease", time.Now().UTC().Add(time.Minute)
		}
		must(t, db.Create(&operation).Error)
		if scenario != "bound_unknown" && scenario != "inflight" {
			must(t, db.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: 1, Action: scenario, Outcome: c.AccessUnknown}).Error)
		}
	}
}

// TestSwitchEntryRefreshRevokesStaleProof preserves newly discovered uncertainty
// instead of leaving a formerly ready receipt usable by a later release consumer.
func TestSwitchEntryRefreshRevokesStaleProof(t *testing.T) {
	worker, db, input, _ := groupFixture(t)
	result, err := worker.SwitchEntry(context.Background(), input)
	must(t, err)
	if !result.EntryReady {
		t.Fatal("fixture entry was not ready")
	}
	seedEntryBlocker(t, db, "revoke")
	result, err = worker.SwitchEntry(context.Background(), input)
	must(t, err)
	var run Run
	must(t, db.First(&run).Error)
	if result.EntryReady || result.Unknown == 0 || run.CompletionReceipt != "" || run.Verified {
		t.Fatalf("stale entry receipt survived unknown effect: %+v %+v", result, run)
	}
}

// TestSwitchEntryRequiresFullFence rejects substituted routes, missing ownership,
// stale capabilities and released recovery even when no admission is needed.
func TestSwitchEntryRequiresFullFence(t *testing.T) {
	for name, mutation := range map[string]string{
		"route":      "UPDATE telegram_group_targets SET route_version=6",
		"scope":      "DELETE FROM telegram_recovery_scope_slots WHERE key='chat:-20'",
		"released":   "UPDATE telegram_group_recovery_target_fences SET released_version=10",
		"capability": "UPDATE telegram_group_targets SET obs_can_invite_users=0",
		"parent":     "UPDATE telegram_recovery_tasks SET state='completed'",
		"history":    "UPDATE telegram_group_routes SET retired_at=NULL WHERE route_version=4",
	} {
		t.Run(name, func(t *testing.T) {
			worker, db, input, _ := groupFixture(t)
			must(t, db.Exec(mutation).Error)
			result, err := worker.SwitchEntry(context.Background(), input)
			if err == nil || result.EntryReady {
				t.Fatalf("invalid fence passed: %+v %v", result, err)
			}
		})
	}
}
