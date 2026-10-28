package container

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"sync/atomic"
	"testing"
	"time"
)

type recoveryLease struct {
	renew    func(context.Context) error
	released atomic.Bool
}

func (l *recoveryLease) Renew(ctx context.Context) error { return l.renew(ctx) }
func (l *recoveryLease) Release(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	l.released.Store(true)
	return nil
}

type recoveryLeaser struct{ lease *recoveryLease }

func (l recoveryLeaser) Acquire(context.Context, string) (c.Lease, error) { return l.lease, nil }
func TestRecoveryLeaseRenewsSlowWorkAndJoins(t *testing.T) {
	var renewals atomic.Int32
	lease := &recoveryLease{renew: func(context.Context) error { renewals.Add(1); return nil }}
	err := withRecoveryLease(context.Background(), recoveryLeaser{lease}, func(ctx context.Context) error {
		timer := time.NewTimer(140 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if lease.released.Load() {
			t.Error("lease released before recovery joined")
		}
		return nil
	}, 5*time.Millisecond, 20*time.Millisecond, 70*time.Millisecond)
	if err != nil || renewals.Load() < 3 || !lease.released.Load() {
		t.Fatalf("err=%v renewals=%d released=%v", err, renewals.Load(), lease.released.Load())
	}
}
func TestRecoveryLeaseFailureCancelsWork(t *testing.T) {
	lost := errors.New("lease lost")
	lease := &recoveryLease{renew: func(context.Context) error { return lost }}
	err := withRecoveryLease(context.Background(), recoveryLeaser{lease}, func(ctx context.Context) error {
		<-ctx.Done()
		if lease.released.Load() {
			t.Error("released before cancellation joined")
		}
		return ctx.Err()
	}, time.Millisecond, 20*time.Millisecond, time.Second)
	if !errors.Is(err, lost) || !lease.released.Load() {
		t.Fatalf("err=%v", err)
	}
}
func TestRecoveryLeaseDeadlineAndShutdownPropagate(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{true: "shutdown", false: "stalled renewal"}[shutdown], func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			lease := &recoveryLease{renew: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
			done := make(chan error, 1)
			go func() {
				done <- withRecoveryLease(parent, recoveryLeaser{lease}, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, time.Millisecond, time.Second, 40*time.Millisecond)
			}()
			<-entered
			if shutdown {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !lease.released.Load() {
					t.Fatalf("err=%v released=%v", err, lease.released.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation failed to join recovery/renewal")
			}
		})
	}
}
