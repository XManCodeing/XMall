package application

import (
	"context"
	"encoding/json"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

type invitePanelHandler struct {
	acceptingPanelHandler
	deny bool
}

// AuthorizeInviteDelivery supplies the isolated current-route guard used by these runtime tests.
func (handler invitePanelHandler) AuthorizeInviteDelivery(context.Context, uint, c.Message) error {
	if handler.deny {
		return c.ErrStale
	}
	return nil
}

// TestInvitePanelNeverReplaysUncertainEdits covers the real delivery state classifier.
func TestInvitePanelNeverReplaysUncertainEdits(t *testing.T) {
	for _, scenario := range []struct {
		name, operation, want string
		failure               error
		deny                  bool
	}{
		{"sent edit", "edit", "sent", nil, false},
		{"empty send receipt", "send", "unknown", nil, false},
		{"unknown edit", "edit", "unknown", applyingFailure{kind: "unknown", uncertain: true}, false},
		{"failed", "edit", "failed", applyingFailure{kind: "invalid_content"}, false},
		{"old route", "edit", "failed", nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &panelApplyStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel}}
			api := &panelApplyAPI{err: scenario.failure}
			manager := NewManager(store, api, "test-key")
			message := c.Message{InviteDelivery: &c.InviteDeliveryRef{InviteID: 8}, Purpose: "panel", Operation: scenario.operation, TargetMessageID: 77}
			raw, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			store.item = &d.Delivery{ID: 1, BotID: 1, UserID: 7, ChatID: 12, TelegramUserID: 12, Purpose: "panel", PayloadCipher: cipher, Operation: scenario.operation}
			NewRuntime(manager, nil, invitePanelHandler{deny: scenario.deny}).deliver(ctx, &d.Instance{ID: 1}, "test", &lifecycleLease{}, cancel)
			calls := 1
			if scenario.deny {
				calls = 0
			}
			if store.status != scenario.want || api.calls != calls {
				t.Fatalf("state=%s sends=%d", store.status, api.calls)
			}
		})
	}
}

type applyingFailure struct {
	kind      string
	uncertain bool
}

func (e applyingFailure) Error() string     { return "rejected" }
func (e applyingFailure) Kind() string      { return e.kind }
func (e applyingFailure) StatusCode() int   { return 400 }
func (e applyingFailure) RetrySeconds() int { return 0 }
func (e applyingFailure) Uncertain() bool   { return e.uncertain }

type panelApplyAPI struct {
	c.API
	err   error
	calls int
}

func (a *panelApplyAPI) Apply(context.Context, string, c.Message) (c.DeliveryResult, error) {
	a.calls++
	return c.DeliveryResult{}, a.err
}

type acceptingPanelHandler struct{ c.Handler }

func (acceptingPanelHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error { return nil }

type panelApplyStore struct {
	*deliveryGuardStore
	replaced bool
	result   c.DeliveryResult
}

func (s *panelApplyStore) PreparePanel(_ context.Context, item *d.Delivery, m *c.Message) error {
	item.Operation = m.Operation
	item.PanelEpoch = m.PanelEpoch
	return nil
}
func (s *panelApplyStore) FinishPanel(_ context.Context, _ *d.Delivery, status string, result c.DeliveryResult, _ string, _ time.Time) error {
	s.status = status
	s.result = result
	s.cancel()
	return nil
}
func (s *panelApplyStore) ReplacePanel(context.Context, *d.Delivery) error {
	s.replaced = true
	return nil
}
func (s *panelApplyStore) Contact(context.Context, uint, int64) (*d.Contact, error) {
	return &d.Contact{Reachable: true, ChatID: 12}, nil
}

func TestPanelSemanticErrorsDriveLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, op, kind, want string
		unknown, replace     bool
	}{{"edit transport retry", "edit", "retryable", "retry", false, false}, {"unchanged", "edit", "not_modified", "sent", false, false}, {"missing", "edit", "target_missing", "retry", false, true}, {"bad content", "edit", "invalid_content", "failed", false, false}, {"unknown send", "send", "unknown", "unknown", true, false}, {"unknown edit", "edit", "unknown", "retry", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &panelApplyStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel}}
			api := &panelApplyAPI{err: applyingFailure{kind: tc.kind, uncertain: tc.unknown}}
			manager := NewManager(store, api, "test-key")
			m := c.Message{Purpose: "panel", Operation: tc.op, PanelEpoch: "epoch", TargetMessageID: 77, Text: "panel"}
			raw, _ := json.Marshal(m)
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			store.item = &d.Delivery{ID: 1, BotID: 1, TelegramUserID: 12, ChatID: 12, UserID: 7, PayloadCipher: cipher, Purpose: "panel", Operation: tc.op, PanelEpoch: "epoch", Attempts: 1}
			NewRuntime(manager, nil, acceptingPanelHandler{}).deliver(ctx, &d.Instance{ID: 1}, "test", &lifecycleLease{}, cancel)
			if store.status != tc.want || store.replaced != tc.replace || api.calls != 1 {
				t.Fatalf("status=%s replace=%v calls=%d", store.status, store.replaced, api.calls)
			}
			if tc.kind == "not_modified" && store.result.MessageID != 77 {
				t.Fatal("unchanged panel lost physical target")
			}
		})
	}
}
