package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// interruptedModule seeds a real interrupted wrapper after all local predecessors.
// fixture owns only an isolated SQLite database; state selects the durable outcome.
func interruptedModule(t *testing.T, fixture *executionFixture, state d.StepState) c.LocalGroupRetry {
	t.Helper()
	confirmExecution(t, fixture)
	fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: "recover_module", before: true}
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); !errors.Is(err, context.Canceled) {
		t.Fatal("missing simulated interruption", err)
	}
	fixture.executor.Store = fixture.store
	fixture.advance(3 * time.Minute)
	if _, err := fixture.store.ExpireLeases(fixture.ctx, 100); err != nil {
		t.Fatal(err)
	}
	var step d.Step
	if err := fixture.db.Where("task_id = ? AND operation = ?", 1, "recover_module").First(&step).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&step).Update("state", state).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.store.Get(fixture.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	return c.LocalGroupRetry{TaskID: 1, TaskVersion: detail.Task.Version, StepID: step.ID, StepVersion: step.Version, ActorID: 7}
}

// TestModuleResumeRequiresAtomicEvidence rejects unsafe wrapper resets and verifies rollback.
func TestModuleResumeRequiresAtomicEvidence(t *testing.T) {
	for _, mode := range []string{"failed", "unknown", "proof", "nil_proof", "lease", "scope", "predecessor", "other", "parent_version", "step_version"} {
		t.Run(mode, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			state := d.StepUnknown
			if mode == "failed" {
				state = d.StepFailed
			}
			input := interruptedModule(t, fixture, state)
			proof := func(tx *gorm.DB, snapshot d.Snapshot) error {
				if tx == nil || snapshot.New.TargetID != 1 {
					t.Fatal("missing transaction/snapshot")
				}
				if mode == "proof" {
					return c.ErrStale
				}
				return nil
			}
			var setupErr error
			switch mode {
			case "nil_proof":
				proof = nil
			case "lease":
				setupErr = fixture.db.Model(&d.Step{}).Where("id = ?", input.StepID).Update("lease_until", time.Now().Add(time.Hour)).Error
			case "scope":
				setupErr = fixture.db.Where("task_id = ? AND key = ?", 1, "chat:-200").Delete(&d.ScopeSlot{}).Error
			case "predecessor":
				setupErr = fixture.db.Model(&d.Step{}).Where("operation = ?", "check_route").Update("state", d.StepFailed).Error
			case "other":
				setupErr = fixture.db.Create(&d.Step{TaskID: 1, BusinessKey: "external", Operation: "invite", State: d.StepUnknown, Version: 1}).Error
			case "parent_version":
				input.TaskVersion++
			case "step_version":
				input.StepVersion++
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			task, err := fixture.store.RetryGroupModule(fixture.ctx, input, proof)
			if mode != "failed" && mode != "unknown" {
				if err == nil {
					t.Fatal("unsafe wrapper reopened")
				}
				var step d.Step
				if err := fixture.db.First(&step, input.StepID).Error; err != nil || step.State != state {
					t.Fatal("wrapper changed", step, err)
				}
				return
			}
			if err != nil || task.State != d.Recovering {
				t.Fatal(task, err)
			}
			if _, err := fixture.store.RetryGroupModule(fixture.ctx, input, proof); !errors.Is(err, c.ErrStale) {
				t.Fatal("duplicate accepted", err)
			}
			step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: 1, BusinessKey: "group.recover_module", Operation: "recover_module"})
			if err != nil || step.State != d.StepPending || step.LeaseToken != "" {
				t.Fatal("fabricated success", step, err)
			}
			if _, err := fixture.store.RetryLocalGroupStep(fixture.ctx, input); err == nil {
				t.Fatal("generic local retry allowed module")
			}
		})
	}
}
