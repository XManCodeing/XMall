package container

import (
	"context"
	"errors"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"time"
)

// withRecoveryLease joins renewal and recovery before releasing ownership. Renewal
// deadlines are measured from request start, not from a potentially delayed reply.
func withRecoveryLease(parent context.Context, leaser runtimecontract.Leaser, recover func(context.Context) error, interval, timeout, safetyTTL time.Duration) error {
	// A pass may traverse many pages, but may not monopolize reconciliation forever.
	parent, stopPass := context.WithTimeout(parent, 2*time.Minute)
	defer stopPass()
	started := time.Now()
	lease, err := leaser.Acquire(parent, "xmall:telegram:fulfillment-recovery")
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lease.Release(ctx)
	}()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	deadline := time.AfterFunc(time.Until(started.Add(safetyTTL)), func() { cancel(errors.New("fulfillment recovery lease expired")) })
	defer deadline.Stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				renewCtx, stop := context.WithTimeout(ctx, timeout)
				err := lease.Renew(renewCtx)
				stop()
				if err != nil {
					cancel(err)
					return
				}
				if ctx.Err() != nil {
					return
				}
				deadline.Reset(time.Until(started.Add(safetyTTL)))
			}
		}
	}()
	err = recover(ctx)
	cause := context.Cause(ctx)
	cancel(nil)
	<-done
	return errors.Join(err, cause)
}
