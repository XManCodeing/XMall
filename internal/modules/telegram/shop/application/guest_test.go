package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type stampedIdentity struct {
	c.Identity
	stamp string
	uid   uint
}

func (s *stampedIdentity) Resolve(context.Context, c.Actor, bool) (uint, error) { return s.uid, nil }
func (s *stampedIdentity) Generation(context.Context, c.Actor) (string, error)  { return s.stamp, nil }
func TestQueuedSensitiveReplyCannotReviveAfterRebinding(t *testing.T) {
	identity := &stampedIdentity{stamp: "10:7", uid: 7}
	service := New(Dependencies{Identity: identity})
	if err := service.AuthorizeStamp(context.Background(), 1, 12, "10:7"); err != nil {
		t.Fatal(err)
	}
	identity.stamp = "11:7"
	if err := service.AuthorizeStamp(context.Background(), 1, 12, "10:7"); !errors.Is(err, c.ErrDenied) {
		t.Fatal("old binding sensitive reply revived")
	}
	if err := service.AuthorizeStamp(context.Background(), 1, 12, ""); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unstamped legacy message accepted")
	}
}

// TestUnboundLegacyInputProducesAuthorizedNotice keeps retired verification input out of support.
func TestUnboundLegacyInputProducesAuthorizedNotice(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		service, store, backend := guestSetup()
		if disabled {
			backend.failure = c.ErrAccountDisabled
		}
		putRoutingState(t, service, store, state{UserID: 7, Step: "code", Email: "old@example.test"}, time.Now().Add(-time.Hour))
		messages, handled, err := service.ConsumeLegacyInput(context.Background(), 1, update(10, "secret-verification"))
		if err != nil || !handled || len(messages) != 1 {
			t.Fatal(handled, err, messages)
		}
		if strings.Contains(messages[0].Text, "secret-verification") || backend.provisions != 0 {
			t.Fatal("legacy secret leaked or account provisioned")
		}
		if err := service.AuthorizePublicReply(context.Background(), 1, messages[0]); err != nil {
			t.Fatal(err)
		}
		_, handled, err = service.ConsumeLegacyInput(context.Background(), 1, update(10, "secret-verification"))
		if !handled || err != nil {
			t.Fatal("replayed secret reached support", err)
		}
	}
}

// guestBackend counts every identity creation and quote request independently.
type guestBackend struct {
	*backend
	provisions, previews int
	failure              error
}

// Resolve only provisions on explicit intent and returns terminal test policies unchanged.
func (b *guestBackend) Resolve(_ context.Context, _ c.Actor, create bool) (uint, error) {
	if errors.Is(b.failure, c.ErrAccountDisabled) {
		return 0, b.failure
	}
	if create {
		b.provisions++
		if b.failure != nil {
			return 0, b.failure
		}
		b.user = 7
	}
	return b.user, nil
}

// Preview records whether public browsing accidentally reached private pricing.
func (b *guestBackend) Preview(ctx context.Context, actor c.Actor, selection c.Selection) (*c.Quote, error) {
	b.previews++
	if actor.UserID == 0 {
		return nil, c.ErrDenied
	}
	return b.backend.Preview(ctx, actor, selection)
}

// guestSetup reuses the existing catalog, checkout and session fixtures.
func guestSetup() (*Service, *memoryStore, *guestBackend) {
	service, store, base := setup()
	base.user = 0
	backend := &guestBackend{backend: base}
	service.d.Identity, service.d.Orders = backend, backend
	return service, store, backend
}

