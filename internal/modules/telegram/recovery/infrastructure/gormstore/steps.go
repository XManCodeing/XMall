package gormstore

import (
	"context"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureStep 接收不可变步骤意图，返回已存在或新建步骤；同 key 不同操作返回冲突。
func (s *Store) EnsureStep(ctx context.Context, input c.StepInput) (*d.Step, error) {
	if input.TaskID == 0 || !codeValid(input.BusinessKey, 128) || !codeValid(input.Operation, 64) {
		return nil, c.ErrInvalid
	}
	var step d.Step
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := lockTask(tx, input.TaskID)
		if err != nil {
			return err
		}
		err = tx.Where("task_id = ? AND business_key = ?", input.TaskID, input.BusinessKey).First(&step).Error
		if err == nil {
			if step.Operation != input.Operation {
				return c.ErrConflict
			}
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if !runningTask(task) {
			return c.ErrStale
		}
		step = d.Step{TaskID: input.TaskID, BusinessKey: input.BusinessKey, Operation: input.Operation,
			State: d.StepPending, Version: 1, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&step).Error
	})
	if err != nil {
		return nil, err
	}
	return &step, nil
}

// stepTransaction 先锁父任务再锁子步骤，调用 fn 后返回事务错误，统一避免 PostgreSQL 锁序反转。
// 初次读取只解析不可变 TaskID，所有状态判断和写入均由事务内 fn 完成。
func (s *Store) stepTransaction(ctx context.Context, stepID uint, fn func(*gorm.DB, *d.Task, *d.Step) error) error {
	var locator d.Step
	if err := s.db.WithContext(ctx).Select("id", "task_id").First(&locator, stepID).Error; err != nil {
		return mapped(err)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := lockTask(tx, locator.TaskID)
		if err != nil {
			return err
		}
		var step d.Step
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&step, stepID).Error; err != nil {
			return mapped(err)
		}
		return fn(tx, task, &step)
	})
}

// expired 检查 step 的租约是否在 now 已到期，返回布尔值，空租约视为未知而非可重试。
func expired(step *d.Step, now time.Time) bool {
	return step.State == d.StepRunning && (step.LeaseUntil == nil || !step.LeaseUntil.After(now))
}

// markExpired 用 tx 把 step 变 unknown 并阻止父任务继续执行，返回数据库错误。
func markExpired(tx *gorm.DB, task *d.Task, step *d.Step, now time.Time) error {
	if err := cas(tx.Model(&d.Step{}).Where("id = ? AND version = ? AND state = ?", step.ID, step.Version, d.StepRunning).
		Updates(map[string]any{"state": d.StepUnknown, "version": step.Version + 1, "lease_token": "", "lease_until": nil,
			"result_code": "lease_expired", "updated_at": now})); err != nil {
		return err
	}
	step.State = d.StepUnknown
	step.Version++
	step.LeaseToken = ""
	step.LeaseUntil = nil
	step.ResultCode = "lease_expired"
	step.UpdatedAt = now
	return flagUnknown(tx, task, now)
}

// ClaimStep 用 input CAS 领取 pending 步骤并返回随机租约；过期 running 只持久转 unknown。
// 返回成功后调用方才可在事务外执行网络操作，失租不可再次领取。
func (s *Store) ClaimStep(ctx context.Context, input c.StepClaim) (*d.Step, error) {
	if input.StepID == 0 || input.ExpectedVersion == 0 || input.Lease < time.Second || input.Lease > 15*time.Minute {
		return nil, c.ErrInvalid
	}
	token, err := randomRef()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	until := now.Add(input.Lease)
	var output d.Step
	lostLease := false
	err = s.stepTransaction(ctx, input.StepID, func(tx *gorm.DB, task *d.Task, step *d.Step) error {
		now = s.now().UTC()
		until = now.Add(input.Lease)
		if expired(step, now) {
			lostLease = true
			return markExpired(tx, task, step, now)
		}
		if !runningTask(task) || step.State != d.StepPending || step.Version != input.ExpectedVersion {
			return c.ErrStale
		}
		if err := cas(tx.Model(&d.Step{}).Where("id = ? AND version = ? AND state = ?", step.ID, step.Version, d.StepPending).
			Updates(map[string]any{"state": d.StepRunning, "version": step.Version + 1, "lease_token": token, "lease_until": until, "updated_at": now})); err != nil {
			return err
		}
		step.State = d.StepRunning
		step.Version++
		step.LeaseToken = token
		step.LeaseUntil = &until
		step.UpdatedAt = now
		output = *step
		return nil
	})
	if err != nil {
		return nil, err
	}
	if lostLease {
		return nil, c.ErrStale
	}
	slog.InfoContext(ctx, "recovery step claimed", "step_id", output.ID, "task_id", output.TaskID)
	return &output, nil
}

