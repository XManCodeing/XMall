package gormstore_test

import (
	"context"
	"errors"
	"fmt"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	rr "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"reflect"
	"strings"
	"testing"
	"time"
)

// seedEntrySwitch persists a completed local entry switch in the isolated fixture.
// It deliberately retains the old active invitation and its encrypted link.
func seedEntrySwitch(t *testing.T, h *accessHarness) {
	t.Helper()
	if err := h.db.AutoMigrate(&rr.Run{}, &gd.RecoveryFence{}, &rd.Task{}); err != nil {
		t.Fatal(err)
	}
	old := h.route
	h.route.ChatID, h.route.RouteVersion = -1002, 2
	if err := h.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"telegram_chat_id": h.route.ChatID, "route_version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&rd.Task{ID: 17, Kind: rd.GroupReplace, State: rd.Completed, ConfirmedAt: &h.now},
		&gd.RecoveryFence{TaskID: 17, TargetID: 1, OldBotID: 9, NewBotID: 9, OldChatID: old.ChatID, NewChatID: h.route.ChatID, InitialRouteVersion: 1, ReleasedVersion: 4},
		&rr.Run{TaskID: 17, TargetID: 1, BotID: 9, OldChatID: old.ChatID, NewChatID: h.route.ChatID, OldRouteVersion: 1, NewRouteVersion: 2, Generation: 3, Remaining: 1, CompletionReceipt: "membership-entry:17:1:2"},
	}
	for _, row := range rows {
		if err := h.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestEntrySwitchRequestsNewRoute preserves all old facts while explicitly
// requesting a new physical invitation, without changing entitlement or expiry.
func TestEntrySwitchRequestsNewRoute(t *testing.T) {
	h, ctx := accessSetup(t)
	previous := accessActive(t, h, ctx)
	old, err := h.store.Invite(ctx, previous.InviteID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
	if err != nil {
		t.Fatal(err)
	}
	seedEntrySwitch(t, h)
	read, err := h.service.ExistingMembershipInvite(ctx, h.identity, 1)
	if err != nil || read.Link != "" || read.InviteID != 0 {
		t.Fatalf("old link leaked on current route: %+v %v", read, err)
	}
	current, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil || current.InviteID == previous.InviteID || current.InviteState != "pending" {
		t.Fatalf("new route request failed: %+v %v", current, err)
	}
	again, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil || again.InviteID != current.InviteID || h.calls != 1 {
		t.Fatalf("repeat request duplicated work: %+v %v calls=%d", again, err, h.calls)
	}
	saved, err := h.store.Invite(ctx, previous.InviteID)
	if err != nil || !reflect.DeepEqual(saved, old) {
		t.Fatalf("old invitation rewritten: %+v %v", saved, err)
	}
	after, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("entitlement changed: %+v %v", after, err)
	}
	invite, err := h.store.Invite(ctx, current.InviteID)
	if err != nil || invite.ChatID != h.route.ChatID || invite.RouteVersion != 2 {
		t.Fatalf("wrong new physical route: %+v %v", invite, err)
	}
}

// TestEntrySwitchExpiryKeepsRouteLiabilities verifies new-route expiry cleanup
// remains operational while old-route cleanup is retained for review, not falsified.
func TestEntrySwitchExpiryKeepsRouteLiabilities(t *testing.T) {
	h, ctx := accessSetup(t)
	old := accessActive(t, h, ctx)
	seedEntrySwitch(t, h)
	current := accessActive(t, h, ctx)
	expireCleanupHarness(t, h)
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 25); err != nil {
		t.Fatal(err)
	}
	platform := &cleanupRacePlatform{accessHarness: h}
	h.onMember = func(context.Context) {
		if platform.bans > 0 {
			h.memberStatus = "left"
		}
	}
	lifecycle, err := a.NewLifecycle(h.store, h, platform)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Now = func() time.Time { return h.now }
	if _, err := lifecycle.Run(ctx, 9, 25); err != nil {
		t.Fatal(err)
	}
	var tasks []d.AccessOperation
	if err := h.db.Where("kind = ?", "lifecycle").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	oldReview, newDone := false, false
	for _, task := range tasks {
		oldReview = oldReview || (task.ChatID == -1001 && task.State == "needs_review")
		newDone = newDone || (task.ChatID == -1002 && task.State == "succeeded")
	}
	if !oldReview || !newDone || platform.bans != 1 {
		t.Fatalf("expiry route isolation failed: %+v bans=%d", tasks, platform.bans)
	}
	for _, inviteID := range []uint{old.InviteID, current.InviteID} {
		var count int64
		if err := h.db.Model(&d.InviteCleanupItem{}).Where("invite_id = ?", inviteID).Count(&count).Error; err != nil || count == 0 {
			t.Fatalf("cleanup responsibility lost: invite=%d count=%d err=%v", inviteID, count, err)
		}
	}
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err == nil {
		t.Fatal("expired member received a new invitation")
	}
}

