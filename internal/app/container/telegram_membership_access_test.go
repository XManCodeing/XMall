package container

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	telegrambootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	accessbootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess"
	"github.com/XManCodeing/XMall/internal/constants"
	identitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	identitystore "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/infrastructure/gormstore"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membershipapp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	memberdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	membershiphttp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/transport/http"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMembershipInviteDeliveryAssociation composes real invite retrieval and existing runtime receipts.
func TestMembershipInviteDeliveryAssociation(t *testing.T) {
	for _, outcome := range []string{"sent", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			link := fixture.createInvite(t)
			queue := runtimestore.New(fixture.db)
			bot := &accessbootstrap.Bot{Website: fixture.website, Deliveries: queue, DeliveryGuard: fixture.service}
			actor := shop.Actor{BotID: 1, TelegramID: membershipAssemblyTelegramID, UserID: 7, Generation: "1:7"}
			entry, err := bot.Request(fixture.ctx, actor, 2)
			if err != nil || entry.InviteDelivery == nil || entry.InviteURL != link {
				t.Fatal("existing invite unavailable", err)
			}
			invite := entry.InviteDelivery.InviteID
			status, err := queue.InviteDelivery(fixture.ctx, 1, invite)
			if err != nil || status.State != "not_requested" {
				t.Fatal("website claim counted as delivery", status, err)
			}
			key := "membership-invite:" + strconv.FormatUint(uint64(invite), 10) + ":update:20:0"
			row := &runtimedomain.Delivery{BotID: 1, Key: key, ChatID: membershipAssemblyTelegramID, TelegramUserID: membershipAssemblyTelegramID, Purpose: "panel", Operation: "edit", PayloadCipher: "encrypted", PanelEpoch: "epoch", PanelRevision: 1}
			if err := queue.Enqueue(fixture.ctx, row); err != nil {
				t.Fatal(err)
			}
			status, err = queue.InviteDelivery(fixture.ctx, 1, invite)
			if err != nil || status.State != "queued" {
				t.Fatal("enqueue counted as delivered", status, err)
			}
			claim, err := queue.ClaimDelivery(fixture.ctx, 1, "receipt", time.Now().Add(time.Minute))
			if err != nil || claim == nil {
				t.Fatal("claim", err)
			}
			messageID := int64(0)
			if outcome == "sent" {
				messageID = 88
			}
			if err := queue.FinishPanel(fixture.ctx, claim, outcome, runtime.DeliveryResult{MessageID: messageID}, "", time.Now()); err != nil {
				t.Fatal(err)
			}
			status, err = queue.InviteDelivery(fixture.ctx, 1, invite)
			expected := outcome
			if outcome == "sent" {
				expected = "delivered"
			}
			if err != nil || status.State != expected {
				t.Fatal(status, err)
			}
			fixture.service.Deliveries = queue
			tasks, err := fixture.service.ListAccessTasks(fixture.ctx, 1, 0, 10)
			if err != nil || len(tasks) != 1 || tasks[0].Delivery == nil || tasks[0].Delivery.State != expected {
				t.Fatal("admin delivery projection", err)
			}
			body, err := json.Marshal(tasks)
			if err != nil || strings.Contains(string(body), link) {
				t.Fatal("admin GET leaked invite")
			}
			retry, err := bot.Request(fixture.ctx, actor, 2)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "unknown" {
				if retry.InviteURL != "" || retry.State != "unknown" {
					t.Fatal("unknown blindly replayed")
				}
			} else if retry.InviteURL != link || retry.InviteDelivery.InviteID != invite {
				t.Fatal("resend did not reuse invite")
			}
			if fixture.platform.creates != 1 {
				t.Fatal("resend created invite")
			}
			countMembershipRows(t, fixture.db, &memberdomain.AccessInvite{}, 1)
			countMembershipRows(t, fixture.db, &memberdomain.LedgerSource{}, 1)
		})
	}
}

// claimUnknownApproval creates a real pending join then simulates an approval whose worker lost its lease.
// Returns the stale lease for late-result assertions; runtime recovery must never send it again.
func (fixture *membershipAccessFixture) claimUnknownApproval(t *testing.T) *memberdomain.AccessOperation {
	t.Helper()
	link := fixture.createInvite(t)
	if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.joinUpdate(membershipAssemblyTelegramID, link)); err != nil {
		t.Fatal(err)
	}
	fixture.now = time.Now().UTC()
	operation, err := fixture.service.Store.ClaimAccess(fixture.ctx, 1, "synthetic-stale-approval-lease", fixture.now, time.Second)
	if err != nil || operation.Kind != "approve" {
		t.Fatalf("claim approval: %+v %v", operation, err)
	}
	fixture.now = fixture.now.Add(2 * time.Second)
	fixture.service.Now = func() time.Time { return fixture.now }
	return operation
}

// reconciliation returns the single secret-free recovery row through the repository's administrative read port.
func (fixture *membershipAccessFixture) reconciliation(t *testing.T) membership.AccessReconciliation {
	t.Helper()
	rows, err := fixture.service.Store.ListAccessReconciliations(fixture.ctx, 1, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("recovery view: %+v %v", rows, err)
	}
	return rows[0]
}

