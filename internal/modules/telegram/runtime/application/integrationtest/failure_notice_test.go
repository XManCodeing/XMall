package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// failingNoticeIdentity counts account resolution, returning only a synthetic
// dependency error. Provisioning is never a valid fallback for this failure.
type failingNoticeIdentity struct {
	shop.Identity
	calls atomic.Int32
}

// Resolve ignores actor and returns a dependency outage for either read mode.
func (identity *failingNoticeIdentity) Resolve(context.Context, shop.Actor, bool) (uint, error) {
	identity.calls.Add(1)
	return 0, errors.New("synthetic database unavailable; private body must not leak")
}

// failureFixture builds real encrypted storage and shop dispatch with a failing
// identity dependency; the returned API never contacts Telegram.
func failureFixture(t *testing.T) (*fixture, *reminderAPI, *failingNoticeIdentity) {
	t.Helper()
	api := &reminderAPI{message: 321}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	identity := &failingNoticeIdentity{}
	service := shopapp.New(shopapp.Dependencies{Store: fixture.store, Identity: identity, Secret: "test-key"})
	fixture.dispatcher = bootstrap.NewUpdateDispatcher(service, service, bootstrap.UpdateModules{})
	return fixture, api, identity
}

// readFailureNotice returns the persisted delivery and decrypted envelope for
// the fixture's first update; decoding errors terminate the calling test.
func readFailureNotice(t *testing.T, fixture *fixture) (d.Delivery, c.Message) {
	t.Helper()
	var delivery d.Delivery
	must(t, fixture.db.First(&delivery, "key = ?", "failure:1").Error)
	plain, err := fixture.manager.Open(delivery.PayloadCipher)
	must(t, err)
	var message c.Message
	must(t, json.Unmarshal([]byte(plain), &message))
	return delivery, message
}

