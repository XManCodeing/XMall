package container

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/authz"
	admin "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	refund "github.com/XManCodeing/XMall/internal/modules/order/application/refund"
	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMembershipCombinedRefundActualAssembly uses the real payment evidence, money service, store and DisposeRefund.
// It simulates failure before local disposition, then reconstructs the workflow to prove restart recovery.
func TestMembershipCombinedRefundActualAssembly(t *testing.T) {
	fixture, rights, worker := delayedRefundFixture(t)
	if err := fixture.db.AutoMigrate(&orderdomain.MembershipRefundRequest{}); err != nil {
		t.Fatal(err)
	}
	auth, err := authz.NewService(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	identities := supportAdminIdentityStore{admins: map[uint]*admin.Admin{9: {ID: 9, IsSuper: true}}}
	container := &Container{AdminStore: identities, AuthzService: auth, MembershipRefundAdmin: rights, OrderRefundService: refund.New(orderstore.New(fixture.db, "synthetic-combined-refund"), nil, nil, nil, nil, nil)}
	input := order.MembershipRefundInput{RequestKey: "actual-assembly-refund", OrderID: 1, ActorAdminID: 9, Method: "manual", Amount: "1.00", ItemIDs: []uint{1}}
	if err := fixture.db.Exec("CREATE TRIGGER pause_combined_disposal BEFORE INSERT ON telegram_membership_refund_dispositions BEGIN SELECT RAISE(ABORT, 'synthetic unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	first, err := container.MembershipRefundWorkflow().Execute(fixture.ctx, input)
	if err != nil || first == nil || first.FundsState != "completed" || first.RightsState != "pending" {
		t.Fatal(first, err)
	}
	if err := fixture.db.Exec("DROP TRIGGER pause_combined_disposal").Error; err != nil {
		t.Fatal(err)
	}
	// A fresh worker must respect durable backoff rather than immediately retrying.
	if err := container.membershipRefundRecovery(fixture.db)(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := container.OrderRefundService.ListMembershipRefunds(1, 9)
	if err != nil || len(pending) != 1 || pending[0].RetryAttempts != 1 || pending[0].RightsState != "pending" {
		t.Fatal("restart bypassed backoff", pending, err)
	}
	// Advance the durable due time, then reconstruct the actual compensation adapter.
	if err := fixture.db.Model(&orderdomain.MembershipRefundRequest{}).Where("id = ?", first.ID).Update("retry_after", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := container.membershipRefundRecovery(fixture.db)(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if err := container.membershipRefundRecovery(fixture.db)(fixture.ctx); err != nil {
		t.Fatal("restart replay", err)
	}
	second, err := container.MembershipRefundWorkflow().Execute(fixture.ctx, input)
	if err != nil || second.RightsState != "queued" || second.RefundID != first.RefundID {
		rows, readErr := rights.RefundCandidates(fixture.ctx, 1)
		if readErr == nil && len(rows) > 0 {
			selection := rows[0].RefundSelection
			selection.ActorAdminID = 9
			t.Log("disposal cause", rights.DisposeRefund(fixture.ctx, selection))
		}
		t.Fatal(second, err, readErr)
	}
	countMembershipRows(t, fixture.db, &orderdomain.OrderRefundRecord{}, 1)
	countMembershipRows(t, fixture.db, &membership.RefundDisposition{}, 1)
	countMembershipRows(t, fixture.db, &membership.LedgerRevocation{}, 2)
	if _, err := worker.RunOnce(fixture.ctx, 10); err != nil {
		t.Fatal(err)
	}
	for _, target := range []uint{2, 3} {
		state, err := rights.Recalculate(fixture.ctx, membership.EntitlementScope{TargetID: target, BeneficiaryTelegramID: membershipAssemblyTelegramID}, fixture.now)
		if err != nil || state.Active {
			t.Fatal("late grant escaped termination", state, err)
		}
	}
}

// TestMembershipRefundCorruptSelectionDoesNotBlockPage covers malformed JSON in an older durable row.
func TestMembershipRefundCorruptSelectionDoesNotBlockPage(t *testing.T) {
	fixture, _, _ := delayedRefundFixture(t)
	if err := fixture.db.AutoMigrate(&orderdomain.MembershipRefundRequest{}); err != nil {
		t.Fatal(err)
	}
	service := refund.New(orderstore.New(fixture.db, "synthetic-corrupt-refund"), nil, nil, nil, nil, nil)
	bad := order.MembershipRefundRequest{RequestKey: "corrupt-old-request", OrderID: 999, ActorAdminID: 9, RefundID: 1, FundsState: "completed", RightsState: "pending", ItemIDs: []uint{1}}
	if err := fixture.db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&bad).Update("item_ids", "{bad-json").Error; err != nil {
		t.Fatal(err)
	}
	input := order.MembershipRefundInput{RequestKey: "valid-after-corrupt", OrderID: 1, ActorAdminID: 9, Method: "manual", Amount: "1.00", ItemIDs: []uint{1}}
	request, err := service.CommitMembershipRefund(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	container := &Container{OrderRefundService: service}
	if err := container.membershipRefundRecovery(fixture.db)(fixture.ctx); err == nil {
		t.Fatal("corrupt intent was not reported")
	}
	var badProgress struct {
		RetryAttempts uint
		RetryAfter    *time.Time
	}
	if err := fixture.db.Table("membership_refund_requests").Select("retry_attempts", "retry_after").Where("id = ?", bad.ID).Scan(&badProgress).Error; err != nil || badProgress.RetryAttempts != 1 || badProgress.RetryAfter == nil {
		t.Fatal(badProgress, err)
	}
	var valid order.MembershipRefundRequest
	if err := fixture.db.First(&valid, request.ID).Error; err != nil || valid.RightsState != "queued" {
		t.Fatal(valid, err)
	}
}

// TestMembershipRefundManualBudgetRecovery resumes the same authorized money key after automatic exhaustion.
func TestMembershipRefundManualBudgetRecovery(t *testing.T) {
	fixture, rights, _ := delayedRefundFixture(t)
	if err := fixture.db.AutoMigrate(&orderdomain.MembershipRefundRequest{}); err != nil {
		t.Fatal(err)
	}
	auth, err := authz.NewService(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	container := &Container{AdminStore: supportAdminIdentityStore{admins: map[uint]*admin.Admin{9: {ID: 9, IsSuper: true}}}, AuthzService: auth, MembershipRefundAdmin: rights, OrderRefundService: refund.New(orderstore.New(fixture.db, "synthetic-manual-recovery"), nil, nil, nil, nil, nil)}
	input := order.MembershipRefundInput{RequestKey: "manual-budget-recovery", OrderID: 1, ActorAdminID: 9, Method: "manual", Amount: "1.00", ItemIDs: []uint{1}}
	request, err := container.OrderRefundService.CommitMembershipRefund(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&orderdomain.MembershipRefundRequest{}).Where("id = ?", request.ID).
		Updates(map[string]any{"rights_state": "pending", "retry_attempts": order.MembershipRefundRetryLimit}).Error; err != nil {
		t.Fatal(err)
	}
	if err := container.membershipRefundRecovery(fixture.db)(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	countMembershipRows(t, fixture.db, &membership.RefundDisposition{}, 0)
	// The existing administrator UI selects pending requests and replays their original key.
	listed, err := container.MembershipRefundWorkflow().Requests(fixture.ctx, 9, 1)
	if err != nil || len(listed) != 1 || listed[0].RightsState != "pending" || listed[0].RetryAttempts != order.MembershipRefundRetryLimit || listed[0].RequestKey != input.RequestKey {
		t.Fatal("exhausted request disappeared from existing recovery entry", listed, err)
	}
	resumed, err := container.MembershipRefundWorkflow().Execute(fixture.ctx, input)
	if err != nil || resumed.RightsState != "queued" || resumed.RefundID != request.RefundID {
		t.Fatal(resumed, err)
	}
	countMembershipRows(t, fixture.db, &orderdomain.OrderRefundRecord{}, 1)
	countMembershipRows(t, fixture.db, &membership.RefundDisposition{}, 1)
}

// TestMembershipRefundReadOnlyPreview proves no writes, paid ownership checks and other-purchase preservation.
func TestMembershipRefundReadOnlyPreview(t *testing.T) {
	fixture, rights, _ := delayedRefundFixture(t)
	other := membership.LedgerSource{OrderItemID: 99, TargetID: 2, Operation: "grant", AccountID: 7, BeneficiaryTelegramID: membershipAssemblyTelegramID, PaidAt: fixture.now, Kind: membership.TermPermanent}
	if err := fixture.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec("PRAGMA query_only = ON").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		preview, err := rights.PreviewRefund(fixture.ctx, 1, []uint{1})
		if err != nil || preview == nil || len(preview.Groups) != 2 {
			t.Fatal(preview, err)
		}
		for _, group := range preview.Groups {
			if group.TargetID == 2 && (!group.Active || !group.Permanent) {
				t.Fatalf("other permanent purchase lost: %+v", group)
			}
			if group.TargetID == 3 && group.Active {
				t.Fatalf("selected purchase survived: %+v", group)
			}
			if group.RemovalState != "unconfirmed" || group.CleanupState != "unconfirmed" {
				t.Fatalf("fabricated external result: %+v", group)
			}
		}
	}
	for _, items := range [][]uint{{999}, {1, 1}, {0}} {
		if _, err := rights.PreviewRefund(fixture.ctx, 1, items); err == nil {
			t.Fatalf("invalid selection accepted: %v", items)
		}
	}
}

// TestMembershipCombinedRefundNewAdminPermissions checks freshly seeded admins, not superuser bypasses.
func TestMembershipCombinedRefundNewAdminPermissions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "permissions.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	identities := supportAdminIdentityStore{admins: map[uint]*admin.Admin{}}
	container := &Container{AdminStore: identities, AuthzService: auth}
	cases := []struct {
		roles   []string
		allowed bool
	}{{[]string{"support"}, false}, {[]string{"finance"}, false}, {[]string{"system_admin"}, false}, {[]string{"support", "finance"}, true}, {[]string{"readonly_auditor"}, false}}
	for index, fixture := range cases {
		actor := uint(index + 1)
		identities.admins[actor] = &admin.Admin{ID: actor}
		if err := auth.SetAdminRoles(actor, fixture.roles); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"wallet", "manual", "existing"} {
			if err := container.authorizeMembershipRefund(context.Background(), actor, method); (err == nil) != fixture.allowed {
				t.Fatalf("roles=%v method=%s err=%v", fixture.roles, method, err)
			}
		}
		if fixture.allowed {
			for _, method := range []string{"GET", "POST"} {
				allowed, err := auth.EnforceAdmin(actor, "/admin/telegram-bot/membership/orders/900/refund-and-terminate", method)
				if err != nil || !allowed {
					t.Fatalf("route missing %s %v", method, err)
				}
			}
		}
	}
	if err := auth.SetAdminRoles(777, []string{"support", "finance"}); err != nil {
		t.Fatal(err)
	}
	if err := container.authorizeMembershipRefund(context.Background(), 777, "wallet"); err == nil {
		t.Fatal("non-admin actor accepted")
	}
}
