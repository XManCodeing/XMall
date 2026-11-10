package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
)

// TestTopicAdminMappingSnapshot preserves physical IDs and operation ownership.
func TestTopicAdminMappingSnapshot(t *testing.T) {
	store, db := fixture(t)
	now := time.Now().UTC()
	topic := createRecoveryTopic(t, store, db, now)
	if err := db.Model(&topic).Updates(map[string]any{"chat_id": int64(-9007199254740993), "thread_id": int64(9007199254740993)}).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := NewAdmin(db).TopicOperation(context.Background(), topic.OperationID)
	if err != nil || detail.Mapping == nil || detail.Mapping.ID != topic.ID || detail.OperationVersion == "" {
		t.Fatal(detail, err)
	}
	raw, err := json.Marshal(detail)
	if err != nil || !strings.Contains(string(raw), `"chat_id":"-9007199254740993"`) || !strings.Contains(string(raw), `"thread_id":"9007199254740993"`) {
		t.Fatal(string(raw), err)
	}
	if err = db.Model(&d.TopicMapping{}).Where("id = ?", topic.ID).Update("bot_id", 2).Error; err != nil {
		t.Fatal(err)
	}
	detail, err = NewAdmin(db).TopicOperation(context.Background(), topic.OperationID)
	if err != nil || detail.Mapping != nil {
		t.Fatal("cross bot mapping", detail, err)
	}
}

// TestTopicDeletionOperationCAS rejects a newer operation with the same mapping.
func TestTopicDeletionOperationCAS(t *testing.T) {
	store, db := fixture(t)
	now := time.Now().UTC()
	topic := createRecoveryTopic(t, store, db, now)
	var operation d.TopicOperation
	if err := db.First(&operation, topic.OperationID).Error; err != nil {
		t.Fatal(err)
	}
	in := c.TopicDeletionConfirmation{OperationID: operation.ID, OperationVersion: operation.Version + 1, ActorID: 9, Generation: 1, ExpectedVersion: topic.Version, ChatID: topic.ChatID, ThreadID: topic.ThreadID, RouteVersion: topic.RouteVersion, Evidence: "ticket:1"}
	if err := store.ConfirmTopicDeleted(context.Background(), 1, topic.ID, in, now); err != c.ErrConflict {
		t.Fatal(err)
	}
	in.OperationVersion = operation.Version
	if err := store.ConfirmTopicDeleted(context.Background(), 1, topic.ID, in, now); err != nil {
		t.Fatal(err)
	}
}

