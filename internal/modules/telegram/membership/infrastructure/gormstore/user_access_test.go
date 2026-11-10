package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	ud "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"gorm.io/gorm"
)

// TestOrderHistoryScopeAndCompatibility proves shared ownership and parent/child scope.
func TestOrderHistoryScopeAndCompatibility(t *testing.T) {
	db, now := userAccessDB(t)
	for _, item := range []uint{10, 11, 12} {
		userAccessSeed(t, db, item, 1, 1, 42, true, now)
	}
	if err := db.Model(&od.Order{}).Where("id = 11").Update("parent_id", 10).Error; err != nil {
		t.Fatal(err)
	}
	store := New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	history, err := store.OrderMembershipHistory(ctx, 1, 42, 10)
	if err != nil || len(history) != 1 || len(history[0].Routes) != 1 || history[0].TargetID != 1 {
		t.Fatalf("deduplication failed: %+v %v", history, err)
	}
	targets, err := store.OrderMembershipTargets(ctx, 1, 42, 10)
	if err != nil || !reflect.DeepEqual(targets, []uint{1}) {
		t.Fatalf("legacy API drift: %v %v", targets, err)
	}
	for _, input := range []struct {
		account     uint
		beneficiary int64
		order       uint
	}{{1, 42, 99}, {1, 43, 10}, {2, 42, 10}} {
		_, historyErr := store.OrderMembershipHistory(ctx, input.account, input.beneficiary, input.order)
		_, legacyErr := store.OrderMembershipTargets(ctx, input.account, input.beneficiary, input.order)
		if !errors.Is(historyErr, c.ErrUntrustedIdentity) || !errors.Is(legacyErr, c.ErrUntrustedIdentity) {
			t.Fatalf("ownership drift: %v %v", historyErr, legacyErr)
		}
	}
}

// TestOrderHistoryOnlyContainsSuppliedSnapshotRoutes checks order-local provenance.
func TestOrderHistoryOnlyContainsSuppliedSnapshotRoutes(t *testing.T) {
	old := d.SnapshotTarget{TargetID: 2, BotInstanceID: 1, TelegramChatID: -10, RouteVersion: 4}
	newRoute := old
	newRoute.RouteVersion = 5
	snapshots := []d.OrderItemSnapshot{{Targets: []d.SnapshotTarget{old, old, newRoute}}, {Targets: []d.SnapshotTarget{old, {TargetID: 0}}}}
	result := orderMembershipHistory(snapshots)
	if len(result) != 1 || len(result[0].Routes) != 2 || result[0].Routes[0].ChatID != -10 || result[0].Routes[1].RouteVersion != 5 {
		t.Fatalf("route facts lost: %+v", result)
	}
}

// TestCurrentObservationTitleRequiresMatchingRoute rejects obsolete route titles.
func TestCurrentObservationTitleRequiresMatchingRoute(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	for _, scenario := range []struct {
		version uint64
		want    string
	}{{2, "Observed chat"}, {1, "群1"}} {
		if err := db.Model(&gd.Target{}).Where("id = 1").Updates(map[string]any{"obs_title": "Observed chat", "obs_route_version": scenario.version}).Error; err != nil {
			t.Fatal(err)
		}
		rows := userAccessRead(t, db, 0, 1, now)
		if len(rows) != 1 || rows[0].Title != scenario.want {
			t.Fatalf("wrong physical title: %+v", rows)
		}
	}
}