// TestMembershipUnknownApprovalRuntime covers actual single-connection runtime dispatch and historical identity reads.
func TestMembershipUnknownApprovalRuntime(t *testing.T) {
	for _, scenario := range []string{"member", "left", "restricted_member", "restricted_left", "rebound", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			stale := fixture.claimUnknownApproval(t)
			result := membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "member"}
			if scenario == "left" {
				result.Status = "left"
			}
			if strings.HasPrefix(scenario, "restricted") {
				result.Status, result.IsMember = "restricted", scenario == "restricted_member"
			}
			if scenario == "rebound" {
				if err := fixture.db.Model(&identitydomain.Identity{}).Where("id = 1").Update("provider_user_id", "88").Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoked" {
				if err := fixture.db.Create(&memberdomain.LedgerRevocation{OrderItemID: 1, TargetID: 2, BeneficiaryTelegramID: membershipAssemblyTelegramID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			fixture.platform.memberResult = &result
			before := fixture.platform.queries
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			row := fixture.reconciliation(t)
			if row.ParentOperationID != stale.ID || row.MemberStatus != result.Status || row.ObservedAt == nil || fixture.platform.queries != before+1 || fixture.platform.queriedTelegramID != membershipAssemblyTelegramID {
				t.Fatalf("wrong reconciliation: %+v", row)
			}
			if scenario == "revoked" && (row.State != "needs_review" || row.ReviewReason != "member_without_entitlement") {
				t.Fatalf("missing persistent manual follow-up: %+v", row)
			}
			var member memberdomain.AccessMember
			if err := fixture.db.First(&member).Error; err != nil || member.IsMember != (result.Status == "member" || result.IsMember) {
				t.Fatalf("member observation: %+v %v", member, err)
			}
			expectedRevokes := 0
			if scenario == "revoked" {
				// Final entitlement loss now independently retires the known invitation;
				// it must still never retry the unknown approval or its completed read.
				expectedRevokes = 1
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.queries != before+1 || fixture.platform.approves != 0 || fixture.platform.revokes != expectedRevokes {
				t.Fatal("unknown effect or finished observation replayed", err)
			}
			var parent memberdomain.AccessOperation
			if err := fixture.db.First(&parent, stale.ID).Error; err != nil || parent.State != membership.AccessUnknown {
				t.Fatal("observation rewrote approval", err)
			}
			var join memberdomain.AccessJoin
			if err := fixture.db.First(&join, stale.JoinID).Error; err != nil || join.State != membership.AccessUnknown {
				t.Fatal("observation rewrote join", err)
			}
		})
	}
}

// TestMembershipReconcileRetryAndLease verifies persisted backoff and old read/approval leases cannot overwrite state.
func TestMembershipReconcileRetryAndLease(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	stale := fixture.claimUnknownApproval(t)
	fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessUnknown, ErrorClass: "network"}}
	before := fixture.platform.queries
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	row := fixture.reconciliation(t)
	if row.State != membership.AccessRetryable || row.ErrorClass != "network" || !row.NextAttemptAt.After(fixture.now) || row.ObservedAt != nil {
		t.Fatalf("missing persisted backoff: %+v", row)
	}
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.queries != before+1 {
		t.Fatal("read retried before backoff", err)
	}
	newJoin := fixture.joinUpdate(membershipAssemblyTelegramID, "unknown")
	newJoin.ID++
	newJoin.JoinRequest.Date = fixture.now.Unix()
	if _, err := fixture.bridge.Handle(fixture.ctx, 1, newJoin); err != nil {
		t.Fatal(err)
	}
	if current := fixture.reconciliation(t); current.State != membership.AccessRetryable {
		t.Fatal("new request watermark cancelled historical read")
	}
	fixture.now = row.NextAttemptAt.Add(time.Second)
	old, err := fixture.service.Store.ClaimAccessReconcile(fixture.ctx, 1, "synthetic-old-reconcile-lease", fixture.now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(2 * time.Second)
	fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "member"}
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	late := membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "left"}
	if err := fixture.service.Store.FinishAccessReconcile(fixture.ctx, old, late, fixture.now, fixture.now); !errors.Is(err, membership.ErrConflict) {
		t.Fatal("late read not rejected", err)
	}
	if err := fixture.service.Store.FinishAccess(fixture.ctx, stale, membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, "", "", fixture.now); !errors.Is(err, membership.ErrConflict) {
		t.Fatal("late approval not rejected", err)
	}
	row = fixture.reconciliation(t)
	if row.MemberStatus != "member" || row.State != membership.AccessSucceeded || fixture.platform.approves != 0 {
		t.Fatalf("late lease corrupted result: %+v", row)
	}
}

// TestMembershipReconcileRouteSwitch checks both before-call and in-flight switches cannot query or persist a wrong route.
func TestMembershipReconcileRouteSwitch(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(strconv.FormatBool(during), func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			fixture.claimUnknownApproval(t)
			switchRoute := func() {
				if err := fixture.db.Model(&groupsdomain.Target{}).Where("id = 2").Update("route_version", 2).Error; err != nil {
					t.Fatal(err)
				}
			}
			if during {
				fixture.platform.onQuery = switchRoute
			} else {
				switchRoute()
			}
			before := fixture.platform.queries
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			row := fixture.reconciliation(t)
			if row.ObservedAt != nil || (!during && fixture.platform.queries != before) {
				t.Fatalf("wrong route used: %+v", row)
			}
			countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
		})
	}
}

