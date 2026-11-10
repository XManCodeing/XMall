package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// TestSupportRouteClassifiesPersistedCapability verifies the real SQL projection
// stays fail-closed while temporary evidence failures remain retryable.
func TestSupportRouteClassifiesPersistedCapability(t *testing.T) {
	for _, name := range []string{"expired", "checking", "network", "rate_limited", "left", "permissions", "disabled", "module_disabled", "route", "purpose"} {
		t.Run(name, func(t *testing.T) {
			svc, store, db, _ := setup(t)
			ctx := context.Background()
			target, err := svc.Save(ctx, 0, c.Input{Purpose: "support", BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.SaveModule(ctx, 1, 7, c.ModuleInput{Partition: "support", TargetID: target.ID, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err = svc.ResolveSupportRoute(ctx, 1); err != nil {
				t.Fatal("ready route rejected", err)
			}
			fields := map[string]any{}
			pending := false
			switch name {
			case "expired":
				fields["obs_observed_at"] = time.Now().UTC().Add(-6 * time.Minute)
				pending = true
			case "checking":
				fields["obs_state"] = "checking"
				pending = true
			case "network", "rate_limited":
				// A failed check replaces the observation with no permission data.
				fields = map[string]any{"obs_state": "unknown", "obs_error_class": name, "obs_observed_at": nil, "obs_member_status": "", "obs_can_manage_topics": false, "obs_retry_after_seconds": 73}
				pending = true
			case "left":
				fields = map[string]any{"obs_state": "left", "obs_member_status": "kicked", "obs_observed_at": time.Now().UTC().Add(-6 * time.Minute)}
			case "permissions":
				fields = map[string]any{"obs_state": "missing_permissions", "obs_can_manage_topics": false}
			case "disabled":
				fields["enabled"] = false
			case "module_disabled":
				if err = db.Model(&d.ModuleConfig{}).Where("bot_instance_id = ?", 1).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			case "route":
				fields["route_version"] = 2
			case "purpose":
				fields["purpose"] = "membership"
			}
			if len(fields) > 0 {
				if err = db.Model(&d.Target{}).Where("id = ?", target.ID).Updates(fields).Error; err != nil {
					t.Fatal(err)
				}
			}
			modules, err := store.Modules(ctx, 1)
			if err != nil || len(modules) != 1 || modules[0].Enabled || modules[0].EffectiveEnabled || modules[0].TargetID != target.ID {
				t.Fatal("effective gate changed", modules, err)
			}
			route, err := svc.ResolveSupportRoute(ctx, 1)
			if route != (c.SupportRoute{}) || errors.Is(err, c.ErrCapabilityPending) != pending || errors.Is(err, c.ErrCapability) == pending {
				t.Fatalf("route=%+v err=%v pending=%v", route, err, pending)
			}
			if name == "rate_limited" {
				var retry interface{ RetrySeconds() int }
				if !errors.As(err, &retry) || retry.RetrySeconds() != 73 {
					t.Fatal("retry delay lost", err)
				}
			}
		})
	}
}