// TestEntrySwitchRejectsUnprovenOrUnknown never treats stale proof or late
// uncertain Telegram effects as permission to create a second invitation.
func TestEntrySwitchRejectsUnprovenOrUnknown(t *testing.T) {
	for _, scenario := range []string{"no_receipt", "not_released", "not_completed", "unknown_invite", "unknown_receipt", "unknown_operation", "wrong_route", "fenced"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := accessSetup(t)
			previous := accessActive(t, h, ctx)
			seedEntrySwitch(t, h)
			var err error
			switch scenario {
			case "no_receipt":
				err = h.db.Model(&rr.Run{}).Where("task_id = ?", 17).Update("completion_receipt", "").Error
			case "not_released":
				err = h.db.Model(&gd.RecoveryFence{}).Where("task_id = ?", 17).Update("released_version", 0).Error
			case "not_completed":
				err = h.db.Model(&rd.Task{}).Where("id = ?", 17).Update("state", "recovering").Error
			case "unknown_invite":
				err = h.db.Model(&d.AccessInvite{}).Where("id = ?", previous.InviteID).Update("state", "unknown").Error
			case "unknown_receipt":
				var operation d.AccessOperation
				if err = h.db.Where("invite_id = ?", previous.InviteID).First(&operation).Error; err == nil {
					err = h.db.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: 999, Action: "create", Outcome: "unknown"}).Error
				}
			case "unknown_operation":
				err = h.db.Model(&d.AccessOperation{}).Where("invite_id = ?", previous.InviteID).Update("state", "unknown").Error
			case "wrong_route":
				err = h.db.Model(&rr.Run{}).Where("task_id = ?", 17).Update("new_chat_id", -1003).Error
			case "fenced":
				err = h.db.Model(&gd.Target{}).Where("id = ?", 1).Update("recovery_task_id", 18).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err == nil && current != nil && current.InviteID != previous.InviteID {
				t.Fatalf("unproven replacement accepted: %+v", current)
			}
			var count int64
			if err := h.db.Model(&d.AccessInvite{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal(fmt.Sprintf("unexpected invitations: %d", count), err)
			}
		})
	}
}

// cleanupRacePlatform 包装内存平台，记录撤销链接并在回包前注入续费；不访问真实 Telegram。
type cleanupRacePlatform struct {
	*accessHarness
	revokedLinks []string
	onRevoke     func()
	bans         int
}

// RevokeMembershipInvite 记录本次物理链接并执行测试回调，返回模拟结果。
func (platform *cleanupRacePlatform) RevokeMembershipInvite(_ context.Context, _ c.AccessRoute, link string) c.AccessAPIResult {
	platform.revokedLinks = append(platform.revokedLinks, link)
	if platform.onRevoke != nil {
		platform.onRevoke()
	}
	return c.AccessAPIResult{Outcome: platform.outcome}
}

