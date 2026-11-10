package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// interruptStore simulates process cancellation immediately after a step's durable commit.
type interruptStore struct {
	c.Store
	operation string
	cancel    context.CancelFunc
}

// CompleteStep commits using the real repository, then interrupts the selected phase.
// input is unchanged; the caller receives the actual receipt rather than a fabricated error.
func (store *interruptStore) CompleteStep(ctx context.Context, input c.StepCompletion) (*d.Step, error) {
	step, err := store.Store.CompleteStep(ctx, input)
	if err == nil && step.Operation == store.operation {
		store.cancel()
	}
	return step, err
}

// TestGroupCheckpointedWorkResumes keeps known pending work recoverable without
// releasing the route or retrying ambiguous actions. t owns synthetic SQLite state.
func TestGroupCheckpointedWorkResumes(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	fixture.module.checkpointed, fixture.module.remaining = true, 2
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if !errors.Is(err, a.ErrRecoveryPending) || task.State != d.Recovering || fixture.module.actor != 7 {
		t.Fatalf("checkpoint did not remain recoverable: %+v %v", task, err)
	}
	current, err := fixture.routes.Get(fixture.ctx, 1)
	if err != nil || current.RecoveryTaskID != task.ID {
		t.Fatalf("pending work released fence: %+v %v", current, err)
	}
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: task.ID, BusinessKey: "group.recover_module", Operation: "recover_module"})
	if err != nil || step.State != d.StepPending || step.LeaseToken != "" {
		t.Fatalf("checkpoint did not yield lease: %+v %v", step, err)
	}
	fixture.authority.denied = true
	if _, err = fixture.executor.ExecuteGroup(fixture.ctx, task.ID, 7); err == nil || fixture.module.calls != 1 {
		t.Fatal("revoked actor resumed module", err)
	}
	fixture.authority.denied, fixture.module.remaining = false, 0
	checks := fixture.probe.checks
	if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"obs_observed_at": time.Now().Add(-time.Hour), "obs_checked_at": time.Now().Add(-time.Hour), "next_check_at": time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	task, err = fixture.executor.ExecuteGroup(fixture.ctx, task.ID, 8)
	if fixture.probe.checks <= checks {
		t.Fatal("resumed module did not refresh expired capability")
	}
	if err != nil || task.State != d.Completed || fixture.module.calls != 2 || fixture.module.actor != 8 {
		t.Fatalf("checkpoint resume failed: %+v %v", task, err)
	}
}

// TestGroupReleaseRechecksRevocation keeps the fence when permission is revoked
// during module execution, even if the module returns complete durable evidence.
func TestGroupReleaseRechecksRevocation(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	fixture.module.after = func() { fixture.authority.denied = true }
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if err == nil || task.State == d.Completed {
		t.Fatalf("revoked actor released recovery: %+v %v", task, err)
	}
	current, err := fixture.routes.Get(fixture.ctx, 1)
	if err != nil || current.RecoveryTaskID != task.ID {
		t.Fatalf("revoked actor removed fence: %+v %v", current, err)
	}
}

// TestGroupCheckpointCannotRetryUnknown requires explicit review even when a
// module also marks its local checkpoint durable; unknown always wins.
func TestGroupCheckpointCannotRetryUnknown(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	fixture.module.checkpointed, fixture.module.remaining, fixture.module.unknown = true, 1, 1
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if !errors.Is(err, a.ErrRecoveryReview) || task.State != d.NeedsReview {
		t.Fatalf("unknown checkpoint not quarantined: %+v %v", task, err)
	}
	fixture.module.unknown = 0
	if _, err = fixture.executor.ExecuteGroup(fixture.ctx, task.ID, 7); !errors.Is(err, a.ErrRecoveryReview) || fixture.module.calls != 1 {
		t.Fatal("unknown work automatically retried", err)
	}
}

// TestGroupCheckpointRejectsOrdinaryPhase verifies the pending outcome is not
// a general-purpose retry switch for external actions or other phase names.
func TestGroupCheckpointRejectsOrdinaryPhase(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: fixture.task.ID, BusinessKey: "group.freeze", Operation: "freeze"})
	if err != nil {
		t.Fatal(err)
	}
	step, err = fixture.store.ClaimStep(fixture.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.CompleteStep(fixture.ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepPending, ResultCode: "module_checkpointed"})
	if !errors.Is(err, c.ErrInvalid) {
		t.Fatalf("ordinary phase accepted pending reset: %v", err)
	}
}

