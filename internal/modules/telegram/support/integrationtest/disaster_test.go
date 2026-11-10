package integrationtest

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// disasterDB returns an isolated, single-connection fixture without external IO.
func disasterDB(t *testing.T) (*gorm.DB, c.DisasterContinuationInput) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "disaster.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	mustDisaster(t, db.AutoMigrate(&rd.Instance{}, &rd.GenerationExit{}, &rd.Contact{}, &rd.Delivery{}, &d.DisasterCheckpoint{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.ReadyDispatch{}, &d.Downstream{}, &d.TopicMapping{}, &d.TopicOperation{}, &d.ReplyMapping{}))
	mustDisaster(t, db.Create(&rd.Instance{ID: 1, TelegramID: 100, Mode: "internal", State: "stopped", Generation: 3}).Error)
	mustDisaster(t, db.Create(&rd.Instance{ID: 2, TelegramID: 200, Mode: "internal", State: "stopped", Generation: 4}).Error)
	mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 1).Updates(map[string]any{"version": 1, "recovery_task_id": 9, "recovery_version": 1, "recovery_generation": 3}).Error)
	now := time.Now().UTC()
	for generation := uint64(1); generation <= 3; generation++ {
		mustDisaster(t, db.Create(&rd.GenerationExit{BotID: 1, Version: 1, Generation: generation, JoinedAt: &now}).Error)
	}
	return db, c.DisasterContinuationInput{Key: "recovery-1", SourceBotID: 1, DestinationBotID: 2, SourceGeneration: 3, ActorID: 5, Evidence: "case:B7"}
}

// disasterStore installs the real transaction-bound runtime drain reader.
func disasterStore(db *gorm.DB) *ss.Store {
	return ss.New(db).WithRecoveryDrain(func(tx *gorm.DB) r.RecoveryFenceStore { return rs.New(tx) })
}

// mustDisaster fails the current test on any storage error.
func mustDisaster(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestDisasterContinuationRestartBoundedAndNoInheritance verifies local isolation,
// restart checkpoints, retained uncertainty and no destination transport identity.
func TestDisasterContinuationRestartBoundedAndNoInheritance(t *testing.T) {
	db, input := disasterDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	mustDisaster(t, db.Create(&d.Session{ID: 1, BotID: 1, UserID: 42, ChatID: 42, State: "open", Version: 1}).Error)
	mustDisaster(t, db.Create(&rd.Contact{BotID: 1, TelegramUserID: 42, ChatID: 42, Reachable: true}).Error)
	mustDisaster(t, db.Create(&d.TopicOperation{ID: 1, BotID: 1, SessionID: 1, TargetID: 1, RouteVersion: 1, State: "unknown", Action: "create", Version: 1}).Error)
	mustDisaster(t, db.Create(&d.TopicMapping{ID: 1, OperationID: 1, BotID: 1, SessionID: 1, TargetID: 1, ChatID: -100, ThreadID: 99, State: "open", Version: 1}).Error)
	for index, state := range []string{"ready", "unknown", "sending"} {
		mustDisaster(t, db.Create(&d.BufferedMessage{ID: uint(index + 1), BotID: 1, SessionID: 1, UpdateID: int64(index + 1), State: state, PayloadCipher: "opaque-file-and-message-id", RetainUntil: now.Add(time.Hour)}).Error)
	}
	mustDisaster(t, db.Create(&d.ReadyDispatch{MessageID: 3, BotID: 1, TopicID: 1, PayloadCipher: "immutable-envelope"}).Error)
	mustDisaster(t, db.Create(&d.Downstream{BotID: 1, SessionID: 1, State: "unknown"}).Error)
	mustDisaster(t, db.Create(&d.Challenge{BotID: 1, SessionID: 1, Ref: "challenge", State: "pending", NoticePending: true}).Error)
	port := disasterStore(db)
	progress, err := port.BeginDisasterContinuation(ctx, input, now)
	mustDisaster(t, err)
	repeat, err := port.BeginDisasterContinuation(ctx, input, now)
	mustDisaster(t, err)
	if repeat != progress {
		t.Fatal("begin retry changed checkpoint")
	}
	for step := 0; !progress.LocalComplete && step < 30; step++ {
		before := progress
		progress, err = disasterStore(db).AdvanceDisasterContinuation(ctx, input.Key, before.Revision, 1, now)
		mustDisaster(t, err)
		if progress.Revision != before.Revision+1 {
			t.Fatal("unbounded or skipped revision")
		}
		if _, err = port.AdvanceDisasterContinuation(ctx, input.Key, before.Revision, 1, now); !errors.Is(err, c.ErrConflict) {
			t.Fatalf("stale CAS: %v", err)
		}
	}
	if !progress.LocalComplete || progress.CancelledMessages != 1 || progress.RetainedMessages != 2 || progress.RetainedReplies != 1 || progress.UncertainTopics != 1 || progress.RetiredTopics != 1 || progress.ClosedSessions != 1 {
		t.Fatalf("wrong scope: %+v", progress)
	}
	var topic d.TopicMapping
	mustDisaster(t, db.First(&topic, 1).Error)
	if topic.State != "retired" || topic.ThreadID != 99 {
		t.Fatal("physical ownership lost")
	}
	var operation d.TopicOperation
	mustDisaster(t, db.First(&operation, 1).Error)
	if !operation.Retired || operation.State != "unknown" {
		t.Fatal("unknown topic was resolved")
	}
	var message d.BufferedMessage
	mustDisaster(t, db.First(&message, 2).Error)
	if message.State != "unknown" || message.PayloadCipher != "opaque-file-and-message-id" {
		t.Fatal("unknown delivery replayed or destroyed")
	}
	for _, model := range []any{&d.Session{}, &d.BufferedMessage{}, &d.TopicMapping{}, &rd.Contact{}, &rd.Delivery{}} {
		var count int64
		mustDisaster(t, db.Model(model).Where("bot_id = ?", 2).Count(&count).Error)
		if count != 0 {
			t.Fatal("cross-bot identity/receipt/reachability inherited")
		}
	}
	resolution := c.Resolution{ActorID: 5, ExpectedVersion: operation.Version, Decision: "succeeded", ThreadID: 100, Evidence: "case:B7"}
	if err := port.ResolveTopic(ctx, 1, 1, resolution, now); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("retired topic revived: %v", err)
	}
}