// TestMembershipApprovalChangedDuringCall proves a real sent approval becomes unknown and schedules a separate historical read.
func TestMembershipApprovalChangedDuringCall(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoked), func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			link := fixture.createInvite(t)
			if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.joinUpdate(membershipAssemblyTelegramID, link)); err != nil {
				t.Fatal(err)
			}
			fixture.platform.afterApprove = func() {
				var err error
				if revoked {
					err = fixture.db.Create(&memberdomain.LedgerRevocation{OrderItemID: 1, TargetID: 2, BeneficiaryTelegramID: membershipAssemblyTelegramID}).Error
				} else {
					err = fixture.db.Model(&identitydomain.Identity{}).Where("id = 1").Update("provider_user_id", "88").Error
				}
				if err != nil {
					t.Fatal(err)
				}
				fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "member"}
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			row := fixture.reconciliation(t)
			want := membership.AccessSucceeded
			if revoked {
				want = "needs_review"
			}
			if row.MemberStatus != "member" || row.State != want || fixture.platform.approves != 1 {
				t.Fatalf("missing real follow-up: %+v", row)
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.approves != 1 {
				t.Fatal("unknown approval was repeated", err)
			}
		})
	}
}

// membershipReconcileFence provides a test-controlled live runtime lease, independently of DB operation leases.
type membershipReconcileFence struct{ lost bool }

// Check returns a stale-generation error after ownership is lost; inputs identify the protected route.
func (fence *membershipReconcileFence) Check(_ context.Context, bot uint, generation uint64) error {
	if fence.lost || bot != 1 || generation != 7 {
		return runtime.ErrStale
	}
	return nil
}

// TestMembershipReconcileRuntimeFenceLost discards an in-flight observation when runtime ownership changes.
func TestMembershipReconcileRuntimeFenceLost(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	fixture.claimUnknownApproval(t)
	fence := &membershipReconcileFence{}
	fixture.ctx = runtime.WithExecutionFence(fixture.ctx, fence)
	fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "member"}
	fixture.platform.onQuery = func() { fence.lost = true }
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	row := fixture.reconciliation(t)
	if row.State != membership.AccessRetryable || row.ObservedAt != nil {
		t.Fatalf("lost runtime fence accepted observation: %+v", row)
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
}

