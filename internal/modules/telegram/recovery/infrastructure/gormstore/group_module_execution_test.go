package gormstore_test

import (
	"context"
	"errors"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// moduleEvidenceProbe models the narrow module capability using isolated SQL only.
type moduleEvidenceProbe struct {
	fixture       *executionFixture
	unknown       bool
	proofs, calls int
}

// ResumeGroupModule checks the transaction and reopens only the wrapper CAS.
func (probe *moduleEvidenceProbe) ResumeGroupModule(ctx context.Context, input c.LocalGroupRetry) (*d.Task, error) {
	return probe.fixture.store.RetryGroupModule(ctx, input, func(tx *gorm.DB, snapshot d.Snapshot) error {
		probe.proofs++
		var count int64
		if err := tx.WithContext(ctx).Model(&d.Task{}).Where("id = ?", input.TaskID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 || probe.unknown {
			return c.ErrStale
		}
		return nil
	})
}

// RecoverGroup supplies completion only when original checked execution reaches it.
func (probe *moduleEvidenceProbe) RecoverGroup(_ context.Context, input c.GroupRecoveryRequest) (c.GroupRecoveryResult, error) {
	probe.calls++
	return c.GroupRecoveryResult{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion, Verified: true}, nil
}

// TestGroupModuleResumeReentersCheckedExecution proves wrapper CAS is not completion.
// Unknown module evidence and revoked permissions cannot reopen/release the wrapper.
func TestGroupModuleResumeReentersCheckedExecution(t *testing.T) {
	for _, mode := range []string{"interrupted", "failed", "unknown", "revoked", "version"} {
		t.Run(mode, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			state := d.StepUnknown
			if mode == "failed" {
				state = d.StepFailed
			}
			input := interruptedModule(t, fixture, state)
			probe := &moduleEvidenceProbe{fixture: fixture, unknown: mode == "unknown"}
			fixture.executor.Modules = probe
			executor := fixture.executor
			if mode == "revoked" {
				fixture.authority.denied = true
			}
			if mode == "version" {
				input.TaskVersion++
			}
			task, err := executor.ExecuteGroupVersion(fixture.ctx, 1, input.TaskVersion, 7)
			if mode == "unknown" || mode == "revoked" || mode == "version" {
				if err == nil || probe.calls != 0 {
					t.Fatal("unsafe execution", task, err)
				}
				target, readErr := fixture.routes.Get(fixture.ctx, 1)
				if readErr != nil || target.RecoveryTaskID != 1 {
					t.Fatal("fence released", target, readErr)
				}
				return
			}
			if err != nil || task.State != d.Completed || probe.proofs != 1 || probe.calls != 1 {
				t.Fatal("resume failed", task, err, probe)
			}
			if _, err := executor.ExecuteGroupVersion(fixture.ctx, 1, input.TaskVersion, 7); !errors.Is(err, c.ErrStale) {
				t.Fatal("stale resume", err)
			}
			if _, err := executor.ExecuteGroupVersion(fixture.ctx, 1, task.Version, 7); err != nil || probe.calls != 1 {
				t.Fatal("duplicate effect", err)
			}
		})
	}
}
