package application

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type retryStampHandler struct {
	c.Handler
	calls    int
	changeOn int
}

func (h *retryStampHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error { return nil }
func (h *retryStampHandler) AuthorizeStamp(_ context.Context, _ uint, _ int64, expected string) error {
	h.calls++
	if expected != "original-binding" || h.calls >= h.changeOn {
		return errors.New("binding changed")
	}
	return nil
}

func TestRetriedDeliveryStillChecksOriginalStamp(t *testing.T) {
	for _, changeOn := range []int{1, 2} {
		t.Run(time.Duration(changeOn).String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &deliveryGuardStore{cancel: cancel}
			manager := NewManager(store, &apiStub{}, "test-key")
			cipher, err := manager.Seal(`{"text":"private","identity_stamp":"original-binding"}`)
			if err != nil {
				t.Fatal(err)
			}
			store.item = &d.Delivery{ID: 1, BotID: 1, BroadcastID: 4, TelegramUserID: 12, ChatID: 12, UserID: 7, PayloadCipher: cipher, Status: "processing", Attempts: 9}
			handler := &retryStampHandler{changeOn: changeOn}
			runtime := NewRuntime(manager, nil, handler)
			// Send is deliberately absent: any external send panics.
			runtime.deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
			if store.status != "failed" || handler.calls != changeOn {
				t.Fatalf("status=%s checks=%d", store.status, handler.calls)
			}
		})
	}
}
