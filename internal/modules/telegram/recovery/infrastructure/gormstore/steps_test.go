package gormstore_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestRecoveryScopeConstraintDDL checks production model DDL for both server dialects.
// t owns a local backing connection; DryRun never connects to or executes on a server.
// SQL generation is a regression guard, not a substitute for real server acceptance.
func TestRecoveryScopeConstraintDDL(t *testing.T) {
	h := setup(t)
	pool, err := h.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []gorm.Dialector{
		mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{Conn: pool}),
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			var output bytes.Buffer
			db, err := gorm.Open(dialect, &gorm.Config{
				DryRun: true, DisableAutomaticPing: true,
				Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().CreateTable(&d.ScopeSlot{}); err != nil {
				t.Fatalf("generate scope DDL: %v", err)
			}
			ddl := output.String()
			if !strings.Contains(ddl, "recovery_scope_nonempty") ||
				!strings.Contains(ddl, "CHECK (telegram_recovery_scope_slots.key <> '')") {
				t.Fatalf("scope CHECK must qualify reserved column without dialect-specific quotes: %s", ddl)
			}
		})
	}
}

// TestConcurrentStepAndClaim 验证步骤唯一键、重复意图冲突及并发 CAS 只能领取一次。
func TestConcurrentStepAndClaim(t *testing.T) {
	h := setup(t)
	task := confirmed(t, h, "task")
	var wait sync.WaitGroup
	ids := make(chan uint, 10)
	errs := make(chan error, 10)
	for index := 0; index < 10; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			step, err := h.store.EnsureStep(h.ctx, c.StepInput{TaskID: task.ID, BusinessKey: "one", Operation: "replace_route"})
			if err != nil {
				errs <- err
				return
			}
			ids <- step.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var stepID uint
	for id := range ids {
		if stepID != 0 && stepID != id {
			t.Fatal("duplicate step")
		}
		stepID = id
	}
	if _, err := h.store.EnsureStep(h.ctx, c.StepInput{TaskID: task.ID, BusinessKey: "one", Operation: "different"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	claims := make(chan error, 10)
	for index := 0; index < 10; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := h.store.ClaimStep(h.ctx, c.StepClaim{StepID: stepID, ExpectedVersion: 1, Lease: time.Minute})
			claims <- err
		}()
	}
	wait.Wait()
	close(claims)
	succeeded := 0
	for err := range claims {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, c.ErrStale) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("claimed %d times", succeeded)
	}
}

// TestLeaseExpiryIsUnknown 验证扫过期/再领取/迟到结果均不会重放副作用，任务保持资源锁。
func TestLeaseExpiryIsUnknown(t *testing.T) {
	for _, trigger := range []string{"sweep", "claim", "completion"} {
		t.Run(trigger, func(t *testing.T) {
			h := setup(t)
			task := confirmed(t, h, "task")
			step := claimed(t, h, planned(t, h, task.ID, "one"))
			h.advance(time.Minute)
			switch trigger {
			case "sweep":
				count, err := h.store.ExpireLeases(h.ctx, 10)
				if err != nil || count != 1 {
					t.Fatalf("expire %d %v", count, err)
				}
			case "claim":
				if _, err := h.store.ClaimStep(h.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute}); !errors.Is(err, c.ErrStale) {
					t.Fatal(err)
				}
			case "completion":
				if _, err := h.store.CompleteStep(h.ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepSucceeded, ResultCode: "ok"}); !errors.Is(err, c.ErrStale) {
					t.Fatal(err)
				}
			}
			page, err := h.store.ListSteps(h.ctx, task.ID, 0, 10)
			if err != nil || page.Items[0].State != d.StepUnknown || page.Items[0].LeaseToken != "" {
				t.Fatalf("unknown: %+v %v", page, err)
			}
			detail, err := h.store.Get(h.ctx, task.ID)
			if err != nil || detail.Task.State != d.NeedsReview {
				t.Fatalf("parent: %+v %v", detail, err)
			}
			if _, err := h.store.ClaimStep(h.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: page.Items[0].Version, Lease: time.Minute}); !errors.Is(err, c.ErrStale) {
				t.Fatal(err)
			}
			count, err := h.store.ExpireLeases(h.ctx, 10)
			if err != nil || count != 0 {
				t.Fatalf("second sweep %d %v", count, err)
			}
			other := preflight(t, h, "other")
			if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: other.PreflightRef, ExpectedVersion: 1, ActorID: 7}); !errors.Is(err, c.ErrScopeBusy) {
				t.Fatalf("unknown released scope: %v", err)
			}
		})
	}
}

