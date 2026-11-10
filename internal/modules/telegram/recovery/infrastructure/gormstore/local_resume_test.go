package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// lostLocalReceipt simulates a process stopping after claim or before the step
// receipt commit. The real business repository may already have committed.
type lostLocalReceipt struct {
	c.Store
	operation string
	before    bool
}

// ClaimStep commits intent then returns cancellation before the selected action.
func (store *lostLocalReceipt) ClaimStep(ctx context.Context, input c.StepClaim) (*d.Step, error) {
	step, err := store.Store.ClaimStep(ctx, input)
	if err == nil && store.before && step.Operation == store.operation {
		return nil, context.Canceled
	}
	return step, err
}

// CompleteStep drops only the selected receipt, preserving the real running lease.
func (store *lostLocalReceipt) CompleteStep(ctx context.Context, input c.StepCompletion) (*d.Step, error) {
	page, err := store.Store.ListSteps(ctx, 1, 0, 100)
	if err != nil {
		return nil, err
	}
	for _, step := range page.Items {
		if step.ID == input.StepID && step.Operation == store.operation {
			return nil, context.Canceled
		}
	}
	return store.Store.CompleteStep(ctx, input)
}

// TestLocalGroupInterruptionMatrix exercises both sides of the three local
// operation commits, including reconstruction, active-lease rejection and replay.
func TestLocalGroupInterruptionMatrix(t *testing.T) {
	for _, operation := range []string{"freeze", "switch_route", "check_route", "release"} {
		for _, before := range []bool{false, true} {
			name := operation + "/lost_receipt"
			if before {
				name = operation + "/before_call"
			}
			t.Run(name, func(t *testing.T) {
				fixture := setupExecution(t, "membership")
				confirmExecution(t, fixture)
				fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: operation, before: before}
				if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				fixture.executor.Store = fixture.store
				detail, err := fixture.store.Get(fixture.ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7); !errors.Is(err, c.ErrStale) {
					t.Fatal("live lease accepted", err)
				}
				fixture.advance(3 * time.Minute)
				if operation == "check_route" {
					if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"obs_checked_at": time.Now().Add(-time.Minute), "next_check_at": time.Now().Add(-time.Second)}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if _, err = fixture.store.ExpireLeases(fixture.ctx, 100); err != nil {
					t.Fatal(err)
				}
				detail, err = fixture.store.Get(fixture.ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				task, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7)
				if err != nil || task.State != d.Completed {
					t.Fatalf("resume failed: %+v %v", task, err)
				}
				if fixture.module.calls != 1 {
					t.Fatal("module replayed", fixture.module.calls)
				}
				if _, err = fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7); !errors.Is(err, c.ErrStale) {
					t.Fatal("old parent accepted", err)
				}
				if _, err = fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, task.Version, 7); err != nil {
					t.Fatal(err)
				}
				var target gd.Target
				if err = fixture.db.First(&target, 1).Error; err != nil || target.Version != 4 || target.RouteVersion != 2 || target.RecoveryTaskID != 0 {
					t.Fatalf("mutation repeated: %+v %v", target, err)
				}
			})
		}
	}
}

// interruptedFreeze creates durable failed/unknown local intent without effects.
// It returns the exact parent/step CAS for isolation tests using real SQLite rows.
func interruptedFreeze(t *testing.T, fixture *executionFixture, state d.StepState) c.LocalGroupRetry {
	t.Helper()
	confirmExecution(t, fixture)
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: 1, BusinessKey: "group.freeze", Operation: "freeze"})
	if err != nil {
		t.Fatal(err)
	}
	step, err = fixture.store.ClaimStep(fixture.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	step, err = fixture.store.CompleteStep(fixture.ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: state, ResultCode: "interrupted"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.store.Get(fixture.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	return c.LocalGroupRetry{TaskID: 1, TaskVersion: detail.Task.Version, StepID: step.ID, StepVersion: step.Version, ActorID: 7}
}

// TestLocalGroupResumeIsolation refuses foreign tasks, stale CAS, missing scope,
// revoked actors, stale routes and unrelated unknown external intent.
func TestLocalGroupResumeIsolation(t *testing.T) {
	for _, mode := range []string{"task", "parent_version", "step_version", "scope", "unknown", "actor", "route"} {
		t.Run(mode, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			input := interruptedFreeze(t, fixture, d.StepUnknown)
			switch mode {
			case "task":
				input.TaskID++
			case "parent_version":
				input.TaskVersion++
			case "step_version":
				input.StepVersion++
			case "scope":
				if err := fixture.db.Where("task_id = ? AND key = ?", 1, "chat:-200").Delete(&d.ScopeSlot{}).Error; err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := fixture.db.Create(&d.Step{TaskID: 1, BusinessKey: "external.invite", Operation: "invite", State: d.StepUnknown, Version: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "actor":
				fixture.authority.denied = true
			case "route":
				if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Update("version", 99).Error; err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if mode == "actor" || mode == "route" {
				_, err = fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, input.TaskVersion, 7)
			} else {
				_, err = fixture.store.RetryLocalGroupStep(fixture.ctx, input)
			}
			if err == nil {
				t.Fatal("unsafe resume accepted")
			}
			var target gd.Target
			if err := fixture.db.First(&target, 1).Error; err != nil || target.TelegramChatID != -100 || target.RecoveryTaskID != 0 {
				t.Fatalf("changed target: %+v %v", target, err)
			}
		})
	}
}

// TestLocalFailedResumeCAS verifies failed pure local work can resume once, but
// the same request cannot reset the new pending intent or mark it successful.
func TestLocalFailedResumeCAS(t *testing.T) {
	fixture := setupExecution(t, "membership")
	input := interruptedFreeze(t, fixture, d.StepFailed)
	task, err := fixture.store.RetryLocalGroupStep(fixture.ctx, input)
	if err != nil || task.State != d.Fencing {
		t.Fatal(task, err)
	}
	if _, err = fixture.store.RetryLocalGroupStep(fixture.ctx, input); !errors.Is(err, c.ErrStale) {
		t.Fatal("duplicate CAS accepted", err)
	}
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: 1, BusinessKey: "group.freeze", Operation: "freeze"})
	if err != nil || step.State != d.StepPending {
		t.Fatal("retry fabricated success", step, err)
	}
	task, err = fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, task.Version, 7)
	if err != nil || task.State != d.Completed {
		t.Fatal(task, err)
	}
}