// refundExecutionFixture disposes a renewal with older member/invitation provenance.
func refundExecutionFixture(t *testing.T) (*Store, *gorm.DB, d.RefundDisposition, d.LedgerSource) {
	t.Helper()
	store, db, input := refundTestFixture(t)
	now := time.Now().UTC()
	historical := d.LedgerSource{ID: 3, OrderItemID: 99, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, Kind: d.TermMonths, Months: 1, PaidAt: now.AddDate(-1, 0, 0), CreatedAt: now.AddDate(-1, 0, 0)}
	userAccessCreate(t, db, &historical)
	if err := db.Model(&d.AccessMember{}).Where("target_id = ?", 1).Update("source_id", historical.ID).Error; err != nil {
		t.Fatal(err)
	}
	userAccessCreate(t, db, &d.AccessInvite{ID: 1, SourceID: historical.ID, TargetID: 1, TelegramID: historical.BeneficiaryTelegramID, BotID: 9, ChatID: -1, RouteVersion: 1, State: "active", LinkHash: "0123456789012345678901234567890123456789012345678901234567890123", LinkCipher: "test-encrypted", CreatedAt: now})
	if err := store.DisposeRefund(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var audit d.RefundDisposition
	if err := db.First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	var source d.LedgerSource
	if err := db.First(&source, 1).Error; err != nil {
		t.Fatal(err)
	}
	return store, db, audit, source
}

// TestRefundExecutionRenewalHistory preserves exact removal and independent cleanup evidence.
func TestRefundExecutionRenewalHistory(t *testing.T) {
	store, db, audit, source := refundExecutionFixture(t)
	if audit.ExecutionVersion != 1 || len(audit.Executions) != 2 {
		t.Fatalf("missing bindings: %+v", audit)
	}
	execution := audit.Executions[0]
	if execution.CleanupCycleID == 0 || len(execution.Members) != 1 || execution.Members[0].OperationID == 0 {
		t.Fatalf("missing execution: %+v", execution)
	}
	operationID := execution.Members[0].OperationID
	if err := db.Model(&d.AccessOperation{}).Where("id = ?", operationID).Updates(map[string]any{"ban_confirmed": true, "ban_outcome": "succeeded", "state": "succeeded", "lifecycle_phase": "done", "updated_at": audit.CreatedAt.Add(time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&d.InviteCleanupItem{}).Where("cycle_id = ?", execution.CleanupCycleID).Updates(map[string]any{"state": "succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "newer-noop", Kind: "lifecycle", TargetID: 1, TelegramID: source.BeneficiaryTelegramID, BotID: 9, ChatID: -1, RouteVersion: 1, LifecyclePhase: "not_required", State: "succeeded", CreatedAt: audit.CreatedAt.Add(2 * time.Second)})
	rows, err := store.OrderStates(context.Background(), []uint{1}, time.Now().UTC())
	if err != nil || len(rows) != 2 || rows[0].MemberRemoval != "removed" || rows[0].InviteCleanup != "succeeded" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	var invite d.AccessInvite
	if err := db.First(&invite, 1).Error; err != nil || invite.SourceID != 3 {
		t.Fatalf("provenance rewritten %+v %v", invite, err)
	}
	for _, outcome := range []string{"unknown", "failed"} {
		if err := db.Model(&d.InviteCleanupItem{}).Where("cycle_id = ?", execution.CleanupCycleID).Updates(map[string]any{"state": outcome, "operation_id": 0}).Error; err != nil {
			t.Fatal(err)
		}
		row := c.AdminOrderGroupState{Rights: "revoked"}
		if _, err := projectRefundExecution(db, source, &row, audit.CreatedAt); err != nil || row.MemberRemoval != "removed" || row.InviteCleanup != outcome {
			t.Fatalf("independent=%+v err=%v", row, err)
		}
	}
}

// TestLegacyRefundExecutionProof accepts only exact atomic enqueue evidence.
func TestLegacyRefundExecutionProof(t *testing.T) {
	for _, scenario := range []string{"exact", "later_task", "other_order", "other_identity", "other_target", "other_route", "ambiguous_version"} {
		t.Run(scenario, func(t *testing.T) {
			_, db, audit, source := refundExecutionFixture(t)
			execution := audit.Executions[0]
			userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "legacy-later-noop", Kind: "lifecycle", TargetID: 1, TelegramID: source.BeneficiaryTelegramID, BotID: 9, ChatID: -1, RouteVersion: 1, EntitlementVersion: execution.EntitlementVersion, CleanupCycleID: execution.CleanupCycleID, LifecyclePhase: "not_required", State: "succeeded", CreatedAt: audit.CreatedAt.Add(2 * time.Second)})
			if err := db.Model(&d.RefundDisposition{}).Where("id = ?", audit.ID).Updates(map[string]any{"execution_version": 0, "executions": nil}).Error; err != nil {
				t.Fatal(err)
			}
			updates := map[string]any{"ban_confirmed": true, "state": "succeeded"}
			switch scenario {
			case "later_task":
				updates["created_at"] = audit.CreatedAt.Add(time.Second)
			case "other_identity":
				updates["telegram_id"] = source.BeneficiaryTelegramID + 1
			case "other_target":
				updates["target_id"] = 99
			case "other_route":
				updates["chat_id"] = -99
			case "other_order":
				source.OrderItemID = 999
			case "ambiguous_version":
				userAccessCreate(t, db, &d.LedgerOutbox{OrderItemID: 999, TargetID: 1, BeneficiaryTelegramID: source.BeneficiaryTelegramID, Operation: "revoke", ProjectionVersion: execution.EntitlementVersion, CreatedAt: audit.CreatedAt})
			}
			if err := db.Model(&d.AccessOperation{}).Where("id = ?", execution.Members[0].OperationID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			row := c.AdminOrderGroupState{Rights: "revoked"}
			if _, err := projectRefundExecution(db, source, &row, audit.CreatedAt); err != nil {
				t.Fatal(err)
			}
			if scenario == "exact" && row.MemberRemoval != "removed" {
				t.Fatalf("strict legacy evidence lost: %+v", row)
			}
			if scenario != "exact" && row.MemberRemoval == "removed" {
				t.Fatalf("borrowed evidence: %+v", row)
			}
			var unchanged d.RefundDisposition
			if err := db.First(&unchanged, audit.ID).Error; err != nil || unchanged.ExecutionVersion != 0 || len(unchanged.Executions) != 0 {
				t.Fatalf("legacy read wrote bindings: %+v %v", unchanged, err)
			}
		})
	}
}

// TestRefundExecutionCoverageAndTaskIsolation checks exact-route failure states.
func TestRefundExecutionCoverageAndTaskIsolation(t *testing.T) {
	_, db, audit, source := refundExecutionFixture(t)
	execution := audit.Executions[0]
	for _, outcome := range []string{"unknown", "failed", "pending"} {
		if err := db.Model(&d.AccessOperation{}).Where("id = ?", execution.Members[0].OperationID).Updates(map[string]any{"state": outcome, "ban_confirmed": false}).Error; err != nil {
			t.Fatal(err)
		}
		row := c.AdminOrderGroupState{Rights: "revoked"}
		if _, err := projectRefundExecution(db, source, &row, audit.CreatedAt); err != nil || row.MemberRemoval != outcome {
			t.Fatalf("outcome=%+v err=%v", row, err)
		}
	}
	if err := db.Model(&d.AccessOperation{}).Where("id = ?", execution.Members[0].OperationID).Updates(map[string]any{"ban_confirmed": true, "chat_id": -999}).Error; err != nil {
		t.Fatal(err)
	}
	row := c.AdminOrderGroupState{Rights: "revoked"}
	if _, err := projectRefundExecution(db, source, &row, audit.CreatedAt); err != nil || row.MemberRemoval != "unconfirmed" {
		t.Fatalf("route=%+v err=%v", row, err)
	}
	execution.Covered = true
	states, err := refundMemberStates(db, execution, audit.CreatedAt)
	if err != nil || len(states) != 1 || states[0] != "not_required" {
		t.Fatalf("coverage=%v err=%v", states, err)
	}
}

// TestOrderLiabilitiesStayPurchaseScoped checks source-bound evidence and retained coverage.
// t owns isolated SQLite facts; no external platform calls are made.
func TestOrderLiabilitiesStayPurchaseScoped(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSeed(t, db, 1, 1, 1, 42, true, now)
	userAccessSeed(t, db, 2, 1, 1, 42, false, now)
	source := userAccessSource(t, db, 1, 1, 1, 42, d.TermMonths, 1, now.AddDate(0, -3, 0))
	lost := now.AddDate(0, -2, 0)
	userAccessCreate(t, db, &d.AccessMember{SourceID: source, TargetID: 1, TelegramID: 42, BotID: 9, ChatID: -1, ObservedAt: lost, Status: "member", IsMember: true})
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "own-ban", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1, State: "succeeded", BanConfirmed: true, CreatedAt: lost.Add(time.Hour), UpdatedAt: lost.Add(time.Hour)})
	userAccessCreate(t, db, &d.AccessInvite{ID: 1, SourceID: source, TelegramID: 42, TargetID: 1})
	userAccessCreate(t, db, &d.InviteCleanupItem{CycleID: 1, InviteID: 1, State: "unknown"})
	userAccessCreate(t, db, &d.InviteCleanupItem{CycleID: 2, InviteID: 99, State: "succeeded"})
	rows, err := New(db).OrderStates(context.Background(), []uint{1, 2}, now)
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %+v %v", rows, err)
	}
	if rows[0].MemberRemoval != "removed" || !rows[0].MemberRemoved || rows[0].InviteCleanup != "unknown" {
		t.Fatalf("own evidence: %+v", rows[0])
	}
	if rows[1].Rights != "none" || rows[1].MemberRemoval != "none" || rows[1].InviteCleanup != "none" {
		t.Fatalf("unpaid borrowed evidence: %+v", rows[1])
	}
	userAccessSource(t, db, 3, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Hour))
	rows, err = New(db).OrderStates(context.Background(), []uint{1}, now)
	if err != nil || rows[0].MemberRemoval != "not_required" || rows[0].MemberRemoved {
		t.Fatalf("retained coverage: %+v %v", rows, err)
	}
}

