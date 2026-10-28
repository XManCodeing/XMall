package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type lifecycleStore struct {
	c.Store
	instance   d.Instance
	began      chan struct{}
	heartbeats atomic.Int32
}

func (s *lifecycleStore) BeginGeneration(context.Context, uint, uint64) (*d.Instance, error) {
	i := s.instance
	i.Generation++
	close(s.began)
	return &i, nil
}
func (s *lifecycleStore) Heartbeat(context.Context, uint, uint64, string, string, uint64) error {
	s.heartbeats.Add(1)
	return nil
}
func (s *lifecycleStore) Get(context.Context, uint) (*d.Instance, error) {
	i := s.instance
	i.Generation++
	return &i, nil
}
func (s *lifecycleStore) ClaimInbox(context.Context, uint, string, time.Time) (*d.Inbox, error) {
	return nil, nil
}
func (s *lifecycleStore) ClaimDelivery(context.Context, uint, string, time.Time) (*d.Delivery, error) {
	return nil, nil
}

type lifecycleLease struct {
	released atomic.Bool
	lose     bool
}

func (l *lifecycleLease) Renew(context.Context) error {
	if l.lose {
		return errors.New("lost")
	}
	return nil
}
func (l *lifecycleLease) Release(context.Context) error { l.released.Store(true); return nil }

type lifecycleLeaser struct{ lease *lifecycleLease }

func (l lifecycleLeaser) Acquire(context.Context, string) (c.Lease, error) { return l.lease, nil }

type lifecycleAPI struct {
	c.API
	polled chan struct{}
}

func (a *lifecycleAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (a *lifecycleAPI) Poll(ctx context.Context, _ string, _ int64) ([]c.Update, error) {
	close(a.polled)
	<-ctx.Done()
	return nil, ctx.Err()
}

type lifecycleHandler struct{ c.Handler }

type deliveryGuardStore struct {
	c.Store
	item   *d.Delivery
	cancel context.CancelFunc
	status string
}

func (s *deliveryGuardStore) ClaimDelivery(context.Context, uint, string, time.Time) (*d.Delivery, error) {
	item := s.item
	s.item = nil
	return item, nil
}
func (s *deliveryGuardStore) FinishDelivery(_ context.Context, _ uint, _ string, status string, _ int64, _ string, _ time.Time) error {
	s.status = status
	s.cancel()
	return nil
}

type deliveryGuardHandler struct {
	c.Handler
	checks int
}

func (h *deliveryGuardHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	h.checks++
	if h.checks > 1 {
		return errors.New("binding revoked")
	}
	return nil
}
func TestDeliveryRechecksAuthorizationImmediatelyBeforeSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &deliveryGuardStore{cancel: cancel}
	manager := NewManager(store, &apiStub{}, "test-key")
	cipher, err := manager.Seal(`{"text":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	store.item = &d.Delivery{ID: 1, BotID: 1, TelegramUserID: 12, ChatID: 12, UserID: 7, PayloadCipher: cipher}
	handler := &deliveryGuardHandler{}
	runtime := NewRuntime(manager, nil, handler)
	// apiStub.Send is deliberately absent: reaching it would panic.
	runtime.deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if handler.checks != 2 || store.status != "failed" {
		t.Fatalf("checks=%d status=%s", handler.checks, store.status)
	}
}

func TestCancelLongPollJoinsBeforeLeaseRelease(t *testing.T) {
	s := &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}
	api := &lifecycleAPI{polled: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	cipher, err := m.Seal("dummy")
	if err != nil {
		t.Fatal(err)
	}
	s.instance.TokenCipher = cipher
	lease := &lifecycleLease{}
	runtime := NewRuntime(m, lifecycleLeaser{lease}, lifecycleHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runtime.runBot(ctx, s.instance); close(done) }()
	select {
	case <-api.polled:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	if lease.released.Load() {
		t.Fatal("released lease during active poll")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not join children")
	}
	if !lease.released.Load() {
		t.Fatal("lease not released after draining")
	}
}
func TestLostLeaseDoesNotBeginPolling(t *testing.T) {
	s := &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}
	api := &lifecycleAPI{polled: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	cipher, _ := m.Seal("dummy")
	s.instance.TokenCipher = cipher
	lease := &lifecycleLease{lose: true}
	runtime := NewRuntime(m, lifecycleLeaser{lease}, lifecycleHandler{})
	runtime.runBot(context.Background(), s.instance)
	select {
	case <-api.polled:
		t.Fatal("lost owner polled Telegram")
	default:
	}
	if !lease.released.Load() {
		t.Fatal("lease cleanup omitted")
	}
}