// BanMembershipMember 记录模拟踢人，不访问真实平台。
func (platform *cleanupRacePlatform) BanMembershipMember(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	platform.bans++
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// UnbanMembershipMember 返回模拟解禁结果。
func (platform *cleanupRacePlatform) UnbanMembershipMember(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// TestCleanupMissingMemberObservation 通过既有生命周期只读查询落可信事实，不把缺失观测当离群。
func TestCleanupMissingMemberObservation(t *testing.T) {
	for _, status := range []string{"member", "left", "failed", "renewed"} {
		t.Run(status, func(t *testing.T) {
			h, ctx := accessSetup(t)
			accessActive(t, h, ctx)
			expireCleanupHarness(t, h)
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			h.memberStatus = status
			if status == "failed" {
				h.memberOutcome = c.AccessRetryable
			}
			if status == "renewed" {
				h.memberStatus = "member"
				renewCleanupEntitlement(t, h)
			}
			platform := &cleanupRacePlatform{accessHarness: h}
			service, err := a.NewLifecycle(h.store, h, platform)
			if err != nil {
				t.Fatal(err)
			}
			service.Now = func() time.Time { return h.now }
			if count, err := service.Run(ctx, 9, 1); err != nil || count != 1 {
				t.Fatalf("read missing member: %d %v", count, err)
			}
			var operation d.AccessOperation
			if err := h.db.Where("kind = ?", "lifecycle").First(&operation).Error; err != nil {
				t.Fatal(err)
			}
			var member d.AccessMember
			read := h.db.Where("target_id = ?", 1).Find(&member)
			if read.Error != nil {
				t.Fatal(read.Error)
			}
			if status == "failed" {
				if read.RowsAffected != 0 || operation.State != "retryable" || operation.LifecyclePhase != "check" {
					t.Fatalf("failed read fabricated observation: %+v", operation)
				}
				return
			}
			if read.RowsAffected != 1 || member.Status != h.memberStatus || member.ObservationSource != "query" {
				t.Fatalf("trusted observation missing: %+v", member)
			}
			expected := "ban"
			if status == "left" {
				expected = "not_required"
			}
			if status == "renewed" {
				expected = "done"
			}
			if operation.LifecyclePhase != expected || platform.bans != 0 {
				t.Fatalf("unexpected lifecycle: %+v bans=%d", operation, platform.bans)
			}
		})
	}
}

// TestCleanupUndispatchedCreateRenewal 安全取消未租用创建，已租用未知仍保留责任。
func TestCleanupUndispatchedCreateRenewal(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		name := "not_dispatched"
		if dispatched {
			name = "dispatched"
		}
		t.Run(name, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			if dispatched {
				if _, err := h.store.ClaimAccess(ctx, 9, "dispatched-cleanup-token", h.now, time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			expireCleanupHarness(t, h)
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			renewCleanupEntitlement(t, h)
			current, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if dispatched {
				if !errors.Is(err, c.ErrConflict) {
					t.Fatalf("dispatched invite replaced: %v", err)
				}
				return
			}
			if err != nil || current.InviteID == entry.InviteID || current.InviteState != "pending" {
				t.Fatalf("cancelled create blocked renewal: %+v %v", current, err)
			}
			var item d.InviteCleanupItem
			if err := h.db.Where("invite_id = ?", entry.InviteID).First(&item).Error; err != nil || item.State != "succeeded" || item.Reason != "create_not_dispatched" {
				t.Fatalf("missing cancellation evidence: %+v %v", item, err)
			}
			if h.calls != 0 {
				t.Fatal("cancelled creation was dispatched")
			}
		})
	}
}

// renewCleanupEntitlement 插入新一期真实来源并推进投影版本；失败直接终止当前测试。
func renewCleanupEntitlement(t *testing.T, h *accessHarness) {
	t.Helper()
	source := d.LedgerSource{OrderItemID: 2, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: h.identity.TelegramID, AccountID: 7, OrderID: 2, PaidAt: h.now, Kind: d.TermMonths, Months: 1, CreatedAt: h.now}
	if err := h.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&d.LedgerProjection{}).Where("target_id = ?", 1).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
}

// TestCleanupRenewalAtExpiryBoundary 验证退休不可逆，续费拒绝取链、派送和批准，回收后才能新建。
func TestCleanupRenewalAtExpiryBoundary(t *testing.T) {
	h, ctx := accessSetup(t)
	entry := accessActive(t, h, ctx)
	delivery, err := h.service.PrepareInviteDelivery(ctx, h.identity, entry.InviteID)
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
	if err != nil || eligible.ExpiresAt == nil {
		t.Fatalf("missing expiry: %+v %v", eligible, err)
	}
	expireCleanupHarness(t, h)
	h.now = *eligible.ExpiresAt
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	renewCleanupEntitlement(t, h)
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("retired invitation reused: %v", err)
	}
	existing, err := h.service.ExistingMembershipInvite(ctx, h.identity, 1)
	if err != nil || existing.Link != "" || existing.InviteState == "active" {
		t.Fatalf("retired invitation exposed: %+v %v", existing, err)
	}
	message := rt.Message{Purpose: "panel", ChatID: h.identity.TelegramID, OwnerUserID: h.identity.AccountID, InviteDelivery: delivery}
	if err := h.service.AuthorizeInviteDelivery(ctx, 9, message); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("queued retired delivery authorized: %v", err)
	}
	if _, err := h.service.PrepareInviteDelivery(ctx, h.identity, entry.InviteID); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("retired delivery authorized: %v", err)
	}
	invite, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil || !invite.Retired {
		t.Fatalf("retirement missing: %v", err)
	}
	input := c.AccessJoinRequest{BotID: 9, ChatID: -1001, TelegramID: h.identity.TelegramID, UpdateID: 123, InviteLink: entry.Link, ReceivedAt: h.now}
	if _, err := h.store.EnqueueJoin(ctx, input, invite, "approve", h.now); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("retired approve authorized: %v", err)
	}
	platform := &cleanupRacePlatform{accessHarness: h}
	h.service.Platform = platform
	if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	current, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil || current.InviteID == entry.InviteID || current.InviteState != "pending" || len(platform.revokedLinks) != 1 {
		t.Fatalf("retired invitation not replaced: %+v calls=%d err=%v", current, len(platform.revokedLinks), err)
	}
}