// TestOrderRenewalRetainsInvite 使用 t 的隔离账本验证续费保留入口和既有回收证据，无返回值。
func TestOrderRenewalRetainsInvite(t *testing.T) {
	for _, scenario := range []struct {
		name, cleanup, invite, operation, want string
	}{
		{"retained", "", "active", "", "not_required"},
		{"unknown_invite", "", "unknown", "", "unconfirmed"},
		{"standalone_revoke", "", "active", "failed", "unconfirmed"},
		{"completed", "succeeded", "active", "", "succeeded"},
		{"failed", "failed", "active", "", "failed"},
		{"unknown", "unknown", "active", "", "unknown"},
		{"pending", "pending", "active", "", "pending"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, now := userAccessDB(t)
			userAccessSeed(t, db, 1, 1, 1, 42, true, now)
			source := userAccessSource(t, db, 1, 1, 1, 42, d.TermMonths, 1, now.Add(-31*24*time.Hour))
			// 续费发生于旧单到期前；删除旧贡献后续费也已过期，必须按完整累计期限判断。
			userAccessSource(t, db, 2, 1, 1, 42, d.TermMonths, 1, now.Add(-30*24*time.Hour-time.Hour))
			userAccessCreate(t, db, &d.AccessInvite{ID: 1, SourceID: source, TelegramID: 42, TargetID: 1, State: scenario.invite})
			if scenario.cleanup != "" {
				userAccessCreate(t, db, &d.InviteCleanupItem{CycleID: 1, InviteID: 1, State: scenario.cleanup})
			}
			if scenario.operation != "" {
				userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "standalone-revoke", Kind: "revoke", InviteID: 1, State: scenario.operation})
			}
			rows, err := New(db).OrderStates(context.Background(), []uint{1}, now)
			if err != nil || len(rows) != 1 || rows[0].Rights != "expired" || rows[0].InviteCleanup != scenario.want {
				t.Fatalf("renewal cleanup: %+v %v; want %s", rows, err, scenario.want)
			}
			ids, err := New(db).MatchingOrderItems(context.Background(), c.AdminOrderStateFilter{InviteCleanup: "not_required"}, now)
			if err != nil || (scenario.want == "not_required" && (len(ids) != 1 || ids[0] != 1)) || (scenario.want != "not_required" && len(ids) != 0) {
				t.Fatalf("cleanup filter: %v %v", ids, err)
			}
			if scenario.name == "retained" {
				rows, err = New(db).OrderStates(context.Background(), []uint{1}, now.Add(29*24*time.Hour))
				if err != nil || len(rows) != 1 || rows[0].InviteCleanup != "unconfirmed" {
					t.Fatalf("expired total coverage incorrectly retained: %+v %v", rows, err)
				}
			}
		})
	}
}

