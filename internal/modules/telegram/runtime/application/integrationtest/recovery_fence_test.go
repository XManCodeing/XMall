package integrationtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
)

// heldPollAPI deliberately ignores cancellation until the test releases transport.
// Context cancellation and lease release alone cannot manufacture an HTTP join.
type heldPollAPI struct {
	idleAPI
	entered, cancelled, release chan struct{}
	once                        sync.Once
}

// PollWithSubscription exposes actual entry/cancellation and waits for explicit exit.
func (api *heldPollAPI) PollWithSubscription(ctx context.Context, _ string, _ int64, _ []string) ([]c.Update, error) {
	api.once.Do(func() { close(api.entered) })
	<-ctx.Done()
	close(api.cancelled)
	<-api.release
	return nil, ctx.Err()
}

// TestRemoteRecoveryFenceWaitsForUncooperativeHTTP exercises the real supervisor,
// SQL admission ledger and a second Store facade with MaxOpenConns(1).
func TestRemoteRecoveryFenceWaitsForUncooperativeHTTP(t *testing.T) {
	api := &heldPollAPI{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	var released atomic.Bool
	runtime := app.NewRuntime(fixture.manager, leaser{lease{release: func() { released.Store(true) }}}, fixture.dispatcher)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runtime.Start(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime not joined")
		}
	}()
	defer close(api.release)
	select {
	case <-api.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	remote := gormstore.New(fixture.db)
	current, err := remote.Get(context.Background(), fixture.bot.ID)
	must(t, err)
	fence := c.RecoveryFence{BotID: current.ID, TaskID: 101, Version: current.Version, Generation: current.Generation}
	must(t, remote.BlockRecovery(context.Background(), fence))
	select {
	case <-api.cancelled:
	case <-time.After(4 * time.Second):
		t.Fatal("remote fence did not cancel worker")
	}
	if err := remote.RecoveryDrained(context.Background(), fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("in-flight HTTP certified drained: %v", err)
	}
	if released.Load() {
		t.Fatal("lease released before HTTP join")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if err := runtime.BlockAndDrainRecovery(waitCtx, fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("drain timeout did not remain blocked: %v", err)
	}
	// Stop's timeout is only a timeout, not evidence of drain.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopCancel()
	if err := runtime.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop unexpectedly joined: %v", err)
	}
	if err := remote.RecoveryDrained(context.Background(), fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("timeout certified drain: %v", err)
	}
	// Release the held transport; deferred close still handles early failures.
	api.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime failed to join")
	}
	must(t, remote.RecoveryDrained(context.Background(), fence))
	must(t, runtime.BlockAndDrainRecovery(context.Background(), fence))
	if !released.Load() {
		t.Fatal("lease not released after join")
	}
}