// TestCleanupRenewalDuringRevoke 验证飞行中续费不抹掉真实成功，unknown 不重放或新建。
func TestCleanupRenewalDuringRevoke(t *testing.T) {
	for _, outcome := range []string{c.AccessSucceeded, c.AccessUnknown} {
		t.Run(outcome, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			expireCleanupHarness(t, h)
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			h.outcome = outcome
			platform := &cleanupRacePlatform{accessHarness: h, onRevoke: func() { renewCleanupEntitlement(t, h) }}
			h.service.Platform = platform
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			expected := "revoked"
			if outcome == c.AccessUnknown {
				expected = "unknown"
			}
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil || invite.State != expected || !invite.Retired || len(platform.revokedLinks) != 1 {
				t.Fatalf("revoke evidence changed: state=%s calls=%d err=%v", invite.State, len(platform.revokedLinks), err)
			}
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil || len(platform.revokedLinks) != 1 {
				t.Fatalf("revoke replayed: calls=%d err=%v", len(platform.revokedLinks), err)
			}
			if outcome == c.AccessUnknown {
				if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); !errors.Is(err, c.ErrConflict) {
					t.Fatalf("unknown liability recreated: %v", err)
				}
			}
		})
	}
}

// TestCleanupCreateReturnsAfterExpiry 验证创建在有效期内发出但到期后返回时，URL 证据可清理且不能发放。
func TestCleanupCreateReturnsAfterExpiry(t *testing.T) {
	h, ctx := accessSetup(t)
	entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	h.afterCall = func() { expireCleanupHarness(t, h) }
	if _, err := h.service.RunAccess(ctx, 9, 1); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("late creation must lose its lease: %v", err)
	}
	h.afterCall = nil
	platform := &cleanupRacePlatform{accessHarness: h}
	h.service.Platform = platform
	if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	invite, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil || invite.State != "revoked" || len(platform.revokedLinks) != 1 {
		t.Fatalf("late creation not cleaned: invite=%+v calls=%d err=%v", invite, len(platform.revokedLinks), err)
	}
}

