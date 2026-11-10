package gormstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"image/png"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestBlockedNoticeChecksAndCallbackTombstones checks block expiration, pending
// challenge denial, and replay-proof callback restriction on the actual store.
func TestBlockedNoticeChecksAndCallbackTombstones(t *testing.T) {
	store, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	update := r.Update{ID: 1, Private: true, UserID: 42, ChatID: 42, MessageID: 1}
	outcome, err := store.Accept(ctx, 1, update, "encrypted", 9, "synthetic-ref", c.DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	notice, err := store.Notice(ctx, 1, outcome.Challenge.Ref)
	if err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Minute)
	if err = store.SetSupportBlock(ctx, 1, outcome.Session.ID, 55, 0, true, &until, "synthetic", now); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNotice(ctx, notice, now); !errors.Is(err, c.ErrDenied) {
		t.Fatal("queued challenge escaped", err)
	}
	callback := r.Update{ID: 2, Private: true, UserID: 42, ChatID: 42, CallbackID: "synthetic-click"}
	for range 2 {
		outcome, err = store.Verify(ctx, 1, callback, "synthetic-ref", c.DefaultPolicy(), now)
		if err != nil || outcome.Reason != "blocked" {
			t.Fatal("blocked callback retried", outcome, err)
		}
	}
	var rows []d.BufferedMessage
	if err = db.Where("update_id = ?", 2).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PayloadCipher != "" || rows[0].FeedbackPending {
		t.Fatal("callback generated a notice or leaked input")
	}
	outcome, err = store.Verify(ctx, 1, callback, "synthetic-ref", c.DefaultPolicy(), until.Add(time.Second))
	if err != c.ErrDenied || outcome.Session.State == "open" {
		t.Fatal("expired block replay reopened consultation", err)
	}
	var challenge d.Challenge
	if err = db.First(&challenge).Error; err != nil {
		t.Fatal(err)
	}
	if challenge.State != "cancelled" || challenge.NoticePending {
		t.Fatal("challenge not permanently cancelled")
	}
}

// TestVerificationUsesServerInstant checks SQL round-trips, expiry boundaries and
// repeated clicks using UTC instants originating in US and Asian server timezones.
// Client locale changes only feedback language, never the challenge lifetime.
func TestVerificationUsesServerInstant(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
		locale string
	}{{"US", -8 * 3600, "en"}, {"Asia", 8 * 3600, "zh-hans"}} {
		t.Run(zone.name, func(t *testing.T) {
			store, _ := fixture(t)
			ctx := context.Background()
			policy := c.DefaultPolicy()
			instant := time.Date(2026, 11, 4, 23, 0, 0, 0, time.UTC)
			serverNow := instant.In(time.FixedZone(zone.name, zone.offset)).UTC()
			update := r.Update{ID: 1, Private: true, UserID: 42, ChatID: 42, MessageID: 1, Locale: zone.locale}
			outcome, err := store.Accept(ctx, 1, update, "encrypted", 9, "timezone-test", policy, serverNow)
			if err != nil || outcome.Challenge == nil {
				t.Fatal("challenge creation failed", err)
			}
			challenge, err := store.Challenge(ctx, "timezone-test")
			if err != nil || !challenge.ExpiresAt.Equal(instant.Add(policy.ChallengeTTL)) {
				t.Fatal("persisted expiry differs from server instant", err)
			}
			callback := r.Update{ID: 2, Private: true, UserID: 42, ChatID: 42, CallbackID: "click", Locale: zone.locale}
			outcome, err = store.Verify(ctx, 1, callback, c.VerificationAnswer(*challenge, challenge.AnswerIndex), policy, serverNow.Add(time.Second))
			if err != nil || outcome.Reason != "passed" {
				t.Fatal("immediate click must pass", outcome.Reason, err)
			}
			callback.ID++
			outcome, err = store.Verify(ctx, 1, callback, c.VerificationAnswer(*challenge, challenge.AnswerIndex), policy, challenge.ExpiresAt.Add(time.Second))
			if err != nil || outcome.Reason != "passed" {
				t.Fatal("repeated click must preserve verified state", outcome.Reason, err)
			}
			update.ID, update.UserID, update.ChatID = 4, 43, 43
			outcome, err = store.Accept(ctx, 1, update, "encrypted", 9, "expiry-test", policy, serverNow)
			if err != nil || outcome.Challenge == nil {
				t.Fatal("second challenge creation failed", err)
			}
			callback.ID, callback.UserID, callback.ChatID = 5, 43, 43
			outcome, err = store.Verify(ctx, 1, callback, c.VerificationAnswer(*outcome.Challenge, outcome.Challenge.AnswerIndex), policy, serverNow.Add(policy.ChallengeTTL))
			if err != nil || outcome.Reason != "expired" {
				t.Fatal("unverified challenge must expire at the boundary", outcome.Reason, err)
			}
		})
	}
}