// TestOrderExpiredInviteRouteEvidence 验证 t 中本单邀请路由可关联到期任务，返回值为空。
// 覆盖旧成员归属、已撤销后续来源、跨路由任务和较新在群观察；缺少任务不推断成功。
func TestOrderExpiredInviteRouteEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name, want                                             string
		task, observation, revokedLater, liveLater, wrongRoute bool
	}{
		{name: "old_member_source", task: true, want: "not_required"},
		{name: "revoked_later_source", task: true, revokedLater: true, want: "not_required"},
		{name: "unrevoked_later_source", task: true, liveLater: true, want: "unconfirmed"},
		{name: "no_task", want: "unconfirmed"},
		{name: "wrong_route_version", task: true, wrongRoute: true, want: "unconfirmed"},
		{name: "newer_member_observation", task: true, observation: true, want: "present"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, now := userAccessDB(t)
			userAccessSeed(t, db, 1, 1, 1, 42, true, now)
			paidAt := now.Add(-90 * 24 * time.Hour)
			source := userAccessSource(t, db, 1, 1, 1, 42, d.TermMonths, 1, paidAt)
			lost := paidAt.Add(30 * 24 * time.Hour)
			member := d.AccessMember{SourceID: source + 99, TargetID: 1, TelegramID: 42, BotID: 9, ChatID: -1, RouteVersion: 1, Status: "left", ObservedAt: lost.Add(-time.Hour)}
			if scenario.observation {
				member.Status, member.IsMember, member.ObservedAt = "member", true, now
			}
			userAccessCreate(t, db, &member)
			userAccessCreate(t, db, &d.AccessInvite{SourceID: source, TargetID: 1, TelegramID: 42, BotID: 9, ChatID: -1, RouteVersion: 1})
			if scenario.revokedLater || scenario.liveLater {
				userAccessSource(t, db, 2, 1, 1, 42, d.TermMonths, 1, lost.Add(-time.Hour))
				if scenario.revokedLater {
					userAccessCreate(t, db, &d.LedgerRevocation{OrderItemID: 2, TargetID: 1, BeneficiaryTelegramID: 42, RevokedAt: lost.Add(-time.Minute)})
				}
			}
			if scenario.task {
				operation := d.AccessOperation{BusinessKey: "expiry-noop", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1, RouteVersion: 1, State: "succeeded", LifecyclePhase: "not_required", MemberStatus: "left", CreatedAt: lost.Add(time.Second), UpdatedAt: lost.Add(time.Second)}
				if scenario.wrongRoute {
					operation.RouteVersion = 2
				}
				userAccessCreate(t, db, &operation)
			}
			rows, err := New(db).OrderStates(context.Background(), []uint{1}, now)
			if err != nil || len(rows) != 1 || rows[0].MemberRemoval != scenario.want || rows[0].MemberRemoved {
				t.Fatalf("rows=%+v err=%v want=%s", rows, err, scenario.want)
			}
			ids, err := New(db).MatchingOrderItems(context.Background(), c.AdminOrderStateFilter{MemberRemoval: scenario.want}, now)
			if err != nil || len(ids) != 1 || ids[0] != 1 {
				t.Fatalf("filter ids=%v err=%v", ids, err)
			}
		})
	}
}

// TestOrderFilterRequiresSameGroup rejects cross-target conjunction and preserves unknown evidence.
func TestOrderFilterRequiresSameGroup(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSeed(t, db, 1, 1, 1, 42, true, now)
	if err := db.Model(&d.OrderItemSnapshot{}).Where("order_item_id = ?", 1).Updates(&d.OrderItemSnapshot{Targets: []d.SnapshotTarget{{TargetID: 1}, {TargetID: 2}}}).Error; err != nil {
		t.Fatal(err)
	}
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Hour))
	userAccessSource(t, db, 1, 1, 2, 42, d.TermMonths, 1, now.AddDate(-1, 0, 0))
	ids, err := New(db).MatchingOrderItems(context.Background(), c.AdminOrderStateFilter{Rights: "active", MemberRemoval: "unconfirmed"}, now)
	if err != nil || len(ids) != 0 {
		t.Fatalf("cross-target match: %v %v", ids, err)
	}
	for _, test := range []struct {
		operation d.AccessOperation
		want      string
	}{
		{d.AccessOperation{State: "failed"}, "failed"},
		{d.AccessOperation{State: "succeeded", LifecyclePhase: "not_required"}, "not_required"},
		{d.AccessOperation{State: "retryable", BanOutcome: "unknown"}, "unknown"},
		{d.AccessOperation{State: "pending"}, "pending"},
		{d.AccessOperation{State: "succeeded"}, "unconfirmed"},
	} {
		if got := orderRemovalOutcome(test.operation); got != test.want {
			t.Fatalf("%+v: %s", test.operation, got)
		}
	}
}

