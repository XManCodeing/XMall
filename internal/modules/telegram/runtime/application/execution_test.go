package application

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type executionStore struct {
	c.Store
	row *d.Instance
	err error
}

func (s executionStore) Get(context.Context, uint) (*d.Instance, error) { return s.row, s.err }

type executionLease struct {
	c.Lease
	err   error
	calls int
}

func (l *executionLease) Renew(context.Context) error { l.calls++; return l.err }

func TestSupervisedReconcileCarriesExecutionFence(t *testing.T) {
	s := &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", State: "running", Version: 1, Generation: 6}, began: make(chan struct{})}
	api := &lifecycleAPI{polled: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	s.instance.TokenCipher, _ = m.Seal("dummy")
	lease := &lifecycleLease{}
	runtime := NewRuntime(m, lifecycleLeaser{lease}, lifecycleHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	checked := make(chan error, 1)
	runtime.Reconcile = func(ctx context.Context, bot uint) error {
		err := c.CheckExecutionFence(ctx, bot, 7)
		checked <- err
		cancel()
		return err
	}
	runtime.runBot(ctx, s.instance)
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("reconcile did not execute with fence")
	}
	if !lease.released.Load() {
		t.Fatal("lease not released")
	}
}

func TestExecutionFenceRequiresLiveMatchingLease(t *testing.T) {
	for _, name := range []string{"valid", "wrong_bot", "wrong_generation", "lost_lease", "rotated", "stopped", "disabled", "external", "database_error", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			row := d.Instance{ID: 1, Generation: 7, Version: 3, Enabled: true, Mode: "internal", State: "running"}
			lease := &executionLease{}
			store := executionStore{row: &row}
			fence := &leasedExecutionFence{store: store, lease: lease, instance: row, cancel: cancel}
			bot, gen := uint(1), uint64(7)
			switch name {
			case "wrong_bot":
				bot = 2
			case "wrong_generation":
				gen++
			case "lost_lease":
				lease.err = errors.New("lease gone")
			case "rotated":
				row.Version++
			case "stopped":
				row.State = "stopped"
			case "disabled":
				row.Enabled = false
			case "external":
				row.Mode = "external"
			case "database_error":
				store.err = errors.New("unavailable")
				fence.store = store
			case "cancelled":
				cancel()
			}
			err := c.CheckExecutionFence(c.WithExecutionFence(ctx, fence), bot, gen)
			if (err == nil) != (name == "valid") {
				t.Fatalf("err=%v", err)
			}
			if name == "lost_lease" && ctx.Err() == nil {
				t.Fatal("lease loss did not stop siblings")
			}
			if (name == "wrong_bot" || name == "wrong_generation" || name == "cancelled") && lease.calls != 0 {
				t.Fatal("invalid caller renewed lease")
			}
		})
	}
	if err := c.CheckExecutionFence(context.Background(), 1, 7); !errors.Is(err, c.ErrStale) {
		t.Fatalf("unleased execution allowed: %v", err)
	}
}