// TestDailyArithmeticGate covers buffering, identity, order and Beijing day rollover.
func TestDailyArithmeticGate(t *testing.T) {
	store, db := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 15, 58, 0, 0, time.UTC)
	policy := c.DefaultPolicy()
	policy.Retention, policy.ChallengeTTL = time.Minute, time.Minute
	update := r.Update{ID: 1, Private: true, UserID: 42, ChatID: 42, MessageID: 1, FirstName: "Test", LastName: "Customer", Username: "customer"}
	first, err := store.Accept(ctx, 1, update, "first", 5, "daily", policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.DisplayName != "Test Customer" || first.Session.Username != "customer" || first.Session.State != "waiting_verification" || !first.Challenge.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal(first)
	}
	update.ID++
	if _, err = store.Accept(ctx, 1, update, "second", 6, "unused", policy, now); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Ready(ctx, 1, first.Session.ID, 20, now)
	if err != nil || len(rows) != 0 {
		t.Fatal("unverified content released", rows, err)
	}
	var stored []d.BufferedMessage
	if err = db.Where("session_id = ?", first.Session.ID).Order("sequence").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range stored {
		if row.State != "buffered" || row.PayloadCipher == "" || row.RetainUntil.Before(first.Challenge.ExpiresAt) {
			t.Fatal(row)
		}
	}
	callback := r.Update{ID: 3, UserID: 42, ChatID: 42}
	answer := c.VerificationAnswer(*first.Challenge, first.Challenge.AnswerIndex)
	passed, err := store.Verify(ctx, 1, callback, answer, policy, now.Add(time.Second))
	if err != nil || passed.Reason != "passed" {
		t.Fatal(passed, err)
	}
	rows, err = store.Ready(ctx, 1, first.Session.ID, 20, now)
	if err != nil || len(rows) != 2 || rows[0].Sequence != 1 || rows[1].Sequence != 2 {
		t.Fatal(rows, err)
	}
	update.ID = 4
	sameDay, err := store.Accept(ctx, 1, update, "third", 5, "same", policy, now.Add(time.Minute))
	if err != nil || sameDay.Session.State != "open" || sameDay.Challenge.ID != first.Challenge.ID {
		t.Fatal(sameDay, err)
	}
	update.ID = 5
	nextDay, err := store.Accept(ctx, 1, update, "next", 4, "next", policy, now.Add(2*time.Minute))
	if err != nil || nextDay.Session.State != "waiting_verification" || nextDay.Challenge.ID == first.Challenge.ID {
		t.Fatal(nextDay, err)
	}
	var next d.BufferedMessage
	if err = db.Where("update_id = 5").First(&next).Error; err != nil || next.State != "buffered" {
		t.Fatal(next, err)
	}
}

