package application

import (
	"context"
	"encoding/json"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

// proactiveHandler requires a real non-empty stamp and dedicated purpose without exposing sessions.
type proactiveHandler struct {
	acceptingPanelHandler
	deny bool
}

// AuthorizeProactiveDelivery supplies the additional purpose guard for the runtime transport test.
func (handler proactiveHandler) AuthorizeProactiveDelivery(_ context.Context, _ uint, message c.Message) error {
	if handler.deny || message.Purpose != "membership_proactive" {
		return c.ErrStale
	}
	return nil
}

// AuthorizeStamp rejects missing or forged binding generations independently of the purpose guard.
func (proactiveHandler) AuthorizeStamp(_ context.Context, _ uint, _ int64, stamp string) error {
	if stamp != "1:7" {
		return c.ErrStale
	}
	return nil
}

// proactiveAPI returns configurable real receipt facts without implementing any panel API.
type proactiveAPI struct {
	c.API
	receipt int64
	err     error
	calls   int
}

// Send records one external send; it never edits a session or an existing message.
func (api *proactiveAPI) Send(context.Context, string, c.Message) (int64, error) {
	api.calls++
	return api.receipt, api.err
}

// TestProactiveSendWithoutSession verifies the real delivery loop, independent send and unknown retention.
// Empty receipts, uncertain transport and missing stamp must never be mistaken for success or replayed.
func TestProactiveSendWithoutSession(t *testing.T) {
	for _, scenario := range []struct {
		name, state, stamp string
		receipt            int64
		failure            error
		deny               bool
		calls              int
	}{
		{"success", "sent", "1:7", 123, nil, false, 1},
		{"missing receipt", "unknown", "1:7", 0, nil, false, 1},
		{"unknown", "unknown", "1:7", 0, applyingFailure{kind: "unknown", uncertain: true}, false, 1},
		{"rejected", "failed", "1:7", 0, applyingFailure{kind: "invalid_content"}, false, 1},
		{"missing stamp", "failed", "", 0, nil, false, 0},
		{"denied", "failed", "1:7", 0, nil, true, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &deliveryGuardStore{cancel: cancel}
			api := &proactiveAPI{receipt: scenario.receipt, err: scenario.failure}
			manager := NewManager(store, api, "test-key")
			message := c.Message{ProactiveDelivery: &c.ProactiveDeliveryRef{ID: 1, Stage: "slow"}, TargetType: c.TargetShopPrivate, Purpose: "membership_proactive", Operation: "send", OwnerUserID: 7, ChatID: 12, BusinessRef: "membership-proactive:1:slow", IdentityStamp: scenario.stamp, Text: "preparing"}
			raw, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			store.item = &d.Delivery{ID: 1, BotID: 1, UserID: 7, ChatID: 12, TelegramUserID: 12, Key: message.BusinessRef, TargetType: string(c.TargetShopPrivate), Purpose: message.Purpose, Operation: "send", PayloadCipher: cipher}
			NewRuntime(manager, nil, proactiveHandler{deny: scenario.deny}).deliver(ctx, &d.Instance{ID: 1}, "test", &lifecycleLease{}, cancel)
			if store.status != scenario.state || api.calls != scenario.calls {
				t.Fatalf("state=%s calls=%d", store.status, api.calls)
			}
		})
	}
}