// TestOrderNoRightsAndOldCycle checks canceled/unposted tombstones and later-order ban isolation.
// t uses synthetic facts; no absence of processing is reported as successful cleanup.
func TestOrderNoRightsAndOldCycle(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSeed(t, db, 1, 1, 1, 42, true, now)
	userAccessSeed(t, db, 2, 1, 1, 42, true, now)
	if err := db.Model(&od.Order{}).Where("id = ?", 2).Update("status", "canceled").Error; err != nil {
		t.Fatal(err)
	}
	userAccessCreate(t, db, &d.LedgerRevocation{OrderItemID: 2, TargetID: 1, BeneficiaryTelegramID: 42, RevokedAt: now})
	source := userAccessSource(t, db, 1, 1, 1, 42, d.TermMonths, 1, now.AddDate(0, -6, 0))
	userAccessSource(t, db, 3, 1, 1, 42, d.TermMonths, 1, now.AddDate(0, -3, 0))
	userAccessCreate(t, db, &d.AccessMember{SourceID: source, TargetID: 1, TelegramID: 42, BotID: 9, ChatID: -1, ObservedAt: now.AddDate(0, -6, 0)})
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "later-order-ban", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1, State: "succeeded", BanConfirmed: true, CreatedAt: now.Add(-time.Hour)})
	rows, err := New(db).OrderStates(context.Background(), []uint{1, 2}, now)
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %+v %v", rows, err)
	}
	if rows[0].MemberRemoval != "unconfirmed" || rows[0].MemberRemoved {
		t.Fatalf("borrowed later ban: %+v", rows[0])
	}
	if rows[1].Rights != "none" || rows[1].MemberRemoval != "none" || rows[1].InviteCleanup != "none" {
		t.Fatalf("unposted tombstone fabricated rights: %+v", rows[1])
	}
}

// TestOrderStateIndependentEvidence verifies per-purchase expiration and independent mixed outcomes.
func TestOrderStateIndependentEvidence(t *testing.T) {
	db, now := userAccessDB(t)
	for item := uint(1); item <= 3; item++ {
		userAccessSeed(t, db, item, 1, item, 42, true, now)
	}
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Hour))
	userAccessSource(t, db, 2, 1, 2, 42, d.TermMonths, 1, now.AddDate(-1, 0, 0))
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "removed", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1, BanConfirmed: true, State: "succeeded"})
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "failed", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -2, State: "failed"})
	rows, err := New(db).OrderStates(context.Background(), []uint{1, 2, 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Rights != "active" || !rows[0].Permanent || rows[0].MemberRemoval != "none" || rows[0].InviteCleanup != "none" {
		t.Fatalf("mixed evidence: %+v", rows)
	}
	if rows[1].Rights != "expired" || rows[1].ExpiresAt == nil || rows[2].Rights != "pending" {
		t.Fatalf("expiration evidence: %+v", rows)
	}
	userAccessCreate(t, db, &d.InviteCleanupCycle{ID: 1, TargetID: 1, TelegramID: 42, EntitlementVersion: 1})
	userAccessCreate(t, db, &d.InviteCleanupItem{CycleID: 1, InviteID: 1, State: "succeeded"})
	userAccessCreate(t, db, &d.InviteCleanupItem{CycleID: 1, InviteID: 2, State: "failed"})
	userAccessCreate(t, db, &d.AccessOperation{BusinessKey: "cleanup-mixed", Kind: "lifecycle", TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1, BanConfirmed: true, State: "succeeded", CleanupCycleID: 1})
	rows, err = New(db).OrderStates(context.Background(), []uint{1}, now)
	if err != nil || rows[0].InviteCleanup != "none" || rows[0].MemberRemoved {
		t.Fatalf("cleanup masked: %+v %v", rows, err)
	}
	userAccessSource(t, db, 5, 1, 2, 42, d.TermMonths, 1, now.Add(-time.Hour))
	rows, err = New(db).OrderStates(context.Background(), []uint{2}, now)
	if err != nil || rows[0].Rights != "expired" {
		t.Fatalf("renewal revived old purchase: %+v %v", rows, err)
	}
	userAccessCreate(t, db, &d.LedgerRevocation{OrderItemID: 1, TargetID: 1, BeneficiaryTelegramID: 42, RevokedAt: now})
	userAccessSource(t, db, 4, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Minute))
	rows, err = New(db).OrderStates(context.Background(), []uint{1}, now)
	if err != nil || rows[0].Rights != "revoked" {
		t.Fatalf("renewal restored revoked purchase: %+v %v", rows, err)
	}
}

// TestAggregateOrderEvidence prevents a first success masking other routes.
func TestAggregateOrderEvidence(t *testing.T) {
	for _, fixture := range []struct {
		states []string
		want   string
	}{{nil, "unconfirmed"}, {[]string{"succeeded", "failed"}, "mixed"}, {[]string{"removed", "unconfirmed"}, "mixed"}, {[]string{"removed", "removed"}, "removed"}} {
		if got := aggregateOrderEvidence(fixture.states); got != fixture.want {
			t.Fatalf("%v: %s", fixture.states, got)
		}
	}
}

