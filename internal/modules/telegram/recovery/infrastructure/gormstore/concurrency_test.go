package gormstore_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// TestCompetingPreflightsRace 验证并存预检同时确认时只允许一个任务获得全部资源。
func TestCompetingPreflightsRace(t *testing.T) {
	h := setup(t)
	first := preflight(t, h, "first")
	second := preflight(t, h, "second")
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, task := range []*d.Task{first, second} {
		wait.Add(1)
		go func(task *d.Task) {
			defer wait.Done()
			_, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7})
			results <- err
		}(task)
	}
	wait.Wait()
	close(results)
	success, busy := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, c.ErrScopeBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || busy != 1 {
		t.Fatalf("success=%d busy=%d", success, busy)
	}
}

// TestIdentityTransferAndNormalizedScope 验证第三种任务、字符串身份 ID、scope 集合规范化幂等。
func TestIdentityTransferAndNormalizedScope(t *testing.T) {
	h := setup(t)
	request := c.PreflightInput{Kind: d.IdentityTransfer, RequestKey: "identity", ActorID: 7, Reason: "verified transfer", TTL: time.Minute,
		Snapshot: d.Snapshot{Scope: d.Scope{AccountIDs: []string{"9", "8", "9"}, TelegramIDs: []string{"9007199254740993", "9007199254740994"}},
			Old: d.Endpoint{AccountID: 8, TelegramID: 9007199254740993}, New: d.Endpoint{AccountID: 9, TelegramID: 9007199254740994}, ExpectedOld: d.Versions{Binding: 10}}}
	first, err := h.store.CreatePreflight(h.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Snapshot.Scope.AccountIDs = []string{"8", "9"}
	replay, err := h.store.CreatePreflight(h.ctx, request)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("set normalization %+v %v", replay, err)
	}
	request.Snapshot.ExpectedOld.Binding++
	if _, err := h.store.CreatePreflight(h.ctx, request); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("version conflict %v", err)
	}
	if _, err := h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: first.PreflightRef, ExpectedVersion: 1, ActorID: 7}); err != nil {
		t.Fatal(err)
	}
}

// TestBotReplacementAndVersionCAS 验证 bot 替换持久化以及父任务状态推进的严格 CAS。
func TestBotReplacementAndVersionCAS(t *testing.T) {
	h := setup(t)
	request := c.PreflightInput{Kind: d.BotReplace, RequestKey: "bot", ActorID: 7, Reason: "replace", TTL: time.Minute,
		Snapshot: d.Snapshot{Scope: d.Scope{BotIDs: []string{"2", "3"}}, Old: d.Endpoint{BotID: 2}, New: d.Endpoint{BotID: 3}}}
	task, err := h.store.CreatePreflight(h.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	task, err = h.store.Confirm(h.ctx, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: 1, ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: 1, State: d.Switching}); !errors.Is(err, c.ErrStale) {
		t.Fatal(err)
	}
	if _, err := h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Completed}); !errors.Is(err, c.ErrStale) {
		t.Fatal(err)
	}
	if _, err := h.store.Transition(h.ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Switching}); err != nil {
		t.Fatal(err)
	}
}