// TestMembershipReconcileUntrustedSource refuses enrollment even for a platform member without original trusted provenance.
func TestMembershipReconcileUntrustedSource(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	fixture.claimUnknownApproval(t)
	if err := fixture.db.Delete(&memberdomain.LedgerSource{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	before := fixture.platform.queries
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	row := fixture.reconciliation(t)
	if row.State != "needs_review" || row.ReviewReason != "identity" || fixture.platform.queries != before {
		t.Fatalf("untrusted read performed: %+v", row)
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
}

// TestMembershipRefreshMember requires a read-only recovery before an explicit, revalidated retry.
// The real assembly uses one SQLite connection and checks the runtime fence on every platform request.
func TestMembershipRefreshMember(t *testing.T) {
	for _, scenario := range []string{"left", "query_failed", "route_before", "route_during", "fence", "late_lease", "rebound"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			if _, err := fixture.website.Request(fixture.ctx, 7, 2); err != nil {
				t.Fatal(err)
			}
			fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "kicked"}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			store := fixture.service.Store.(membership.AccessAdminRepository)
			task, err := store.AccessTask(fixture.ctx, 1, 1)
			if err != nil || task.State != "failed" || fixture.platform.creates != 0 {
				t.Fatalf("blocked task=%+v err=%v", task, err)
			}
			input := membership.AccessRecoveryInput{Action: "retry", ExpectedVersion: task.Version, ExpectedRouteVersion: task.RouteVersion, Reason: "operator_verified"}
			if err := fixture.service.RecoverAccess(fixture.ctx, 1, 1, 9, input); err == nil {
				t.Fatal("retry bypassed kicked")
			}
			task, err = store.AccessTask(fixture.ctx, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			input.Action, input.ExpectedVersion = "refresh_member", task.Version
			if err := fixture.service.RecoverAccess(fixture.ctx, 1, 1, 9, input); err != nil {
				t.Fatal(err)
			}
			fixture.platform.memberResult.Status = "left"
			if scenario == "query_failed" {
				fixture.platform.memberResult.Outcome = membership.AccessRetryable
			}
			if scenario == "route_before" {
				fixture.invalidate(t, "route_after_queue")
			}
			if scenario == "rebound" {
				if err := fixture.db.Model(&identitydomain.Identity{}).Where("user_id = ?", 7).Update("provider_user_id", "99").Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "route_during" {
				fixture.platform.onQuery = func() { fixture.invalidate(t, "route_after_queue") }
			}
			if scenario == "fence" {
				fence := &membershipReconcileFence{}
				fixture.ctx = runtime.WithExecutionFence(fixture.ctx, fence)
				fixture.platform.onQuery = func() { fence.lost = true }
			}
			if scenario == "late_lease" {
				fixture.service.Now = func() time.Time { return fixture.now }
				fixture.now = time.Now().UTC()
				fixture.platform.onQuery = func() { fixture.now = fixture.now.Add(2 * time.Minute) }
			}
			if _, err := fixture.service.RunAccessReconcile(fixture.ctx, 1, 1); err != nil && scenario != "late_lease" {
				t.Fatal(err)
			}
			var member memberdomain.AccessMember
			if err := fixture.db.First(&member).Error; err != nil {
				t.Fatal(err)
			}
			want := "kicked"
			if scenario == "left" || scenario == "rebound" {
				want = "left"
			}
			if member.Status != want || fixture.platform.creates != 0 || fixture.platform.approves != 0 || fixture.platform.declines != 0 || fixture.platform.revokes != 0 {
				t.Fatalf("refresh side effect/status=%+v", member)
			}
			task, err = store.AccessTask(fixture.ctx, 1, 1)
			if err != nil || task.State != "failed" {
				t.Fatalf("refresh changed parent=%+v err=%v", task, err)
			}
			if scenario != "left" && scenario != "rebound" {
				return
			}
			if fixture.platform.queriedTelegramID != membershipAssemblyTelegramID {
				t.Fatal("refresh queried current binding instead of historical beneficiary")
			}
			if _, err := fixture.service.RunAccess(fixture.ctx, 1, 1); err != nil || fixture.platform.creates != 0 {
				t.Fatalf("refresh automatically retried: %v", err)
			}
			input.Action, input.ExpectedVersion = "retry", task.Version
			err = fixture.service.RecoverAccess(fixture.ctx, 1, 1, 9, input)
			if scenario == "rebound" {
				if err == nil {
					t.Fatal("rebound beneficiary granted access")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.creates != 1 {
				t.Fatalf("explicit retry err=%v creates=%d", err, fixture.platform.creates)
			}
		})
	}
}

// TestMembershipRefreshApproval preserves a failed approval until an explicit retry, including retryable parents.
// t reports real runtime and single-connection failures; no administrative action sends Telegram writes.
func TestMembershipRefreshApproval(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	link := fixture.createInvite(t)
	if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.joinUpdate(membershipAssemblyTelegramID, link)); err != nil {
		t.Fatal(err)
	}
	fixture.platform.memberResult = &membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "kicked"}
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	store := fixture.service.Store.(membership.AccessAdminRepository)
	var parent memberdomain.AccessOperation
	if err := fixture.db.Where("kind = ?", "approve").First(&parent).Error; err != nil {
		t.Fatal(err)
	}
	// A scheduled task must stop automatic retries as soon as the administrator chooses read-only recovery.
	if err := fixture.db.Model(&parent).Update("state", membership.AccessRetryable).Error; err != nil {
		t.Fatal(err)
	}
	input := membership.AccessRecoveryInput{Action: "refresh_member", ExpectedVersion: parent.Version, ExpectedRouteVersion: parent.RouteVersion, Reason: "operator_verified"}
	if err := fixture.service.RecoverAccess(fixture.ctx, 1, parent.ID, 9, input); err != nil {
		t.Fatal(err)
	}
	fixture.platform.memberResult.Status = "left"
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	task, err := store.AccessTask(fixture.ctx, 1, parent.ID)
	if err != nil || task.State != "failed" || fixture.platform.approves != 0 || fixture.platform.creates != 1 {
		t.Fatalf("read changed approval: %+v %v", task, err)
	}
	input.Action, input.ExpectedVersion = "retry", task.Version
	if err := fixture.service.RecoverAccess(fixture.ctx, 1, parent.ID, 9, input); err != nil {
		t.Fatal(err)
	}
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.approves != 1 {
		t.Fatalf("explicit approval retry: %v", err)
	}
}

const membershipAssemblyTelegramID int64 = 9007199254740993
const membershipAssemblyLink = "https://t.me/+synthetic-membership-access"

// membershipAccessFixture holds real stores and the assembled entrypoints; only Telegram I/O is replaced.
type membershipAccessFixture struct {
	db       *gorm.DB
	ctx      context.Context
	now      time.Time
	bridge   *accessbootstrap.Runtime
	website  *accessbootstrap.Website
	service  *membershipapp.AccessService
	platform *membershipAccessPlatform
}

// membershipAccessPlatform records platform effects while checking the actual DB and execution fence.
type membershipAccessPlatform struct {
	t                           *testing.T
	db                          *gorm.DB
	creates, approves, declines int
	revokes, queries            int
	memberResult                *membership.AccessMemberResult
	afterApprove                func()
	onQuery                     func()
	queriedTelegramID           int64
}

// check validates ctx/route and reads the sole DB connection; it returns no value and fails t on a held transaction.
func (platform *membershipAccessPlatform) check(ctx context.Context, route membership.AccessRoute) {
	platform.t.Helper()
	if err := runtime.CheckExecutionFence(ctx, route.BotID, route.Generation); err != nil {
		platform.t.Fatalf("platform called without runtime fence: %v", err)
	}
	if route.TargetID != 2 || route.BotID != 1 || route.ChatID != -1001 || route.RouteVersion != 1 {
		platform.t.Fatalf("wrong physical route: %+v", route)
	}
	var instance runtimedomain.Instance
	if err := platform.db.WithContext(ctx).First(&instance, route.BotID).Error; err != nil {
		platform.t.Fatalf("platform could not acquire sole SQLite connection: %v", err)
	}
}

// CreateMembershipInvite checks request and ctx, returning a deterministic successful Telegram-shaped invite.
func (platform *membershipAccessPlatform) CreateMembershipInvite(ctx context.Context, request membership.AccessCreateInvite) membership.AccessInviteResult {
	platform.check(ctx, request.Route)
	if request.Name == "" || request.Term != memberdomain.InviteTermLongTerm || !request.ExpiresAt.IsZero() {
		platform.t.Fatal("missing explicit long-term invite parameters")
	}
	platform.creates++
	return membership.AccessInviteResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Link: membershipAssemblyLink}
}

// ApproveMembershipJoin checks action/ctx and returns success only for the trusted numeric beneficiary.
func (platform *membershipAccessPlatform) ApproveMembershipJoin(ctx context.Context, action membership.AccessJoinAction) membership.AccessAPIResult {
	platform.check(ctx, action.Route)
	if action.TelegramID != membershipAssemblyTelegramID {
		platform.t.Fatal("approved another identity or temporary user_chat_id")
	}
	platform.approves++
	if platform.afterApprove != nil {
		platform.afterApprove()
	}
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// DeclineMembershipJoin records action/ctx and returns a successful decline, never a member observation.
func (platform *membershipAccessPlatform) DeclineMembershipJoin(ctx context.Context, action membership.AccessJoinAction) membership.AccessAPIResult {
	platform.check(ctx, action.Route)
	platform.declines++
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// RevokeMembershipInvite checks route/link/ctx and returns success without any external network call.
func (platform *membershipAccessPlatform) RevokeMembershipInvite(ctx context.Context, route membership.AccessRoute, link string) membership.AccessAPIResult {
	platform.check(ctx, route)
	if link != membershipAssemblyLink {
		platform.t.Fatal("revoke received wrong invite")
	}
	platform.revokes++
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// GetMembershipMember records action/ctx and returns left; approval must not imply a positive query.
func (platform *membershipAccessPlatform) GetMembershipMember(ctx context.Context, action membership.AccessJoinAction) membership.AccessMemberResult {
	platform.check(ctx, action.Route)
	platform.queries++
	platform.queriedTelegramID = action.TelegramID
	if platform.onQuery != nil {
		platform.onQuery()
	}
	if platform.memberResult != nil {
		return *platform.memberResult
	}
	return membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: "left"}
}

// newMembershipAccessFixture uses t/contacts to build a single-connection DB and returns actual bootstrap assembly.
func newMembershipAccessFixture(t *testing.T, contacts bool) *membershipAccessFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "membership-access.db")), &gorm.Config{})
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
	db = db.WithContext(ctx)
	models := append(memberstore.AccessModels(), &memberdomain.IdentityMigration{}, &memberdomain.LedgerSource{}, &memberdomain.LedgerProjection{}, &memberdomain.LedgerRevocation{}, &memberdomain.OrderItemSnapshot{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &identitydomain.Identity{}, &userdomain.User{}, &groupsdomain.Target{}, &groupsdomain.Route{}, &runtimedomain.Instance{}, &runtimedomain.GenerationExit{}, &runtimedomain.Delivery{}, &runtimedomain.Session{})
	models = append(models, memberstore.LifecycleModels()...)
	if contacts {
		models = append(models, &runtimedomain.Contact{})
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	fixture := &membershipAccessFixture{db: db, ctx: runtime.WithExecutionFence(ctx, supportAssemblyFence{}), now: time.Now().UTC().Truncate(time.Second)}
	fixture.seed(t)
	queue := runtimestore.New(db)
	container := &Container{ExternalIdentityStore: identitystore.New(db), UserStore: userstore.New(db)}
	container.TelegramBotManager = telegrambootstrap.NewManager(queue, "synthetic-membership-assembly-key")
	previousDB := gormdb.DB
	gormdb.DB = db
	defer func() { gormdb.DB = previousDB }()
	container.initGroupRecovery(queue)
	fixture.bridge, fixture.website = container.initMembershipAccess(queue)
	gormdb.DB = previousDB
	assertMembershipRecoveryPorts(t, container, fixture.bridge, fixture.website)
	service, ok := fixture.bridge.Service.(*membershipapp.AccessService)
	if !ok || fixture.website.Access != service || fixture.website.Reader == nil || fixture.bridge.Migrations == nil {
		t.Fatal("website/runtime do not share the assembled access service")
	}
	fixture.service = service
	fixture.platform = &membershipAccessPlatform{t: t, db: db}
	service.Platform = fixture.platform
	fixture.bridge.Migrations.Platform = fixture.platform
	return fixture
}

// seed persists real source/projection/snapshot/identity/route/instance rows in fixture; t reports errors, no return.
func (fixture *membershipAccessFixture) seed(t *testing.T) {
	t.Helper()
	paid := fixture.now.Add(-time.Hour)
	rows := []any{
		&userdomain.User{ID: 7, Email: "membership-assembly@example.invalid", Status: constants.UserStatusActive},
		&identitydomain.Identity{ID: 1, UserID: 7, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "9007199254740993"},
		&runtimedomain.Instance{ID: 1, TelegramID: 100, Username: "assemblybot", Enabled: true, Generation: 7, Mode: "internal", State: "running", LastSeenAt: &fixture.now},
		&groupsdomain.Target{ID: 2, Name: "会员群", Purpose: "membership", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -1001, OccupancySlot: "-1001", Observation: groupsdomain.Observation{Generation: 7, RouteVersion: 1, BotTelegramID: 100, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, State: "ready", ObservedAt: &fixture.now}},
		&groupsdomain.Route{ID: 1, TargetID: 2, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -1001, EffectiveAt: paid},
		&orderdomain.Order{ID: 1, OrderNo: "synthetic-membership-access", UserID: 7, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paid},
		&orderdomain.OrderItem{ID: 1, OrderID: 1, ProductID: 3, SKUID: 4, Quantity: 1, TitleJSON: jsonmap.JSON{}},
		&memberdomain.OrderItemSnapshot{OrderItemID: 1, ProductID: 3, SKUID: 4, AccountID: 7, BeneficiaryTelegramID: membershipAssemblyTelegramID, IdentityVersion: 1, ConfigVersion: 1, Kind: memberdomain.TermPermanent, Quantity: 1, Targets: []memberdomain.SnapshotTarget{{TargetID: 2, BotInstanceID: 1, TelegramChatID: -1001, RouteVersion: 1}}, CreatedAt: paid},
		&memberdomain.LedgerSource{ID: 1, OrderItemID: 1, TargetID: 2, Operation: "grant", AccountID: 7, BeneficiaryTelegramID: membershipAssemblyTelegramID, PaidAt: paid, Kind: memberdomain.TermPermanent},
		&memberdomain.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: membershipAssemblyTelegramID, Active: true, Kind: memberdomain.TermPermanent, Version: 1},
	}
	for _, row := range rows {
		if err := fixture.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// countMembershipRows asserts want records for model in db; t reports errors, no return.
func countMembershipRows(t *testing.T, db *gorm.DB, model any, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(model).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%T count=%d want=%d err=%v", model, count, want, err)
	}
}

// createInvite requests as the website account, runs fenced reconciliation, and returns the real entry URL.
func (fixture *membershipAccessFixture) createInvite(t *testing.T) string {
	t.Helper()
	for range 2 {
		result, err := fixture.website.Request(fixture.ctx, 7, 2)
		if err != nil || result.State != "processing" || result.InviteURL != "" {
			t.Fatalf("website enqueue result=%+v err=%v", result, err)
		}
	}
	if fixture.platform.creates != 0 || fixture.platform.approves != 0 {
		t.Fatal("website performed platform I/O")
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessInvite{}, 1)
	countMembershipRows(t, fixture.db, &memberdomain.AccessOperation{}, 1)
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	entry, err := fixture.website.Request(fixture.ctx, 7, 2)
	if err != nil || entry.State != "available" || entry.InviteURL != membershipAssemblyLink || fixture.platform.creates != 1 {
		t.Fatalf("runtime invite result=%+v err=%v creates=%d", entry, err, fixture.platform.creates)
	}
	var invite memberdomain.AccessInvite
	if err := fixture.db.First(&invite).Error; err != nil {
		t.Fatal(err)
	}
	if invite.LinkCipher == "" || invite.LinkCipher == entry.InviteURL || invite.LinkHash == "" {
		t.Fatal("invitation secret not encrypted/fingerprinted")
	}
	return entry.InviteURL
}

// joinUpdate builds a trusted native update for applicant/link; return value deliberately has a different user_chat_id.
func (fixture *membershipAccessFixture) joinUpdate(applicant int64, link string) runtime.Update {
	return runtime.Update{ID: 101, Type: "chat_join_request", ChatID: -1001, ChatType: "supergroup", UserID: 999, JoinRequest: &runtime.JoinRequest{User: runtime.UserReference{ID: applicant}, UserChatID: 888, Date: fixture.now.Unix(), InviteLink: &runtime.InviteLink{InviteLink: link, CreatesJoinRequest: true}}}
}

// memberUpdate builds an independent chat_member event for subject; no invitation or approval is required.
func (fixture *membershipAccessFixture) memberUpdate(subject int64) runtime.Update {
	return runtime.Update{ID: 102, Type: "chat_member", ChatID: -1001, ChatType: "supergroup", Member: &runtime.MemberUpdate{Subject: runtime.UserReference{ID: subject}, Date: fixture.now.Unix(), New: runtime.ChatMember{User: runtime.UserReference{ID: subject}, Status: "member", IsMember: true}}}
}

// TestMembershipAccessActualAssembly exercises real website/runtime/Bot flows for t without contacts or with an empty table.
func TestMembershipAccessActualAssembly(t *testing.T) {
	for _, contacts := range []bool{false, true} {
		name := "without_contact_table"
		if contacts {
			name = "zero_contacts"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, contacts)
			page, err := fixture.website.List(fixture.ctx, 7, 0, 20)
			if err != nil || len(page.Items) != 1 || !page.Items[0].CanRequest || page.Items[0].BeneficiaryTelegramID != "9007199254740993" {
				t.Fatalf("website list=%+v err=%v", page, err)
			}
			countMembershipRows(t, fixture.db, &memberdomain.AccessOperation{}, 0)
			link := fixture.createInvite(t)
			bot := &accessbootstrap.Bot{Website: fixture.website, Deliveries: runtimestore.New(fixture.db), DeliveryGuard: fixture.service}
			actor := shop.Actor{BotID: 1, TelegramID: membershipAssemblyTelegramID, UserID: 7, Generation: "1:7"}
			botPage, err := bot.List(fixture.ctx, actor, 0, 20)
			if err != nil || len(botPage.Items) != 1 {
				t.Fatalf("bot list=%+v err=%v", botPage, err)
			}
			botEntry, err := bot.Request(fixture.ctx, actor, 2)
			if err != nil || botEntry.InviteURL != link || fixture.platform.creates != 1 {
				t.Fatal("bot did not reuse website invite", err)
			}
			update := fixture.joinUpdate(membershipAssemblyTelegramID, link)
			for range 2 {
				if messages, err := fixture.bridge.Handle(fixture.ctx, 1, update); err != nil || len(messages) != 0 {
					t.Fatal("join inbox replay", err)
				}
			}
			if fixture.platform.approves != 0 {
				t.Fatal("update receipt directly approved")
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil || fixture.platform.approves != 1 {
				t.Fatal("trusted applicant approval", err)
			}
			countMembershipRows(t, fixture.db, &memberdomain.AccessJoin{}, 1)
			countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
			if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.memberUpdate(membershipAssemblyTelegramID)); err != nil {
				t.Fatal(err)
			}
			var member memberdomain.AccessMember
			if err := fixture.db.First(&member).Error; err != nil || member.SourceID != 1 || member.ObservationSource != "chat_member" || !member.IsMember {
				t.Fatalf("actual member=%+v err=%v", member, err)
			}
			page, err = fixture.website.List(fixture.ctx, 7, 0, 20)
			if err != nil || len(page.Items) != 1 || page.Items[0].State != "member" || page.Items[0].CanRequest {
				t.Fatalf("member projection=%+v err=%v", page, err)
			}
			if contacts {
				countMembershipRows(t, fixture.db, &runtimedomain.Contact{}, 0)
			} else if fixture.db.Migrator().HasTable(&runtimedomain.Contact{}) {
				t.Fatal("contact table was unexpectedly created")
			}
			countMembershipRows(t, fixture.db, &userdomain.User{}, 1)
		})
	}
}

