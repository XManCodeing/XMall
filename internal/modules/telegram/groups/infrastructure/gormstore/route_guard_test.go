package gormstore_test

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"strconv"
	"testing"
	"time"
)

func TestRouteChangesRequireSeparateDisableCAS(t *testing.T) {
	for _, layer := range []string{"application", "store"} {
		for _, change := range []string{"chat", "purpose", "instance"} {
			t.Run(layer+"/"+change, func(t *testing.T) {
				svc, store, _, _ := setup(t)
				ctx := context.Background()
				v := create(t, svc)
				if _, err := svc.Check(ctx, v.ID, 1, 7, "manual"); err != nil {
					t.Fatal(err)
				}
				save := func(in c.Input) error {
					if layer == "application" {
						_, err := svc.Save(ctx, v.ID, in)
						return err
					}
					chat, _ := strconv.ParseInt(in.TelegramChatID, 10, 64)
					return store.Save(ctx, &d.Target{ID: v.ID, Name: in.Name, Purpose: in.Purpose, BotInstanceID: in.BotInstanceID, TelegramChatID: chat, Enabled: in.Enabled}, in.ExpectedVersion, in.ExpectedRouteVersion)
				}
				base := c.Input{Name: v.Name, Purpose: v.Purpose, BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true, ExpectedVersion: 1, ExpectedRouteVersion: 1}
				if err := save(base); err != nil {
					t.Fatal(err)
				}
				base.ExpectedVersion = 2
				for _, enabled := range []bool{false, true} {
					in := base
					in.Enabled = enabled
					switch change {
					case "chat":
						in.TelegramChatID = "-1002"
					case "purpose":
						in.Purpose = "membership"
					case "instance":
						in.BotInstanceID = 2
					}
					if err := save(in); !errors.Is(err, c.ErrConflict) {
						t.Fatalf("enabled route changed with enabled=%v: %v", enabled, err)
					}
				}
				current, _ := svc.Detail(ctx, v.ID)
				if !current.Enabled || current.Version != 2 || current.RouteVersion != 1 || len(current.Routes) != 1 {
					t.Fatalf("rejected save mutated target: %+v", current)
				}
				base.Name = "renamed"
				if err := save(base); err != nil {
					t.Fatal("name edit", err)
				}
				base.ExpectedVersion = 3
				current, _ = svc.Detail(ctx, v.ID)
				if current.Name != "renamed" || current.RouteVersion != 1 || len(current.Routes) != 1 {
					t.Fatal("name edit changed route")
				}
				stale := base
				stale.ExpectedVersion = 2
				stale.Enabled = false
				if err := save(stale); !errors.Is(err, c.ErrConflict) {
					t.Fatal("stale config accepted", err)
				}
				stale = base
				stale.ExpectedRouteVersion = 2
				if err := save(stale); !errors.Is(err, c.ErrConflict) {
					t.Fatal("stale route accepted", err)
				}
				base.Enabled = false
				if err := save(base); err != nil {
					t.Fatal("disable-only", err)
				}
				base.ExpectedVersion = 4
				current, _ = svc.Detail(ctx, v.ID)
				if current.Enabled || current.RouteVersion != 1 || len(current.Routes) != 1 {
					t.Fatal("disable-only changed route")
				}
				switch change {
				case "chat":
					base.TelegramChatID = "-1002"
				case "purpose":
					base.Purpose = "membership"
				case "instance":
					base.BotInstanceID = 2
				}
				if err := save(base); err != nil {
					t.Fatal("disabled change", err)
				}
				current, _ = svc.Detail(ctx, v.ID)
				if current.Enabled || current.Version != 5 || current.RouteVersion != 2 || len(current.Routes) != 2 || current.Routes[1].RetiredAt == nil {
					t.Fatalf("invalid new route: %+v", current)
				}
			})
		}
	}
}

func TestModuleReadsGateCurrentTarget(t *testing.T) {
	for _, change := range []string{"disabled", "purpose", "instance", "stale", "route", "error", "permissions", "missing"} {
		t.Run(change, func(t *testing.T) {
			svc, store, db, _ := setup(t)
			ctx := context.Background()
			v := create(t, svc)
			if _, err := svc.Check(ctx, v.ID, 1, 7, "manual"); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Save(ctx, v.ID, c.Input{Name: v.Name, Purpose: v.Purpose, BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true, ExpectedVersion: 1, ExpectedRouteVersion: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.SaveModule(ctx, 1, 7, c.ModuleInput{Partition: "support", TargetID: v.ID, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			rows, err := store.Modules(ctx, 1)
			if err != nil || len(rows) != 1 || !rows[0].Enabled || !rows[0].ConfiguredEnabled || !rows[0].EffectiveEnabled {
				t.Fatal("valid module gated", rows, err)
			}
			fields := map[string]any{}
			switch change {
			case "disabled":
				fields["enabled"] = false
			case "purpose":
				fields["purpose"] = "membership"
			case "instance":
				fields["bot_instance_id"] = 2
			case "stale":
				fields["obs_observed_at"] = time.Now().UTC().Add(-6 * time.Minute)
			case "route":
				fields["route_version"] = 2
			case "error":
				fields["obs_error_class"] = "network"
			case "permissions":
				fields["obs_state"] = "missing_permissions"
				fields["obs_can_manage_topics"] = false
			case "missing":
				if err := db.Delete(&d.Target{}, v.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(fields) > 0 {
				if err := db.Model(&d.Target{}).Where("id = ?", v.ID).Updates(fields).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, read := range []func(context.Context, uint) ([]c.ModuleView, error){store.Modules, svc.Modules} {
				rows, err = read(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row.Partition == "support" && (row.Enabled || row.EffectiveEnabled || !row.ConfiguredEnabled || row.Version != 1 || row.TargetID != v.ID) {
						t.Fatalf("invalid reference active or CAS changed: %+v", row)
					}
				}
			}
			var persisted d.ModuleConfig
			if err := db.First(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if !persisted.Enabled || persisted.Version != 1 {
				t.Fatal("read mutated desired configuration")
			}
			if change == "permissions" {
				if err := db.Model(&d.Target{}).Where("id = ?", v.ID).Updates(map[string]any{"obs_state": "ready", "obs_can_manage_topics": true}).Error; err != nil {
					t.Fatal(err)
				}
				rows, err = svc.Modules(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row.Partition == "support" && (!row.ConfiguredEnabled || !row.EffectiveEnabled || !row.Enabled || row.Version != persisted.Version) {
						t.Fatalf("restored capability did not recover without CAS write: %+v", row)
					}
				}
			}
			var after d.ModuleConfig
			if err := db.First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if after != persisted {
				t.Fatal("GET wrote module configuration")
			}
			for _, column := range []string{"configured_enabled", "effective_enabled"} {
				if db.Migrator().HasColumn(&d.ModuleConfig{}, column) {
					t.Fatalf("derived column persisted: %s", column)
				}
			}
		})
	}
}
