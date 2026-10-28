package application

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

type deliveryLossStore struct {
	*lifecycleStore
	item             *d.Delivery
	polled           <-chan struct{}
	inboxStarted     chan struct{}
	reconcileStarted chan struct{}
	inboxDone        chan struct{}
}

func (s *deliveryLossStore) ClaimDelivery(ctx context.Context, _ uint, _ string, _ time.Time) (*d.Delivery, error) {
	for _, started := range []<-chan struct{}{s.polled, s.inboxStarted, s.reconcileStarted} {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-started:
		}
	}
	item := s.item
	s.item = nil
	return item, nil
}
func (s *deliveryLossStore) ClaimInbox(ctx context.Context, _ uint, _ string, _ time.Time) (*d.Inbox, error) {
	close(s.inboxStarted)
	<-ctx.Done()
	close(s.inboxDone)
	return nil, ctx.Err()
}

type deliveryLossLease struct {
	lifecycleLease
	polled <-chan struct{}
}

func (l *deliveryLossLease) Renew(context.Context) error {
	select {
	case <-l.polled:
		return errors.New("delivery detected lost lease")
	default:
		return nil
	}
}

type deliveryLossLeaser struct{ c.Lease }

func (l deliveryLossLeaser) Acquire(context.Context, string) (c.Lease, error) { return l.Lease, nil }
func TestDeliveryLeaseLossCancelsWholeBot(t *testing.T) {
	api := &lifecycleAPI{polled: make(chan struct{})}
	s := &deliveryLossStore{lifecycleStore: &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}, polled: api.polled, inboxStarted: make(chan struct{}), reconcileStarted: make(chan struct{}), inboxDone: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	s.instance.TokenCipher, _ = m.Seal("dummy")
	payload, _ := m.Seal(`{"text":"test"}`)
	s.item = &d.Delivery{ID: 1, PayloadCipher: payload}
	lease := &deliveryLossLease{polled: api.polled}
	runtime := NewRuntime(m, deliveryLossLeaser{lease}, &deliveryGuardHandler{})
	reconciled := make(chan struct{})
	runtime.Reconcile = func(ctx context.Context, _ uint) error {
		close(s.reconcileStarted)
		<-ctx.Done()
		close(reconciled)
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { runtime.runBot(ctx, s.instance); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery lease failure left sibling workers running")
	}
	for name, ch := range map[string]<-chan struct{}{"inbox": s.inboxDone, "reconcile": reconciled} {
		select {
		case <-ch:
		default:
			t.Errorf("%s not joined", name)
		}
	}
	if !lease.released.Load() {
		t.Fatal("lease not released after joining")
	}
}
