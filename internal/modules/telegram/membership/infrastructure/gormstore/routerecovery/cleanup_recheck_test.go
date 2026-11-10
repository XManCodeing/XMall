package routerecovery

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// TestCleanupReadRecheck closes only definite no-effect work using actual verify.
// It preserves the original failed response and never retries a ban or unban.
func TestCleanupReadRecheck(t *testing.T) {
	for _, scenario := range []string{"failed", "administrator", "administrator_before_ban", "unknown", "unknown_unban", "kicked", "no_arrival", "active_lease", "source", "binding", "route", "permission", "scope", "late_unknown"} {
		t.Run(scenario, func(t *testing.T) {
			worker, db, input, probe := groupFixture(t)
			probe.newStatus, probe.banOutcome = "member", c.AccessFailed
			if scenario == "administrator" {
				probe.oldStatus = "administrator"
			}
			if scenario == "unknown" {
				probe.banOutcome = c.AccessUnknown
			}
			for attempt := range 3 {
				if scenario == "administrator_before_ban" && attempt == 1 {
					probe.oldStatus = "administrator"
				}
				_, err := worker.RecoverRoute(context.Background(), input)
				must(t, err)
			}
			var operation d.AccessOperation
			must(t, db.Where("kind = ?", "lifecycle").First(&operation).Error)
			bans := probe.bans
			probe.oldStatus = "left"
			switch scenario {
			case "unknown_unban":
				must(t, db.Model(&operation).Updates(map[string]any{"unban_outcome": "unknown", "lifecycle_phase": "unknown_unban"}).Error)
				must(t, db.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: 998, Action: "unban", Outcome: "unknown"}).Error)
			case "kicked":
				probe.oldStatus = "kicked"
			case "no_arrival":
				probe.newStatus = "left"
			case "active_lease":
				must(t, db.Model(&operation).Updates(map[string]any{"state": "creating", "lease_token": "live", "lease_until": time.Now().Add(time.Hour)}).Error)
			case "source":
				must(t, db.Model(&d.AccessMember{}).Where("chat_id = ?", -10).Update("source_id", 999).Error)
			case "binding":
				must(t, db.Model(&operation).Update("business_key", "another-task").Error)
			case "route":
				must(t, db.Model(&operation).Update("route_version", 99).Error)
			case "permission":
				probe.deny = true
			case "scope":
				must(t, db.Exec("DELETE FROM telegram_recovery_scope_slots WHERE key = 'chat:-10'").Error)
			case "late_unknown":
				must(t, db.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: 999, Action: "ban", Outcome: "unknown"}).Error)
			}
			result, err := worker.RecoverRoute(context.Background(), input)
			want := scenario == "failed" || scenario == "administrator" || scenario == "administrator_before_ban"
			if want {
				must(t, err)
			}
			if result.Verified != want || probe.bans != bans || probe.unbans != 0 {
				t.Fatalf("unsafe cleanup result %+v error=%v bans=%d", result, err, probe.bans)
			}
			if want {
				must(t, db.First(&operation, operation.ID).Error)
				if operation.State != "succeeded" || operation.LifecyclePhase != "done" || operation.BanConfirmed {
					t.Fatalf("not a real verify completion: %+v", operation)
				}
				result, err = worker.RecoverRoute(context.Background(), input)
				must(t, err)
				if !result.Verified || probe.bans != bans {
					t.Fatal("duplicate continuation changed conclusion")
				}
			}
			if scenario == "unknown" || scenario == "late_unknown" {
				var count int64
				must(t, db.Model(&d.AccessResult{}).Where("operation_id = ? AND action = ? AND outcome = ?", operation.ID, "ban", "unknown").Count(&count).Error)
				if count != 1 {
					t.Fatal("unknown receipt lost")
				}
			}
		})
	}
}
