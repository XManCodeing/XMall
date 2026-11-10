package application

import (
	"context"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// TestNavigationRestoresListsAndDoesNotReplayPayments exercises real CAS-persisted callbacks.
func TestNavigationRestoresListsAndDoesNotReplayPayments(t *testing.T) {
	service, store, backend := setup()
	messages := run(t, service, update(1, "/start"))
	messages = click(t, service, 2, messages, text("zh-CN", "orders"))
	st := savedState(t, service, store)
	st.Current = action{Kind: "orders", Owner: 7, Page: 3, Value: "pending_payment"}
	st.Filter = "pending_payment"
	st.LegacyInputThrough = 77
	setSavedState(t, service, store, st)
	messages = click(t, service, 3, messages, "ORDER · ")
	messages = click(t, service, 4, messages, text("zh-CN", "go_pay"))
	messages = click(t, service, 5, messages, "Pay")
	if backend.payments != 1 {
		t.Fatal("payment intent not executed")
	}
	run(t, service, update(6, "/back"))
	if savedState(t, service, store).Current.Kind != "channels" {
		t.Fatal("payment did not return to channels")
	}
	run(t, service, update(7, "/back"))
	if savedState(t, service, store).Current.Kind != "order" {
		t.Fatal("channels did not return to order")
	}
	run(t, service, update(8, "/back"))
	st = savedState(t, service, store)
	if st.Current.Kind != "orders" || st.Current.Page != 3 || st.Filter != "pending_payment" || st.LegacyInputThrough != 77 {
		t.Fatalf("list context or legacy fence lost: %+v", st.Current)
	}
	if backend.payments != 1 || backend.creates != 0 {
		t.Fatal("back replayed a mutation")
	}
}

// TestNavigationBoundedLanguageAndHome verifies stack replacement, bound and overlay semantics.
func TestNavigationBoundedLanguageAndHome(t *testing.T) {
	service, _, _ := setup()
	view := &view{service: service, actor: c.Actor{UserID: 7, Locale: "en-US"}, state: &state{Current: action{Kind: "home"}}}
	for index := 0; index < 40; index++ {
		view.messages = nil
		if err := view.navigate(context.Background(), action{Kind: "help", Value: string(rune('a' + index))}); err != nil {
			t.Fatal(err)
		}
	}
	if len(view.state.Pages) != navigationLimit {
		t.Fatal("unbounded navigation")
	}
	before := len(view.state.Pages)
	if err := view.navigate(context.Background(), action{Kind: "language"}); err != nil {
		t.Fatal(err)
	}
	if err := view.navigate(context.Background(), action{Kind: "locale", Value: "zh-TW"}); err != nil {
		t.Fatal(err)
	}
	if len(view.state.Pages) != before {
		t.Fatal("locale pushed navigation")
	}
	if err := view.navigate(context.Background(), action{Kind: "home"}); err != nil {
		t.Fatal(err)
	}
	if len(view.state.Pages) != 0 {
		t.Fatal("home retained history")
	}
}

// TestReadOnlyPagesRejectBusinessActions proves persisted locations cannot execute writes.
func TestReadOnlyPagesRejectBusinessActions(t *testing.T) {
	service, _, backend := setup()
	for _, kind := range []string{"pay", "confirm", "cancel_confirm", "membership_request", "resend_delivery", "resend_code"} {
		view := &view{service: service, actor: c.Actor{UserID: 7, Locale: "en-US"}, state: &state{}}
		if err := view.renderPage(context.Background(), pageState{Kind: kind, ID: 8, Owner: 7}); err != nil {
			t.Fatal(err)
		}
		if view.state.Current.Kind != "home" || backend.payments != 0 || backend.creates != 0 {
			t.Fatal("mutating page restored")
		}
	}
}

// TestNavigationDeniedPageFallsBack verifies stale ownership clears partial output and never replays writes.
func TestNavigationDeniedPageFallsBack(t *testing.T) {
	service, _, backend := setup()
	view := &view{service: service, actor: c.Actor{UserID: 7, Locale: "en-US"}, state: &state{Pages: []pageState{{Kind: "order", ID: 8, Owner: 999}}}}
	if err := view.goBack(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view.state.Current.Kind != "home" || len(view.state.Pages) != 0 || backend.payments != 0 || backend.creates != 0 {
		t.Fatal("denied history did not safely return home")
	}
}

// existingAccessStub records read-only accesses separately from explicit Request calls.
type existingAccessStub struct {
	*membershipAccessStub
	reads int
}

// GetExisting returns the fixture invitation and records reads without creating anything.
func (stub *existingAccessStub) GetExisting(context.Context, c.Actor, uint) (c.MembershipInvitation, error) {
	stub.reads++
	return stub.result, nil
}

// TestMembershipSingleTargetUsesExisting verifies direct entry, refresh and nil expiry safety.
func TestMembershipSingleTargetUsesExisting(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		service, _, _, access := membershipAccessSetup()
		reader := &existingAccessStub{membershipAccessStub: access}
		reader.result.ExpiresAt, reader.result.Permanent = nil, permanent
		service.SetMembershipAccess(reader)
		messages := run(t, service, update(1, "/memberships"))
		if hasMembershipURL(messages, reader.result.InviteURL) != permanent {
			t.Fatal("nil expiry inferred permanence")
		}
		messages = click(t, service, 2, messages, text("zh-CN", "refresh"))
		if reader.requests != 0 || reader.reads != 2 {
			t.Fatal("existing link read triggered request")
		}
		if permanent && messages[0].InviteDelivery == nil {
			t.Fatal("link lost delivery authorization scope")
		}
	}
}
