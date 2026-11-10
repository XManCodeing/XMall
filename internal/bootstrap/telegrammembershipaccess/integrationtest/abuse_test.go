package abuse_test

import (
	"context"
	"encoding/json"
	"errors"
	bridge "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess"
	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

// fixture supplies immutable evidence and fake queue without platform access.
type fixture struct {
	r.Store
	row      m.JoinAbuse
	contact  d.Contact
	receipt  r.InviteDeliveryStatus
	inserted []*d.Delivery
	invalid  bool
}

// JoinAbuse revalidates the original binding.
func (f *fixture) JoinAbuse(context.Context, uint, time.Time) (*m.JoinAbuse, error) {
	if f.invalid {
		return nil, m.ErrUntrustedIdentity
	}
	return &f.row, nil
}

// JoinAbuses returns one bounded evidence page.
func (f *fixture) JoinAbuses(_ context.Context, _ uint, after uint, _ int, _ time.Time) ([]m.JoinAbuse, error) {
	if after >= f.row.ID {
		return nil, nil
	}
	return []m.JoinAbuse{f.row}, nil
}

// Contact returns the known original private contact.
func (f *fixture) Contact(context.Context, uint, int64) (*d.Contact, error) { return &f.contact, nil }

// BusinessDelivery supplies exact prior delivery state.
func (f *fixture) BusinessDelivery(context.Context, uint, string) (r.InviteDeliveryStatus, error) {
	return f.receipt, nil
}

// Enqueue captures messages and simulates unique-key insertion.
func (f *fixture) Enqueue(_ context.Context, delivery *d.Delivery) error {
	f.inserted = append(f.inserted, delivery)
	f.receipt.State = "queued"
	return nil
}

// MembershipRoute returns the current physical execution scope.
func (f *fixture) MembershipRoute(context.Context, uint) (m.AccessRoute, error) {
	return m.AccessRoute{TargetID: f.row.TargetID, BotID: f.row.BotID, ChatID: f.row.ChatID, RouteVersion: f.row.RouteVersion, Generation: f.row.Generation}, nil
}

// Check provides a test-only execution fence.
func (f *fixture) Check(context.Context, uint, uint64) error { return nil }

// cipher keeps test envelopes inspectable without production secrets.
type cipher struct{}

// Seal returns the test payload for inspection.
func (cipher) Seal(value string) (string, error) { return value, nil }

// Open returns the test plaintext.
func (cipher) Open(value string) (string, error) { return value, nil }

// TestIndependentAuthorization covers original-owner checks and unknown-send suppression.
func TestIndependentAuthorization(t *testing.T) {
	now := time.Now().UTC()
	f := &fixture{row: m.JoinAbuse{ID: 1, InviteID: 2, AccountID: 7, BotID: 9, TargetID: 3, TelegramID: 100, ChatID: -1001, BindingVersion: 1, RouteVersion: 1, Generation: 3, Window: now}, contact: d.Contact{BotID: 9, TelegramUserID: 100, ChatID: 100, UserID: 7, Reachable: true}, receipt: r.InviteDeliveryStatus{State: "not_requested"}}
	service := &bridge.JoinAbuseNotifications{Store: f, Queue: f, Routes: f, Cipher: cipher{}, Now: func() time.Time { return now }}
	ctx := r.WithExecutionFence(context.Background(), f)
	if err := service.Reconcile(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if len(f.inserted) != 1 {
		t.Fatal("missing independent message")
	}
	var message r.Message
	if err := json.Unmarshal([]byte(f.inserted[0].PayloadCipher), &message); err != nil {
		t.Fatal(err)
	}
	if message.ChatID != 100 || message.PanelEpoch != "" || message.Operation != "send" || len(message.Buttons) != 0 {
		t.Fatal("wrong recipient or panel")
	}
	if err := service.AuthorizeJoinAbuseDelivery(ctx, 9, message); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"applicant", "panel", "link", "generation", "contact", "binding"} {
		candidate := message
		f.contact.UserID, f.invalid = 7, false
		switch scenario {
		case "applicant":
			candidate.ChatID = 88
		case "panel":
			candidate.PanelEpoch = "shopping-panel"
		case "link":
			candidate.Buttons = [][]r.Button{{{URL: "https://t.me/+secret"}}}
		case "generation":
			reference := *candidate.JoinAbuseDelivery
			reference.Generation++
			candidate.JoinAbuseDelivery = &reference
		case "contact":
			f.contact.UserID = 8
		case "binding":
			f.invalid = true
		}
		var invalid r.DeliveryInvalidated
		if err := service.AuthorizeJoinAbuseDelivery(ctx, 9, candidate); !errors.As(err, &invalid) {
			t.Fatalf("accepted %s: %v", scenario, err)
		}
	}
	f.invalid, f.contact.UserID = false, 7
	f.receipt.State = "unknown"
	f.row.ID++
	if err := service.Reconcile(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if len(f.inserted) != 1 {
		t.Fatal("unknown replayed under another key")
	}
}
