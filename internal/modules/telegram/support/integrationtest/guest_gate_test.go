package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	boot "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	ra "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type guestGateFixture struct {
	db         *gorm.DB
	repo       *ss.Store
	service    *a.Service
	dispatcher *boot.UpdateDispatcher
	manager    *ra.Manager
}

// newGuestGate creates real stores and dispatcher without any website identity.
// t owns the single-connection database; the returned fixture never performs HTTP.
func newGuestGate(t *testing.T) guestGateFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "guest.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	mustGuest(t, err)
	pool, err := db.DB()
	mustGuest(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	mustGuest(t, db.AutoMigrate(&rd.Instance{}, &rd.Session{}, &rd.Delivery{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.SupportBlock{}, &d.Album{}, &d.AlbumItem{}, &d.ReadyDispatch{}, &d.Downstream{}, &d.DownstreamAlbum{}, &d.OperationControl{}, &d.OperationAudit{}, &d.OperationReference{}))
	mustGuest(t, db.Create(&rd.Instance{ID: 1, TelegramID: 123, Enabled: true, Mode: "internal", State: "running", Generation: 1}).Error)
	queue := rs.New(db)
	manager := ra.NewManager(queue, nil, "synthetic-guest-test")
	repo := ss.New(db, manager)
	service := a.New(repo, manager, modules{}, queue)
	service.DeliveryReady = capability{} // Test-only capability; production stays closed.
	storefront := shop.New(shop.Dependencies{Store: queue, Identity: noCreateIdentity{t: t}, Secret: "synthetic-guest-test"})
	dispatcher := boot.NewUpdateDispatcher(storefront, storefront, boot.UpdateModules{Support: service, SupportCallbacks: service, Targets: map[r.TargetType]r.TargetAuthorizer{r.TargetSupportPrivate: service}})
	return guestGateFixture{db, repo, service, dispatcher, manager}
}

// TestVerificationFeedbackAcrossLocales exercises the production dispatcher and
// service clock normalization; a successful persisted verification must say passed.
func TestVerificationFeedbackAcrossLocales(t *testing.T) {
	for _, scenario := range []struct {
		locale string
		offset int
		want   string
	}{{"en", -8 * 3600, "Verified. Unexpired messages are queued."}, {"zh-hans", 8 * 3600, "验证通过，未过期的咨询已排队。"}} {
		t.Run(scenario.locale, func(t *testing.T) {
			fixture := newGuestGate(t)
			now := time.Date(2026, 11, 4, 23, 0, 0, 0, time.UTC).In(time.FixedZone("server", scenario.offset))
			fixture.service.Now = func() time.Time { return now }
			update := guestMessage(1)
			update.Locale = scenario.locale
			_, err := fixture.dispatcher.Handle(context.Background(), 1, update)
			mustGuest(t, err)
			var challenge d.Challenge
			mustGuest(t, fixture.db.First(&challenge).Error)
			now = now.Add(time.Second)
			update.ID, update.Type, update.CallbackID, update.CallbackData = 2, "callback_query", "synthetic-click", "support:"+c.VerificationAnswer(challenge, challenge.AnswerIndex)
			answer := ""
			ctx := r.WithCallbackResponder(context.Background(), func(text string) { answer = text })
			_, err = fixture.dispatcher.Handle(ctx, 1, update)
			mustGuest(t, err)
			if answer != scenario.want {
				t.Fatalf("unexpected verification feedback: %q", answer)
			}
			mustGuest(t, fixture.db.First(&challenge, challenge.ID).Error)
			if challenge.State != "passed" {
				t.Fatal("feedback does not match persisted verification", challenge.State)
			}
		})
	}
}

// TestVerificationCleanupUsesCallbackProof checks unknown sends, replay and strict ownership.
// t owns real stores; no external request or website identity participates.
func TestVerificationCleanupUsesCallbackProof(t *testing.T) {
	fixture := newGuestGate(t)
	ctx := context.Background()
	_, err := fixture.dispatcher.Handle(ctx, 1, guestMessage(1))
	mustGuest(t, err)
	var challenge d.Challenge
	mustGuest(t, fixture.db.First(&challenge).Error)
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "support_gate").Updates(map[string]any{"status": "unknown", "attempts": 1}).Error)
	callback := guestMessage(2)
	callback.Type, callback.CallbackID, callback.MessageID = "callback_query", "test-click", 80
	callback.CallbackData = "support:invalid-reference"
	_, err = fixture.dispatcher.Handle(ctx, 1, callback)
	mustGuest(t, err)
	var count int64
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "support_gate_cleanup").Count(&count).Error)
	if count != 0 {
		t.Fatal("wrong answer removed challenge")
	}
	callback.ID, callback.CallbackData = 3, "support:"+c.VerificationAnswer(challenge, challenge.AnswerIndex)
	for range 2 {
		_, err = fixture.dispatcher.Handle(ctx, 1, callback)
		mustGuest(t, err)
	}
	var deliveries []rd.Delivery
	mustGuest(t, fixture.db.Where("purpose = ?", "support_gate_cleanup").Find(&deliveries).Error)
	if len(deliveries) != 1 || deliveries[0].Status != "pending" {
		t.Fatal("cleanup was lost or duplicated", len(deliveries))
	}
	message := gateEnvelope(t, fixture, deliveries[0])
	if message.Operation != "delete" || message.ChatID != 42 || message.TargetMessageID != 80 {
		t.Fatal("cleanup does not target the callback message")
	}
	guard := boot.SupportPrivateAuthorizer{Notices: fixture.service}
	mustGuest(t, guard.AuthorizeTarget(ctx, 1, message))
	for _, mutate := range []func(*r.Message){
		func(message *r.Message) { message.ChatID++ },
		func(message *r.Message) { message.TargetMessageID++ },
		func(message *r.Message) { message.Text = "unexpected" },
		func(message *r.Message) { message.BusinessRef = "support:gate-cleanup:2" },
		func(message *r.Message) { message.Operation = "send" },
	} {
		altered := message
		mutate(&altered)
		if guard.AuthorizeTarget(ctx, 1, altered) == nil {
			t.Fatal("altered cleanup authorized")
		}
	}
	if guard.AuthorizeTarget(ctx, 2, message) == nil {
		t.Fatal("cross-bot cleanup authorized")
	}
	callback.ID = 4
	_, err = fixture.dispatcher.Handle(ctx, 1, callback)
	mustGuest(t, err)
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "support_gate_cleanup").Count(&count).Error)
	if count != 1 {
		t.Fatal("another callback duplicated cleanup")
	}
	// Callback proof is encrypted and contains no copied customer text/media.
	var proof d.BufferedMessage
	mustGuest(t, fixture.db.Where("update_id = ?", 3).First(&proof).Error)
	plain, err := fixture.manager.Open(proof.PayloadCipher)
	mustGuest(t, err)
	var saved r.Update
	mustGuest(t, json.Unmarshal([]byte(plain), &saved))
	if saved.Text != "" || len(saved.Media) != 0 || saved.MessageID != 80 {
		t.Fatal("cleanup retained unrelated callback content")
	}
	mustGuest(t, fixture.repo.Expire(ctx, 1, time.Now().Add(31*24*time.Hour)))
	mustGuest(t, fixture.db.First(&proof, proof.ID).Error)
	if proof.PayloadCipher != "" {
		t.Fatal("old cleanup proof retained indefinitely")
	}
}

