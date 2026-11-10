package gormstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Confirm 按随机引用及版本确认，返回原父任务；所有 scope 槽位与状态在同一事务获取。
// 失败回滚全部槽位。相同 actor/version 重放不重新占锁、不创建第二任务。
func (s *Store) Confirm(ctx context.Context, input c.Confirmation) (*d.Task, error) {
	if input.ActorID == 0 || input.ExpectedVersion == 0 || len(input.PreflightRef) != 64 {
		return nil, c.ErrInvalid
	}
	var task d.Task
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("preflight_ref = ?", input.PreflightRef).First(&task).Error; err != nil {
			return mapped(err)
		}
		if task.ConfirmedAt != nil {
			if task.ConfirmedVersion == input.ExpectedVersion && task.ConfirmedActorID == input.ActorID {
				return nil
			}
			return c.ErrStale
		}
		if task.State != d.AwaitingConfirmation || task.Version != input.ExpectedVersion {
			return c.ErrStale
		}
		now = s.now().UTC()
		if !task.ExpiresAt.After(now) {
			return c.ErrExpired
		}
		if err := acquireScope(tx, &task); err != nil {
			return err
		}
		if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ? AND state = ?", task.ID, input.ExpectedVersion, d.AwaitingConfirmation).
			Updates(map[string]any{"state": d.Fencing, "version": task.Version + 1, "confirmed_version": task.Version,
				"confirmed_actor_id": input.ActorID, "confirmed_at": now, "updated_at": now})); err != nil {
			return err
		}
		task.ConfirmedVersion, task.ConfirmedActorID = task.Version, input.ActorID
		task.Version++
		task.State = d.Fencing
		task.ConfirmedAt = &now
		task.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "recovery task confirmed", "task_id", task.ID, "version", task.Version)
	return &task, nil
}

// acquireScope 从 task 的可信快照生成槽位，使用 tx 原子插入，返回占用冲突或数据库错误。
func acquireScope(tx *gorm.DB, task *d.Task) error {
	var snapshot d.Snapshot
	if err := json.Unmarshal([]byte(task.SnapshotJSON), &snapshot); err != nil {
		return err
	}
	keys, err := snapshotKeys(&snapshot)
	if err != nil {
		return err
	}
	for _, key := range keys {
		slot := d.ScopeSlot{Key: key, TaskID: task.ID}
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&slot)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrScopeBusy
		}
	}
	return nil
}

// Cancel 使用 input 的 actor/version 撤销未确认任务并释放槽位，返回原父任务及错误。
func (s *Store) Cancel(ctx context.Context, input c.Cancellation) (*d.Task, error) {
	if input.TaskID == 0 || input.ActorID == 0 || input.ExpectedVersion == 0 {
		return nil, c.ErrInvalid
	}
	var task *d.Task
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		task, err = lockTask(tx, input.TaskID)
		if err != nil {
			return err
		}
		if task.State == d.Cancelled && task.CancelledVersion == input.ExpectedVersion && task.CancelledActorID == input.ActorID {
			return nil
		}
		if task.State != d.AwaitingConfirmation || task.Version != input.ExpectedVersion {
			return c.ErrStale
		}
		if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ? AND state = ?", task.ID, task.Version, d.AwaitingConfirmation).
			Updates(map[string]any{"state": d.Cancelled, "version": task.Version + 1, "cancelled_version": task.Version,
				"cancelled_actor_id": input.ActorID, "updated_at": now})); err != nil {
			return err
		}
		if err := tx.Where("task_id = ?", task.ID).Delete(&d.ScopeSlot{}).Error; err != nil {
			return err
		}
		task.CancelledVersion, task.CancelledActorID = task.Version, input.ActorID
		task.Version++
		task.State = d.Cancelled
		task.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "recovery task cancelled", "task_id", task.ID)
	return task, nil
}

// allowedTransition 校验 from 到 to 的有向状态推进，返回是否允许；终态不复活。
func allowedTransition(from, to d.State) bool {
	switch from {
	case d.Fencing:
		return to == d.Switching || to == d.NeedsReview || to == d.PartialFailed
	case d.Switching:
		return to == d.Recovering || to == d.NeedsReview || to == d.PartialFailed
	case d.Recovering:
		return to == d.Completed || to == d.NeedsReview || to == d.PartialFailed
	case d.NeedsReview, d.PartialFailed:
		return to == d.Recovering
	default:
		return false
	}
}

// Transition 仅按 input CAS 推进真实执行器提供的状态，返回持久任务；不执行平台迁移。
// completed 必须具有至少一个步骤且全部成功，否则拒绝，避免空执行器直接宣称成功。
func (s *Store) Transition(ctx context.Context, input c.Transition) (*d.Task, error) {
	if input.TaskID == 0 || input.ExpectedVersion == 0 || !validState(input.State) {
		return nil, c.ErrInvalid
	}
	var task *d.Task
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		task, err = lockTask(tx, input.TaskID)
		if err != nil {
			return err
		}
		if task.Version != input.ExpectedVersion || !allowedTransition(task.State, input.State) {
			return c.ErrStale
		}
		if input.State == d.Completed {
			if err := completionReady(tx, task.ID); err != nil {
				return err
			}
		}
		if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ?", task.ID, task.Version).
			Updates(map[string]any{"state": input.State, "version": task.Version + 1, "updated_at": now})); err != nil {
			return err
		}
		if input.State == d.Completed {
			if err := tx.Where("task_id = ?", task.ID).Delete(&d.ScopeSlot{}).Error; err != nil {
				return err
			}
		}
		task.State = input.State
		task.Version++
		task.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "recovery task transitioned", "task_id", task.ID, "state", task.State, "version", task.Version)
	return task, nil
}

// completionReady 使用 tx 检查 taskID 的非空成功步骤集合，返回未就绪或数据库错误。
func completionReady(tx *gorm.DB, taskID uint) error {
	var total, incomplete int64
	if err := tx.Model(&d.Step{}).Where("task_id = ?", taskID).Count(&total).Error; err != nil {
		return err
	}
	if err := tx.Model(&d.Step{}).Where("task_id = ? AND state <> ?", taskID, d.StepSucceeded).Count(&incomplete).Error; err != nil {
		return err
	}
	if total == 0 || incomplete > 0 {
		return c.ErrStale
	}
	return nil
}

// runningTask 返回任务是否仍允许计划或领取步骤；review/failed 停止新副作用但保留占锁。
func runningTask(task *d.Task) bool {
	return task.State == d.Fencing || task.State == d.Switching || task.State == d.Recovering
}

// flagUnknown 在 tx 中将 task 进入人工核验并递增版本，返回更新错误，保留资源槽位。
func flagUnknown(tx *gorm.DB, task *d.Task, now time.Time) error {
	if task.State == d.NeedsReview {
		return nil
	}
	return cas(tx.Model(&d.Task{}).Where("id = ? AND version = ?", task.ID, task.Version).
		Updates(map[string]any{"state": d.NeedsReview, "version": task.Version + 1, "updated_at": now}))
}
