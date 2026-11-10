package gormstore_test

import (
	"context"
	"errors"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
)

// TestEnablePreviewAndFinalCheck proves previews never register a group and final
// permission loss saves disabled, including a previously enabled group.
func TestEnablePreviewAndFinalCheck(t *testing.T) {
	for _, purpose := range []string{"support", "membership"} {
		t.Run(purpose, func(t *testing.T) {
			service, _, db, platform := setup(t)
			ctx := context.Background()
			input := c.Input{Name: "Group", Purpose: purpose, BotInstanceID: 1, TelegramChatID: "-100123", Enabled: true}
			preview, err := service.PreviewEnable(ctx, 0, input)
			if err != nil || !preview.Enabled {
				t.Fatal(preview, err)
			}
			var count int64
			db.Model(&d.Target{}).Count(&count)
			if count != 0 {
				t.Fatal("preview registered group")
			}
			// Permissions revoked after the switch was turned on.
			platform.result.Observation.CanManageTopics = false
			platform.result.Observation.CanRestrictMembers = false
			saved, err := service.Save(ctx, 0, input)
			if err != nil || saved.Enabled || !saved.EnableRejected || len(saved.MissingCapabilities) == 0 {
				t.Fatal(saved, err)
			}
			if len(saved.Operations) != 1 || saved.Operations[0].Source != "enable" {
				t.Fatal("missing durable check log")
			}
			platform.result.Observation.CanManageTopics = true
			platform.result.Observation.CanRestrictMembers = true
			input.ExpectedVersion, input.ExpectedRouteVersion = saved.Version, saved.RouteVersion
			saved, err = service.Save(ctx, saved.ID, input)
			if err != nil || !saved.Enabled || saved.EnableRejected {
				t.Fatal(saved, err)
			}
			input.ExpectedVersion = saved.Version
			platform.result.ErrorClass = "network"
			saved, err = service.Save(ctx, saved.ID, input)
			if err != nil || saved.Enabled || !saved.EnableRejected || saved.Observation.ErrorClass != "network" {
				t.Fatal(saved, err)
			}
		})
	}
}

// TestEnableCheckRejectsConcurrentInvalidation verifies a delayed check cannot
// overwrite newer observation, version or recovery ownership changes.
func TestEnableCheckRejectsConcurrentInvalidation(t *testing.T) {
	for _, field := range []string{"observation_seq", "version", "recovery_task_id"} {
		t.Run(field, func(t *testing.T) {
			service, _, db, platform := setup(t)
			target := create(t, service)
			platform.check = func() {
				if err := db.Model(&d.Target{}).Where("id = ?", target.ID).UpdateColumn(field, gorm.Expr(field+" + 1")).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.Save(context.Background(), target.ID, c.Input{Name: target.Name, Purpose: target.Purpose, BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true, ExpectedVersion: target.Version, ExpectedRouteVersion: target.RouteVersion})
			if !errors.Is(err, c.ErrConflict) {
				t.Fatal("stale check persisted", err)
			}
			var stored d.Target
			db.First(&stored, target.ID)
			if stored.Enabled {
				t.Fatal("stale check enabled group")
			}
		})
	}
}