// TestFailedRoundNeverReleased checks wrong answers, expiry workers and lazy expiry.
func TestFailedRoundNeverReleased(t *testing.T) {
	for _, mode := range []string{"wrong", "worker", "ingress", "callback"} {
		t.Run(mode, func(t *testing.T) {
			store, db := fixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			policy := c.DefaultPolicy()
			update := r.Update{ID: 1, UserID: 42, ChatID: 42}
			first, err := store.Accept(ctx, 1, update, "keep-for-admin", 14, "first", policy, now)
			if err != nil {
				t.Fatal(err)
			}
			update.ID = 2
			if _, err = store.Accept(ctx, 1, update, "second", 6, "unused", policy, now); err != nil {
				t.Fatal(err)
			}
			later := now.Add(time.Hour + policy.Cooldown)
			callback := r.Update{ID: 3, UserID: 42, ChatID: 42}
			switch mode {
			case "wrong":
				result, err := store.Verify(ctx, 1, callback, c.VerificationAnswer(*first.Challenge, (first.Challenge.AnswerIndex+1)%5), policy, now)
				if err != nil || result.Reason != "wrong" {
					t.Fatal(result, err)
				}
			case "worker":
				if err = store.Expire(ctx, 1, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "callback":
				result, err := store.Verify(ctx, 1, callback, c.VerificationAnswer(*first.Challenge, first.Challenge.AnswerIndex), policy, now.Add(time.Hour))
				if err != nil || result.Reason != "expired" {
					t.Fatal(result, err)
				}
			}
			update.ID = 4
			renewed, err := store.Accept(ctx, 1, update, "fresh", 5, "renewed", policy, later)
			if err != nil {
				t.Fatal(err)
			}
			callback.ID = 5
			result, err := store.Verify(ctx, 1, callback, c.VerificationAnswer(*renewed.Challenge, renewed.Challenge.AnswerIndex), policy, later)
			if err != nil || result.Reason != "passed" {
				t.Fatal(result, err)
			}
			ready, err := store.Ready(ctx, 1, first.Session.ID, 20, later)
			if err != nil || len(ready) != 1 || ready[0].UpdateID != 4 {
				t.Fatal("old round revived", ready, err)
			}
			var old []d.BufferedMessage
			if err = db.Where("update_id IN ?", []int64{1, 2}).Find(&old).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range old {
				if row.State != "rejected" || row.PayloadCipher == "" {
					t.Fatal("admin evidence lost", row)
				}
			}
		})
	}
}

// TestVerificationEnvelopeStrict proves PNG/buttons survive JSON and every extra capability is denied.
func TestVerificationEnvelopeStrict(t *testing.T) {
	store, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	out, err := store.Accept(ctx, 1, r.Update{ID: 1, UserID: 42, ChatID: 42}, "cipher", 6, "picture", c.DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	service := a.New(store, policyIngressAdapters{}, nil, nil)
	service.Now = func() time.Time { return now }
	// Build through the real notice outbox, never a parallel test renderer.
	deliveries := &gateDeliveryCapture{}
	service.Deliveries = deliveries
	if err = service.Reconcile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var message r.Message
	if err = json.Unmarshal([]byte(deliveries.request.PayloadCipher), &message); err != nil {
		t.Fatal(err)
	}
	if _, err = png.Decode(bytes.NewReader(message.Photo)); err != nil {
		t.Fatal(err)
	}
	if len(message.Buttons) != 2 || len(message.Buttons[0]) != 3 || len(message.Buttons[1]) != 2 {
		t.Fatal(message.Buttons)
	}
	if err = service.AuthorizeTarget(ctx, 1, message); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*r.Message){func(m *r.Message) { m.Text += "changed" }, func(m *r.Message) { m.Photo = append([]byte{}, m.Photo...); m.Photo[0] ^= 1 }, func(m *r.Message) { m.Transfer = &r.TransferRequest{} }, func(m *r.Message) { m.TargetMessageID = 9 }, func(m *r.Message) { m.RouteVersion = 3 }, func(m *r.Message) { m.PhotoURL = "https://example.invalid" }} {
		tampered := message
		mutate(&tampered)
		if err = service.AuthorizeTarget(ctx, 1, tampered); err != c.ErrDenied {
			t.Fatal("altered envelope accepted", err)
		}
	}
	result, err := store.Verify(ctx, 1, r.Update{ID: 2, UserID: 42, ChatID: 42}, out.Challenge.Ref, c.DefaultPolicy(), now)
	if err != nil || result.Reason != "invalid" {
		t.Fatal("bare ref accepted", result, err)
	}
}

type gateDeliveryCapture struct{ request *r.DeliveryRequest }

// EnqueueRequest captures the real encrypted envelope for authorization assertions.
func (capture *gateDeliveryCapture) EnqueueRequest(_ context.Context, request *r.DeliveryRequest) error {
	capture.request = request
	return nil
}

func fixture(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	sql, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { sql.Close() })
	if e = db.AutoMigrate(&rd.Instance{}, &rd.Delivery{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.TopicOperation{}, &d.TopicMapping{}, &d.ReadyDispatch{}, &d.ReplyMapping{}, &d.ResolutionAudit{}, &d.Downstream{}, &d.DownstreamAlbum{}, &d.OperationAudit{}, &d.OperationReference{}, &d.OperationControl{}, &d.SupportBlock{}, &d.Album{}, &d.AlbumItem{}); e != nil {
		t.Fatal(e)
	}
	if e = db.Create(&rd.Instance{ID: 1, TelegramID: 100, Generation: 1, Enabled: true, Mode: "internal", State: "running"}).Error; e != nil {
		t.Fatal(e)
	}
	return New(db), db
}

// TestTerminalCleanupPagesAndMetadata covers finite cleanup pages and immutable
// metadata; each repeated Expire call advances because cleared rows are excluded.
func TestTerminalCleanupPagesAndMetadata(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	for index := 1; index <= 105; index++ {
		row := d.BufferedMessage{BotID: 1, UpdateID: int64(index), State: "delivered", PayloadCipher: "cipher", CreatedAt: old, UpdatedAt: old}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	audit := d.OperationAudit{BotID: 1, ReferenceID: 1, ActorID: 44, Action: "note", NoteCipher: "audit", CreatedAt: old}
	ref := d.OperationReference{BotID: 1, Ref: "expired-note", NoteCipher: "reference", ExpiresAt: old, CreatedAt: old}
	if err := db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ref).Error; err != nil {
		t.Fatal(err)
	}
	for page, expected := range []int64{5, 0} {
		if err := s.Expire(ctx, 1, now); err != nil {
			t.Fatal(page, err)
		}
		var count int64
		if err := db.Model(&d.BufferedMessage{}).Where("payload_cipher <> ''").Count(&count).Error; err != nil || count != expected {
			t.Fatal(page, count, err)
		}
	}
	var loaded d.OperationAudit
	if err := db.First(&loaded, audit.ID).Error; err != nil || loaded.NoteCipher != "" || loaded.ActorID != audit.ActorID || !loaded.CreatedAt.Equal(old) {
		t.Fatal(loaded.ID, err)
	}
	if err := db.First(&ref, ref.ID).Error; err != nil || ref.NoteCipher != "" || ref.ConsumedAt != nil {
		t.Fatal(ref.ID, err)
	}
}

// TestCleanupProtectsAlbumAndRetryableRows ensures even an old terminal sibling
// cannot lose ciphertext while its leader still needs source/proof recovery.
func TestCleanupProtectsAlbumAndRetryableRows(t *testing.T) {
	s, db := fixture(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	for index, state := range []string{"sending", "unknown", "failed", "pending", "delivered", "internal"} {
		row := d.Downstream{BotID: 1, UpdateID: int64(index + 1), State: state, SourceCipher: "source", EnvelopeCipher: "envelope", UpdatedAt: old, AlbumID: 1}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Expire(context.Background(), 1, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&d.Downstream{}).Where("source_cipher = '' OR envelope_cipher = ''").Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

// TestCleanupReadyEnvelopeAndAlbumDependencies protects all sources until the
// leader has acknowledged delivery, then clears source/envelope without mappings.
func TestCleanupReadyEnvelopeAndAlbumDependencies(t *testing.T) {
	s, db := fixture(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	leader := d.BufferedMessage{BotID: 1, UpdateID: 501, State: "sending", PayloadCipher: "leader", UpdatedAt: old}
	follower := d.BufferedMessage{BotID: 1, UpdateID: 502, State: "delivered", PayloadCipher: "follower", UpdatedAt: old}
	for _, row := range []*d.BufferedMessage{&leader, &follower} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&d.AlbumItem{AlbumID: 1, BufferedMessageID: row.ID, SourceMessageID: row.UpdateID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	dispatch := d.ReadyDispatch{BotID: 1, MessageID: leader.ID, PayloadCipher: "envelope"}
	if err := db.Create(&dispatch).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(context.Background(), 1, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&follower, follower.ID).Error; err != nil || follower.PayloadCipher == "" {
		t.Fatal(err)
	}
	if err := db.Model(&leader).UpdateColumn("state", "delivered").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(context.Background(), 1, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&dispatch, dispatch.ID).Error; err != nil || dispatch.PayloadCipher != "" {
		t.Fatal(err)
	}
	if err := db.First(&follower, follower.ID).Error; err != nil || follower.PayloadCipher != "" {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&d.AlbumItem{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

// TestExpiredControlCleanupPages preserves live menus and update/ref tombstones.
// The maintenance clock is supplied explicitly; only ciphertext may change.
func TestExpiredControlCleanupPages(t *testing.T) {
	s, db := fixture(t)
	now := time.Now().UTC()
	for index := 1; index <= 103; index++ {
		expires := now
		bot := uint(1)
		if index == 102 {
			expires = now.Add(time.Second)
		}
		if index == 103 {
			bot = 2
		}
		row := d.OperationControl{BotID: bot, UpdateID: int64(index), Ref: fmt.Sprintf("menu-%d", index), PayloadCipher: "menu", ExpiresAt: expires, CreatedAt: now.Add(-time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, remaining := range []int64{3, 2} {
		if err := s.Expire(context.Background(), 1, now); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := db.Model(&d.OperationControl{}).Where("payload_cipher <> ''").Count(&count).Error; err != nil || count != remaining {
			t.Fatal(count, err)
		}
	}
	row, err := s.ControlForUpdate(context.Background(), 1, 1)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Ref != "menu-1" || row.PayloadCipher != "" || !row.ExpiresAt.Equal(now) || !row.CreatedAt.Equal(now.Add(-time.Minute)) {
		t.Fatal("control metadata changed")
	}
}

// TestRejectedCleanupRequiresNoDispatch covers abnormal rejected rows, leader
// protection, retention boundaries and a runtime row not yet linked by ID.
func TestRejectedCleanupRequiresNoDispatch(t *testing.T) {
	s, db := fixture(t)
	now := time.Now().UTC()
	old := now.Add(-supportTerminalAge)
	for index := 1; index <= 6; index++ {
		row := d.Downstream{BotID: 1, UpdateID: int64(index), State: "rejected", SourceCipher: "source", UpdatedAt: old}
		switch index {
		case 2:
			row.EnvelopeCipher = "prepared"
		case 3:
			row.DeliveryID = 123
		case 4:
			row.UpdatedAt = old.Add(time.Second)
		case 5:
			row.AlbumID = 50
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if index == 5 {
			if err := db.Create(&d.DownstreamAlbum{ID: 50, BotID: 1, LeaderID: row.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if index == 6 {
			if err := db.Create(&rd.Delivery{BotID: 1, Key: c.DownstreamBusinessRef(row.ID), Status: "unknown", PayloadHeld: true}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Expire(context.Background(), 1, now); err != nil {
		t.Fatal(err)
	}
	var rows []d.Downstream
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if (row.SourceCipher == "") != (row.UpdateID == 1) {
			t.Fatalf("unexpected cleanup update=%d", row.UpdateID)
		}
		if row.State != "rejected" {
			t.Fatal("state changed")
		}
	}
}

// TestConcurrentCorrectWrongAnswersCannotReviveRound exercises the shared database lock.
// Whichever answer commits first fixes the round; the losing callback cannot change it.
func TestConcurrentCorrectWrongAnswersCannotReviveRound(t *testing.T) {
	store, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	policy := c.DefaultPolicy()
	first, err := store.Accept(ctx, 1, r.Update{ID: 1, UserID: 42, ChatID: 42}, "cipher", 6, "race", policy, now)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index, answer := range []int{first.Challenge.AnswerIndex, (first.Challenge.AnswerIndex + 1) % 5} {
		group.Add(1)
		go func(updateID int64, answer int) {
			defer group.Done()
			if _, err := store.Verify(ctx, 1, r.Update{ID: updateID, UserID: 42, ChatID: 42}, c.VerificationAnswer(*first.Challenge, answer), policy, now); err != nil {
				t.Error(err)
			}
		}(int64(index+2), answer)
	}
	group.Wait()
	var challenge d.Challenge
	if err = db.First(&challenge, first.Challenge.ID).Error; err != nil {
		t.Fatal(err)
	}
	var message d.BufferedMessage
	if err = db.Where("update_id = 1").First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if challenge.Attempts != 1 || (challenge.State == "passed" && message.State != "ready") || (challenge.State == "locked" && message.State != "rejected") {
		t.Fatal(challenge.State, message.State, challenge.Attempts)
	}
	if challenge.State != "passed" && challenge.State != "locked" {
		t.Fatal(challenge.State)
	}
}

func TestConcurrentFirstMessagesAndReplay(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	var wg sync.WaitGroup
	for n := 1; n <= 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := s.Accept(ctx, 1, r.Update{ID: int64(n), UserID: 50, ChatID: 50}, "encrypted", 9, fmt.Sprintf("ref-%d", n), p, now)
			if e != nil {
				t.Error(e)
			}
		}(n)
	}
	wg.Wait()
	var count int64
	db.Model(&d.Session{}).Count(&count)
	if count != 1 {
		t.Fatalf("sessions %d", count)
	}
	db.Model(&d.Challenge{}).Count(&count)
	if count != 1 {
		t.Fatalf("challenges %d", count)
	}
	var ch d.Challenge
	db.First(&ch)
	if _, e := s.Verify(ctx, 2, r.Update{UserID: 50, ChatID: 50}, c.VerificationAnswer(ch, ch.AnswerIndex), p, now); !errors.Is(e, c.ErrDenied) {
		t.Fatalf("foreign bot %v", e)
	}
	if out, e := s.Verify(ctx, 1, r.Update{UserID: 51, ChatID: 51}, c.VerificationAnswer(ch, ch.AnswerIndex), p, now); e != nil || out.Reason != "invalid" {
		t.Fatalf("foreign user %v", e)
	}
	for range 2 {
		if _, e := s.Verify(ctx, 1, r.Update{UserID: 50, ChatID: 50}, c.VerificationAnswer(ch, ch.AnswerIndex), p, now); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.Ready(ctx, 1, ch.SessionID, 100, now)
	if e != nil || len(rows) != 12 {
		t.Fatalf("ready %d %v", len(rows), e)
	}
	for i, row := range rows {
		if row.Sequence != uint64(i+1) || row.PayloadCipher != "encrypted" {
			t.Fatalf("row %+v", row)
		}
	}
	if _, e = s.Accept(ctx, 1, r.Update{ID: 1, UserID: 50, ChatID: 50}, "changed", 7, "other", p, now); e != nil {
		t.Fatal(e)
	}
	db.Model(&d.BufferedMessage{}).Count(&count)
	if count != 12 {
		t.Fatalf("duplicate %d", count)
	}
}
func TestQueueBoundsExpiryAndCooldown(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	p.MaxMessages = 1
	first, e := s.Accept(ctx, 1, r.Update{ID: 1, UserID: 50, ChatID: 50}, "secret", 6, "ref", p, now)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.Accept(ctx, 1, r.Update{ID: 2, UserID: 50, ChatID: 50}, "secret", 6, "ref2", p, now)
	if e != nil || second.Reason != "limit" {
		t.Fatalf("%+v %v", second, e)
	}
	expired, e := s.Verify(ctx, 1, r.Update{UserID: 50, ChatID: 50}, c.VerificationAnswer(*first.Challenge, first.Challenge.AnswerIndex), p, now.Add(p.ChallengeTTL))
	if e != nil || expired.Reason != "expired" {
		t.Fatalf("%+v %v", expired, e)
	}
	cooling, e := s.Accept(ctx, 1, r.Update{ID: 3, UserID: 50, ChatID: 50}, "secret", 6, "ref3", p, now.Add(p.ChallengeTTL))
	if e != nil || cooling.Reason != "cooldown" {
		t.Fatalf("%+v %v", cooling, e)
	}
	if e = s.Expire(ctx, 1, now.Add(p.Retention)); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Ready(ctx, 1, first.Session.ID, 10, now.Add(p.Retention))
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
}