// CompleteStep 按 input 的租约及版本记录成功/明确失败/未知，返回结果；过期结果不能覆盖 unknown。
// 唯一可退回 pending 的情形是仍在 recovering 的群模块明确持久检查点，不重置未知外部动作。
// 迟到响应只能由后续显式核验流程处理，本底座不自动重放或将 unknown 变成功。
func (s *Store) CompleteStep(ctx context.Context, input c.StepCompletion) (*d.Step, error) {
	if input.StepID == 0 || input.ExpectedVersion == 0 || len(input.LeaseToken) != 64 || !codeValid(input.ResultCode, 64) ||
		(input.Outcome != d.StepSucceeded && input.Outcome != d.StepFailed && input.Outcome != d.StepUnknown && input.Outcome != d.StepPending) {
		return nil, c.ErrInvalid
	}
	now := s.now().UTC()
	var output d.Step
	lostLease := false
	err := s.stepTransaction(ctx, input.StepID, func(tx *gorm.DB, task *d.Task, step *d.Step) error {
		now = s.now().UTC()
		if expired(step, now) {
			lostLease = true
			return markExpired(tx, task, step, now)
		}
		// Only the live, task-owned group module checkpoint may yield its lease.
		// Failed/unknown/expired steps can never be reset through this path.
		if input.Outcome == d.StepPending && (task.Kind != d.GroupReplace || task.State != d.Recovering || step.Operation != "recover_module" || step.BusinessKey != "group.recover_module" || input.ResultCode != "module_checkpointed") {
			return c.ErrInvalid
		}
		if step.State != d.StepRunning || step.Version != input.ExpectedVersion || step.LeaseToken != input.LeaseToken {
			return c.ErrStale
		}
		if err := cas(tx.Model(&d.Step{}).Where("id = ? AND version = ? AND state = ? AND lease_token = ?", step.ID, step.Version, d.StepRunning, input.LeaseToken).
			Updates(map[string]any{"state": input.Outcome, "version": step.Version + 1, "lease_token": "", "lease_until": nil,
				"result_code": input.ResultCode, "updated_at": now})); err != nil {
			return err
		}
		if input.Outcome == d.StepUnknown {
			if err := flagUnknown(tx, task, now); err != nil {
				return err
			}
		} else if input.Outcome == d.StepFailed && task.State != d.NeedsReview && task.State != d.PartialFailed {
			if err := cas(tx.Model(&d.Task{}).Where("id = ? AND version = ?", task.ID, task.Version).
				Updates(map[string]any{"state": d.PartialFailed, "version": task.Version + 1, "updated_at": now})); err != nil {
				return err
			}
		}
		step.State = input.Outcome
		step.Version++
		step.LeaseToken = ""
		step.LeaseUntil = nil
		step.ResultCode = input.ResultCode
		step.UpdatedAt = now
		output = *step
		return nil
	})
	if err != nil {
		return nil, err
	}
	if lostLease {
		return nil, c.ErrStale
	}
	slog.InfoContext(ctx, "recovery step result persisted", "step_id", output.ID, "outcome", output.State)
	return &output, nil
}

// ExpireLeases 按 limit 有界回收过期租约，返回实际转 unknown 数量；失败返回已提交数量和错误。
// 每项使用父子相同锁序，不持有全局 DB 连接跨事务，也不会把未知步骤放回 pending。
func (s *Store) ExpireLeases(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	now := s.now().UTC()
	var steps []d.Step
	if err := s.db.WithContext(ctx).Select("id").Where("state = ? AND (lease_until <= ? OR lease_until IS NULL)", d.StepRunning, now).
		Order("id ASC").Limit(limit).Find(&steps).Error; err != nil {
		return 0, err
	}
	var count int64
	for _, candidate := range steps {
		changed := false
		err := s.stepTransaction(ctx, candidate.ID, func(tx *gorm.DB, task *d.Task, step *d.Step) error {
			if !expired(step, now) {
				return nil
			}
			changed = true
			return markExpired(tx, task, step, now)
		})
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	if count > 0 {
		slog.WarnContext(ctx, "recovery leases became unknown", "count", count)
	}
	return count, nil
}