// TestDisasterLegacyAttemptAndRollback protects runtime-owned ambiguous work and
// verifies checkpoint/data rollback together when a local persistence write fails.
func TestDisasterLegacyAttemptAndRollback(t *testing.T) {
	db, input := disasterDB(t)
	input.DestinationBotID = input.SourceBotID
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	mustDisaster(t, db.Create(&d.BufferedMessage{ID: 1, BotID: 1, SessionID: 7, UpdateID: 1, State: "claimed", PayloadCipher: "keep"}).Error)
	mustDisaster(t, db.Create(&rd.Delivery{BotID: 1, Key: c.ReadyBusinessRef(7, 1), Status: "unknown"}).Error)
	port := disasterStore(db)
	progress, err := port.BeginDisasterContinuation(ctx, input, now)
	mustDisaster(t, err)
	for progress.Phase != "messages" {
		progress, err = port.AdvanceDisasterContinuation(ctx, input.Key, progress.Revision, 1, now)
		mustDisaster(t, err)
	}
	mustDisaster(t, db.Exec("CREATE TRIGGER reject_checkpoint BEFORE UPDATE ON telegram_support_disaster_checkpoints BEGIN SELECT RAISE(ABORT, 'checkpoint failure'); END").Error)
	failed, err := port.AdvanceDisasterContinuation(ctx, input.Key, progress.Revision, 1, now)
	if err == nil || failed.LocalComplete || failed.Revision != 0 {
		t.Fatal("failed checkpoint reported progress")
	}
	persisted, err := port.DisasterContinuationProgress(ctx, input.Key)
	mustDisaster(t, err)
	if persisted != progress {
		t.Fatal("checkpoint advanced despite rollback")
	}
	mustDisaster(t, db.Exec("DROP TRIGGER reject_checkpoint").Error)
	progress, err = port.AdvanceDisasterContinuation(ctx, input.Key, progress.Revision, 1, now)
	mustDisaster(t, err)
	if progress.RetainedMessages != 1 || progress.CancelledMessages != 0 {
		t.Fatalf("legacy attempt incorrectly cancelled: %+v", progress)
	}
	var message d.BufferedMessage
	mustDisaster(t, db.First(&message, 1).Error)
	if message.State != "claimed" || message.PayloadCipher != "keep" {
		t.Fatal("legacy runtime source mutated")
	}
}

// TestDisasterContinuationFencesAndConcurrentCAS verifies single-winner advancement
// and fail-closed runtime fencing with a single SQLite connection.
func TestDisasterContinuationFencesAndConcurrentCAS(t *testing.T) {
	db, input := disasterDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	port := disasterStore(db)
	mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 1).Update("state", "running").Error)
	if _, err := port.BeginDisasterContinuation(ctx, input, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("running source accepted: %v", err)
	}
	mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 1).Update("state", "stopped").Error)
	progress, err := port.BeginDisasterContinuation(ctx, input, now)
	mustDisaster(t, err)
	altered := input
	altered.DestinationBotID = 1
	if _, err := port.BeginDisasterContinuation(ctx, altered, now); !errors.Is(err, c.ErrConflict) {
		t.Fatal("immutable plan changed")
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for attempt := 0; attempt < 2; attempt++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := disasterStore(db).AdvanceDisasterContinuation(ctx, input.Key, progress.Revision, 1, now)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners=%d", wins)
	}
	persisted, err := port.DisasterContinuationProgress(ctx, input.Key)
	mustDisaster(t, err)
	mustDisaster(t, db.Model(&rd.Instance{}).Where("id = ?", 1).Update("generation", 4).Error)
	if _, err := port.AdvanceDisasterContinuation(ctx, input.Key, persisted.Revision, 1, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("generation changed: %v", err)
	}
}
