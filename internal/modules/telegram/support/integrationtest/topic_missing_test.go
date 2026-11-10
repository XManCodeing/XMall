package integrationtest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
)

// TestMissingTopicRecovery exercises persisted rejection -> replacement -> same-row
// retry. t owns an isolated SQLite database; unknown/ordinary failures stay untouched.
func TestMissingTopicRecovery(t *testing.T) {
	for _, scenario := range []string{"missing", "unknown", "partial", "ordinary", "replacement_unknown", "stale_route"} {
		t.Run(scenario, func(t *testing.T) { runMissingTopicRecovery(t, scenario) })
	}
}

// runMissingTopicRecovery runs one rejection scenario against isolated storage.
// t reports assertions; scenario selects evidence, never a production input.
func runMissingTopicRecovery(t *testing.T, scenario string) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	fixture.store.WithDeliveryRecovery(func(tx *gorm.DB) r.DeliveryRecoveryStore { return rs.New(tx) })
	operation := d.TopicOperation{BotID: 1, SessionID: fixture.message.SessionID, TargetID: 1, ChatID: -100, RouteVersion: 1, State: "succeeded", Action: "create", Version: 1, Generation: 1}
	mustDisaster(t, fixture.db.Create(&operation).Error)
	mustDisaster(t, fixture.db.Model(&d.TopicMapping{}).Where("bot_id = ?", 1).Update("operation_id", operation.ID).Error)
	queue := rs.New(fixture.db)
	mustDisaster(t, queue.EnqueueRequest(ctx, &fixture.request))
	attempt, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	mustDisaster(t, err)
	if attempt == nil {
		t.Fatal("missing runtime attempt")
	}
	status, detail := "failed", "topic_missing"
	if scenario == "unknown" {
		status = "unknown"
	}
	if scenario == "ordinary" {
		detail = "telegram rejected message"
	}
	receipt := r.DeliveryReceipt{}
	if scenario == "partial" {
		status, receipt.MessageIDs = "unknown", []int64{77}
	}
	mustDisaster(t, queue.FinishDeliveryReceipt(ctx, attempt.ID, attempt.Claim, status, receipt, detail, fixture.now))
	if scenario == "stale_route" {
		fixture.setRoute(c.DeliveryBinding{ChatID: -200, RouteVersion: 2})
	}
	state, err := fixture.store.ReconcileReady(ctx, 1, fixture.message.ID, 1, fixture.now)
	if scenario == "stale_route" {
		if err == nil {
			t.Fatal("stale route recovered")
		}
		return
	}
	mustDisaster(t, err)
	var previous d.TopicMapping
	mustDisaster(t, fixture.db.First(&previous).Error)
	if scenario == "unknown" || scenario == "ordinary" || scenario == "partial" {
		if previous.State != "open" || state != status {
			t.Fatalf("unsafe recovery %s %s", state, previous.State)
		}
		return
	}
	if state != "topic_missing" || previous.State != "deleted" {
		t.Fatalf("missing not detected %s %s", state, previous.State)
	}
	job, err := fixture.store.ClaimTopic(ctx, 1, fixture.message.SessionID, 1, 1, 1, -100, "replacement", fixture.now, time.Minute)
	mustDisaster(t, err)
	if !job.Execute {
		t.Fatal("replacement not claimed")
	}
	result := c.TopicResult{State: "succeeded", ThreadID: 21}
	if scenario == "replacement_unknown" {
		result = c.TopicResult{State: "unknown"}
	}
	mustDisaster(t, fixture.store.FinishTopic(ctx, 1, job.Operation.ID, 1, "replacement", result, fixture.now))
	state, err = fixture.store.ReconcileReady(ctx, 1, fixture.message.ID, 1, fixture.now)
	mustDisaster(t, err)
	var delivery rd.Delivery
	mustDisaster(t, fixture.db.First(&delivery, attempt.ID).Error)
	if scenario == "replacement_unknown" {
		if state != "topic_missing" || delivery.Status != "failed" {
			t.Fatal("unknown replacement retried")
		}
		again, claimErr := fixture.store.ClaimTopic(ctx, 1, fixture.message.SessionID, 1, 1, 1, -100, "again", fixture.now.Add(2*time.Minute), time.Minute)
		mustDisaster(t, claimErr)
		if again.Execute {
			t.Fatal("unknown create duplicated")
		}
		return
	}
	assertMissingTopicRetry(t, fixture, attempt, delivery, state)
}

// assertMissingTopicRetry checks unchanged source and same-row acknowledgement.
// fixture owns test storage; attempt/delivery are persisted before/after retry.
func assertMissingTopicRetry(t *testing.T, fixture *recoveryFixture, attempt *rd.Delivery, delivery rd.Delivery, state string) {
	ctx := context.Background()
	queue := rs.New(fixture.db)
	if state != "sending" || delivery.Status != "retry" || delivery.Attempts != 1 || delivery.Key != attempt.Key {
		t.Fatalf("not same-row retry: %+v", delivery)
	}
	plain, err := fixture.cipher.Open(delivery.PayloadCipher)
	mustDisaster(t, err)
	var envelope r.Message
	mustDisaster(t, json.Unmarshal([]byte(plain), &envelope))
	if envelope.ThreadID != 21 || envelope.Transfer.SourceChatID != 10 || envelope.Transfer.SourceMessageIDs[0] != 9 {
		t.Fatal("original forward changed")
	}
	mustDisaster(t, fixture.store.AuthorizeTargetAt(ctx, 1, envelope, fixture.now))
	retry, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	mustDisaster(t, err)
	if retry == nil || retry.ID != attempt.ID {
		t.Fatal("retry row lost")
	}
	mustDisaster(t, queue.FinishDeliveryReceipt(ctx, retry.ID, retry.Claim, "sent", r.DeliveryReceipt{MessageIDs: []int64{88}}, "", fixture.now))
	state, err = fixture.store.ReconcileReady(ctx, 1, fixture.message.ID, 1, fixture.now)
	mustDisaster(t, err)
	if state != "delivered" {
		t.Fatal("message not acknowledged", state)
	}
}