// TestAccessListUsesCurrentBotRoute verifies pre-pagination Bot filtering and rejects old route observations.
// t owns the single-connection fixture; failures stop the test, and no platform calls occur.
func TestAccessListUsesCurrentBotRoute(t *testing.T) {
	db, now := userAccessDB(t)
	for target := uint(1); target <= 3; target++ {
		userAccessSource(t, db, target, 1, target, 42, d.TermPermanent, 0, now.Add(-time.Hour))
		userAccessCreate(t, db, &d.LedgerProjection{TargetID: target, BeneficiaryTelegramID: 42, Version: 1})
	}
	if err := db.Model(&gd.Target{}).Where("id = ?", 1).Update("bot_instance_id", 8).Error; err != nil {
		t.Fatal(err)
	}
	userAccessCreate(t, db, &d.AccessInvite{ID: 1, AccountID: 1, TelegramID: 42, TargetID: 2, BotID: 9, ChatID: -1002, RouteVersion: 2, State: "active", ExpiresAt: now.Add(time.Hour)})
	// A newer historical invitation must not replace the current route's visible state.
	userAccessCreate(t, db, &d.AccessInvite{ID: 2, AccountID: 1, TelegramID: 42, TargetID: 2, BotID: 8, ChatID: -1002, RouteVersion: 1, State: "unknown", ExpiresAt: now.Add(time.Hour)})
	userAccessCreate(t, db, &d.AccessMember{TargetID: 2, TelegramID: 42, BotID: 8, ChatID: -1002, RouteVersion: 1, Status: "member", IsMember: true})
	identity := c.AccessIdentity{AccountID: 1, TelegramID: 42, BotID: 9}
	rows, err := New(db).ListAccess(context.Background(), identity, 0, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TargetID != 2 || rows[0].BotID != 9 || rows[0].InviteID != 1 || rows[0].InviteState != "active" || rows[0].MemberStatus != "" {
		t.Fatalf("stale or cross-Bot state exposed: %+v", rows)
	}
	next, err := New(db).ListAccess(context.Background(), identity, rows[0].TargetID, 1, now)
	if err != nil || len(next) != 1 || next[0].TargetID != 3 {
		t.Fatalf("current Bot cursor skipped target: %+v, %v", next, err)
	}
	identity.BotID = 8
	old, err := New(db).ListAccess(context.Background(), identity, 0, 20, now)
	if err != nil || len(old) != 1 || old[0].TargetID != 1 {
		t.Fatalf("old Bot retained moved targets: %+v, %v", old, err)
	}
	identity.BotID = 0
	if _, err := New(db).ListAccess(context.Background(), identity, 0, 20, now); !errors.Is(err, c.ErrInvalid) {
		t.Fatalf("missing Bot accepted: %v", err)
	}
}

// TestUserApprovalRevokeFailuresNeverAvailable keeps active invitations from hiding operation anomalies.
func TestUserApprovalRevokeFailuresNeverAvailable(t *testing.T) {
	for _, kind := range []string{"approve", "revoke"} {
		for _, state := range []string{"failed", "unknown", "retryable"} {
			t.Run(kind+state, func(t *testing.T) {
				db, now := userAccessDB(t)
				source := userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Hour))
				userAccessCreate(t, db, &d.AccessInvite{ID: 1, AccountID: 1, TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 2, SourceID: source, State: "active", ExpiresAt: now.Add(time.Hour)})
				userAccessCreate(t, db, &d.AccessOperation{BusinessKey: kind + state, Kind: kind, InviteID: 1, AccountID: 1, TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 2, State: state})
				rows := userAccessRead(t, db, 0, 20, now)
				if len(rows) != 1 || rows[0].State == "available" || rows[0].CanRequest {
					t.Fatalf("unsafe projection: %+v", rows)
				}
			})
		}
	}
}

// userAccessDB 复用单连接真实 SQLite，新增只用于测试的身份、账本和当前路由模型。
func userAccessDB(t *testing.T) (*gorm.DB, time.Time) {
	t.Helper()
	db, now := paidEventDB(t)
	models := append(AccessModels(), &ud.User{}, &ei.Identity{}, &gd.Target{}, &d.LedgerSource{}, &d.LedgerRevocation{}, &d.LedgerProjection{}, &d.IdentityMigration{}, &d.RefundDisposition{}, &d.LedgerOutbox{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	userAccessCreate(t, db, &ud.User{ID: 1, Email: "projection@example.invalid", Status: constants.UserStatusActive})
	userAccessCreate(t, db, &ei.Identity{UserID: 1, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "42"})
	for targetID := uint(1); targetID <= 3; targetID++ {
		userAccessCreate(t, db, &gd.Target{ID: targetID, Name: fmt.Sprintf("群%d", targetID), Purpose: "membership", Enabled: true, RouteVersion: 2, BotInstanceID: 9, TelegramChatID: -1000 - int64(targetID), OccupancySlot: fmt.Sprintf("target:%d", targetID), Observation: gd.Observation{RouteVersion: 2, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, State: "ready", ObservedAt: &now}})
	}
	return db, now
}

// userAccessCreate 在 db 保存测试 value，失败立即终止当前 t。
func userAccessCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatal(err)
	}
}

// userAccessSeed 保存 account/telegram 的实际订单项和快照；paid=false 不写付款时刻。
func userAccessSeed(t *testing.T, db *gorm.DB, item, account, target uint, telegram int64, paid bool, now time.Time) {
	t.Helper()
	order := od.Order{ID: item, UserID: account, OrderNo: fmt.Sprintf("projection-%d", item), Status: constants.OrderStatusPendingPayment, Currency: "CNY"}
	if paid {
		order.Status, order.PaidAt = constants.OrderStatusPaid, &now
	}
	userAccessCreate(t, db, &order)
	userAccessCreate(t, db, &od.OrderItem{ID: item, OrderID: item, ProductID: 999, SKUID: 999, Quantity: 1, TitleJSON: jsonmap.JSON{}})
	userAccessCreate(t, db, &d.OrderItemSnapshot{OrderItemID: item, ProductID: 999, SKUID: 999, AccountID: account, BeneficiaryTelegramID: telegram, Kind: d.TermPermanent, Quantity: 1, Targets: []d.SnapshotTarget{{TargetID: target}}})
}