// TestCleanupOldCycleDoesNotTouchRenewedInvite 验证旧周期重复扫描与迟到成功回包均只更新原邀请 ID。
func TestCleanupOldCycleDoesNotTouchRenewedInvite(t *testing.T) {
	h, ctx := accessSetup(t)
	old := accessActive(t, h, ctx)
	expireCleanupHarness(t, h)
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	operation, err := h.store.ClaimAccess(ctx, 9, "old-cycle-cleanup-token", h.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.FinishAccess(ctx, operation, c.AccessAPIResult{Outcome: c.AccessSucceeded}, "", "", h.now); err != nil {
		t.Fatal(err)
	}
	renewCleanupEntitlement(t, h)
	current, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil || current.InviteID == old.InviteID {
		t.Fatalf("new period invitation missing: %+v %v", current, err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	if err := h.store.FinishAccess(ctx, operation, c.AccessAPIResult{Outcome: c.AccessSucceeded}, "", "", h.now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("old completion accepted: %v", err)
	}
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	invite, err := h.store.Invite(ctx, current.InviteID)
	if err != nil || invite.State != "active" {
		t.Fatalf("new invitation changed: %+v %v", invite, err)
	}
	var liabilities int64
	if err := h.db.Model(&d.InviteCleanupItem{}).Where("invite_id = ?", current.InviteID).Count(&liabilities).Error; err != nil || liabilities != 0 {
		t.Fatalf("old cycle captured renewed invitation: %d %v", liabilities, err)
	}
}

// TestCleanupMemberCompletionIsIndependent 验证成员移出成功不覆盖链接未知或明确撤销失败。
func TestCleanupMemberCompletionIsIndependent(t *testing.T) {
	for _, cleanupOutcome := range []string{c.AccessUnknown, c.AccessFailed} {
		t.Run(cleanupOutcome, func(t *testing.T) {
			h, ctx := accessSetup(t)
			if cleanupOutcome == c.AccessUnknown {
				h.outcome = c.AccessUnknown
			}
			if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.ObserveAccess(ctx, c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: h.identity.TelegramID, Status: "member", ObservedAt: h.now, Source: "query"}); err != nil {
				t.Fatal(err)
			}
			expireCleanupHarness(t, h)
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			if cleanupOutcome == c.AccessFailed {
				h.outcome = c.AccessFailed
				if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
					t.Fatal(err)
				}
			}
			operation, err := h.store.ClaimLifecycle(ctx, 9, "member-independent-token", h.now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.FinishLifecycle(ctx, operation, c.LifecycleResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessSucceeded}, Phase: "done"}, h.now); err != nil {
				t.Fatal(err)
			}
			rows, err := h.store.ListLifecycle(ctx, 9, 0, 100)
			if err != nil || len(rows) != 1 {
				t.Fatalf("list: %+v %v", rows, err)
			}
			if rows[0].MemberOutcome != c.AccessSucceeded || rows[0].InviteCleanup != cleanupOutcome || rows[0].State == c.AccessSucceeded {
				t.Fatalf("member completion hid link liability: %+v", rows[0])
			}
		})
	}
}

