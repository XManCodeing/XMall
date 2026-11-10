package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	shopcontract "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// guestDeliveryAPI supplies callback acknowledgements without contacting Telegram.
type guestDeliveryAPI struct{ reminderAPI }

// Apply simulates uncertain delivery when configured; no real Telegram call occurs.
func (api *guestDeliveryAPI) Apply(ctx context.Context, token string, message runtimecontract.Message) (runtimecontract.DeliveryResult, error) {
	if api.failure != nil {
		api.calls.Add(1)
		return runtimecontract.DeliveryResult{}, api.failure
	}
	return api.reminderAPI.Apply(ctx, token, message)
}

// Answer accepts only the test's callback acknowledgement and returns no error.
func (*guestDeliveryAPI) Answer(context.Context, string, string, string) error { return nil }

// unboundIdentity rejects provisioning: public entry must only resolve identity.
// The embedded port makes an accidental private operation fail the integration test.
type unboundIdentity struct {
	shopcontract.Identity
	resolveError error
}

// Resolve returns an unbound account for reads and a terminal restriction for writes.
// actor and ctx are unused because this fixture never accesses an account store.
func (identity unboundIdentity) Resolve(_ context.Context, _ shopcontract.Actor, provision bool) (uint, error) {
	if identity.resolveError != nil {
		return 0, identity.resolveError
	}
	if provision {
		return 0, shopcontract.ErrRegistrationDisabled
	}
	return 0, nil
}

