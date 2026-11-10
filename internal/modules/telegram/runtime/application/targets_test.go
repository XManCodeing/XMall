package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type subscriptionHandler struct {
	c.Handler
	types []string
	err   error
}

func (h subscriptionHandler) AllowedUpdates(context.Context, uint) ([]string, error) {
	return h.types, h.err
}

type basicPollAPI struct {
	c.API
	calls  int
	offset int64
}

func (a *basicPollAPI) Poll(_ context.Context, _ string, offset int64) ([]c.Update, error) {
	a.calls++
	a.offset = offset
	return []c.Update{{ID: offset}}, nil
}

type subscriptionPollAPI struct {
	basicPollAPI
	types []string
}

func (a *subscriptionPollAPI) PollWithSubscription(ctx context.Context, token string, offset int64, types []string) ([]c.Update, error) {
	a.types = types
	return a.Poll(ctx, token, offset)
}
func TestRuntimeSubscriptionsFailClosedAndKeepOffset(t *testing.T) {
	for _, tc := range []struct {
		name     string
		types    []string
		extended bool
		want     error
	}{
		{"basic fallback", []string{"callback_query", "message"}, false, nil},
		{"subset not legacy", []string{"message"}, false, c.ErrSubscriptionUnsupported},
		{"expanded legacy", []string{"message", "chat_member"}, false, c.ErrSubscriptionUnsupported},
		{"expanded", []string{"message", "chat_member"}, true, nil},
		{"empty", nil, true, c.ErrInvalidSubscription},
		{"unknown", []string{"future"}, true, c.ErrInvalidSubscription},
		{"duplicate", []string{"message", "message"}, true, c.ErrInvalidSubscription},
	} {
		t.Run(tc.name, func(t *testing.T) {
			basic := &basicPollAPI{}
			expanded := &subscriptionPollAPI{}
			var api c.API = basic
			if tc.extended {
				api = expanded
			}
			r := NewRuntime(NewManager(nil, api, "test-key"), nil, subscriptionHandler{types: tc.types})
			updates, err := r.poll(context.Background(), 1, "dummy", 876)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v want %v", err, tc.want)
			}
			calls := basic.calls + expanded.calls
			if tc.want != nil {
				if calls != 0 {
					t.Fatal("invalid subscription polled")
				}
				return
			}
			if calls != 1 || len(updates) != 1 || updates[0].ID != 876 {
				t.Fatal("offset changed")
			}
			if tc.extended && !reflect.DeepEqual(expanded.types, tc.types) {
				t.Fatal("subscription changed")
			}
		})
	}
	// No provider preserves the historical fixed subscription.
	api := &basicPollAPI{}
	r := NewRuntime(NewManager(nil, api, "test-key"), nil, acceptingPanelHandler{})
	if _, err := r.poll(context.Background(), 1, "dummy", 9); err != nil || api.offset != 9 {
		t.Fatal(err)
	}
}

type routedHandler struct {
	c.Handler
	received []c.Update
	messages []c.Message
}

func (h *routedHandler) AllowedUpdates(context.Context, uint) ([]string, error) {
	return nil, c.ErrInvalidSubscription // already queued events must never consult this
}

func (h *routedHandler) Handle(_ context.Context, _ uint, u c.Update) ([]c.Message, error) {
	h.received = append(h.received, u)
	return h.messages, nil
}

type targetHandler struct {
	*routedHandler
	checks   int
	revokeAt int
}

