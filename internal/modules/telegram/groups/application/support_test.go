package application

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

type supportRouteStore struct {
	c.Store
	target  d.Target
	modules []c.ModuleView
}

func (s *supportRouteStore) Get(context.Context, uint) (*d.Target, error) { return &s.target, nil }
func (s *supportRouteStore) Modules(context.Context, uint) ([]c.ModuleView, error) {
	return s.modules, nil
}

type supportRouteProbe struct {
	c.Probe
	identity c.Identity
}

func (p supportRouteProbe) Instance(context.Context, uint) (c.Identity, error) {
	return p.identity, nil
}

type actorProbe struct {
	supportRouteProbe
	allowed bool
	after   func()
	calls   int
}

func (p *actorProbe) SupportMember(context.Context, uint, int64, int64) (bool, error) {
	p.calls++
	if p.after != nil {
		p.after()
	}
	return p.allowed, nil
}
func TestSupportMemberRechecksRouteAfterLiveLookup(t *testing.T) {
	for _, name := range []string{"valid", "left", "changed_route", "wrong_chat", "anonymous"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			store := &supportRouteStore{target: d.Target{ID: 9, BotInstanceID: 1, TelegramChatID: -100, Purpose: "support", Enabled: true, RouteVersion: 2, Observation: d.Observation{State: "ready", RouteVersion: 2, Generation: 7, BotTelegramID: 99, ObservedAt: &now, ChatType: "supergroup", IsForum: true, MemberStatus: "administrator", CanManageTopics: true}}, modules: []c.ModuleView{{ModuleConfig: d.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 9}, EffectiveEnabled: true}}}
			probe := &actorProbe{supportRouteProbe: supportRouteProbe{identity: c.Identity{TelegramID: 99, Generation: 7, Running: true}}, allowed: true}
			chat, actor := int64(-100), int64(77)
			switch name {
			case "left":
				probe.allowed = false
			case "changed_route":
				probe.after = func() { store.target.RouteVersion++; store.target.Observation.RouteVersion++ }
			case "wrong_chat":
				chat--
			case "anonymous":
				actor = 0
			}
			svc := New(store, probe)
			svc.Now = func() time.Time { return now }
			err := svc.AuthorizeSupportMember(context.Background(), 1, chat, actor, 2)
			if (err == nil) != (name == "valid") {
				t.Fatalf("err=%v", err)
			}
			if (name == "wrong_chat" || name == "anonymous") && probe.calls != 0 {
				t.Fatal("invalid actor/route reached platform")
			}
		})
	}
}

func TestSupportRouteRequiresFreshGenerationAndCurrentConfiguration(t *testing.T) {
	for _, name := range []string{"valid", "old_generation", "old_bot_identity", "expired", "old_route", "missing_permission", "disabled", "wrong_purpose", "wrong_bot", "module_disabled", "module_ineffective", "duplicate_module", "stopped"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			observed := now.Add(-time.Minute)
			store := &supportRouteStore{target: d.Target{ID: 9, BotInstanceID: 1, TelegramChatID: -100, Purpose: "support", Enabled: true, RouteVersion: 2, Observation: d.Observation{State: "ready", RouteVersion: 2, Generation: 7, BotTelegramID: 99, ObservedAt: &observed, ChatType: "supergroup", IsForum: true, MemberStatus: "administrator", CanManageTopics: true}}, modules: []c.ModuleView{{ModuleConfig: d.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 9}, EffectiveEnabled: true}}}
			probe := supportRouteProbe{identity: c.Identity{TelegramID: 99, Generation: 7, Running: true}}
			switch name {
			case "old_generation":
				store.target.Observation.Generation--
			case "old_bot_identity":
				store.target.Observation.BotTelegramID--
			case "expired":
				observed = now.Add(-10 * time.Minute)
			case "old_route":
				store.target.RouteVersion++
			case "missing_permission":
				store.target.Observation.CanManageTopics = false
			case "disabled":
				store.target.Enabled = false
			case "wrong_purpose":
				store.target.Purpose = "membership"
			case "wrong_bot":
				store.target.BotInstanceID = 2
			case "module_disabled":
				store.modules[0].Enabled = false
			case "module_ineffective":
				store.modules[0].EffectiveEnabled = false
			case "duplicate_module":
				store.modules = append(store.modules, store.modules[0])
			case "stopped":
				probe.identity.Running = false
			}
			svc := New(store, probe)
			svc.Now = func() time.Time { return now }
			route, err := svc.ResolveSupportRoute(context.Background(), 1)
			if (err == nil) != (name == "valid") {
				t.Fatalf("route=%+v err=%v", route, err)
			}
			if name == "valid" && (route.ChatID != -100 || route.Generation != 7 || route.RouteVersion != 2 || route.TargetID != 9) {
				t.Fatalf("route=%+v", route)
			}
		})
	}
}
