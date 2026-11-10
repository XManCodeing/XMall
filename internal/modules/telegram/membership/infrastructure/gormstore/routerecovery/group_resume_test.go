package routerecovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// TestGroupResumeUsesExistingCleanup proves reopening evidence is not success and
// the original read-only lifecycle recheck closes definite failures without another ban.
func TestGroupResumeUsesExistingCleanup(t *testing.T) {
	for _, mode := range []string{"failed", "administrator", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			worker, db, input, probe := groupFixture(t)
			probe.newStatus, probe.banOutcome = "member", c.AccessFailed
			if mode == "administrator" {
				probe.oldStatus = "administrator"
			}
			if mode == "unknown" {
				probe.banOutcome = c.AccessUnknown
			}
			for range 3 {
				_, err := worker.RecoverRoute(context.Background(), input)
				must(t, err)
			}
			bans := probe.bans
			probe.oldStatus = "left"
			err := db.Transaction(func(tx *gorm.DB) error { return worker.CheckGroupResume(context.Background(), tx, input) })
			if mode == "unknown" {
				if err == nil {
					t.Fatal("unknown ban reopened")
				}
				return
			}
			must(t, err)
			result, err := worker.RecoverRoute(context.Background(), input)
			must(t, err)
			if !result.Verified || probe.bans != bans || probe.unbans != 0 {
				t.Fatal("cleanup bypass/repeated write", result)
			}
		})
	}
}

// TestGroupResumeEvidence keeps ambiguous Telegram effects separate from wrapper failure.
// Every case uses isolated SQL and the same full scans used by normal completion.
func TestGroupResumeEvidence(t *testing.T) {
	for _, mode := range []string{"empty", "safe_ban", "unknown_binding", "invite", "revoke", "approve", "ban", "unban", "late_result", "lease", "route", "generation"} {
		t.Run(mode, func(t *testing.T) {
			worker, db, input, probe := groupFixture(t)
			switch mode {
			case "invite":
				must(t, db.Create(&d.AccessInvite{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, State: "unknown"}).Error)
			case "revoke":
				must(t, db.Create(&Action{TaskID: 3, TargetID: 2, Kind: "group_revoke", Reference: "8", State: "unknown"}).Error)
			case "route":
				must(t, db.Table("telegram_group_targets").Where("id = 2").Update("route_version", 99).Error)
			case "generation":
				must(t, db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 99).Error)
			case "empty":
			default:
				operation := d.AccessOperation{BusinessKey: "route-recovery:3:2:42", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, Generation: 7, TelegramID: 42, Kind: "lifecycle", State: "unknown", Version: 1}
				if mode == "safe_ban" {
					operation.State, operation.LifecyclePhase, operation.BanOutcome = "needs_review", "ban", "failed"
				}
				if mode == "approve" {
					operation.Kind = "approve"
				}
				if mode == "lease" {
					operation.State, operation.LeaseToken, operation.LeaseUntil = "pending", "active", time.Now().Add(time.Minute)
				}
				if mode == "late_result" || mode == "unknown_binding" {
					operation.State = "failed"
				}
				must(t, db.Create(&operation).Error)
				bindingState := "succeeded"
				if mode == "unknown_binding" {
					bindingState = "unknown"
				}
				must(t, db.Create(&Action{TaskID: 3, TargetID: 2, Kind: "group_operation", Reference: fmt.Sprint(operation.ID), State: bindingState}).Error)
				outcome := c.AccessUnknown
				if mode == "safe_ban" || mode == "unknown_binding" {
					outcome = c.AccessFailed
				}
				action := "ban"
				if mode == "approve" || mode == "unban" {
					action = mode
				}
				must(t, db.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: 1, Action: action, Outcome: outcome}).Error)
			}
			err := db.Transaction(func(tx *gorm.DB) error { return worker.CheckGroupResume(context.Background(), tx, input) })
			if mode == "empty" || mode == "safe_ban" {
				must(t, err)
			} else if err == nil {
				t.Fatal("uncertain module allowed resume")
			}
			if probe.creates+probe.approves+probe.bans+probe.unbans != 0 {
				t.Fatal("evidence scan performed HTTP")
			}
		})
	}
}