func (h *targetHandler) AuthorizeTarget(context.Context, uint, c.Message) error {
	h.checks++
	if h.revokeAt > 0 && h.checks >= h.revokeAt {
		return errTarget
	}
	return nil
}
func encryptedEntry(t *testing.T, m *Manager, u c.Update) *d.Inbox {
	t.Helper()
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := m.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &d.Inbox{PayloadCipher: cipher}
}
func TestRuntimeGroupEventsDoNotCreateShopContacts(t *testing.T) {
	for _, u := range []c.Update{
		{Type: "message", ChatID: -100, ChatType: "supergroup", UserID: 12, MessageID: 4, ThreadID: 8, MessageKind: "video"},
		{Type: "channel_post", ChatID: -100, ChatType: "channel", MessageID: 4, SenderChat: &c.ChatReference{ID: -100}},
		{Type: "chat_member", ChatID: -100, ChatType: "supergroup", Member: &c.MemberUpdate{Subject: c.UserReference{ID: 12}}},
		{Type: "chat_join_request", ChatID: -100, ChatType: "supergroup", JoinRequest: &c.JoinRequest{User: c.UserReference{ID: 12}}},
	} {
		// Nil Store methods panic if the runtime attempts any contact/session lookup.
		store := &cleanupStore{}
		m := NewManager(store, cleanupAPI{}, "test-key")
		h := &routedHandler{}
		r := NewRuntime(m, nil, h)
		if err := r.handleEntry(context.Background(), &d.Instance{ID: 1}, "dummy", encryptedEntry(t, m, u)); err != nil {
			t.Fatal(err)
		}
		if len(h.received) != 1 || store.contact != nil || len(store.queued) != 0 {
			t.Fatalf("group not routed safely: %+v", h.received)
		}
	}
}
func TestRuntimeRoutesTargetsWithoutForcingSourceChat(t *testing.T) {
	store := &cleanupStore{}
	m := NewManager(store, cleanupAPI{}, "test-key")
	h := &targetHandler{routedHandler: &routedHandler{messages: []c.Message{{TargetType: c.TargetSupportPrivate, ChatID: 12, BusinessRef: "session:7", Text: "reply"}}}}
	r := NewRuntime(m, nil, h)
	u := c.Update{ID: 8, Type: "message", ChatID: -100, ChatType: "supergroup", UserID: 99, MessageID: 4}
	if err := r.handleEntry(context.Background(), &d.Instance{ID: 1}, "dummy", encryptedEntry(t, m, u)); err != nil {
		t.Fatal(err)
	}
	if len(store.queued) != 1 || store.queued[0].ChatID != 12 || store.queued[0].TargetType != "support_private" || store.queued[0].Priority != 80 || store.contact != nil || h.checks != 1 {
		t.Fatalf("bad routing %+v", store.queued)
	}
	// Queued events do not consult AllowedUpdates: narrowing subscriptions cannot discard them.
}
func TestRuntimeTargetAuthorizationAndLegacyIsolation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		row      string
		msg      c.Message
		chat, tg int64
	}{
		{"missing guard", "support_private", c.Message{TargetType: c.TargetSupportPrivate, ChatID: 12, BusinessRef: "s"}, 12, 12},
		{"row downgrade", "", c.Message{TargetType: c.TargetSupportPrivate, ChatID: 12, BusinessRef: "s"}, 12, 12},
		{"envelope downgrade", "support_private", c.Message{ChatID: 12}, 12, 12},
		{"legacy redirect", "", c.Message{ChatID: 13}, 13, 12},
		{"legacy group", "", c.Message{ChatID: -100}, -100, -100},
		{"unknown kind", "", c.Message{ChatID: 12, TargetKind: "video"}, 12, 12},
		{"group panel", "support_group", c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, BusinessRef: "s", RouteVersion: 1, ThreadID: 2, Purpose: "panel"}, -100, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &deliveryGuardStore{cancel: cancel}
			m := NewManager(store, &apiStub{}, "test-key")
			raw, _ := json.Marshal(tc.msg)
			cipher, _ := m.Seal(string(raw))
			store.item = &d.Delivery{ID: 1, TargetType: tc.row, ChatID: tc.chat, TelegramUserID: tc.tg, PayloadCipher: cipher}
			NewRuntime(m, nil, acceptingPanelHandler{}).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
			if store.status != "failed" {
				t.Fatalf("unsafe target state %q", store.status)
			}
		})
	}
}
func TestSupportEnvelopePurposeAndOperationCannotDiverge(t *testing.T) {
	for _, mutate := range []func(*d.Delivery){
		func(row *d.Delivery) { row.Purpose = "support_reply" },
		func(row *d.Delivery) { row.Operation = "edit" },
	} {
		msg := c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, ThreadID: 7, RouteVersion: 1, BusinessRef: "support:ready:1:2", Purpose: "support_ready", Operation: "send"}
		row := &d.Delivery{TargetType: "support_group", ChatID: -100, Purpose: msg.Purpose, Operation: msg.Operation}
		if _, err := deliveryTarget(row, msg); err != nil {
			t.Fatal(err)
		}
		mutate(row)
		if _, err := deliveryTarget(row, msg); err == nil {
			t.Fatal("envelope accepted divergent durable operation")
		}
	}
}