// TestTopicBackoffSurvivesRestartAndGeneration checks bot-wide persisted 429
// deadlines for repeated scans, another session, restart and execution fencing.
func TestTopicBackoffSurvivesRestartAndGeneration(t *testing.T) {
	store, db := fixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	binding := c.DeliveryBinding{ChatID: -100, RouteVersion: 1}
	allowTestRoute(store, binding)
	for index := 1; index <= 2; index++ {
		if err := db.Create(&d.Session{ID: uint(index), BotID: 1, UserID: int64(index), ChatID: int64(index), State: "open"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	job, err := store.ClaimTopic(ctx, 1, 1, 1, 1, 1, -100, "initial", now, time.Minute)
	if err != nil || !job.Execute {
		t.Fatal(job, err)
	}
	// Reserve the bot while the response is in flight, including other sessions.
	preclaimed, err := store.ClaimTopic(ctx, 1, 2, 1, 1, 1, -100, "concurrent", now, time.Minute)
	if err != nil || preclaimed.Execute {
		t.Fatal("another session bypassed in-flight serialization", preclaimed, err)
	}
	if err = store.FinishTopic(ctx, 1, job.Operation.ID, 1, "initial", c.TopicResult{State: "failed", RetryAfterSeconds: 120}, now); err != nil {
		t.Fatal(err)
	}
	var saved d.TopicOperation
	if err = db.First(&saved, job.Operation.ID).Error; err != nil || saved.RetryAfterSeconds != 120 || saved.RetryNotBefore == nil || !saved.RetryNotBefore.Equal(now.Add(120*time.Second)) {
		t.Fatal(saved, err)
	}
	store = New(db)
	allowTestRoute(store, binding)
	if err = db.Model(&rd.Instance{}).Where("id = ?", 1).Update("generation", 2).Error; err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{0, time.Second, 119 * time.Second, 120*time.Second - time.Nanosecond} {
		for _, session := range []uint{1, 2} {
			for range 3 {
				blocked, err := store.ClaimTopic(ctx, 1, session, 1, 1, 2, -100, "early", now.Add(elapsed), time.Minute)
				if err != nil || blocked.Execute {
					t.Fatal("deadline bypass", elapsed, session, blocked, err)
				}
			}
		}
	}
	if _, err = store.ClaimTopic(ctx, 1, 1, 1, 1, 1, -100, "stale", now.Add(120*time.Second), time.Minute); err == nil {
		t.Fatal("stale generation claimed")
	}
	if err = store.FinishTopic(ctx, 1, job.Operation.ID, 1, "initial", c.TopicResult{State: "succeeded", ThreadID: 44}, now); err == nil {
		t.Fatal("stale result accepted")
	}
	retry, err := store.ClaimTopic(ctx, 1, 1, 1, 1, 2, -100, "retry", now.Add(120*time.Second), time.Minute)
	if err != nil || !retry.Execute || retry.Operation.ID != job.Operation.ID {
		t.Fatal("deadline did not release same slot", retry, err)
	}
}

// createRecoveryTopic creates a real operation/mapping pair using repository ports.
// t is the caller, store/db are its fixture, now is its clock; returns the mapping.
func createRecoveryTopic(t *testing.T, store *Store, db *gorm.DB, now time.Time) d.TopicMapping {
	t.Helper()
	allowTestRoute(store, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	if err := db.Create(&d.Session{ID: 1, BotID: 1, UserID: 10, ChatID: 10, State: "open"}).Error; err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimTopic(context.Background(), 1, 1, 1, 1, 1, -100, "create", now, time.Minute)
	if err != nil || !job.Execute {
		t.Fatal(job, err)
	}
	if err = store.FinishTopic(context.Background(), 1, job.Operation.ID, 1, "create", c.TopicResult{State: "succeeded", ThreadID: 42}, now); err != nil {
		t.Fatal(err)
	}
	var topic d.TopicMapping
	if err = db.Where("operation_id = ?", job.Operation.ID).Take(&topic).Error; err != nil {
		t.Fatal(err)
	}
	return topic
}

// TestTopicReopenBackoffBlocksAllActions ensures retry_after also fences reopen
// and that unknown creation is never retried merely because its deadline passed.
func TestTopicReopenBackoffBlocksAllActions(t *testing.T) {
	store, db := fixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	topic := createRecoveryTopic(t, store, db, now)
	closeJob, err := store.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, "close", "close", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishTopic(ctx, 1, closeJob.Operation.ID, 1, "close", c.TopicResult{State: "succeeded"}, now); err != nil {
		t.Fatal(err)
	}
	if err = db.First(&topic, topic.ID).Error; err != nil {
		t.Fatal(err)
	}
	job, err := store.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, "reopen", "reopen", now, time.Minute)
	if err != nil || !job.Execute {
		t.Fatal(job, err)
	}
	if err = store.FinishTopic(ctx, 1, job.Operation.ID, 1, "reopen", c.TopicResult{State: "failed", RetryAfterSeconds: 30}, now); err != nil {
		t.Fatal(err)
	}
	if err = db.First(&topic, topic.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"reopen", "delete"} {
		blocked, err := store.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, action, "early", now.Add(29*time.Second), time.Minute)
		if err != nil || blocked.Execute {
			t.Fatal(action, blocked, err)
		}
	}
	retry, err := store.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, "reopen", "retry", now.Add(30*time.Second), time.Minute)
	if err != nil || !retry.Execute {
		t.Fatal(retry, err)
	}
	if err = store.FinishTopic(ctx, 1, retry.Operation.ID, 1, "retry", c.TopicResult{State: "unknown"}, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.ClaimTopic(ctx, 1, 1, 1, 1, 1, -100, "unknown", now.Add(time.Hour), time.Minute)
	if err != nil || blocked.Execute || blocked.Operation.State != "unknown" {
		t.Fatal(blocked, err)
	}
}

// TestConfirmedDeletionReplacementKeepsHistoricalOwnership verifies exact
// physical CAS, concurrent replacement, restart and old topic reply isolation.
func TestConfirmedDeletionReplacementKeepsHistoricalOwnership(t *testing.T) {
	store, db := fixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	topic := createRecoveryTopic(t, store, db, now)
	confirm := c.TopicDeletionConfirmation{ActorID: 9, Generation: 1, ExpectedVersion: topic.Version, ChatID: -100, ThreadID: 42, RouteVersion: 1, Evidence: "ticket:deleted"}
	for _, mutate := range []func(*c.TopicDeletionConfirmation){
		func(in *c.TopicDeletionConfirmation) { in.Generation++ },
		func(in *c.TopicDeletionConfirmation) { in.ExpectedVersion++ },
		func(in *c.TopicDeletionConfirmation) { in.ChatID = -200 },
		func(in *c.TopicDeletionConfirmation) { in.ThreadID++ },
		func(in *c.TopicDeletionConfirmation) { in.RouteVersion++ },
	} {
		bad := confirm
		mutate(&bad)
		if err := store.ConfirmTopicDeleted(ctx, 1, topic.ID, bad, now); err == nil {
			t.Fatal("untrusted confirmation accepted", bad)
		}
	}
	if err := store.ConfirmTopicDeleted(ctx, 1, topic.ID, confirm, now); err != nil {
		t.Fatal(err)
	}
	// Keep an actual old reply reference: replacement must not authorize it.
	if err := db.Create(&d.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 77, ThreadID: 42, TopicID: topic.ID, SessionID: 1, UserID: 10, BufferedMessageID: 999, RouteVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmTopicDeleted(ctx, 1, topic.ID, confirm, now); err == nil {
		t.Fatal("stale CAS accepted")
	}
	store = New(db)
	allowTestRoute(store, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	var group sync.WaitGroup
	claims := make(chan c.TopicClaim, 8)
	for index := range 8 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			job, err := store.ClaimTopic(ctx, 1, 1, 1, 1, 1, -100, fmt.Sprintf("replacement-%d", index), now, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if job.Execute {
				claims <- job
			}
		}(index)
	}
	group.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatal("duplicate replacement", len(claims))
	}
	replacement := <-claims
	if replacement.Operation.ID == topic.OperationID || replacement.Operation.SlotVersion != 1 {
		t.Fatal(replacement)
	}
	if err := store.FinishTopic(ctx, 1, replacement.Operation.ID, 1, replacement.Operation.Claim, c.TopicResult{State: "succeeded", ThreadID: 42}, now); err == nil {
		t.Fatal("historical thread reused")
	}
	if err := store.FinishTopic(ctx, 1, replacement.Operation.ID, 1, replacement.Operation.Claim, c.TopicResult{State: "succeeded", ThreadID: 43}, now); err != nil {
		t.Fatal(err)
	}
	assertDeletedTopicIsolation(t, store, db, topic, now)
}

// assertDeletedTopicIsolation proves old authorization cannot revive after a
// fresh mapping and verifies durable audit and history without changing them.
func assertDeletedTopicIsolation(t *testing.T, store *Store, db *gorm.DB, old d.TopicMapping, now time.Time) {
	t.Helper()
	if _, err := store.trustedTopic(db, 1, 1, bindingOf(old), now); err == nil {
		t.Fatal("old mapping authorized")
	}
	update := r.Update{Type: "message", ChatID: -100, UserID: 55, ThreadID: 42, IsTopicMessage: true, Reply: &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 42}}
	if reply, err := store.ResolveNativeReply(context.Background(), 1, update, now); err == nil || reply != nil {
		t.Fatal("old native reply escaped", reply, err)
	}
	if _, err := store.trustedTopic(db, 1, 1, c.DeliveryBinding{ChatID: -100, ThreadID: 43, RouteVersion: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&old, old.ID).Error; err != nil || old.State != "deleted" {
		t.Fatal(old, err)
	}
	var count int64
	if err := db.Model(&d.ResolutionAudit{}).Where("resource = ? AND resource_id = ?", "topic_deleted", old.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// A delayed native reopened event cannot resurrect the historical thread.
	event := c.TopicEvent{BotID: 1, TargetID: 1, RouteVersion: 1, Generation: 1, Update: r.Update{ID: 100, Type: "message", MessageKind: "service", MessageID: 100, ChatID: -100, ChatType: "supergroup", ThreadID: 42, IsTopicMessage: true, TopicService: &r.TopicServiceEvent{State: "open", Date: now.Add(time.Second).Unix()}}}
	if err := store.ObserveTopic(r.WithExecutionFence(context.Background(), eventTestFence{}), event, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.trustedTopic(db, 1, 1, bindingOf(old), now); err == nil {
		t.Fatal("event revived deleted mapping")
	}
}