// userAccessSource 保存已处理购买的事实，返回用于可信成员关联的来源 ID。
func userAccessSource(t *testing.T, db *gorm.DB, item, account, target uint, telegram int64, kind string, months int64, paid time.Time) uint {
	t.Helper()
	source := d.LedgerSource{OrderItemID: item, TargetID: target, Operation: "grant", AccountID: account, BeneficiaryTelegramID: telegram, PaidAt: paid, Kind: kind, Months: months}
	userAccessCreate(t, db, &source)
	return source.ID
}

// userAccessRead 使用超时上下文检查单连接事务无全局连接回落，返回成功 DTO。
func userAccessRead(t *testing.T, db *gorm.DB, after uint, limit int, now time.Time) []c.UserAccessGroup {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	items, err := New(db).ListUserAccess(ctx, 1, after, limit, now)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// TestUserAccessPaidHistoryPaging 验证付款事实、订单归属、稳定去重、分页和未付款排除。
func TestUserAccessPaidHistoryPaging(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSeed(t, db, 1, 1, 2, 42, true, now)
	userAccessSeed(t, db, 2, 1, 1, 42, true, now)
	userAccessSeed(t, db, 3, 1, 1, 42, true, now)
	userAccessSeed(t, db, 4, 1, 3, 42, false, now)
	userAccessSeed(t, db, 5, 2, 3, 42, true, now)
	userAccessSeed(t, db, 6, 1, 3, 42, true, now)
	if err := db.Model(&od.Order{}).Where("id = 6").Update("user_id", 2).Error; err != nil {
		t.Fatal(err)
	}
	first := userAccessRead(t, db, 0, 1, now)
	if len(first) != 1 || first[0].TargetID != 1 || first[0].State != "processing" || first[0].CanRequest || first[0].Permanent {
		t.Fatalf("first=%+v", first)
	}
	second := userAccessRead(t, db, 1, 100, now)
	if len(second) != 1 || second[0].TargetID != 2 {
		t.Fatalf("second=%+v", second)
	}
	if len(userAccessRead(t, db, 2, 100, now)) != 0 {
		t.Fatal("unpaid/other account leaked")
	}
}

// TestUserAccessRecalculateAndTombstone 验证过期 Active 缓存不可信、永久 null 和早到墓碑优先。
func TestUserAccessRecalculateAndTombstone(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSource(t, db, 1, 1, 1, 42, d.TermMonths, 1, now.AddDate(-1, 0, 0))
	userAccessCreate(t, db, &d.LedgerProjection{TargetID: 1, BeneficiaryTelegramID: 42, Active: true, Kind: d.TermPermanent})
	userAccessSource(t, db, 2, 1, 2, 42, d.TermPermanent, 0, now)
	userAccessSeed(t, db, 3, 1, 3, 42, true, now)
	userAccessCreate(t, db, &d.LedgerRevocation{OrderItemID: 3, TargetID: 3, BeneficiaryTelegramID: 42, RevokedAt: now})
	items := userAccessRead(t, db, 0, 100, now)
	if len(items) != 3 || items[0].State != "expired" || items[0].CanRequest || items[0].ExpiresAt == nil || items[1].State != "available" || !items[1].Permanent || items[1].ExpiresAt != nil || !items[1].CanRequest || items[2].State != "revoked" {
		t.Fatalf("items=%+v", items)
	}
	encoded, err := json.Marshal(items[1])
	if err != nil || !strings.Contains(string(encoded), `"target_id":"2"`) || !strings.Contains(string(encoded), `"beneficiary_telegram_id":"42"`) || !strings.Contains(string(encoded), `"expires_at":null`) {
		t.Fatalf("json=%s err=%v", encoded, err)
	}
}

// TestUserAccessBindingHistory 验证解绑/换绑保留原受益身份且不可操作，恢复同人绑定可用。
func TestUserAccessBindingHistory(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	for _, binding := range []string{"43", "invalid", "42"} {
		if err := db.Model(&ei.Identity{}).Where("user_id = 1").Update("provider_user_id", binding).Error; err != nil {
			t.Fatal(err)
		}
		item := userAccessRead(t, db, 0, 100, now)[0]
		if item.BeneficiaryTelegramID != 42 || (binding == "42" && !item.CanRequest) || (binding != "42" && (item.State != "identity_mismatch" || item.CanRequest)) {
			t.Fatalf("binding=%s item=%+v", binding, item)
		}
	}
	if err := db.Where("user_id = 1").Delete(&ei.Identity{}).Error; err != nil {
		t.Fatal(err)
	}
	item := userAccessRead(t, db, 0, 100, now)[0]
	if item.State != "identity_mismatch" || item.CanRequest {
		t.Fatalf("unbound=%+v", item)
	}
}

// TestUserAccessKickedCannotRequest 验证当前群封禁不依赖邀请来源归属，left 恢复申请能力。
func TestUserAccessKickedCannotRequest(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	member := d.AccessMember{TargetID: 1, ChatID: -1001, TelegramID: 42, BotID: 9, RouteVersion: 2, SourceID: 999, Status: "kicked", ObservationSource: "query", ObservedAt: now}
	userAccessCreate(t, db, &member)
	item := userAccessRead(t, db, 0, 100, now)[0]
	if item.CanRequest || item.MemberStatus != "kicked" {
		t.Fatalf("kicked can request: %+v", item)
	}
	if err := db.Model(&member).Update("status", "left").Error; err != nil {
		t.Fatal(err)
	}
	if item = userAccessRead(t, db, 0, 100, now)[0]; !item.CanRequest {
		t.Fatalf("left treated as ban: %+v", item)
	}
}

// TestUserAccessKnownMemberCurrentRoute 验证管理员直接加入无需邀请，restricted 必须结合 IsMember。
func TestUserAccessKnownMemberCurrentRoute(t *testing.T) {
	db, now := userAccessDB(t)
	sourceID := userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	member := d.AccessMember{TargetID: 1, ChatID: -1001, TelegramID: 42, BotID: 9, RouteVersion: 2, SourceID: sourceID, Status: "restricted", IsMember: true, ObservationSource: "chat_member", ObservedAt: now}
	userAccessCreate(t, db, &member)
	item := userAccessRead(t, db, 0, 100, now)[0]
	if item.State != "member" || item.CanRequest || item.MemberStatus != "restricted" {
		t.Fatalf("member=%+v", item)
	}
	for _, fields := range []map[string]any{{"is_member": false}, {"is_member": true, "route_version": 1}, {"route_version": 2, "source_id": sourceID + 100}} {
		if err := db.Model(&d.AccessMember{}).Where("target_id = 1").Updates(fields).Error; err != nil {
			t.Fatal(err)
		}
		item = userAccessRead(t, db, 0, 100, now)[0]
		if item.State != "available" || !item.CanRequest {
			t.Fatalf("stale/untrusted member=%+v", item)
		}
	}
}

// TestUserAccessInviteRecoveryAndScope 验证仅当前路由和本账号来源可影响恢复状态，不输出秘密。
func TestUserAccessInviteRecoveryAndScope(t *testing.T) {
	db, now := userAccessDB(t)
	sourceID := userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	invite := d.AccessInvite{AccountID: 1, TargetID: 1, TelegramID: 42, BotID: 9, ChatID: -1001, RouteVersion: 2, SourceID: sourceID, State: "unknown", LinkCipher: "secret-marker", LinkHash: "secret-hash", ExpiresAt: now.Add(time.Hour)}
	userAccessCreate(t, db, &invite)
	for _, test := range []struct{ state, want string }{{"unknown", "unknown"}, {"creating", "recovering"}, {"active", "available"}} {
		if err := db.Model(&invite).Update("state", test.state).Error; err != nil {
			t.Fatal(err)
		}
		item := userAccessRead(t, db, 0, 100, now)[0]
		encoded, err := json.Marshal(item)
		if err != nil || item.State != test.want || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "link") {
			t.Fatalf("item=%+v json=%s err=%v", item, encoded, err)
		}
	}
	if err := db.Model(&invite).Updates(map[string]any{"state": "unknown", "route_version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if item := userAccessRead(t, db, 0, 100, now)[0]; item.State != "available" {
		t.Fatalf("old route=%+v", item)
	}
}

// TestUserAccessIdentityScope 验证同目标多次换绑购买只采用最近付款身份，不跨账号或身份累计。
func TestUserAccessIdentityScope(t *testing.T) {
	db, now := userAccessDB(t)
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now.Add(-time.Hour))
	userAccessSource(t, db, 2, 1, 1, 43, d.TermMonths, 1, now)
	userAccessSource(t, db, 3, 2, 1, 43, d.TermPermanent, 0, now)
	item := userAccessRead(t, db, 0, 100, now)[0]
	if item.BeneficiaryTelegramID != 43 || item.Permanent || item.ExpiresAt == nil || item.State != "identity_mismatch" || item.CanRequest {
		t.Fatalf("mixed scope=%+v", item)
	}
	userAccessCreate(t, db, &d.LedgerRevocation{OrderItemID: 2, TargetID: 1, BeneficiaryTelegramID: 43, RevokedAt: now})
	item = userAccessRead(t, db, 0, 100, now)[0]
	if item.State != "revoked" || item.Permanent || item.CanRequest {
		t.Fatalf("other scope revived revoked source=%+v", item)
	}
}

// TestUserAccessValidationAndReadOnly 验证参数、账号失效及绑定事务读取不产生写入。
func TestUserAccessValidationAndReadOnly(t *testing.T) {
	db, now := userAccessDB(t)
	for _, limit := range []int{0, -1, 101} {
		if _, err := New(db).ListUserAccess(context.Background(), 1, 0, limit, now); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("limit=%d err=%v", limit, err)
		}
	}
	userAccessSource(t, db, 1, 1, 1, 42, d.TermPermanent, 0, now)
	if err := db.Transaction(func(tx *gorm.DB) error {
		store, err := InTransaction(tx)
		if err != nil {
			return err
		}
		_, err = store.ListUserAccess(context.Background(), 1, 0, 100, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&d.AccessInvite{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("read wrote invite: %d %v", count, err)
	}
	if err := db.Model(&ud.User{}).Where("id = 1").Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).ListUserAccess(context.Background(), 1, 0, 100, now); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("disabled account=%v", err)
	}
}
