package gormstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// harness 保存测试单连接数据库和受互斥保护的可推进时钟。
type harness struct {
	store *s.Store
	db    *gorm.DB
	now   time.Time
	mu    sync.Mutex
	ctx   context.Context
}

// setup 接收 t 并返回隔离单连接仓储；上下文超时用于显式捕获事务自锁。
func setup(t *testing.T) *harness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:recovery-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(s.Models()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	h := &harness{db: db, now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), ctx: ctx}
	h.store = s.NewWithClock(db, func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now })
	return h
}

// advance 接收时间增量并推进测试时钟，无返回值。
func (h *harness) advance(duration time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(duration)
}

// input 接收幂等键并返回无秘密、完整资源集合的测试群替换快照。
func input(key string) c.PreflightInput {
	return c.PreflightInput{Kind: d.GroupReplace, RequestKey: key, ActorID: 7, Reason: "replace unreachable group", VerificationNote: "verified owner", TTL: time.Minute,
		Snapshot: d.Snapshot{Scope: d.Scope{TargetIDs: []string{"1"}, BotIDs: []string{"2"}, ChatIDs: []string{"-1009007199254740993", "-1009007199254740994"}},
			Old: d.Endpoint{TargetID: 1, BotID: 2, ChatID: -1009007199254740993}, New: d.Endpoint{TargetID: 1, BotID: 2, ChatID: -1009007199254740994},
			ExpectedOld: d.Versions{Route: 3, Generation: 4}, ExpectedNew: d.Versions{Route: 3, Generation: 4}}}
}

// preflight 接收 t/h/key 创建预检，失败立即终止，返回父任务。
func preflight(t *testing.T, h *harness, key string) *d.Task {
	t.Helper()
	task, err := h.store.CreatePreflight(h.ctx, input(key))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// confirmed 接收 t/h/key 创建并确认父任务，返回 fencing 任务。
func confirmed(t *testing.T, h *harness, key string) *d.Task {
	t.Helper()
	task := preflight(t, h, key)
	task, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: task.Version, ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// planned 接收 taskID/key 为任务建立步骤，返回 pending 步骤。
func planned(t *testing.T, h *harness, taskID uint, key string) *d.Step {
	t.Helper()
	step, err := h.store.EnsureStep(h.ctx, c.StepInput{TaskID: taskID, BusinessKey: key, Operation: "replace_route"})
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// claimed 接收 step 并领取一分钟租约，返回 running 步骤。
func claimed(t *testing.T, h *harness, step *d.Step) *d.Step {
	t.Helper()
	step, err := h.store.ClaimStep(h.ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// TestPreflightReplayAndJSON 验证幂等内容、随机引用、JSON ID 精度和 TTL 重放不续期。
func TestPreflightReplayAndJSON(t *testing.T) {
	h := setup(t)
	first := preflight(t, h, "request:1")
	h.advance(10 * time.Second)
	replay := preflight(t, h, "request:1")
	if replay.ID != first.ID || !replay.ExpiresAt.Equal(first.ExpiresAt) || len(first.PreflightRef) != 64 {
		t.Fatalf("invalid replay: %+v", replay)
	}
	changed := input("request:1")
	changed.Reason = "other"
	if _, err := h.store.CreatePreflight(h.ctx, changed); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("content conflict: %v", err)
	}
	second := preflight(t, h, "request:2")
	if second.PreflightRef == first.PreflightRef {
		t.Fatal("predictable/shared reference")
	}
	detail, err := h.store.Get(h.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"chat_id":"-1009007199254740993"`, `"target_id":"1"`, `"target_ids":["1"]`, `"route":"3"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("missing %s in %s", expected, encoded)
		}
	}
	for _, forbidden := range []string{"SnapshotJSON", "RequestHash", "lease_token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leaked field %s", forbidden)
		}
	}
}

// TestExpiryAndCancellation 验证边界 TTL、版本 CAS、确认前取消及幂等取消。
func TestExpiryAndCancellation(t *testing.T) {
	h := setup(t)
	task := preflight(t, h, "expired")
	h.advance(time.Minute)
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7}); !errors.Is(err, c.ErrExpired) {
		t.Fatalf("expiry: %v", err)
	}
	if _, err := h.store.Cancel(h.ctx, c.Cancellation{TaskID: task.ID, ExpectedVersion: 2, ActorID: 7}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("stale cancel: %v", err)
	}
	cancel := c.Cancellation{TaskID: task.ID, ExpectedVersion: 1, ActorID: 7}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := h.store.Cancel(h.ctx, cancel)
		if err != nil || result.State != d.Cancelled || result.Version != 2 {
			t.Fatalf("cancel: %+v %v", result, err)
		}
	}
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("confirm cancelled: %v", err)
	}
	active := confirmed(t, h, "active")
	if _, err := h.store.Cancel(h.ctx, c.Cancellation{TaskID: active.ID, ExpectedVersion: active.Version, ActorID: 7}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("cancel confirmed: %v", err)
	}
}