// TestGuestStartTraversesRealOutbox verifies a real encrypted session, dispatcher,
// single-connection store, public authorization and panel delivery without an account.
func TestGuestStartTraversesRealOutbox(t *testing.T) {
	api := &guestDeliveryAPI{reminderAPI: reminderAPI{message: 321}}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: unboundIdentity{}, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	fixture.accept(t, runtimecontract.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	var inbox runtimedomain.Inbox
	must(t, fixture.db.First(&inbox, "update_id = ?", 1).Error)
	if inbox.Status != "done" || inbox.Attempts != 1 {
		t.Fatalf("guest start was retried: state=%s attempts=%d error=%s", inbox.Status, inbox.Attempts, inbox.LastError)
	}
	var delivery runtimedomain.Delivery
	must(t, fixture.db.First(&delivery, "key = ?", "update:1:0").Error)
	if delivery.Status != "sent" || delivery.UserID != 0 || api.calls.Load() != 1 {
		t.Fatalf("public delivery failed: state=%s owner=%d calls=%d error=%s", delivery.Status, delivery.UserID, api.calls.Load(), delivery.LastError)
	}
}

// TestGuestUnavailableSupportIsExplicit verifies that ordinary text is not
// silently treated as accepted consultation when production readiness is closed.
func TestGuestUnavailableSupportIsExplicit(t *testing.T) {
	api := &guestDeliveryAPI{reminderAPI: reminderAPI{message: 321}}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: unboundIdentity{}, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	fixture.accept(t, runtimecontract.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "商品咨询", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	var inbox runtimedomain.Inbox
	must(t, fixture.db.First(&inbox, "update_id = ?", 1).Error)
	var delivery runtimedomain.Delivery
	must(t, fixture.db.First(&delivery, "key = ?", "update:1:0").Error)
	plain, err := fixture.manager.Open(delivery.PayloadCipher)
	must(t, err)
	var message runtimecontract.Message
	must(t, json.Unmarshal([]byte(plain), &message))
	if inbox.Status != "done" || delivery.Status != "sent" || !strings.Contains(message.Text, "客服暂不可用") || strings.Contains(message.Text, "商品咨询") {
		t.Fatalf("unavailable support not explicit: inbox=%s delivery=%s", inbox.Status, delivery.Status)
	}
}

// TestDisabledAccountReceivesOnlyPublicRestriction confirms terminal suspension
// is acknowledged without creating or granting access to a replacement identity.
func TestDisabledAccountReceivesOnlyPublicRestriction(t *testing.T) {
	for _, failure := range []error{shopcontract.ErrAccountDisabled, shopcontract.ErrDenied} {
		t.Run(failure.Error(), func(t *testing.T) {
			testRestrictedIdentityDelivery(t, failure)
		})
	}
}

// testRestrictedIdentityDelivery exercises one terminal identity failure through
// real persistence and sending; failure is never converted into a guest account.
func testRestrictedIdentityDelivery(t *testing.T, failure error) {
	t.Helper()
	api := &guestDeliveryAPI{reminderAPI: reminderAPI{message: 321}}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	identity := unboundIdentity{resolveError: failure}
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: identity, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	fixture.accept(t, runtimecontract.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	var inbox runtimedomain.Inbox
	must(t, fixture.db.First(&inbox, "update_id = ?", 1).Error)
	var delivery runtimedomain.Delivery
	must(t, fixture.db.First(&delivery, "key = ?", "update:1:0").Error)
	plain, err := fixture.manager.Open(delivery.PayloadCipher)
	must(t, err)
	var message runtimecontract.Message
	must(t, json.Unmarshal([]byte(plain), &message))
	if inbox.Status != "done" || inbox.Attempts != 1 || delivery.Status != "sent" || message.OwnerUserID != 0 || len(message.Buttons) != 0 {
		t.Fatalf("disabled account escaped restriction: inbox=%s attempts=%d delivery=%s owner=%d buttons=%d", inbox.Status, inbox.Attempts, delivery.Status, message.OwnerUserID, len(message.Buttons))
	}
}

// TestGuestUnknownSendIsNotAutomaticallyReplayed proves an uncertain public panel
// stays unknown across worker restarts and does not send a duplicate message.
func TestGuestUnknownSendIsNotAutomaticallyReplayed(t *testing.T) {
	api := &guestDeliveryAPI{reminderAPI: reminderAPI{failure: errors.New("synthetic connection lost")}}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: unboundIdentity{}, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	fixture.accept(t, runtimecontract.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	fixture.run(t, 300*time.Millisecond, nil)
	var delivery runtimedomain.Delivery
	must(t, fixture.db.First(&delivery, "key = ?", "update:1:0").Error)
	if delivery.Status != "unknown" || api.calls.Load() != 1 {
		t.Fatalf("uncertain public panel replayed: state=%s calls=%d", delivery.Status, api.calls.Load())
	}
}

// TestGuestWalletRestrictionTraversesRealOutbox verifies that a denied explicit
// wallet intent is delivered once instead of retrying the inbox eight times.
func TestGuestWalletRestrictionTraversesRealOutbox(t *testing.T) {
	api := &guestDeliveryAPI{reminderAPI: reminderAPI{message: 321}}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: unboundIdentity{}, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	fixture.accept(t, runtimecontract.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	var first runtimedomain.Delivery
	must(t, fixture.db.First(&first, "key = ?", "update:1:0").Error)
	plain, err := fixture.manager.Open(first.PayloadCipher)
	must(t, err)
	var panel runtimecontract.Message
	must(t, json.Unmarshal([]byte(plain), &panel))
	var walletCallback string
	for _, row := range panel.Buttons {
		for _, button := range row {
			if strings.Contains(button.Text, "余额") {
				walletCallback = button.Data
			}
		}
	}
	if walletCallback == "" {
		t.Fatal("guest home has no explicit wallet entry")
	}
	fixture.accept(t, runtimecontract.Update{ID: 2, UserID: 12, ChatID: 12, Private: true, CallbackID: "wallet", CallbackData: walletCallback, MessageID: 321, MessageKind: "text", Locale: "zh-CN"})
	fixture.run(t, time.Second, nil)
	var inbox runtimedomain.Inbox
	must(t, fixture.db.First(&inbox, "update_id = ?", 2).Error)
	if inbox.Status != "done" || inbox.Attempts != 1 {
		t.Fatalf("terminal policy retried: state=%s attempts=%d error=%s", inbox.Status, inbox.Attempts, inbox.LastError)
	}
	var response runtimedomain.Delivery
	must(t, fixture.db.First(&response, "key = ?", "update:2:0").Error)
	if response.Status != "sent" || response.UserID != 0 || api.calls.Load() != 2 {
		t.Fatalf("policy response not delivered: state=%s owner=%d calls=%d error=%s", response.Status, response.UserID, api.calls.Load(), response.LastError)
	}
}
