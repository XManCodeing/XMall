package telegrammembership

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRecoveryImmediateAndDrained 检验启动立即恢复、取消传播及 Stop 等待当前批次完成。
func TestRecoveryImmediateAndDrained(t *testing.T) {
	entered := make(chan struct{})
	drained := make(chan struct{})
	service := NewRecoveryService(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(drained)
		return ctx.Err()
	}, time.Hour)
	result := make(chan error, 1)
	go func() { result <- service.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not run immediately")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	default:
		t.Fatal("stop returned before draining")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := service.Start(ctx); err == nil {
		t.Fatal("duplicate start accepted")
	}
}

// TestRecoveryBeforeStartStop 检验尚未启动不虚报完成，之后启动也不会执行已取消的批次。
func TestRecoveryBeforeStartStop(t *testing.T) {
	service := NewRecoveryService(func(context.Context) error { t.Error("stopped worker ran"); return nil }, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(service.Stop(ctx), context.DeadlineExceeded) {
		t.Fatal("unstarted worker reported drained")
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryPanicKeepsConsumerAlive verifies a local batch panic cannot stop future recovery.
// t bounds the loop and checks Stop still observes a drained worker after the second batch.
func TestRecoveryPanicKeepsConsumerAlive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	service := NewRecoveryService(func(context.Context) error {
		calls++
		if calls == 1 {
			panic("synthetic sensitive panic value")
		}
		cancel()
		return nil
	}, time.Millisecond)
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("consumer stopped after panic: calls=%d", calls)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("recovered service did not drain: %v", err)
	}
}

// TestRecoveryRetriesFailure 检验单轮失败不终止服务，并验证缺失依赖明确失败。
func TestRecoveryRetriesFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	service := NewRecoveryService(func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("synthetic transient failure")
	}, time.Millisecond)
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if err := NewRecoveryService(nil, 0).Start(context.Background()); err == nil {
		t.Fatal("missing dependency accepted")
	}
}