// mustGuest fails t on err; it returns normally only for successful persistence.
func mustGuest(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// guestMessage returns a private Telegram-only message for the supplied update ID.
func guestMessage(id int64) r.Update {
	return r.Update{ID: id, Type: "message", MessageKind: "text", Private: true, ChatType: "private", UserID: 42, ChatID: 42, MessageID: id, Text: "guest consultation"}
}

// gateEnvelope decrypts a persisted delivery and returns its exact envelope.
func gateEnvelope(t *testing.T, fixture guestGateFixture, delivery rd.Delivery) r.Message {
	t.Helper()
	plain, err := fixture.manager.Open(delivery.PayloadCipher)
	mustGuest(t, err)
	var message r.Message
	mustGuest(t, json.Unmarshal([]byte(plain), &message))
	return message
}

// TestGuestBlockedGateReplayAndRecovery checks queued challenge invalidation,
// durable rate-limited feedback and restart/unblock behavior through dispatcher.
func TestGuestBlockedGateReplayAndRecovery(t *testing.T) {
	fixture := newGuestGate(t)
	ctx := context.Background()
	_, err := fixture.dispatcher.Handle(ctx, 1, guestMessage(1))
	mustGuest(t, err)
	var session d.Session
	mustGuest(t, fixture.db.First(&session).Error)
	if session.UserID != 42 {
		t.Fatal("support lost Telegram guest identity")
	}
	var challenge d.Challenge
	mustGuest(t, fixture.db.First(&challenge).Error)
	var queued rd.Delivery
	mustGuest(t, fixture.db.First(&queued).Error)
	envelope := gateEnvelope(t, fixture, queued)
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, session.ID, 55, 0, true, nil, "synthetic block", time.Now()))
	if err = fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope); !errors.Is(err, c.ErrDenied) {
		t.Fatal("blocked queued challenge", err)
	}
	for _, id := range []int64{2, 2, 3} {
		_, err = fixture.dispatcher.Handle(ctx, 1, guestMessage(id))
		mustGuest(t, err)
	}
	var rejected []d.BufferedMessage
	mustGuest(t, fixture.db.Where("reason = ?", "blocked").Find(&rejected).Error)
	if len(rejected) != 2 {
		t.Fatal("blocked replay not deduplicated", len(rejected))
	}
	for _, row := range rejected {
		if row.PayloadCipher != "" || row.State != "rejected" {
			t.Fatal("restricted content retained")
		}
	}
	var notices []rd.Delivery
	mustGuest(t, fixture.db.Find(&notices).Error)
	if len(notices) != 2 {
		t.Fatal("restriction feedback not rate limited", len(notices))
	}
	feedback := gateEnvelope(t, fixture, notices[1])
	if len(feedback.Buttons) != 0 || feedback.OwnerUserID != 0 {
		t.Fatal("restriction feedback carries verification/account")
	}
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, feedback))
	feedback.Buttons = envelope.Buttons
	if err = fixture.dispatcher.AuthorizeTarget(ctx, 1, feedback); !errors.Is(err, c.ErrDenied) {
		t.Fatal("restriction button accepted", err)
	}
	// A reconstructed service recovers only pending work, never a cancelled challenge.
	restarted := a.New(ss.New(fixture.db, fixture.manager), fixture.manager, modules{}, rs.New(fixture.db))
	mustGuest(t, restarted.Reconcile(ctx, 1))
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, session.ID, 55, 1, false, nil, "synthetic unblock", time.Now()))
	if err = fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unblock revived challenge", err)
	}
	_, err = fixture.dispatcher.Handle(ctx, 1, guestMessage(2))
	mustGuest(t, err)
	var count int64
	mustGuest(t, fixture.db.Model(&d.Challenge{}).Count(&count).Error)
	if count != 1 {
		t.Fatal("blocked input replay reopened verification")
	}
	_, err = fixture.dispatcher.Handle(ctx, 1, guestMessage(4))
	mustGuest(t, err)
	mustGuest(t, fixture.db.Model(&d.Challenge{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("fresh input did not use original challenge flow")
	}
}

// TestGuestRestrictionDoesNotReopenClosedConsultation verifies the blocked path
// returns durable feedback without changing the closed session or creating a challenge.
func TestGuestRestrictionDoesNotReopenClosedConsultation(t *testing.T) {
	fixture := newGuestGate(t)
	ctx := context.Background()
	_, err := fixture.dispatcher.Handle(ctx, 1, guestMessage(1))
	mustGuest(t, err)
	var session d.Session
	mustGuest(t, fixture.db.First(&session).Error)
	mustGuest(t, fixture.repo.CloseConsultation(ctx, 1, session.ID, session.Version, time.Now()))
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, session.ID, 55, 0, true, nil, "synthetic block", time.Now()))
	_, err = fixture.dispatcher.Handle(ctx, 1, guestMessage(2))
	mustGuest(t, err)
	mustGuest(t, fixture.db.First(&session, session.ID).Error)
	if session.State != "closed" {
		t.Fatal("restriction feedback reopened session")
	}
	var count int64
	mustGuest(t, fixture.db.Model(&d.Challenge{}).Count(&count).Error)
	if count != 1 {
		t.Fatal("restriction created challenge")
	}
}
