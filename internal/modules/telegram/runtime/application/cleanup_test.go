package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type cleanupStore struct {
	c.Store
	contact *d.Contact
	session *d.Session
	queued  []d.Delivery
}

func (s *cleanupStore) Contact(context.Context, uint, int64) (*d.Contact, error) {
	return s.contact, nil
}
func (s *cleanupStore) SaveContact(_ context.Context, contact *d.Contact) error {
	s.contact = contact
	return nil
}
func (s *cleanupStore) GetSession(context.Context, uint, int64, int64) (*d.Session, error) {
	return s.session, nil
}
func (s *cleanupStore) Enqueue(_ context.Context, delivery *d.Delivery) error {
	s.queued = append(s.queued, *delivery)
	return nil
}

type cleanupHandler struct{ target int64 }

func (h cleanupHandler) Handle(context.Context, uint, c.Update) ([]c.Message, error) {
	return []c.Message{{Purpose: "cleanup", Operation: "delete", TargetMessageID: h.target, ChatID: 999, OwnerUserID: 7}}, nil
}
func (cleanupHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	return nil
}

func TestCleanupOnlyTargetsSourceOrInactiveClickedPanel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		callback bool
		active   int64
		target   int64
		want     int
	}{
		{"input source", false, 80, 42, 1},
		{"input other message", false, 80, 43, 0},
		{"stale clicked panel", true, 80, 42, 0},
		{"active clicked panel", true, 42, 42, 0},
		{"unknown active panel", true, 0, 42, 0},
		{"callback other message", true, 80, 43, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &cleanupStore{session: &d.Session{PanelMessageID: tc.active}}
			manager := NewManager(store, &apiStub{}, "test-key")
			runtime := NewRuntime(manager, nil, cleanupHandler{target: tc.target})
			u := c.Update{ID: 1, UserID: 11, ChatID: 11, Private: true, MessageID: 42}
			if tc.callback {
				// No responder is needed: CallbackID triggers a harmless empty answer
				// through this test API, while the update still identifies the source.
				u.CallbackID = "callback"
				manager.API = cleanupAPI{}
			}
			raw, err := json.Marshal(u)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.handleEntry(context.Background(), &d.Instance{ID: 1}, "token", &d.Inbox{PayloadCipher: cipher}); err != nil {
				t.Fatal(err)
			}
			if len(store.queued) != tc.want {
				t.Fatalf("queued %d cleanup actions, want %d", len(store.queued), tc.want)
			}
			if tc.want == 1 {
				previous := int64(0)
				if tc.callback {
					previous = 42
				}
				if store.queued[0].PreviousMessageID != previous {
					t.Fatal("only stale panels may fall back to clearing buttons")
				}
			}
			if tc.want == 1 && (store.queued[0].ChatID != 11 || store.queued[0].TargetMessageID != 42) {
				t.Fatal("cleanup escaped authenticated source chat/message")
			}
		})
	}
}

type cleanupAPI struct{ c.API }

func (cleanupAPI) Answer(context.Context, string, string, string) error { return nil }

// publicHandler grants only the exact public fixture, never owner-zero private messages.
type publicHandler struct {
	message c.Message
	checks  int
}

func (h *publicHandler) Handle(context.Context, uint, c.Update) ([]c.Message, error) {
	return []c.Message{h.message}, nil
}
func (h *publicHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error { return errTarget }
func (h *publicHandler) AuthorizePublicReply(_ context.Context, bot uint, message c.Message) error {
	h.checks++
	if bot != 1 || message.PublicReply == nil || message.ChatID != 11 || message.Text != "public" {
		return errTarget
	}
	return nil
}

// TestPublicEnqueueNeverInheritsOldContact excludes stale contact ownership from guest delivery.
func TestPublicEnqueueNeverInheritsOldContact(t *testing.T) {
	store := &cleanupStore{contact: &d.Contact{BotID: 1, TelegramUserID: 11, ChatID: 11, UserID: 99}}
	manager := NewManager(store, &apiStub{}, "test")
	handler := &publicHandler{message: c.Message{PublicReply: &c.PublicReplyRef{BotID: 1, TelegramID: 11, Generation: "session", ExpiresAt: time.Now().Add(time.Minute)}, TargetType: c.TargetShopPrivate, Purpose: "public_notice", ChatID: 11, Text: "public"}}
	runtime := NewRuntime(manager, nil, handler)
	raw, err := json.Marshal(c.Update{ID: 1, UserID: 11, ChatID: 11, Private: true, Text: "/start"})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := manager.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.handleEntry(context.Background(), &d.Instance{ID: 1}, "", &d.Inbox{PayloadCipher: cipher}); err != nil {
		t.Fatal(err)
	}
	if len(store.queued) != 1 || store.queued[0].UserID != 0 || handler.checks != 1 {
		t.Fatal(store.queued, handler.checks)
	}
	handler.message.PublicReply = nil
	if err := runtime.handleEntry(context.Background(), &d.Instance{ID: 1}, "", &d.Inbox{PayloadCipher: cipher}); err == nil {
		t.Fatal("owner zero accepted without capability")
	}
}

// TestPublicEnvelopeRejectsCrossRecipient rejects mismatched rows before invoking the guard.
func TestPublicEnvelopeRejectsCrossRecipient(t *testing.T) {
	handler := &publicHandler{}
	runtime := NewRuntime(nil, nil, handler)
	message := c.Message{PublicReply: &c.PublicReplyRef{BotID: 1, TelegramID: 11}, TargetType: c.TargetShopPrivate, Purpose: "public_notice", ChatID: 11, Text: "public"}
	for _, row := range []d.Delivery{{ChatID: 12, TelegramUserID: 12}, {ChatID: 11, TelegramUserID: 11, UserID: 7}} {
		if runtime.authorizeMessage(context.Background(), 1, &row, message) == nil {
			t.Fatal("cross recipient authorized")
		}
	}
	if handler.checks != 0 {
		t.Fatal("malformed row reached public guard")
	}
}