// TestCleanupUnknownWithoutURLSurvivesClock 区分可信历史短效自然失效与不能靠时间结案的未知责任。
func TestCleanupUnknownWithoutURLSurvivesClock(t *testing.T) {
	for _, scenario := range []string{"long_term", "limited", "legacy", "untrusted", "unbounded", "late_attempt", "creating", "missing_creation", "margin"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := accessSetup(t)
			h.outcome = c.AccessUnknown
			entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "long_term" {
				setLegacyLimitedInvite(t, h)
			}
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil {
				t.Fatal(err)
			}
			expireCleanupHarness(t, h)
			h.now = h.now.AddDate(10, 0, 0)
			changes := map[string]any{}
			switch scenario {
			case "legacy":
				changes["term"] = ""
			case "untrusted":
				changes["expiry_trusted"] = false
			case "unbounded":
				changes["expires_at"] = invite.CreatedAt.Add(25 * time.Hour)
			case "margin":
				// 保持权益已失效，但邀请仍位于可信截止后的五分钟安全缓冲内。
				changes["created_at"], changes["expires_at"] = h.now.Add(-time.Hour), h.now.Add(-4*time.Minute)
			case "late_attempt", "creating", "missing_creation":
				operation := h.db.Model(&d.AccessOperation{}).Where("invite_id = ? AND kind = ?", invite.ID, "create")
				if scenario == "late_attempt" {
					err = operation.Update("last_attempt_at", invite.ExpiresAt.Add(time.Second)).Error
				} else if scenario == "creating" {
					err = operation.Update("state", "creating").Error
				} else {
					err = operation.Delete(&d.AccessOperation{}).Error
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(changes) > 0 {
				if err := h.db.Model(&d.AccessInvite{}).Where("id = ?", invite.ID).Updates(changes).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			expected, completed := "unknown", uint(0)
			if scenario == "limited" || scenario == "legacy" {
				expected, completed = "succeeded", 1
			}
			rows, err := h.store.ListLifecycle(ctx, 9, 0, 100)
			if err != nil || len(rows) != 1 || rows[0].InviteCleanup != expected || rows[0].InviteCleanupCompleted != completed {
				t.Fatalf("expiry evidence mismatch: %+v %v", rows, err)
			}
			if completed == 1 {
				var operation d.AccessOperation
				if err := h.db.Where("invite_id = ? AND kind = ?", invite.ID, "create").First(&operation).Error; err != nil || operation.State != "unknown" {
					t.Fatalf("natural expiry rewrote unknown creation: %+v %v", operation, err)
				}
			}
		})
	}
}

// setLegacyLimitedInvite 将测试请求转换为历史有限邀请，保留原有异常期限安全回归断言。
// t 为测试上下文，h 为隔离数据库；生产创建默认长期，不改变历史测试的原始安全场景。
func setLegacyLimitedInvite(t *testing.T, h *accessHarness) {
	t.Helper()
	if err := h.db.Model(&d.AccessInvite{}).Where("id > 0").Updates(map[string]any{"term": d.InviteTermLimited, "expires_at": h.now.Add(24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestLongTermInviteContract 验证新邀请长期、旧邀请不被延长、长期未知不能按时钟重建。
func TestLongTermInviteContract(t *testing.T) {
	h, ctx := accessSetup(t)
	entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil || invite.Term != d.InviteTermLongTerm || !invite.ExpiresAt.IsZero() {
		t.Fatalf("term: %+v %v", invite, err)
	}
	setLegacyLimitedInvite(t, h)
	legacy, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
		t.Fatal(err)
	}
	preserved, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil || preserved.Term != d.InviteTermLimited || !preserved.ExpiresAt.Equal(legacy.ExpiresAt) {
		t.Fatal("legacy expiry changed", err)
	}
}

// expireCleanupHarness 让全部来源失效并创建清理扫描表，返回同一测试时钟。
func expireCleanupHarness(t *testing.T, h *accessHarness) {
	t.Helper()
	if err := h.db.AutoMigrate(s.LifecycleModels()...); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 2, 0)
}

// TestCleanupInviteOnlyAndUnknown 检查仅邀请/已离群也被扫描，未知 URL 永不算回收成功。
func TestCleanupInviteOnlyAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "known"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			h, ctx := accessSetup(t)
			if unknown {
				h.outcome = c.AccessUnknown
			}
			if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			expireCleanupHarness(t, h)
			if count, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil || count != 1 {
				t.Fatalf("scan %d %v", count, err)
			}
			rows, err := h.store.ListLifecycle(ctx, 9, 0, 100)
			if err != nil || len(rows) != 1 {
				t.Fatalf("tasks %+v %v", rows, err)
			}
			if rows[0].MemberOutcome != "pending" || rows[0].InviteCleanupTotal != 1 || rows[0].State == "succeeded" {
				t.Fatalf("dual state %+v", rows[0])
			}
			if unknown {
				if rows[0].InviteCleanupUnknown != 1 {
					t.Fatalf("unknown %+v", rows[0])
				}
				if _, err := h.store.ClaimAccess(ctx, 9, "cleanup-unknown-token", h.now, time.Minute); !errors.Is(err, c.ErrNotFound) {
					t.Fatal("unknown recreated", err)
				}
			} else {
				if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
					t.Fatal(err)
				}
				rows, err = h.store.ListLifecycle(ctx, 9, 0, 100)
				if err != nil || rows[0].InviteCleanupCompleted != 1 || rows[0].MemberOutcome != "pending" || rows[0].State == "succeeded" {
					t.Fatalf("cleanup %+v %v", rows, err)
				}
			}
		})
	}
}