// TestGuestBrowseThenExplicitCheckout exercises public replies and fresh private quotation.
func TestGuestBrowseThenExplicitCheckout(t *testing.T) {
	service, _, backend := guestSetup()
	messages := run(t, service, update(1, "/start"))
	if err := service.AuthorizePublicReply(context.Background(), 1, messages[0]); err != nil {
		t.Fatal(err)
	}
	messages = click(t, service, 2, messages, text("zh-CN", "catalog"))
	if backend.provisions != 0 || backend.previews != 0 {
		t.Fatal("browsing created account or private quote")
	}
	old := messages[0]
	messages = click(t, service, 3, messages, text("zh-CN", "continue_checkout"))
	if backend.provisions != 1 || backend.previews != 1 || backend.creates != 0 {
		t.Fatalf("provisions=%d previews=%d orders=%d", backend.provisions, backend.previews, backend.creates)
	}
	if messages[0].OwnerUserID != 7 || messages[0].PublicReply != nil {
		t.Fatal("upgrade retained public authority")
	}
	if err := service.AuthorizePublicReply(context.Background(), 1, old); err == nil {
		t.Fatal("old public capability survived upgrade")
	}
	click(t, service, 4, messages, text("zh-CN", "confirm"))
	if backend.creates != 1 {
		t.Fatal("confirmed checkout did not create order")
	}
}

// TestPublicReplyRejectsTampering covers recipient, content, purpose, generation and expiry.
func TestPublicReplyRejectsTampering(t *testing.T) {
	service, _, _ := guestSetup()
	message := run(t, service, update(1, "/start"))[0]
	for name, mutate := range map[string]func(*rt.Message){
		"content":   func(m *rt.Message) { m.Text = "private secret" },
		"recipient": func(m *rt.Message) { m.ChatID = 12 },
		"owner":     func(m *rt.Message) { m.OwnerUserID = 7 },
		"purpose":   func(m *rt.Message) { m.Purpose = "delivery" },
		"target":    func(m *rt.Message) { m.TargetType = rt.TargetSupportPrivate },
		"stamp":     func(m *rt.Message) { m.IdentityStamp = "old" },
		"epoch":     func(m *rt.Message) { m.PanelEpoch = "old" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := message
			mutate(&changed)
			if service.AuthorizePublicReply(context.Background(), 1, changed) == nil {
				t.Fatal("tampering authorized")
			}
		})
	}
	if service.AuthorizePublicReply(context.Background(), 2, message) == nil {
		t.Fatal("cross bot authorized")
	}
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	if service.AuthorizePublicReply(context.Background(), 1, message) == nil {
		t.Fatal("expired capability authorized")
	}
}

// TestGuestPolicyDenialsAreDeliverable confirms clear terminal replies without inbox retries.
func TestGuestPolicyDenialsAreDeliverable(t *testing.T) {
	for _, failure := range []error{c.ErrRegistrationDisabled, c.ErrInviteRequired, c.ErrAccountDisabled} {
		service, _, backend := guestSetup()
		backend.failure = failure
		messages := run(t, service, update(1, "/start"))
		if failure != c.ErrAccountDisabled {
			messages = click(t, service, 2, messages, text("zh-CN", "account"))
		}
		if err := service.AuthorizePublicReply(context.Background(), 1, messages[0]); err != nil {
			t.Fatal(failure, err)
		}
		if backend.user != 0 {
			t.Fatal("denied account created")
		}
	}
}

// TestGuestStaleButtonsNeverProvision exercises expiry, replay, read-only queries and zero owner.
func TestGuestStaleButtonsNeverProvision(t *testing.T) {
	service, _, backend := guestSetup()
	messages := run(t, service, update(1, "/start"))
	old := update(3, "")
	old.CallbackData = button(t, messages, text("zh-CN", "account"))
	run(t, service, update(2, "/start"))
	run(t, service, old)
	run(t, service, update(4, "/memberships"))
	if backend.provisions != 0 {
		t.Fatal("stale/read-only input provisioned")
	}
	if (&view{actor: c.Actor{}, service: service}).allowed(context.Background(), 0) == nil {
		t.Fatal("zero owner accepted")
	}
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	old.ID = 5
	run(t, service, old)
	if backend.provisions != 0 {
		t.Fatal("expired button provisioned")
	}
}
