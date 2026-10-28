package broadcastapp

import (
	"context"
	"errors"
	broadcastcontract "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/contract"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	"testing"
	"time"
)

type broadcastStoreStub struct {
	items  map[uint]*broadcastdomain.Broadcast
	nextID uint
}

func (s *broadcastStoreStub) Create(ctx context.Context, b *broadcastdomain.Broadcast) error {
	if s.items == nil {
		s.items = map[uint]*broadcastdomain.Broadcast{}
	}
	s.nextID++
	b.ID = s.nextID
	return s.Update(ctx, b)
}
func (s *broadcastStoreStub) Update(_ context.Context, b *broadcastdomain.Broadcast) error {
	v := *b
	s.items[b.ID] = &v
	return nil
}
func (s *broadcastStoreStub) GetByID(_ context.Context, id uint) (*broadcastdomain.Broadcast, error) {
	if b := s.items[id]; b != nil {
		v := *b
		return &v, nil
	}
	return nil, nil
}
func (s *broadcastStoreStub) List(context.Context, broadcastcontract.ListFilter) ([]broadcastdomain.Broadcast, int64, error) {
	var items []broadcastdomain.Broadcast
	for _, b := range s.items {
		items = append(items, *b)
	}
	return items, int64(len(items)), nil
}

type bridgeStub struct {
	fail     bool
	keys     map[uint]bool
	progress Progress
}

func (*bridgeStub) Prepare(context.Context, CreateInput) (string, int, error) {
	return "encrypted", 2, nil
}
func (b *bridgeStub) Enqueue(_ context.Context, job *broadcastdomain.Broadcast) error {
	b.keys[job.ID] = true
	if b.fail {
		return errors.New("interrupted")
	}
	return nil
}
func (b *bridgeStub) Progress(context.Context, uint, uint) (Progress, error) { return b.progress, nil }
func TestResumeUsesLedgerAndQuarantinesUnknown(t *testing.T) {
	store := &broadcastStoreStub{}
	bridge := &bridgeStub{fail: true, keys: map[uint]bool{}, progress: Progress{Total: 2, Sent: 1, Unknown: 1}}
	s := NewRuntimeService(store, nil, bridge, nil)
	ctx := context.Background()
	job, err := s.CreateBroadcast(ctx, CreateInput{BotID: 7, Title: "demo", MessageHTML: "hello", RecipientType: "all"})
	if err != nil || job == nil {
		t.Fatal("expected accepted durable interrupted job")
	}
	bridge.fail = false
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBroadcast(context.Background(), job.ID)
	if got.Status != "needs_review" || got.SuccessCount != 1 || got.UnknownCount != 1 || len(bridge.keys) != 1 {
		t.Fatalf("unexpected progress: %+v", got)
	}
	if got.MessageHTML != "" || len(got.RecipientChatIDs) != 0 || got.PayloadCipher != "encrypted" {
		t.Fatal("plaintext snapshot retained")
	}
}
func TestLegacyNeverRestarts(t *testing.T) {
	store := &broadcastStoreStub{}
	b := &broadcastdomain.Broadcast{Status: "running", RecipientCount: 20, SuccessCount: 3}
	_ = store.Create(context.Background(), b)
	s := NewRuntimeService(store, nil, nil, nil)
	if err := s.ProcessBroadcast(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBroadcast(context.Background(), b.ID)
	if got.Status != "needs_review" || got.SuccessCount != 3 {
		t.Fatal(got)
	}
}
func TestExplicitBotRequired(t *testing.T) {
	s := NewRuntimeService(&broadcastStoreStub{}, nil, &bridgeStub{}, nil)
	_, err := s.CreateBroadcast(context.Background(), CreateInput{Title: "demo", MessageHTML: "hi", RecipientType: "all"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

type cancelStore struct {
	broadcastStoreStub
	phase   string
	entered chan context.Context
}

func (s *cancelStore) block(ctx context.Context, phase string) error {
	if s.phase != phase {
		return nil
	}
	s.entered <- ctx
	<-ctx.Done()
	return ctx.Err()
}
func (s *cancelStore) Create(ctx context.Context, b *broadcastdomain.Broadcast) error {
	if err := s.block(ctx, "create"); err != nil {
		return err
	}
	return s.broadcastStoreStub.Create(ctx, b)
}
func (s *cancelStore) List(ctx context.Context, f broadcastcontract.ListFilter) ([]broadcastdomain.Broadcast, int64, error) {
	if err := s.block(ctx, "list"); err != nil {
		return nil, 0, err
	}
	return s.broadcastStoreStub.List(ctx, f)
}
func (s *cancelStore) GetByID(ctx context.Context, id uint) (*broadcastdomain.Broadcast, error) {
	if err := s.block(ctx, "get"); err != nil {
		return nil, err
	}
	return s.broadcastStoreStub.GetByID(ctx, id)
}
func (s *cancelStore) Update(ctx context.Context, b *broadcastdomain.Broadcast) error {
	if err := s.block(ctx, "update"); err != nil {
		return err
	}
	return s.broadcastStoreStub.Update(ctx, b)
}

func TestRecoveryPropagatesSameContextToEveryStorePhase(t *testing.T) {
	for _, phase := range []string{"create", "list", "get", "update", "legacy-update"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			job := &broadcastdomain.Broadcast{ID: 1, BotID: 7, DeliveryVersion: 1, PayloadCipher: "cipher", Status: "pending", RecipientCount: 2}
			store := &cancelStore{phase: phase, entered: make(chan context.Context, 1), broadcastStoreStub: broadcastStoreStub{items: map[uint]*broadcastdomain.Broadcast{1: job}}}
			if phase == "legacy-update" {
				job.DeliveryVersion = 0
				store.phase = "update"
			}
			bridge := &bridgeStub{keys: map[uint]bool{}, progress: Progress{Total: 2, Sent: 2}}
			svc := NewRuntimeService(store, nil, bridge, nil)
			done := make(chan error, 1)
			go func() {
				if phase == "create" {
					_, err := svc.CreateBroadcast(ctx, CreateInput{BotID: 7, Title: "test", MessageHTML: "test", RecipientType: "all"})
					done <- err
				} else {
					done <- svc.Reconcile(ctx)
				}
			}()
			select {
			case got := <-store.entered:
				if got != ctx {
					t.Error("recovery context was replaced")
				}
			case <-time.After(time.Second):
				t.Fatal("store phase was not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery did not join cancelled database operation")
			}
			if job.Status != "pending" {
				t.Fatal("cancelled write changed persisted broadcast")
			}
		})
	}
}