// TestCleanupRevokePostCallFence 保留平台调用后路由/租约栅栏，绝不因退休授权放开代次变更。
func TestCleanupRevokePostCallFence(t *testing.T) {
	for _, change := range []string{"route", "fence"} {
		t.Run(change, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			expireCleanupHarness(t, h)
			if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
				t.Fatal(err)
			}
			platform := &cleanupRacePlatform{accessHarness: h, onRevoke: func() {
				if change == "route" {
					h.route.RouteVersion++
				} else {
					h.fence = false
				}
			}}
			h.service.Platform = platform
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil || invite.State != "unknown" || len(platform.revokedLinks) != 1 {
				t.Fatalf("post-call fence bypassed: %v", err)
			}
		})
	}
}

// TestCleanupLateCreateEvidence 保证失租迟到创建仍保存证据并加入回收，未知 URL 不会消失。
func TestCleanupLateCreateEvidence(t *testing.T) {
	h, ctx := accessSetup(t)
	entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := h.store.ClaimAccess(ctx, 9, "late-cleanup-token", h.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expireCleanupHarness(t, h)
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	renewCleanupEntitlement(t, h)
	cipher, err := h.Seal("https://t.me/+late-cleanup-link")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.FinishAccess(ctx, operation, c.AccessAPIResult{Outcome: c.AccessSucceeded}, cipher, strings.Repeat("e", 64), h.now); !errors.Is(err, c.ErrConflict) {
		t.Fatal("late result lost CAS", err)
	}
	var revoke d.AccessOperation
	if err := h.db.Where("invite_id = ? AND kind = ?", entry.InviteID, "revoke").First(&revoke).Error; err != nil {
		t.Fatal("late evidence not queued", err)
	}
	if revoke.ParentOperationID != 0 || revoke.CleanupCycleID == 0 {
		t.Fatal("cleanup reused parent field")
	}
}

// TestCleanupHistoricalRoutes 保留旧物理路由回收责任，不借新路由权限假成功。
func TestCleanupHistoricalRoutes(t *testing.T) {
	h, ctx := accessSetup(t)
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	expireCleanupHarness(t, h)
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	h.route.ChatID--
	h.route.RouteVersion++
	if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := h.store.ListLifecycle(ctx, 9, 0, 100)
	if err != nil || len(rows) != 1 || rows[0].InviteCleanupFailed != 1 || rows[0].State == "succeeded" {
		t.Fatalf("route liability %+v %v", rows, err)
	}
}

// TestCleanupAllSourcesAndRenewal 检查续费保留权益，但不阻止精确退休链接回收。
func TestCleanupAllSourcesAndRenewal(t *testing.T) {
	h, ctx := accessSetup(t)
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	expireCleanupHarness(t, h)
	if _, err := h.store.ScanLifecycle(ctx, 9, 100, h.now); err != nil {
		t.Fatal(err)
	}
	source := d.LedgerSource{OrderItemID: 2, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: h.identity.TelegramID, AccountID: 7, OrderID: 2, PaidAt: h.now, Kind: d.TermPermanent, CreatedAt: h.now}
	if err := h.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	var operation d.AccessOperation
	if err := h.db.Where("kind = ?", "revoke").First(&operation).Error; err != nil {
		t.Fatal(err)
	}
	if operation.State != "succeeded" {
		t.Fatal("retired invite not revoked")
	}
	eligible, err := h.store.Eligibility(context.Background(), h.identity, 1, h.now)
	if err != nil || !eligible.Active {
		t.Fatal("renewal missing", err)
	}
}