// TestMembershipAccessRejectsUnsafeJoin verifies t cases never approve mismatched, unknown, unbound or stale-route joins.
func TestMembershipAccessRejectsUnsafeJoin(t *testing.T) {
	for _, scenario := range []string{"wrong_identity", "unknown_link", "unbound", "old_route", "unbind_after_queue", "route_after_queue"} {
		t.Run(scenario, func(t *testing.T) {
			// Confirmed alien refusal also scans the existing private-contact queue.
			fixture := newMembershipAccessFixture(t, true)
			link := fixture.createInvite(t)
			update := fixture.joinUpdate(membershipAssemblyTelegramID, link)
			switch scenario {
			case "wrong_identity":
				update.JoinRequest.User.ID++
			case "unknown_link":
				update.JoinRequest.InviteLink.InviteLink = "https://t.me/+unknown-membership-access"
			case "unbound", "old_route":
				fixture.invalidate(t, scenario)
			}
			if _, err := fixture.bridge.Handle(fixture.ctx, 1, update); err != nil {
				t.Fatal(err)
			}
			if scenario == "unbind_after_queue" {
				fixture.invalidate(t, "unbound")
			}
			if scenario == "route_after_queue" {
				fixture.invalidate(t, "old_route")
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			if fixture.platform.approves != 0 {
				t.Fatal("unsafe join approved")
			}
			countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
			var join memberdomain.AccessJoin
			if err := fixture.db.First(&join).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "unknown_link" && (join.Decision != "needs_review" || fixture.platform.declines != 0) {
				t.Fatal("unknown invite was not preserved for review")
			}
		})
	}
}

// invalidate changes a binding or current route in fixture for scenario; t reports persistence errors, no return.
func (fixture *membershipAccessFixture) invalidate(t *testing.T, scenario string) {
	t.Helper()
	var err error
	if scenario == "unbound" {
		err = fixture.db.Where("user_id = ?", 7).Delete(&identitydomain.Identity{}).Error
	} else {
		err = fixture.db.Model(&groupsdomain.Target{}).Where("id = ?", 2).Updates(map[string]any{"route_version": 2, "obs_route_version": 2}).Error
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestMembershipAccessObservesIndependentMembers verifies t sees direct admissions without approving or creating invitations.
func TestMembershipAccessObservesIndependentMembers(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	for _, subject := range []int64{membershipAssemblyTelegramID + 1, membershipAssemblyTelegramID} {
		if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.memberUpdate(subject)); err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if subject == membershipAssemblyTelegramID {
			want = 1
		}
		countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, want)
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessInvite{}, 0)
	countMembershipRows(t, fixture.db, &memberdomain.AccessJoin{}, 0)
	if fixture.platform.approves != 0 || fixture.platform.creates != 0 || fixture.platform.queries != 0 {
		t.Fatal("native observation unexpectedly performed platform effects")
	}
}

// TestMembershipAccessRequiresExecutionFence verifies t cannot turn an ordinary request context into platform execution.
func TestMembershipAccessRequiresExecutionFence(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	entry, err := fixture.website.Request(fixture.ctx, 7, 2)
	if err != nil || entry.State != "processing" {
		t.Fatalf("enqueue=%+v err=%v", entry, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A worker may persist a fail-closed retry result; either return policy must produce no platform effect.
	_, runErr := fixture.service.RunAccess(ctx, 1, 25)
	if ctx.Err() != nil {
		t.Fatalf("unfenced runtime hung: %v (worker=%v)", ctx.Err(), runErr)
	}
	if fixture.platform.creates != 0 || fixture.platform.approves != 0 || fixture.platform.declines != 0 {
		t.Fatal("unfenced worker performed platform I/O")
	}
	var invite memberdomain.AccessInvite
	if err := fixture.db.First(&invite).Error; err != nil || invite.State == "active" || invite.LinkCipher != "" {
		t.Fatalf("unfenced invite became available: %+v err=%v", invite, err)
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessMember{}, 0)
}

// pagedMembershipReader supplies deterministic account rows for Bot pagination tests.
// The test double implements only the account-scoped read port.
type pagedMembershipReader struct {
	rows []membership.UserAccessGroup
}

// ListUserAccess returns rows after cursor, capped by limit; ctx/account/now are unused fixture inputs.
func (reader pagedMembershipReader) ListUserAccess(_ context.Context, _, cursor uint, limit int, _ time.Time) ([]membership.UserAccessGroup, error) {
	result := []membership.UserAccessGroup{}
	for _, row := range reader.rows {
		if row.TargetID > cursor {
			result = append(result, row)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// TestMembershipBotPaginationFiltersOtherBots verifies t receives only this Bot's beneficiary rows without skipped targets.
func TestMembershipBotPaginationFiltersOtherBots(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	reader := pagedMembershipReader{rows: []membership.UserAccessGroup{{TargetID: 2, BeneficiaryTelegramID: membershipAssemblyTelegramID}}}
	for targetID := uint(3); targetID <= 7; targetID++ {
		botID := uint(1)
		if targetID == 3 || targetID == 5 {
			botID = 9
		}
		group := &groupsdomain.Target{ID: targetID, Purpose: "membership", BotInstanceID: botID, OccupancySlot: "pagination:" + strconv.FormatUint(uint64(targetID), 10)}
		if err := fixture.db.Create(group).Error; err != nil {
			t.Fatal(err)
		}
		beneficiary := membershipAssemblyTelegramID
		if targetID == 6 {
			beneficiary++
		}
		reader.rows = append(reader.rows, membership.UserAccessGroup{TargetID: targetID, BeneficiaryTelegramID: beneficiary})
	}
	fixture.website.Reader = reader
	bot := &accessbootstrap.Bot{Website: fixture.website, Deliveries: runtimestore.New(fixture.db), DeliveryGuard: fixture.service}
	actor := shop.Actor{BotID: 1, TelegramID: membershipAssemblyTelegramID, UserID: 7, Generation: "1:7"}
	first, err := bot.List(fixture.ctx, actor, 0, 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].TargetID != "2" || first.Items[1].TargetID != "4" || first.NextCursor != "4" {
		t.Fatalf("first filtered page=%+v err=%v", first, err)
	}
	second, err := bot.List(fixture.ctx, actor, 4, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].TargetID != "7" || second.NextCursor != "" {
		t.Fatalf("second filtered page=%+v err=%v", second, err)
	}
	countMembershipRows(t, fixture.db, &memberdomain.AccessOperation{}, 0)
	if fixture.platform.creates != 0 || fixture.platform.queries != 0 {
		t.Fatal("pagination performed platform effects")
	}
}

// TestMembershipAccessHTTPUsesSessionAccount exercises real API handlers for t; body/query cannot choose beneficiary or route.
func TestMembershipAccessHTTPUsesSessionAccount(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(func(ctx *gin.Context) { ctx.Set("user_id", uint(7)); ctx.Next() })
	membershiphttp.RegisterUserRoutes(group, membershiphttp.NewUserHandler(fixture.website))
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/user/membership-groups?account_id=8&telegram_id=99"
		if method == "POST" {
			path = "/api/v1/user/membership-groups/2/invitation"
		}
		request := httptest.NewRequest(method, path, strings.NewReader(`{"account_id":8,"telegram_id":"99","bot_id":88,"chat_id":"-999"}`)).WithContext(fixture.ctx)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var response struct {
			StatusCode int             `json:"status_code"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.StatusCode != 0 || recorder.Code != 200 {
			t.Fatalf("API response=%s err=%v", recorder.Body.String(), err)
		}
		if recorder.Header().Get("Cache-Control") != "no-store" || strings.Contains(string(response.Data), "https://") {
			t.Fatal("API exposed or cached invitation")
		}
		if method == "GET" {
			countMembershipRows(t, fixture.db, &memberdomain.AccessInvite{}, 0)
		} else if !strings.Contains(string(response.Data), `"state":"processing"`) {
			t.Fatalf("request did not enqueue: %s", response.Data)
		}
	}
	var invite memberdomain.AccessInvite
	if err := fixture.db.First(&invite).Error; err != nil || invite.AccountID != 7 || invite.TelegramID != membershipAssemblyTelegramID || invite.ChatID != -1001 {
		t.Fatalf("API identity/route escaped session: %+v %v", invite, err)
	}
	if fixture.platform.creates != 0 || fixture.platform.approves != 0 {
		t.Fatal("HTTP performed platform calls")
	}
}