// TestStepOutcomesAndCompletionCAS 验证三种结果、错误租约/版本、完成重放和不可再次领取。
func TestStepOutcomesAndCompletionCAS(t *testing.T) {
	for _, outcome := range []d.StepState{d.StepSucceeded, d.StepFailed, d.StepUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			h := setup(t)
			task := confirmed(t, h, "task")
			step := claimed(t, h, planned(t, h, task.ID, "one"))
			completion := c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: outcome, ResultCode: "observed"}
			bad := completion
			bad.ExpectedVersion++
			if _, err := h.store.CompleteStep(h.ctx, bad); !errors.Is(err, c.ErrStale) {
				t.Fatal(err)
			}
			bad = completion
			bad.LeaseToken = fmt.Sprintf("%064d", 1)
			if _, err := h.store.CompleteStep(h.ctx, bad); !errors.Is(err, c.ErrStale) {
				t.Fatal(err)
			}
			result, err := h.store.CompleteStep(h.ctx, completion)
			if err != nil || result.State != outcome || result.Version != 3 {
				t.Fatalf("result %+v %v", result, err)
			}
			if _, err := h.store.CompleteStep(h.ctx, completion); !errors.Is(err, c.ErrStale) {
				t.Fatalf("completion replay: %v", err)
			}
			if _, err := h.store.ClaimStep(h.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: 3, Lease: time.Minute}); !errors.Is(err, c.ErrStale) {
				t.Fatal(err)
			}
			detail, err := h.store.Get(h.ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := d.Fencing
			if outcome == d.StepFailed {
				want = d.PartialFailed
			}
			if outcome == d.StepUnknown {
				want = d.NeedsReview
			}
			if detail.Task.State != want {
				t.Fatalf("parent=%s want=%s", detail.Task.State, want)
			}
		})
	}
}

// TestRealCompletionReleasesSlots 验证不允许空 completed，成功步骤完成后删除槽位并允许新任务占用。
func TestRealCompletionReleasesSlots(t *testing.T) {
	h := setup(t)
	task := confirmed(t, h, "first")
	var err error
	for _, state := range []d.State{d.Switching, d.Recovering} {
		task, err = h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Completed}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("empty completed: %v", err)
	}
	step := claimed(t, h, planned(t, h, task.ID, "one"))
	if _, err := h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Completed}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("running completed: %v", err)
	}
	if _, err := h.store.CompleteStep(h.ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepSucceeded, ResultCode: "ok"}); err != nil {
		t.Fatal(err)
	}
	task, err = h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Completed})
	if err != nil || task.State != d.Completed {
		t.Fatalf("complete %+v %v", task, err)
	}
	var count int64
	if err := h.db.Model(&d.ScopeSlot{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("slots=%d %v", count, err)
	}
	confirmed(t, h, "second")
	if _, err := h.store.EnsureStep(h.ctx, c.StepInput{TaskID: task.ID, BusinessKey: "new", Operation: "new"}); !errors.Is(err, c.ErrStale) {
		t.Fatal(err)
	}
	// 确认重放在另一任务占锁后也只能返回原 completed 任务，不再抢占槽位。
	replay, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7})
	if err != nil || replay.State != d.Completed {
		t.Fatalf("completed replay %+v %v", replay, err)
	}
}

// TestDatabaseConstraints 验证物理非空唯一槽与 task/business 唯一键，不依赖服务层先查再写。
func TestDatabaseConstraints(t *testing.T) {
	h := setup(t)
	task := confirmed(t, h, "task")
	step := planned(t, h, task.ID, "unique")
	step.ID = 0
	if err := h.db.Create(step).Error; err == nil {
		t.Fatal("duplicate business key accepted")
	}
	if err := h.db.Create(&d.ScopeSlot{Key: "target:1", TaskID: 99}).Error; err == nil {
		t.Fatal("duplicate slot accepted")
	}
	if err := h.db.Create(&d.ScopeSlot{Key: "", TaskID: 99}).Error; err == nil {
		t.Fatal("empty slot accepted")
	}
}

// TestScopeRollbackAndCrossKind 验证部分槽位获取失败原子回滚，跨任务类型相同 bot 也互斥。
func TestScopeRollbackAndCrossKind(t *testing.T) {
	h := setup(t)
	confirmed(t, h, "group")
	other := c.PreflightInput{Kind: d.BotReplace, RequestKey: "bot", ActorID: 7, Reason: "replace", TTL: time.Minute,
		Snapshot: d.Snapshot{Scope: d.Scope{AccountIDs: []string{"9"}, BotIDs: []string{"2", "3"}}, Old: d.Endpoint{BotID: 2}, New: d.Endpoint{BotID: 3}}}
	task, err := h.store.CreatePreflight(h.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7}); !errors.Is(err, c.ErrScopeBusy) {
		t.Fatal(err)
	}
	var count int64
	if err := h.db.Model(&d.ScopeSlot{}).Where("task_id = ?", task.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial locks %d %v", count, err)
	}
	detail, err := h.store.Get(h.ctx, task.ID)
	if err != nil || detail.Task.State != d.AwaitingConfirmation || detail.Task.Version != 1 {
		t.Fatalf("rollback %+v %v", detail, err)
	}
}

// TestInvalidPreflight 验证类型、scope、ID、TTL 和必填输入边界。
func TestInvalidPreflight(t *testing.T) {
	h := setup(t)
	cases := []func(*c.PreflightInput){
		func(in *c.PreflightInput) { in.Kind = "other" }, func(in *c.PreflightInput) { in.ActorID = 0 },
		func(in *c.PreflightInput) { in.TTL = 0 }, func(in *c.PreflightInput) { in.TTL = 2 * time.Hour },
		func(in *c.PreflightInput) { in.Snapshot.Scope = d.Scope{} },
		func(in *c.PreflightInput) { in.Snapshot.Scope.TargetIDs = []string{"01"} },
		func(in *c.PreflightInput) { in.Snapshot.New.BotID = 99 },
		func(in *c.PreflightInput) { in.Snapshot.New = in.Snapshot.Old },
		func(in *c.PreflightInput) { in.Reason = "" },
	}
	for index, mutate := range cases {
		candidate := input(fmt.Sprintf("invalid:%d", index))
		mutate(&candidate)
		if _, err := h.store.CreatePreflight(h.ctx, candidate); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
}
