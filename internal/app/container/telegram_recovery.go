package container

import (
	"context"
	"errors"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"log/slog"
	"time"
)

// withRecoveryLease joins renewal and recovery before releasing ownership. Renewal
// deadlines are measured from request start, not from a potentially delayed reply.
func withRecoveryLease(parent context.Context, leaser runtimecontract.Leaser, work func(context.Context) error, interval, timeout, safetyTTL time.Duration) (err error) {
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
	var renewErr error // Published by closing done; read only after joining.
	defer func() {
		cause := context.Cause(ctx)
		cancel(nil)
		<-done
		err = errors.Join(err, cause, renewErr)
	}()
	go func() {
		defer close(done)
		defer func() {
			if recover() != nil {
				renewErr = errors.New("fulfillment recovery lease renewal panic")
				cancel(renewErr)
				slog.ErrorContext(ctx, "fulfillment recovery lease renewal panic")
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				err := func() error {
					renewCtx, stop := context.WithTimeout(ctx, timeout)
					defer stop()
					return lease.Renew(renewCtx)
				}()
				if err != nil {
					if ctx.Err() == nil {
						renewErr = err
					}
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
	return work(ctx)
}
