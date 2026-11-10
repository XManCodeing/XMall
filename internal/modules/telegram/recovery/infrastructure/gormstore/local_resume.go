package gormstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// RetryLocalGroupStep reopens database-idempotent group operations and the
// read-only capability probe. It verifies durable intent, exact scope and
// successful predecessors, never marks success or resets a module mutation.
// Original route CAS/receipts still have to succeed; lease expiry alone is not
// proof that an action did not execute.
func (s *Store) RetryLocalGroupStep(ctx context.Context, input c.LocalGroupRetry) (*d.Task, error) {
	if input.TaskID == 0 || input.TaskVersion == 0 || input.StepID == 0 || input.StepVersion == 0 || input.ActorID == 0 {
		return nil, c.ErrInvalid
	}
	var output d.Task
	err := s.stepTransaction(ctx, input.StepID, func(tx *gorm.DB, task *d.Task, step *d.Step) error {
		now := s.now().UTC()
		phase, predecessors := localGroupPhase(step)
		if phase == "" || task.Kind != d.GroupReplace || task.ConfirmedAt == nil || task.ID != input.TaskID || task.Version != input.TaskVersion || step.Version != input.StepVersion {
			return c.ErrStale
		}
		if task.State != phase && task.State != d.NeedsReview && task.State != d.PartialFailed {
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
		var steps []d.Step
		if err := tx.Where("task_id = ?", task.ID).Find(&steps).Error; err != nil {
			return err
		}
		for _, other := range steps {
			if other.ID == step.ID {
				continue
			}
			// A second unresolved intent is not covered by this operation's evidence.
			if other.State != d.StepSucceeded {
				return c.ErrStale
			}
			if expected, exists := predecessors[other.BusinessKey]; !exists || other.Operation != expected {
				return c.ErrStale
			}
			delete(predecessors, other.BusinessKey)
		}
		if len(predecessors) != 0 {
			return c.ErrStale
		}
		if err := cas(tx.Model(&d.Step{}).Where("id = ? AND version = ?", step.ID, step.Version).Updates(map[string]any{
			"state": d.StepPending, "version": step.Version + 1, "lease_token": "", "lease_until": nil, "result_code": "local_cas_resume", "updated_at": now,
		})); err != nil {
			return err
		}
		if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ?", task.ID, task.Version).Updates(map[string]any{
			"state": phase, "version": task.Version + 1, "updated_at": now,
		})); err != nil {
			return err
		}
		task.State, task.Version = phase, task.Version+1
		output = *task
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "recovery local CAS resume authorized", "task_id", input.TaskID, "step_id", input.StepID, "actor_id", input.ActorID)
	return &output, nil
}

// localGroupPhase maps exact immutable intent to its original phase and required
// durable predecessors. Unknown keys and module/external mutations are excluded.
func localGroupPhase(step *d.Step) (d.State, map[string]string) {
	if step.BusinessKey != "group."+step.Operation {
		return "", nil
	}
	predecessors := map[string]string{}
	switch step.Operation {
	case "freeze":
		return d.Fencing, predecessors
	case "switch_route":
		predecessors["group.freeze"] = "freeze"
		return d.Switching, predecessors
	case "check_route":
		predecessors["group.freeze"] = "freeze"
		predecessors["group.switch_route"] = "switch_route"
		return d.Recovering, predecessors
	case "release":
		for _, operation := range []string{"freeze", "switch_route", "check_route", "recover_module"} {
			predecessors["group."+operation] = operation
		}
		return d.Recovering, predecessors
	default:
		return "", nil
	}
}

// exactGroupScope checks all persisted scope slots against the immutable snapshot.
// It only reads the bound transaction; no global connection or platform call occurs.
func exactGroupScope(tx *gorm.DB, task *d.Task) error {
	var snapshot d.Snapshot
	if err := json.Unmarshal([]byte(task.SnapshotJSON), &snapshot); err != nil {
		return err
	}
	if snapshot.Old.TargetID == 0 || snapshot.Old.TargetID != snapshot.New.TargetID || snapshot.Old.BotID == 0 || snapshot.Old.BotID != snapshot.New.BotID {
		return c.ErrStale
	}
	expected, err := snapshotKeys(&snapshot)
	if err != nil {
		return err
	}
	var slots []d.ScopeSlot
	if err := tx.Where("task_id = ?", task.ID).Find(&slots).Error; err != nil {
		return err
	}
	actual := make([]string, 0, len(slots))
	for _, slot := range slots {
		actual = append(actual, slot.Key)
	}
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, expected) {
		return c.ErrStale
	}
	return nil
}
