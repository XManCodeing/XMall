package gormstore

import (
	"context"
	"encoding/json"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"testing"
	"time"
)

// TestRejectReadyLegacyContent checks safe rejection independently of bootstrap dependencies.
func TestRejectReadyLegacyContent(t *testing.T) {
	s, db := fixture(t)
	cipher := app.NewManager(nil, nil, "reject-test")
	s.cipher = cipher
	ctx, now := r.WithExecutionFence(context.Background(), eventTestFence{}), time.Now().UTC()
	source := r.Update{ID: 1, UserID: 10, ChatID: 10, Private: true, MessageID: 1, MessageKind: "text", Text: "test"}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Accept(ctx, 1, source, encrypted, 10, "reject-test", c.DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Verify(ctx, 1, r.Update{ID: 2, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimReady(ctx, 1, out.Session.ID, 1, "reject", now, time.Minute)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	source.HasProtectedContent = true
	raw, err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err = cipher.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(claimed).Update("payload_cipher", encrypted).Error; err != nil {
		t.Fatal(err)
	}
	s.WithReadyEncoder(c.EncodeReadyTransfer)
	installTestTopic(t, s, db, out.Session.ID, c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 3})
	if err = db.Model(claimed).Updates(map[string]any{"state": "ready", "claim": "", "lease_until": nil}).Error; err != nil {
		t.Fatal(err)
	}
	worker := a.ReadyWorker{Store: s, Consumer: s, Queue: rejectTestQueue{}, Builder: a.TransferReadyBuilder{}, NewClaim: func() string { return "reject" }}
	if _, err = worker.Tick(ctx, 1, 1, 0, 10, now); err != nil {
		t.Fatal(err)
	}
}

type rejectTestQueue struct{}

// EnqueueDelivery rejects unexpected dispatches in the local invalid-content test.
func (rejectTestQueue) EnqueueDelivery(context.Context, r.DeliveryRequest) error { return c.ErrDenied }

func TestTrustedGenerationAndDeliveryProof(t *testing.T) {
	s, db := fixture(t)
	cipher := app.NewManager(nil, nil, "proof-test-secret")
	s.cipher = cipher
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	target := c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 3}
	out, e := s.Accept(ctx, 1, r.Update{ID: 1, UserID: 10, ChatID: 10}, "cipher", 6, "ref", p, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, 1, r.Update{ID: 2, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), p, now); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimReady(ctx, 1, out.Session.ID, 999, "forged", now, time.Minute); e != c.ErrConflict {
		t.Fatal("forged generation", e)
	}
	installTestTopic(t, s, db, out.Session.ID, target)
	m, e := s.ClaimReady(ctx, 1, out.Session.ID, 1, "claim", now, time.Minute)
	if e != nil || m == nil {
		t.Fatal(m, e)
	}
	db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 2)
	if _, e = s.ClaimReady(ctx, 1, out.Session.ID, 1, "old", now, time.Minute); e != c.ErrConflict {
		t.Fatal(e)
	}
	if e = s.BeginSend(ctx, 1, m.ID, 1, "claim", target, now); e != c.ErrConflict {
		t.Fatal("stale begin", e)
	}
	db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 1)
	if e = s.BeginSend(ctx, 1, m.ID, 1, "claim", target, now); e != nil {
		t.Fatal(e)
	}
	ref := c.ReadyBusinessRef(m.SessionID, m.ID)
	envelope := r.Message{TargetType: r.TargetSupportGroup, BusinessRef: ref, ChatID: target.ChatID, ThreadID: target.ThreadID, RouteVersion: target.RouteVersion, Purpose: "support_ready", Operation: "send"}
	raw, _ := json.Marshal(envelope)
	encrypted, e := cipher.Seal(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	proof := rd.Delivery{BotID: 1, Key: ref, ChatID: target.ChatID, TelegramUserID: 10, TargetType: string(r.TargetSupportGroup), Purpose: "support_ready", Operation: "send", Status: "pending", MessageID: 55, PayloadCipher: encrypted}
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	for _, status := range []string{"pending", "failed", "unknown"} {
		db.Model(&proof).Update("status", status)
		if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrDenied {
			t.Fatalf("accepted %s: %v", status, e)
		}
	}
	db.Model(&proof).Updates(map[string]any{"status": "sent", "bot_id": 2})
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrDenied {
		t.Fatal("foreign bot", e)
	}
	db.Model(&proof).Updates(map[string]any{"bot_id": 1, "chat_id": -200})
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrDenied {
		t.Fatal("foreign target", e)
	}
	db.Model(&proof).Update("chat_id", target.ChatID)
	envelope.BusinessRef = "support:ready:other"
	raw, _ = json.Marshal(envelope)
	wrong, _ := cipher.Seal(string(raw))
	db.Model(&proof).Update("payload_cipher", wrong)
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrDenied {
		t.Fatal("foreign business", e)
	}
	db.Model(&proof).Update("payload_cipher", encrypted)
	db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 2)
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrConflict {
		t.Fatal("stale ack", e)
	}
	db.Table("telegram_bot_instances").Where("id = 1").Updates(map[string]any{"generation": 1, "state": "stopped"})
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != c.ErrConflict {
		t.Fatal("stopped ack", e)
	}
	db.Table("telegram_bot_instances").Where("id = 1").Update("state", "running")
	if e = s.AckReady(ctx, 1, m.ID, 1, "claim", "delivered", proof.ID, now); e != nil {
		t.Fatal(e)
	}
	var saved d.BufferedMessage
	db.First(&saved, m.ID)
	if saved.State != "delivered" || saved.DeliveryID != proof.ID {
		t.Fatal(saved)
	}
}
func TestOutOfOrderCallbackDeduplication(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	for _, id := range []int64{100, 98, 99, 98, 100} {
		if _, e := s.Verify(ctx, 1, r.Update{ID: id, UserID: 10, ChatID: 10}, "unknown", p, now); e != nil {
			t.Fatal(e)
		}
	}
	var session d.Session
	db.Where("bot_id = 1 AND user_id = 10").First(&session)
	if session.Attempts != 3 {
		t.Fatalf("attempts=%d", session.Attempts)
	}
	for _, id := range []int64{96, 97} {
		if _, e := s.Verify(ctx, 1, r.Update{ID: id, UserID: 10, ChatID: 10}, "unknown", p, now); e != nil {
			t.Fatal(e)
		}
	}
	db.First(&session, session.ID)
	if session.Attempts != 5 || !session.CooldownUntil.After(now) {
		t.Fatal(session)
	}
	var count int64
	db.Model(&d.BufferedMessage{}).Where("state = ?", "callback").Count(&count)
	if count != 5 {
		t.Fatal(count)
	}
}
