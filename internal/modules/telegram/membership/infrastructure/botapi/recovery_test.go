package botapi

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// recoveryAuthStub verifies task binding and simulates revocation during token decryption.
type recoveryAuthStub struct {
	calls, failAt int
	task          uint
}

// AuthorizeMembershipRecovery rejects mismatched owners or the configured check.
func (auth *recoveryAuthStub) AuthorizeMembershipRecovery(_ context.Context, input c.BotRouteRecoveryRequest, _ c.AccessRoute) error {
	auth.calls++
	if input.TaskID != auth.task || (auth.failAt > 0 && auth.calls >= auth.failAt) {
		return c.ErrConflict
	}
	return nil
}

// TestRecoveryPlatformHasNoOrdinaryCapabilities proves stopped fenced instances use
// the restricted adapter while ordinary access still fails without a worker fence.
func TestRecoveryPlatformHasNoOrdinaryCapabilities(t *testing.T) {
	access, _, api, route := accessFixture()
	store := access.Store.(*accessStoreStub)
	store.instance.Enabled = false
	store.instance.State = "stopped"
	store.instance.RecoveryTaskID = 11
	auth := &recoveryAuthStub{task: 11}
	factory := RecoveryFactory{Access: access, Authorizer: auth}
	platform := factory.MembershipRecoveryPlatform(c.BotRouteRecoveryRequest{TaskID: 11})
	if _, ok := platform.(c.AccessPlatform); ok {
		t.Fatal("recovery exposes admission")
	}
	if _, ok := platform.(c.LifecyclePlatform); ok {
		t.Fatal("recovery exposes ban")
	}
	api.invite.IsRevoked = true
	result := platform.RevokeMembershipInvite(context.Background(), route, testInvite)
	if result.Outcome != c.AccessSucceeded || len(api.calls) != 1 || auth.calls != 2 {
		t.Fatalf("task adapter failed: %+v", result)
	}
	result = access.RevokeMembershipInvite(context.Background(), route, testInvite)
	if result.Outcome == c.AccessSucceeded || len(api.calls) != 1 {
		t.Fatal("ordinary workers unlocked")
	}
}

// TestBotAdmissionNeverApprovesOrKicksSameChat verifies the capability wrapper itself,
// not only the caller's operation selection, and checks post-decrypt withdrawal.
func TestBotAdmissionNeverApprovesOrKicksSameChat(t *testing.T) {
	access, _, api, route := accessFixture()
	access.Store.(*accessStoreStub).instance.RecoveryTaskID = 11
	input := c.BotRouteRecoveryRequest{TaskID: 11, OldBotID: 1, NewBotID: 2, OldChatID: route.ChatID, NewChatID: route.ChatID}
	factory := &RecoveryFactory{Access: access}
	platform, lifecycle := factory.MembershipBotAdmission(input, &recoveryAuthStub{task: 11})
	ctx := context.Background()
	join := c.AccessJoinAction{Route: route, TelegramID: 456}
	if platform.ApproveMembershipJoin(ctx, join).Outcome == c.AccessSucceeded || platform.DeclineMembershipJoin(ctx, join).Outcome == c.AccessSucceeded || lifecycle.BanMembershipMember(ctx, join).Outcome == c.AccessSucceeded || lifecycle.UnbanMembershipMember(ctx, join).Outcome == c.AccessSucceeded || len(api.calls) != 0 {
		t.Fatal("cross-Bot wrapper exposed unsafe effects")
	}
	input.OldBotID = 2
	input.NewBotID = 1
	input.OldChatID = -200
	input.NewChatID = route.ChatID
	platform, _ = factory.MembershipBotAdmission(input, &recoveryAuthStub{task: 11, failAt: 2})
	result := platform.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, Name: "recovery", ExpiresAt: time.Unix(api.invite.ExpireDate, 0)})
	if result.Outcome == c.AccessSucceeded || len(api.calls) != 0 {
		t.Fatal("withdrawal after decrypt allowed HTTP")
	}
}

// TestRecoveryPlatformRechecksAfterDecrypt blocks HTTP if task ownership is revoked.
func TestRecoveryPlatformRechecksAfterDecrypt(t *testing.T) {
	access, _, api, route := accessFixture()
	access.Store.(*accessStoreStub).instance.RecoveryTaskID = 11
	factory := RecoveryFactory{Access: access, Authorizer: &recoveryAuthStub{task: 11, failAt: 2}}
	platform := factory.MembershipRecoveryPlatform(c.BotRouteRecoveryRequest{TaskID: 11})
	result := platform.GetMembershipMember(context.Background(), c.AccessJoinAction{Route: route, TelegramID: 456})
	if result.Outcome == c.AccessSucceeded || len(api.calls) != 0 {
		t.Fatal("revoked task performed HTTP")
	}
}
