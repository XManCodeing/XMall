package gormstore_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// evidenceOnlyRoutes permits historical reads but fails on any release mutation.
// This proves receipt repair never re-releases a current/new task's target.
type evidenceOnlyRoutes struct {
	gc.RecoveryStore
	gc.RecoveryReleaseReader
	calls int
}

// ReleaseRecovery records the forbidden mutation attempt and returns an error.
func (routes *evidenceOnlyRoutes) ReleaseRecovery(context.Context, gc.RecoveryRelease) (*gd.Target, error) {
	routes.calls++
	return nil, errors.New("historical release must not be invoked again")
}

// lostRelease runs the real business release but drops its parent step receipt.
// The returned request is derived from the immutable confirmed snapshot.
func lostRelease(t *testing.T) (*executionFixture, gc.RecoveryReleaseEvidenceRequest) {
	t.Helper()
	fixture := setupExecution(t, "membership")
	confirmExecution(t, fixture)
	fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: "release"}
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fixture.executor.Store = fixture.store
	detail, err := fixture.store.Get(fixture.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := detail.Snapshot
	return fixture, gc.RecoveryReleaseEvidenceRequest{RecoveryInput: gc.RecoveryInput{
		TaskID: 1, TargetID: snapshot.Old.TargetID, OldBotID: snapshot.Old.BotID, OldChatID: snapshot.Old.ChatID,
		NewBotID: snapshot.New.BotID, NewChatID: snapshot.New.ChatID, ExpectedVersion: snapshot.ExpectedOld.Config, ExpectedRouteVersion: snapshot.ExpectedOld.Route,
	}, Generation: snapshot.ExpectedNew.Generation}
}

// TestHistoricalReleaseRepairsOnlyParent covers observation refresh, restart and
// later target ownership. Historical completion cannot mutate the new route/task.
func TestHistoricalReleaseRepairsOnlyParent(t *testing.T) {
	for _, mode := range []string{"observation", "restart", "new_route"} {
		t.Run(mode, func(t *testing.T) {
			fixture, _ := lostRelease(t)
			if mode == "observation" {
				if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"obs_checked_at": time.Now().Add(-time.Minute), "next_check_at": time.Now().Add(-time.Second)}).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.groups.Check(fixture.ctx, 1, 2, 7, "periodic"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "restart" {
				fixture.probe.generation++
			}
			if mode == "new_route" {
				if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"version": 8, "route_version": 3, "recovery_task_id": 99, "telegram_chat_id": -300, "occupancy_slot": "chat:-300"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			var before gd.Target
			if err := fixture.db.First(&before, 1).Error; err != nil {
				t.Fatal(err)
			}
			routes := &evidenceOnlyRoutes{RecoveryStore: fixture.routes, RecoveryReleaseReader: fixture.routes}
			fixture.executor.Routes = routes
			detail, err := fixture.store.Get(fixture.ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7); !errors.Is(err, c.ErrStale) {
				t.Fatal("active lease accepted", err)
			}
			fixture.advance(3 * time.Minute)
			task, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7)
			if err != nil || task.State != d.Completed || routes.calls != 0 {
				t.Fatal(task, err, routes.calls)
			}
			if _, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, task.Version, 7); err != nil {
				t.Fatal(err)
			}
			var after gd.Target
			if err := fixture.db.First(&after, 1).Error; err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("historical repair touched current route", err)
			}
			if fixture.module.calls != 1 {
				t.Fatal("module replay")
			}
		})
	}
}

// TestHistoricalReleaseEvidenceIdentity verifies immutable task/old/new/CAS tuples
// and historical generation, without requiring today's runtime generation.
func TestHistoricalReleaseEvidenceIdentity(t *testing.T) {
	fixture, original := lostRelease(t)
	for _, mode := range []string{"task", "target", "old_bot", "old_chat", "new_bot", "new_chat", "config", "route", "generation"} {
		t.Run(mode, func(t *testing.T) {
			input := original
			switch mode {
			case "task":
				input.TaskID++
			case "target":
				input.TargetID++
			case "old_bot":
				input.OldBotID++
			case "old_chat":
				input.OldChatID--
			case "new_bot":
				input.NewBotID++
			case "new_chat":
				input.NewChatID--
			case "config":
				input.ExpectedVersion++
			case "route":
				input.ExpectedRouteVersion++
			case "generation":
				input.Generation++
			}
			if _, err := fixture.routes.ReadRecoveryRelease(fixture.ctx, input); err == nil {
				t.Fatal("substituted identity accepted")
			}
		})
	}
	evidence, err := fixture.routes.ReadRecoveryRelease(fixture.ctx, original)
	if err != nil || evidence == nil || evidence.ReleasedVersion != 4 || evidence.ObservationSeq == 0 {
		t.Fatal(evidence, err)
	}
}

// TestHistoricalReleaseMissingEvidence refuses unfenced targets without a valid
// original receipt, with current authorization and scope checks still mandatory.
func TestHistoricalReleaseMissingEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "incomplete", "actor", "scope", "step_cas"} {
		t.Run(mode, func(t *testing.T) {
			fixture, _ := lostRelease(t)
			fixture.advance(3 * time.Minute)
			switch mode {
			case "missing":
				if err := fixture.db.Where("task_id = ?", 1).Delete(&gd.RecoveryFence{}).Error; err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				if err := fixture.db.Model(&gd.RecoveryFence{}).Where("task_id = ?", 1).Update("released_observation_seq", 0).Error; err != nil {
					t.Fatal(err)
				}
			case "actor":
				fixture.authority.denied = true
			case "scope":
				if err := fixture.db.Where("task_id = ? AND key = ?", 1, "chat:-200").Delete(&d.ScopeSlot{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			detail, err := fixture.store.Get(fixture.ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			routes := &evidenceOnlyRoutes{RecoveryStore: fixture.routes, RecoveryReleaseReader: fixture.routes}
			fixture.executor.Routes = routes
			if mode == "step_cas" {
				step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: 1, BusinessKey: "group.release", Operation: "release"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.store.RetryLocalGroupStep(fixture.ctx, c.LocalGroupRetry{TaskID: 1, TaskVersion: detail.Task.Version, StepID: step.ID, StepVersion: step.Version + 1, ActorID: 7})
				if !errors.Is(err, c.ErrStale) {
					t.Fatal(err)
				}
				return
			}
			if _, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7); err == nil {
				t.Fatal("missing evidence accepted")
			}
			if routes.calls != 0 {
				t.Fatal("release replayed")
			}
			latest, err := fixture.store.Get(fixture.ctx, 1)
			if err != nil || latest.Task.State == d.Completed {
				t.Fatal(latest, err)
			}
		})
	}
}
