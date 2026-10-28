package broadcastapp

import (
	"context"
	"errors"
	"testing"

	b "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

type deliveryBridgeStub struct {
	bridgeStub
	bot, broadcast uint
	page, size     int
}

func (s *deliveryBridgeStub) ListBroadcastDeliveries(_ context.Context, bot, broadcast uint, page, size int) ([]DeliveryItem, int64, error) {
	s.bot, s.broadcast, s.page, s.size = bot, broadcast, page, size
	return []DeliveryItem{{ID: 9, BotID: bot, BroadcastID: broadcast}}, 1, nil
}

type retryStoreStub struct {
	broadcastStoreStub
	bot, broadcast, delivery uint
}

func (s *retryStoreStub) RetryBroadcastDelivery(_ context.Context, bot, broadcast, delivery uint) error {
	s.bot, s.broadcast, s.delivery = bot, broadcast, delivery
	return nil
}
func TestDeliveryServiceUsesPersistedBotAndOptionalCapabilities(t *testing.T) {
	store := &retryStoreStub{broadcastStoreStub: broadcastStoreStub{items: map[uint]*b.Broadcast{
		1: {ID: 1, BotID: 7, DeliveryVersion: 1}, 2: {ID: 2},
	}}}
	bridge := &deliveryBridgeStub{}
	s := NewRuntimeService(store, nil, bridge, nil)
	rows, total, err := s.ListBroadcastDeliveries(context.Background(), 1, 0, 999)
	if err != nil || total != 1 || len(rows) != 1 || bridge.bot != 7 || bridge.broadcast != 1 || bridge.page != 1 || bridge.size != 200 {
		t.Fatalf("rows=%v err=%v bridge=%+v", rows, err, bridge)
	}
	if err := s.RetryBroadcastDelivery(context.Background(), 1, 9); err != nil || store.bot != 7 || store.broadcast != 1 || store.delivery != 9 {
		t.Fatalf("retry: %v %+v", err, store)
	}
	for _, tc := range []struct {
		id   uint
		want error
	}{{0, ErrInvalid}, {2, ErrInvalid}, {999, ErrNotFound}} {
		if _, _, err := s.ListBroadcastDeliveries(context.Background(), tc.id, 1, 20); !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
		if err := s.RetryBroadcastDelivery(context.Background(), tc.id, 9); !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	s = NewRuntimeService(&store.broadcastStoreStub, nil, &bridgeStub{}, nil)
	if _, _, err := s.ListBroadcastDeliveries(context.Background(), 1, 1, 20); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.RetryBroadcastDelivery(context.Background(), 1, 9); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestReconcileRepairsStaleTerminalSnapshotAfterRetry(t *testing.T) {
	for _, status := range []string{"failed", "needs_review"} {
		t.Run(status, func(t *testing.T) {
			store := &broadcastStoreStub{items: map[uint]*b.Broadcast{1: {ID: 1, BotID: 7, DeliveryVersion: 1, PayloadCipher: "cipher", RecipientCount: 2, Status: status}}}
			bridge := &bridgeStub{keys: map[uint]bool{}, progress: Progress{Total: 2, Pending: 1, Unknown: 1}}
			s := NewRuntimeService(store, nil, bridge, nil)
			if err := s.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ := store.GetByID(context.Background(), 1)
			if got.Status != "running" || got.CompletedAt != nil {
				t.Fatal(got)
			}
			bridge.progress = Progress{Total: 2, Sent: 1, Unknown: 1}
			if err := s.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ = store.GetByID(context.Background(), 1)
			if got.Status != "needs_review" || got.SuccessCount != 1 || got.UnknownCount != 1 {
				t.Fatal(got)
			}
		})
	}
}
