package gormstore_test

import (
	"errors"
	"testing"
	"time"

	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// TestReadOnlyCheckFailureResumes requires a fresh actual query after permission
// failure, preserving the original cooldown and scope rather than marking success.
func TestReadOnlyCheckFailureResumes(t *testing.T) {
	fixture := setupExecution(t, "membership")
	confirmExecution(t, fixture)
	// Freeze preflight also probes; start the failure only after switching.
	fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: "check_route", before: true}
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); err == nil {
		t.Fatal("missing interruption")
	}
	fixture.executor.Store = fixture.store
	fixture.advance(3 * time.Minute)
	fixture.probe.denied = true
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); !errors.Is(err, gc.ErrCapability) {
		t.Fatal("failed probe accepted", err)
	}
	detail, err := fixture.store.Get(fixture.ctx, 1)
	if err != nil || detail.Task.State != d.NeedsReview || fixture.module.calls != 0 {
		t.Fatal(detail, err)
	}
	fixture.probe.denied = false
	before := fixture.probe.checks
	// Immediate retry must respect the existing group-check cooldown.
	if _, err := fixture.executor.ExecuteGroupVersion(fixture.ctx, 1, detail.Task.Version, 7); !errors.Is(err, gc.ErrBusy) {
		t.Fatal("cooldown bypass", err)
	}
	if fixture.probe.checks != before {
		t.Fatal("probe called during cooldown")
	}
	if err := fixture.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"obs_checked_at": time.Now().Add(-time.Minute), "next_check_at": time.Now().Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7)
	if err != nil || task.State != d.Completed || fixture.probe.checks != before+1 || fixture.module.calls != 1 {
		t.Fatal(task, err)
	}
}

// TestReadOnlyCheckLateObservationCannotOverwrite verifies the read-only retry
// creates a new observation CAS and rejects a response from an abandoned probe.
func TestReadOnlyCheckLateObservationCannotOverwrite(t *testing.T) {
	fixture := setupExecution(t, "membership")
	confirmExecution(t, fixture)
	fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: "check_route", before: true}
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); err == nil {
		t.Fatal("missing interruption")
	}
	fixture.executor.Store = fixture.store
	_, abandoned, err := fixture.routes.BeginCheck(fixture.ctx, 1, 2, "recovery", 7, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fixture.advance(3 * time.Minute)
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7)
	if err != nil || task.State != d.Completed {
		t.Fatal(task, err)
	}
	applied, err := fixture.routes.FinishCheck(fixture.ctx, abandoned, gd.Observation{State: "inaccessible", RouteVersion: 2, Sequence: abandoned.Sequence}, time.Now())
	if err != nil || applied {
		t.Fatal("old probe overwrote current state", applied, err)
	}
}

// TestReadOnlyFailedStepWhitelisted demonstrates explicit failed state is also
// recoverable, without weakening module unknown or prerequisite verification.
func TestReadOnlyFailedStepWhitelisted(t *testing.T) {
	fixture := setupExecution(t, "membership")
	confirmExecution(t, fixture)
	fixture.executor.Store = &lostLocalReceipt{Store: fixture.store, operation: "check_route", before: true}
	if _, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7); err == nil {
		t.Fatal("missing interruption")
	}
	fixture.executor.Store = fixture.store
	step, err := fixture.store.EnsureStep(fixture.ctx, c.StepInput{TaskID: 1, BusinessKey: "group.check_route", Operation: "check_route"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CompleteStep(fixture.ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepFailed, ResultCode: "probe_failed"}); err != nil {
		t.Fatal(err)
	}
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, 1, 7)
	if err != nil || task.State != d.Completed {
		t.Fatal(task, err)
	}
}
