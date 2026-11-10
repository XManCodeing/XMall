package application

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// TestMembershipRouteRequiresFreshCapabilities verifies membership access independently of new sales.
func TestMembershipRouteRequiresFreshCapabilities(t *testing.T) {
	for _, scenario := range []string{"valid_sales_closed", "disabled", "support_target", "stopped", "stale", "old_generation", "old_bot", "old_route", "no_invite", "no_restrict", "wrong_target", "checking", "recovery_fenced"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			observed := now.Add(-time.Minute)
			store := &supportRouteStore{target: d.Target{ID: 9, BotInstanceID: 1, TelegramChatID: -100, Purpose: "membership", Enabled: true, RouteVersion: 2, Observation: d.Observation{State: "ready", RouteVersion: 2, Generation: 7, BotTelegramID: 99, ObservedAt: &observed, ChatType: "supergroup", MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true}}}
			probe := supportRouteProbe{identity: c.Identity{TelegramID: 99, Generation: 7, Running: true}}
			switch scenario {
			case "recovery_fenced":
				store.target.RecoveryTaskID = 71
			case "disabled":
				store.target.Enabled = false
			case "support_target":
				store.target.Purpose = "support"
			case "stopped":
				probe.identity.Running = false
			case "stale":
				observed = now.Add(-10 * time.Minute)
			case "old_generation":
				store.target.Observation.Generation--
			case "old_bot":
				store.target.Observation.BotTelegramID--
			case "old_route":
				store.target.RouteVersion++
			case "no_invite":
				store.target.Observation.CanInviteUsers = false
			case "no_restrict":
				store.target.Observation.CanRestrictMembers = false
			case "wrong_target":
				store.target.ID++
			case "checking":
				store.target.Observation.State = "checking"
			}
			service := New(store, probe)
			service.Now = func() time.Time { return now }
			route, err := service.MembershipRoute(context.Background(), 9)
			if (err == nil) != (scenario == "valid_sales_closed") {
				t.Fatalf("scenario=%s route=%+v err=%v", scenario, route, err)
			}
			if err == nil && (route.BotID != 1 || route.Generation != 7 || route.ChatID != -100) {
				t.Fatalf("incorrect membership route: %+v", route)
			}
		})
	}
}