func TestSupportMediaKindsStayIsolatedFromShopAndMemberOperations(t *testing.T) {
	for _, kind := range []string{"video", "voice", "audio", "animation", "sticker", "album"} {
		for _, target := range []c.TargetType{c.TargetShopPrivate, c.TargetSupportPrivate, c.TargetSupportGroup, c.TargetMemberOperation} {
			t.Run(string(target)+"/"+kind, func(t *testing.T) {
				msg := c.Message{TargetType: target, TargetKind: kind, ChatID: 12, BusinessRef: "support:ready:1:2", RouteVersion: 1}
				if target == c.TargetSupportGroup || target == c.TargetMemberOperation {
					msg.ChatID, msg.ThreadID = -100, 7
				}
				row := &d.Delivery{TargetType: string(target), ChatID: msg.ChatID, TelegramUserID: 12}
				_, err := deliveryTarget(row, msg)
				allowed := target == c.TargetSupportPrivate || target == c.TargetSupportGroup
				if (err == nil) != allowed {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
			})
		}
	}
	msg := c.Message{TargetType: c.TargetSupportGroup, TargetKind: "future_media", ChatID: -100, ThreadID: 7, RouteVersion: 1, BusinessRef: "support:ready:1:2"}
	if _, err := deliveryTarget(&d.Delivery{TargetType: string(c.TargetSupportGroup), ChatID: -100}, msg); err == nil {
		t.Fatal("unknown media kind accepted")
	}
}

func TestRuntimeRechecksNewTargetBeforeSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &deliveryGuardStore{cancel: cancel}
	m := NewManager(store, &apiStub{}, "test-key")
	raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, BusinessRef: "s", RouteVersion: 1, ThreadID: 2})
	cipher, _ := m.Seal(string(raw))
	store.item = &d.Delivery{ID: 1, TargetType: "support_group", ChatID: -100, PayloadCipher: cipher}
	h := &targetHandler{routedHandler: &routedHandler{}, revokeAt: 2}
	NewRuntime(m, nil, h).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if store.status != "failed" || h.checks != 2 {
		t.Fatalf("state=%s checks=%d", store.status, h.checks)
	}
}

type excludingStore struct {
	c.Store
	items    []*d.Delivery
	excluded []int64
}

func (s *excludingStore) ClaimDeliveryExcluding(_ context.Context, _ uint, _ uint64, _ time.Time, excluded []int64) (*d.Delivery, error) {
	s.excluded = append([]int64(nil), excluded...)
	for _, item := range s.items {
		blocked := false
		for _, chat := range excluded {
			if item.ChatID == chat {
				blocked = true
			}
		}
		if !blocked {
			return item, nil
		}
	}
	return nil, nil
}

// TestSupportGroupBudgetSeparatesTransactions checks topic-wide accounting and album weight.
func TestSupportGroupBudgetSeparatesTransactions(t *testing.T) {
	for _, size := range []int{0, 1, 10} {
		message := c.Message{TargetType: c.TargetSupportGroup}
		if size > 0 {
			message.Transfer = &c.TransferRequest{Contents: make([]c.TransferContent, size)}
		}
		if got := deliveryChatCooldown(message); got != time.Duration(max(1, size))*3*time.Second {
			t.Fatal(size, got)
		}
	}
	if got := deliveryChatCooldown(c.Message{TargetType: c.TargetShopPrivate, Purpose: "notification"}); got != time.Second {
		t.Fatal(got)
	}
}

func TestCoolingTargetDoesNotBlockOtherTransactions(t *testing.T) {
	store := &excludingStore{items: []*d.Delivery{{ID: 1, ChatID: -100, Priority: 200}, {ID: 2, ChatID: 12, Priority: 200}}}
	r := NewRuntime(NewManager(store, nil, "test-key"), nil, nil)
	limits := map[int64]time.Time{-100: time.Now().Add(time.Hour), 13: time.Now().Add(-time.Second)}
	item, err := r.claimReadyDelivery(context.Background(), 1, 7, "claim", limits)
	if err != nil || item == nil || item.ID != 2 || !reflect.DeepEqual(store.excluded, []int64{-100}) {
		t.Fatalf("blocked ready transaction: %v %+v %v", err, item, store.excluded)
	}
	if _, ok := limits[13]; ok {
		t.Fatal("expired budget retained")
	}
	// A group is excluded as a chat, so all its threads share the same budget.
}