// TestFailureNoticeSurvivesIdentityOutage proves three fixed localizations,
// bounded business retry, unchanged sessions and delivery without identity reads.
func TestFailureNoticeSurvivesIdentityOutage(t *testing.T) {
	for locale, text := range map[string]string{
		"zh-CN": "服务暂不可用，请稍后重试。",
		"zh-TW": "服務暫不可用，請稍後重試。",
		"en-US": "Service temporarily unavailable. Please try again later.",
	} {
		t.Run(locale, func(t *testing.T) {
			fixture, api, identity := failureFixture(t)
			session := d.Session{BotID: fixture.bot.ID, TelegramUserID: 12, ChatID: 12, Version: 3, State: "preserved", DataCipher: "unchanged", PanelEpoch: "original", PanelRevision: 9}
			must(t, fixture.db.Create(&session).Error)
			// Compare two database-decoded values, not Create's in-memory timestamps
			// (which may retain a local location or monotonic clock metadata).
			var baseline d.Session
			must(t, fixture.db.First(&baseline, session.ID).Error)
			fixture.accept(t, c.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start secret-order", Locale: locale})
			fixture.run(t, 700*time.Millisecond, nil)
			var inbox d.Inbox
			must(t, fixture.db.First(&inbox, "update_id = ?", 1).Error)
			delivery, message := readFailureNotice(t, fixture)
			if inbox.Status != "retry" || inbox.Attempts != 1 || delivery.Status != "sent" || api.calls.Load() != 1 || identity.calls.Load() != 1 {
				t.Fatalf("outage lost retry or bypassed identity: inbox=%s attempts=%d delivery=%s sends=%d resolves=%d", inbox.Status, inbox.Attempts, delivery.Status, api.calls.Load(), identity.calls.Load())
			}
			want := c.Message{TargetType: c.TargetShopPrivate, BusinessRef: message.BusinessRef, Purpose: "runtime_failure", Operation: "send", IdentityStamp: message.IdentityStamp, ChatID: 12, Text: text}
			if delivery.UserID != 0 || !reflect.DeepEqual(message, want) || !strings.HasPrefix(message.IdentityStamp, "failure-v1:") {
				t.Fatal("notice was not a fixed, signed, content-free envelope")
			}
			var after d.Session
			must(t, fixture.db.First(&after, session.ID).Error)
			if !reflect.DeepEqual(baseline, after) {
				t.Fatal("failure notice overwrote the existing session")
			}
		})
	}
}

// TestFailureNoticeDeduplicatesEightRetries keeps the original eight-attempt
// business limit and proves worker restarts do not replay failed history/notices.
func TestFailureNoticeDeduplicatesEightRetries(t *testing.T) {
	fixture, api, identity := failureFixture(t)
	fixture.accept(t, c.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start"})
	for attempt := 1; attempt <= 8; attempt++ {
		must(t, fixture.db.Model(&d.Inbox{}).Where("update_id = ?", 1).Update("available_at", time.Now().UTC().Add(-time.Second)).Error)
		fixture.run(t, 400*time.Millisecond, nil)
		var inbox d.Inbox
		must(t, fixture.db.First(&inbox, "update_id = ?", 1).Error)
		want := "retry"
		if attempt == 8 {
			want = "failed"
		}
		if inbox.Attempts != attempt || inbox.Status != want {
			t.Fatalf("attempt %d: attempts=%d state=%s", attempt, inbox.Attempts, inbox.Status)
		}
	}
	fixture.run(t, 200*time.Millisecond, nil)
	var count int64
	must(t, fixture.db.Model(&d.Delivery{}).Count(&count).Error)
	if count != 1 || api.calls.Load() != 1 || identity.calls.Load() != 8 {
		t.Fatalf("failure replayed or became guest: deliveries=%d sends=%d resolves=%d", count, api.calls.Load(), identity.calls.Load())
	}
}

// noticeHandler injects processing errors or a forged handler reply; identity
// authorization always fails and counts calls to prove no generic owner bypass.
type noticeHandler struct {
	messages []c.Message
	failure  error
	checks   atomic.Int32
}

// Handle returns the configured synthetic result for any accepted update.
func (handler *noticeHandler) Handle(context.Context, uint, c.Update) ([]c.Message, error) {
	return handler.messages, handler.failure
}

// AuthorizeDelivery denies every ordinary reply and records attempted checks.
func (handler *noticeHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	handler.checks.Add(1)
	return errors.New("synthetic identity unavailable")
}

// runNoticeHandler starts real workers with handler and fixture storage for a
// short bounded period, joining all workers before returning to the caller.
func runNoticeHandler(t *testing.T, fixture *fixture, handler c.Handler) {
	t.Helper()
	runtime := app.NewRuntime(fixture.manager, leaser{lease{}}, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	must(t, runtime.Start(ctx))
	var err error
	fixture.bot, err = fixture.store.Get(context.Background(), fixture.bot.ID)
	must(t, err)
}

// TestFailureNoticeRequiresAuthenticPrivateInbox rejects group, malformed,
// mismatched encrypted envelopes and already-failed historical inbox entries.
func TestFailureNoticeRequiresAuthenticPrivateInbox(t *testing.T) {
	for _, scenario := range []string{"group", "sender_chat", "chat_mismatch", "update_mismatch", "corrupt_cipher", "historical_failed"} {
		t.Run(scenario, func(t *testing.T) {
			api := &reminderAPI{message: 321}
			fixture := setup(t, api, bootstrap.UpdateModules{})
			update := c.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start"}
			switch scenario {
			case "group":
				update.Private, update.ChatID, update.ChatType = false, -12, "group"
			case "sender_chat":
				update.SenderChat = &c.ChatReference{ID: -12}
			case "chat_mismatch":
				update.ChatID = 13
			}
			fixture.accept(t, update)
			switch scenario {
			case "update_mismatch":
				must(t, fixture.db.Model(&d.Inbox{}).Where("update_id = ?", 1).Update("update_id", 2).Error)
			case "corrupt_cipher":
				must(t, fixture.db.Model(&d.Inbox{}).Where("update_id = ?", 1).Update("payload_cipher", "invalid").Error)
			case "historical_failed":
				must(t, fixture.db.Model(&d.Inbox{}).Where("update_id = ?", 1).Updates(map[string]any{"status": "failed", "attempts": 8}).Error)
			}
			runNoticeHandler(t, fixture, &noticeHandler{failure: errors.New("synthetic dependency outage")})
			var count int64
			must(t, fixture.db.Model(&d.Delivery{}).Count(&count).Error)
			if count != 0 || api.calls.Load() != 0 {
				t.Fatal("unverified private inbox produced a notice")
			}
		})
	}
}

// TestFailureNoticeRejectsTampering reuses an authentic persisted capability but
// mutates each security boundary before real pre-send authorization executes.
func TestFailureNoticeRejectsTampering(t *testing.T) {
	for _, scenario := range []string{"text", "buttons", "owner", "recipient", "bot", "expiry", "signature", "target", "purpose", "key", "media", "business_ref"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, api, _ := failureFixture(t)
			fixture.accept(t, c.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start"})
			fixture.run(t, 400*time.Millisecond, nil)
			delivery, message := readFailureNotice(t, fixture)
			if delivery.Status != "sent" || api.calls.Load() != 1 {
				t.Fatal("authentic notice was not sent before tamper test")
			}
			// Production queue timestamps are UTC. A local +08:00 timestamp is
			// lexically later than the UTC claim cutoff in SQLite, leaving a row
			// pending even though its instant is already due. Clear all receipt
			// and lease state as well: this fixture models a fresh unsent attempt.
			changes := map[string]any{
				"status": "pending", "available_at": time.Now().UTC().Add(-time.Second),
				"claimed_until": nil, "claim": "", "receipt_claim": "", "attempts": 0,
				"message_id": 0, "message_ids": nil, "message_kind": "", "last_error": "",
			}
			switch scenario {
			case "text":
				message.Text = "sensitive order body"
			case "buttons":
				message.Buttons = [][]c.Button{{{Text: "buy", Data: "secret"}}}
			case "owner":
				message.OwnerUserID, changes["user_id"] = 7, uint(7)
			case "recipient":
				message.ChatID, changes["chat_id"], changes["telegram_user_id"] = 13, int64(13), int64(13)
			case "bot":
				otherToken, err := fixture.manager.Seal("456:test")
				must(t, err)
				other := d.Instance{TelegramID: 456, TokenCipher: otherToken, Enabled: true, Mode: "internal", Version: 1}
				must(t, fixture.db.Create(&other).Error)
				changes["bot_id"] = other.ID
			case "expiry":
				message.BusinessRef = "failure:1:1"
			case "signature":
				message.IdentityStamp = "failure-v1:forged"
			case "target":
				message.TargetType, changes["target_type"] = c.TargetSupportPrivate, string(c.TargetSupportPrivate)
			case "purpose":
				message.Purpose, changes["purpose"] = "notice", "notice"
			case "key":
				changes["key"] = "failure:2"
			case "media":
				message.PhotoURL = "https://example.invalid/secret"
			case "business_ref":
				message.BusinessRef = strings.Replace(message.BusinessRef, "failure:1:", "failure:2:", 1)
				changes["key"] = "failure:2"
			}
			raw, err := json.Marshal(message)
			must(t, err)
			changes["payload_cipher"], err = fixture.manager.Seal(string(raw))
			must(t, err)
			must(t, fixture.db.Model(&d.Delivery{}).Where("id = ?", delivery.ID).Updates(changes).Error)
			fixture.run(t, 400*time.Millisecond, nil)
			var after d.Delivery
			must(t, fixture.db.First(&after, delivery.ID).Error)
			if after.Status != "failed" || after.Attempts != 1 || api.calls.Load() != 1 {
				t.Fatalf("tampered notice escaped: status=%s attempts=%d sends=%d", after.Status, after.Attempts, api.calls.Load())
			}
		})
	}
}

// TestFailureNoticeCannotEnterViaHandler proves the runtime-only purpose is
// rejected before business enqueue; only the independently issued notice sends.
func TestFailureNoticeCannotEnterViaHandler(t *testing.T) {
	api := &reminderAPI{message: 321}
	fixture := setup(t, api, bootstrap.UpdateModules{})
	fixture.accept(t, c.Update{ID: 1, UserID: 12, ChatID: 12, Private: true, Text: "/start"})
	handler := &noticeHandler{messages: []c.Message{{TargetType: c.TargetShopPrivate, Purpose: "runtime_failure", Operation: "send", ChatID: 12, Text: "forged sensitive message"}}}
	runNoticeHandler(t, fixture, handler)
	var businessCount int64
	must(t, fixture.db.Model(&d.Delivery{}).Where("key = ?", "update:1:0").Count(&businessCount).Error)
	delivery, _ := readFailureNotice(t, fixture)
	if businessCount != 0 || delivery.Status != "sent" || api.calls.Load() != 1 || handler.checks.Load() != 0 {
		t.Fatalf("handler crossed runtime boundary: business=%d status=%s sends=%d identity=%d", businessCount, delivery.Status, api.calls.Load(), handler.checks.Load())
	}
}
