package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type restartLifecycleService struct {
	stopErr error
	stopped bool
}

func (*restartLifecycleService) Name() string                    { return "restart-test" }
func (*restartLifecycleService) Start(ctx context.Context) error { <-ctx.Done(); return nil }
func (s *restartLifecycleService) Stop(context.Context) error    { s.stopped = true; return s.stopErr }

func TestRunnerDrainsBeforeCleanupAndPropagatesStopFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := &restartLifecycleService{}
		failure := errors.New("shutdown timeout")
		if fail {
			s.stopErr = failure
		}
		r := NewRunner(s)
		cleaned := false
		r.cleanup = []func() error{func() error {
			if !s.stopped {
				t.Error("cleanup before service stop")
			}
			cleaned = true
			return nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := r.Run(ctx, time.Second, nil)
		if fail {
			if !errors.Is(err, failure) || cleaned {
				t.Fatalf("failed drain must block exec cleanup: err=%v cleaned=%v", err, cleaned)
			}
		} else if err != nil || !cleaned {
			t.Fatalf("successful drain: err=%v cleaned=%v", err, cleaned)
		}
	}
}