// TestExpiredCheckpointCannotYield verifies a late successful-looking checkpoint
// cannot reclaim an expired lease or bypass the parent's unknown quarantine.
func TestExpiredCheckpointCannotYield(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	fixture.module.checkpointed, fixture.module.remaining = true, 1
	fixture.module.after = func() { fixture.advance(3 * time.Minute) }
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if err == nil || task.State != d.NeedsReview {
		t.Fatalf("expired checkpoint escaped quarantine: %+v %v", task, err)
	}
	steps, err := fixture.store.ListSteps(fixture.ctx, task.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps.Items {
		if step.Operation == "recover_module" {
			if step.State != d.StepUnknown {
				t.Fatalf("expired module returned to pending: %+v", step)
			}
			return
		}
	}
	t.Fatal("module checkpoint missing")
}

// TestSupportContinuationOwnsFinalization ensures the stopped-runtime branch
// does not fall back to generic member checks, step wrapping or double completion.
func TestSupportContinuationOwnsFinalization(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	calls := 0
	fixture.executor.ContinueSupport = func(ctx context.Context, request c.GroupRecoveryRequest) error {
		calls++
		if request.ActorID != 7 || request.TaskID != fixture.task.ID || request.Purpose != "support" || request.NewRouteVersion != 2 {
			t.Fatalf("invalid support request: %+v", request)
		}
		if calls == 1 {
			return a.ErrRecoveryPending
		}
		// Model the module-owned atomic finalization with the real transition
		// store, after the genuine freeze and route-switch steps completed.
		detail, err := fixture.store.Get(ctx, request.TaskID)
		if err != nil {
			return err
		}
		_, err = fixture.store.Transition(ctx, c.Transition{TaskID: request.TaskID, ExpectedVersion: detail.Task.Version, State: d.Completed})
		return err
	}
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if !errors.Is(err, a.ErrRecoveryPending) || task.State != d.Recovering || fixture.module.calls != 0 {
		t.Fatalf("support did not retain bounded progress: %+v %v", task, err)
	}
	task, err = fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if err != nil || task.State != d.Completed || calls != 2 || fixture.module.calls != 0 {
		t.Fatalf("support finalized twice or fell back: %+v %v", task, err)
	}
	steps, err := fixture.store.ListSteps(fixture.ctx, task.ID, 0, 20)
	if err != nil || len(steps.Items) != 2 {
		t.Fatalf("support was wrapped in generic recovery/release steps: %+v %v", steps, err)
	}
}

// TestGroupExecutionResumesEveryCommittedPhase reconstructs the executor after each boundary.
// Successful module recovery is not called again, including cancellation after release.
func TestGroupExecutionResumesEveryCommittedPhase(t *testing.T) {
	for _, operation := range []string{"freeze", "switch_route", "check_route", "recover_module", "release"} {
		t.Run(operation, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			confirmExecution(t, fixture)
			ctx, cancel := context.WithCancel(fixture.ctx)
			defer cancel()
			fixture.executor.Store = &interruptStore{Store: fixture.store, operation: operation, cancel: cancel}
			if _, err := fixture.executor.ExecuteGroup(ctx, fixture.task.ID, 7); !errors.Is(err, context.Canceled) {
				t.Fatal("expected persisted interruption", err)
			}
			executor := a.NewGroupExecutor(fixture.store, fixture.groups, fixture.routes, fixture.probe, fixture.authority, fixture.module)
			task, err := executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
			if err != nil || task.State != d.Completed || fixture.module.calls != 1 {
				t.Fatalf("resume task=%+v moduleCalls=%d error=%v", task, fixture.module.calls, err)
			}
		})
	}
}

// TestGroupExecutionResumesLocalLostLease verifies the explicitly whitelisted
// database CAS can run after interruption; expiry is not treated as success.
func TestGroupExecutionResumesLocalLostLease(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: fixture.task.ID, BusinessKey: "group.freeze", Operation: "freeze"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.ClaimStep(fixture.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Second}); err != nil {
		t.Fatal(err)
	}
	fixture.advance(2 * time.Second)
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if err != nil || task.State != d.Completed {
		t.Fatal("local CAS did not resume", task, err)
	}
	current, err := fixture.routes.Get(fixture.ctx, 1)
	if err != nil || current.RecoveryTaskID != 0 || current.TelegramChatID != -200 {
		t.Fatal(current, err)
	}
}

// TestGroupExecutionRechecksAuthorityAndGeneration refuses stale confirmations before effects.
func TestGroupExecutionRechecksAuthorityAndGeneration(t *testing.T) {
	for _, revoked := range []bool{true, false} {
		name := "generation"
		if revoked {
			name = "authority"
		}
		t.Run(name, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			confirmExecution(t, fixture)
			if revoked {
				fixture.authority.denied = true
			} else {
				fixture.probe.generation++
			}
			if _, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7); err == nil {
				t.Fatal("stale execution accepted")
			}
			current, err := fixture.routes.Get(fixture.ctx, 1)
			if err != nil || current.RecoveryTaskID != 0 || current.TelegramChatID != -100 {
				t.Fatal(current, err)
			}
		})
	}
}