// TestConcurrentConfirmationAndScope 验证单连接并发确认同预检、并存预检与跨任务独占。
func TestConcurrentConfirmationAndScope(t *testing.T) {
	h := setup(t)
	first := preflight(t, h, "first")
	second := preflight(t, h, "second")
	var wait sync.WaitGroup
	results := make(chan error, 12)
	for index := 0; index < 12; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: first.PreflightRef, ExpectedVersion: 1, ActorID: 7})
			if err == nil && task.ID != first.ID {
				err = fmt.Errorf("duplicate task %d", task.ID)
			}
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: second.PreflightRef, ExpectedVersion: 1, ActorID: 7}); !errors.Is(err, c.ErrScopeBusy) {
		t.Fatalf("scope conflict: %v", err)
	}
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: first.PreflightRef, ExpectedVersion: 1, ActorID: 8}); !errors.Is(err, c.ErrStale) {
		t.Fatalf("actor replay: %v", err)
	}
	var tasks, slots int64
	if err := h.db.Model(&d.Task{}).Count(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&d.ScopeSlot{}).Count(&slots).Error; err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || slots != 4 {
		t.Fatalf("tasks=%d slots=%d", tasks, slots)
	}
}

// TestConcurrentPreflight 验证同 request_key 的并发请求只产生一个预检。
func TestConcurrentPreflight(t *testing.T) {
	h := setup(t)
	var wait sync.WaitGroup
	ids := make(chan uint, 10)
	errs := make(chan error, 10)
	for index := 0; index < 10; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task, err := h.store.CreatePreflight(h.ctx, input("shared"))
			if err != nil {
				errs <- err
				return
			}
			ids <- task.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var expected uint
	for id := range ids {
		if expected != 0 && id != expected {
			t.Fatal("duplicate preflight")
		}
		expected = id
	}
}

// TestTaskAndStepPagination 验证列表游标、详情缺失及分页输入边界。
func TestTaskAndStepPagination(t *testing.T) {
	h := setup(t)
	active := confirmed(t, h, "active")
	preflight(t, h, "second")
	preflight(t, h, "third")
	page, err := h.store.List(h.ctx, c.ListFilter{Limit: 2, Kind: d.GroupReplace})
	if err != nil || len(page.Items) != 2 || page.NextAfterID == 0 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = h.store.List(h.ctx, c.ListFilter{Limit: 2, AfterID: page.NextAfterID})
	if err != nil || len(page.Items) != 1 || page.NextAfterID != 0 {
		t.Fatalf("last page: %+v %v", page, err)
	}
	for index := 0; index < 3; index++ {
		planned(t, h, active.ID, fmt.Sprintf("step:%d", index))
	}
	steps, err := h.store.ListSteps(h.ctx, active.ID, 0, 2)
	if err != nil || len(steps.Items) != 2 || steps.NextAfterID == 0 {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	steps, err = h.store.ListSteps(h.ctx, active.ID, steps.NextAfterID, 2)
	if err != nil || len(steps.Items) != 1 || steps.NextAfterID != 0 {
		t.Fatalf("steps last: %+v %v", steps, err)
	}
	if _, err := h.store.Get(h.ctx, 999); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := h.store.List(h.ctx, c.ListFilter{Limit: 101}); !errors.Is(err, c.ErrInvalid) {
		t.Fatal(err)
	}
}
