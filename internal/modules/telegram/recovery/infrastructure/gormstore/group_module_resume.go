package gormstore

import (
	"context"
	"encoding/json"
	"log/slog"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// RetryGroupModule reopens only the group module wrapper after bootstrap's module
// proof succeeds in the same parent/step transaction. proof must use tx exclusively;
// it may not perform network I/O or certify success. No membership dependency lives here.
func (s *Store) RetryGroupModule(ctx context.Context, input c.LocalGroupRetry, proof func(*gorm.DB, d.Snapshot) error) (*d.Task, error) {
	if input.TaskID == 0 || input.TaskVersion == 0 || input.StepID == 0 || input.StepVersion == 0 || input.ActorID == 0 || proof == nil {
		return nil, c.ErrInvalid
	}
	var output d.Task
	err := s.stepTransaction(ctx, input.StepID, func(tx *gorm.DB, task *d.Task, step *d.Step) error {
		now := s.now().UTC()
		if task.ID != input.TaskID || task.Version != input.TaskVersion || step.Version != input.StepVersion || task.Kind != d.GroupReplace || task.ConfirmedAt == nil || step.BusinessKey != "group.recover_module" || step.Operation != "recover_module" {
			return c.ErrStale
		}
		if task.State != d.Recovering && task.State != d.NeedsReview && task.State != d.PartialFailed {
			return c.ErrStale
		}
		if step.State != d.StepFailed && step.State != d.StepUnknown && !expired(step, now) {
			return c.ErrStale
		}
		if step.LeaseUntil != nil && step.LeaseUntil.After(now) {
			return c.ErrStale
		}
		if err := exactGroupScope(tx, task); err != nil {
			return err
		}
		if err := modulePredecessors(tx, task.ID, step.ID); err != nil {
			return err
		}
		var snapshot d.Snapshot
		if err := json.Unmarshal([]byte(task.SnapshotJSON), &snapshot); err != nil {
			return err
		}
		if err := proof(tx, snapshot); err != nil {
			return err
		}
		if err := cas(tx.Model(&d.Step{}).Where("id = ? AND version = ?", step.ID, step.Version).Updates(map[string]any{"state": d.StepPending, "version": step.Version + 1, "lease_token": "", "lease_until": nil, "result_code": "module_evidence_resume", "updated_at": now})); err != nil {
			return err
		}
		if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ?", task.ID, task.Version).Updates(map[string]any{"state": d.Recovering, "version": task.Version + 1, "updated_at": now})); err != nil {
			return err
		}
		task.State, task.Version = d.Recovering, task.Version+1
		output = *task
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "group module wrapper reopened after atomic evidence", "task_id", input.TaskID, "step_id", input.StepID, "actor_id", input.ActorID,
		"task_version", input.TaskVersion, "step_version", input.StepVersion, "basis", "module_evidence_resume")
	return &output, nil
}

// modulePredecessors requires exactly three successful local intents and no other
// wrapper or live step; taskID/stepID identify the locked immutable parent and wrapper.
func modulePredecessors(tx *gorm.DB, taskID, stepID uint) error {
	var steps []d.Step
	if err := tx.Where("task_id = ? AND id <> ?", taskID, stepID).Find(&steps).Error; err != nil {
		return err
	}
	expected := map[string]bool{"freeze": true, "switch_route": true, "check_route": true}
	for _, step := range steps {
		if !expected[step.Operation] || step.BusinessKey != "group."+step.Operation || step.State != d.StepSucceeded || step.LeaseToken != "" || step.LeaseUntil != nil {
			return c.ErrStale
		}
		delete(expected, step.Operation)
	}
	if len(expected) != 0 {
		return c.ErrStale
	}
	return nil
}
