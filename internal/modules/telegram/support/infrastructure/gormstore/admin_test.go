package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	supportapp "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestVerificationCardDurations checks day/week/month/permanent support-only expiry semantics.
func TestVerificationCardDurations(t *testing.T) {
	for _, days := range []int{1, 7, 30, 0} {
		t.Run(fmt.Sprintf("days_%d", days), func(t *testing.T) {
			store, db, _, now := operationFixture(t)
			var owner d.Session
			if err := db.First(&owner, 1).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.transaction(context.Background(), func(tx *gorm.DB) error {
				return applyVerificationBlock(tx, d.OperationReference{ActorID: 55, BlockDays: days}, owner, now)
			}); err != nil {
				t.Fatal(err)
			}
			var block d.SupportBlock
			if err := db.First(&block).Error; err != nil {
				t.Fatal(err)
			}
			if days == 0 {
				if block.ExpiresAt != nil {
					t.Fatal("permanent block expires")
				}
				return
			}
			if block.ExpiresAt == nil || !block.ExpiresAt.Equal(now.Add(time.Duration(days)*24*time.Hour)) {
				t.Fatal("wrong duration", block)
			}
		})
	}
}

// TestVerificationCardDailyReceiptAndBlock covers identity-only cards, exact receipts and idempotency.
func TestVerificationCardDailyReceiptAndBlock(t *testing.T) {
	store, db, update, now := operationFixture(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	store.cipher = runtimeapp.NewManager(nil, nil, "synthetic-verification-card")
	must(db.AutoMigrate(&d.OperationControl{}))
	must(db.Model(&d.Session{}).Where("id = 1").Updates(map[string]any{"state": "waiting_verification", "display_name": "测试昵称", "username": "tester"}).Error)
	must(db.Create(&d.Challenge{BotID: 1, SessionID: 1, UserID: 10, ChatID: 10, Ref: "card-challenge", State: "pending", Version: 1, CreatedAt: now}).Error)
	must(db.Create(&d.BufferedMessage{BotID: 1, SessionID: 1, UpdateID: 300, State: "buffered", PayloadCipher: "never-forward", CreatedAt: now}).Error)
	rows, err := store.VerificationSessions(ctx, 1, 0, 50, now)
	must(err)
	if len(rows) != 1 {
		t.Fatal("intercepted session missing", rows)
	}
	card, err := store.EnsureVerificationControl(ctx, 1, 1, 1, now)
	must(err)
	again, err := store.EnsureVerificationControl(ctx, 1, 1, 1, now.Add(time.Minute))
	must(err)
	if card.ID != again.ID || card.ActorID != 0 {
		t.Fatal("daily card duplicated or actor forged")
	}
	plain, err := store.cipher.Open(card.PayloadCipher)
	must(err)
	var message r.Message
	must(json.Unmarshal([]byte(plain), &message))
	if message.Text != "对方ID：10\n消息来自：@tester" || len(message.Buttons) != 1 || len(message.Buttons[0]) != 4 {
		t.Fatal(message)
	}
	_, err = store.CheckControl(ctx, 1, message, now)
	must(err)
	if _, err = store.trustedTopic(db, 1, 1, c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}, now); !errors.Is(err, c.ErrDenied) {
		t.Fatal("ordinary content authorized", err)
	}
	update.MessageID = 900
	ref := strings.TrimPrefix(message.Buttons[0][2].Data, c.OperationCallbackPrefix)
	if err = store.ApplyVerificationOperation(ctx, 1, update, ref, 1, now); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unsent card accepted", err)
	}
	must(db.Create(&rd.Delivery{BotID: 1, Key: message.BusinessRef, ChatID: -100, TargetType: string(r.TargetSupportGroup), Purpose: c.OperationControlPurpose, Operation: "send", PayloadCipher: card.PayloadCipher, Status: "sent", MessageID: 900, MessageIDs: []int64{900}}).Error)
	update.MessageID = 901
	if err = store.ApplyVerificationOperation(ctx, 1, update, ref, 1, now); !errors.Is(err, c.ErrDenied) {
		t.Fatal("copied card accepted", err)
	}
	update.MessageID, update.UserID = 900, 777
	must(store.ApplyVerificationOperation(ctx, 1, update, ref, 1, now))
	var block d.SupportBlock
	must(db.First(&block).Error)
	if block.ExpiresAt == nil || !block.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || block.ActorID != 777 {
		t.Fatal(block)
	}
	otherRef := strings.TrimPrefix(message.Buttons[0][3].Data, c.OperationCallbackPrefix)
	must(store.ApplyVerificationOperation(ctx, 1, update, otherRef, 1, now.Add(time.Minute)))
	must(db.First(&block).Error)
	if block.ExpiresAt == nil || !block.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatal("repeat click extended block", block)
	}
	var buffered d.BufferedMessage
	must(db.First(&buffered).Error)
	if buffered.State != "cancelled" || buffered.PayloadCipher != "" {
		t.Fatal("blocked buffer retained", buffered)
	}
	var audits int64
	must(db.Model(&d.OperationAudit{}).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("duplicate audit", audits)
	}
	must(db.Model(&gd.Target{}).Where("id = 1").UpdateColumn("route_version", 2).Error)
	if err = store.ApplyVerificationOperation(ctx, 1, update, ref, 1, now); !errors.Is(err, c.ErrDenied) {
		t.Fatal("stale card accepted", err)
	}
}

// TestPendingConsultationVisibleOnlyToAdmin verifies that durable buffered text
// is auditable in admin without becoming eligible for the merchant's delivery queue.
func TestPendingConsultationVisibleOnlyToAdmin(t *testing.T) {
	store, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cipher := runtimeapp.NewManager(nil, nil, "synthetic-admin-captcha")
	source := r.Update{ID: 100, UserID: 42, ChatID: 42, Private: true, MessageID: 10, MessageKind: "text", Text: "pending consultation"}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Accept(ctx, 1, source, encrypted, int64(len(raw)), "synthetic-pending", c.DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	var message d.BufferedMessage
	if err = db.Where("update_id = ?", source.ID).First(&message).Error; err != nil || message.State != "buffered" {
		t.Fatal("unverified message was not buffered", err)
	}
	audited := false
	admin := supportapp.NewAdmin(NewAdmin(db), cipher, func(_ context.Context, actor uint, path string, exact bool) error {
		if actor != 9 || path != c.AdminContentPath || !exact {
			return c.ErrDenied
		}
		return nil
	}, func(_ context.Context, actor, session, id uint, result string) error {
		audited = actor == 9 && session == outcome.Session.ID && id == message.ID && result == "viewed"
		return nil
	})
	content, err := admin.Content(ctx, 9, outcome.Session.ID, message.ID)
	if err != nil || !content.Available || content.Text != source.Text || !audited {
		t.Fatal("pending content unavailable to authorized admin", err)
	}
	ready, err := store.Ready(ctx, 1, outcome.Session.ID, 20, now)
	if err != nil || len(ready) != 0 {
		t.Fatal("pending consultation reached merchant delivery", err)
	}
	if _, err = admin.Content(ctx, 8, outcome.Session.ID, message.ID); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unauthorized admin read", err)
	}
}

// TestRecoveryFenceRejectsPolicyWrites proves policy creation and CAS share runtime locks.
// A single connection detects accidental global DB reads inside the guarded transaction.
func TestRecoveryFenceRejectsPolicyWrites(t *testing.T) {
	store, db := fixture(t)
	if err := db.AutoMigrate(&rd.Instance{}, &d.BotPolicy{}); err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range []uint{81, 82} {
		if err := db.Create(&rd.Instance{ID: id, TelegramID: int64(90000 + id), Version: 1, Mode: "internal", State: "stopped"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	store.WithPolicyRecoveryGuard(func(tx *gorm.DB, bot uint) error {
		return runtimestore.New(tx).GuardRecoveryWrite(ctx, bot)
	})
	policy := c.DefaultPolicy()
	if _, err := store.SavePolicy(ctx, 81, 9, 0, policy, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{81, 82} {
		if err := runtimestore.New(db).BlockRecovery(ctx, r.RecoveryFence{BotID: id, TaskID: 91, Version: 1}); err != nil {
			t.Fatal(err)
		}
	}
	policy.MaxMessages++
	for id, expected := range map[uint]uint64{81: 1, 82: 0} {
		if _, err := store.SavePolicy(ctx, id, 9, expected, policy, time.Now().UTC()); !errors.Is(err, r.ErrRecoveryBlocked) {
			t.Fatalf("policy %d bypassed fence: %v", id, err)
		}
	}
	actual, version, err := store.Policy(ctx, 81)
	if err != nil || version != 1 || actual.MaxMessages != c.DefaultPolicy().MaxMessages {
		t.Fatalf("policy mutated: %+v %d %v", actual, version, err)
	}
}

// TestBotPolicyCASDefaultIsolation exercises durable CAS including competing first writes.
func TestBotPolicyCASDefaultIsolation(t *testing.T) {
	store, db := fixture(t)
	if err := db.AutoMigrate(&d.BotPolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	policy, version, err := store.Policy(ctx, 1)
	if err != nil || version != 0 || policy != c.DefaultPolicy() {
		t.Fatal(policy, version, err)
	}
	policy.MaxMessages = 1
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := store.SavePolicy(ctx, 1, 9, 0, policy, now); results <- err }()
	}
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal(successes)
	}
	isolated, v, err := store.Policy(ctx, 2)
	if err != nil || v != 0 || isolated != c.DefaultPolicy() {
		t.Fatal(isolated, v, err)
	}
	policy.MaxMessages = 2
	if _, err = store.SavePolicy(ctx, 1, 9, 1, policy, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SavePolicy(ctx, 1, 9, 1, policy, now); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	restarted := New(db)
	actual, v, err := restarted.Policy(ctx, 1)
	if err != nil || v != 2 || actual.MaxMessages != 2 {
		t.Fatal(actual, v, err)
	}
	policy.MaxAttempts = 0
	if _, err = store.SavePolicy(ctx, 1, 9, 2, policy, now); !errors.Is(err, c.ErrPolicyInvalid) {
		t.Fatal(err)
	}
	if err = db.Model(&d.BotPolicy{}).Where("bot_id = ?", 1).Update("max_attempts", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Policy(ctx, 1); !errors.Is(err, c.ErrPolicyInvalid) {
		t.Fatal("corruption silently defaulted", err)
	}
}

// policyIngressAdapters emulate only transport readiness/queue, not persistence or policy behavior.
type policyIngressAdapters struct{}

func (policyIngressAdapters) Modules(context.Context, uint) ([]g.ModuleView, error) {
	return []g.ModuleView{{ModuleConfig: gd.ModuleConfig{Partition: "support", Enabled: true, TargetID: 1}, EffectiveEnabled: true}}, nil
}
func (policyIngressAdapters) Ready(context.Context, uint, uint) (bool, error)          { return true, nil }
func (policyIngressAdapters) EnqueueRequest(context.Context, *r.DeliveryRequest) error { return nil }
func (policyIngressAdapters) Seal(value string) (string, error)                        { return value, nil }
func (policyIngressAdapters) Open(value string) (string, error)                        { return value, nil }

// TestBotPolicyHandleAndHistoricalChallenge proves runtime uses each bot's policy while snapshots stay immutable.
func TestBotPolicyHandleAndHistoricalChallenge(t *testing.T) {
	store, db := fixture(t)
	if err := db.Create(&rd.Instance{ID: 2, TelegramID: 101, Generation: 1, Enabled: true, Mode: "internal", State: "running"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&d.BotPolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	policy := c.DefaultPolicy()
	policy.MaxMessages = 1
	if _, err := store.SavePolicy(ctx, 1, 9, 0, policy, now); err != nil {
		t.Fatal(err)
	}
	adapters := policyIngressAdapters{}
	store.cipher = adapters
	service := supportapp.New(store, adapters, adapters, adapters).WithPolicies(store)
	service.Now = func() time.Time { return now }
	// Production assembly remains closed even with a persisted policy.
	if enabled, err := service.ReadyEnabled(ctx, 1); err != nil || enabled {
		t.Fatal(enabled, err)
	}
	service.DeliveryReady = adapters
	update := r.Update{ID: 10, UserID: 10, ChatID: 10, Private: true, MessageID: 10, Text: "consultation"}
	for _, bot := range []uint{1, 2} {
		for _, id := range []int64{10, 11} {
			update.ID = id
			update.MessageID = id
			if _, err := service.Handle(ctx, bot, update); err != nil {
				t.Fatal(err)
			}
		}
	}
	var rejected d.BufferedMessage
	if err := db.Where("bot_id = ? AND update_id = ?", 1, 11).First(&rejected).Error; err != nil || rejected.Reason != "limit" {
		t.Fatal(rejected, err)
	}
	var other d.BufferedMessage
	if err := db.Where("bot_id = ? AND update_id = ?", 2, 11).First(&other).Error; err != nil || other.State != "buffered" {
		t.Fatal(other, err)
	}
	var old d.Challenge
	if err := db.Where("bot_id = ?", 1).First(&old).Error; err != nil {
		t.Fatal(err)
	}
	// Policy edits cannot shorten an existing arithmetic challenge or grant extra guesses.
	policy.MaxAttempts = 1
	policy.ChallengeTTL = time.Minute
	policy.Retention = 2 * time.Minute
	policy.MaxMessages = 5
	if _, err := store.SavePolicy(ctx, 1, 9, 1, policy, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	update = r.Update{ID: 12, UserID: 10, ChatID: 10, Private: true, MessageID: 80, CallbackID: "click", CallbackData: "support:" + c.VerificationAnswer(old, old.AnswerIndex)}
	if _, err := service.Handle(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	var current d.Challenge
	if err := db.First(&current, old.ID).Error; err != nil || current.State != "passed" || current.Attempts != 1 || current.MaxAttempts != old.MaxAttempts || !current.ExpiresAt.Equal(old.ExpiresAt) {
		t.Fatal(current, err)
	}
	var original d.BufferedMessage
	if err := db.Where("bot_id = ? AND update_id = ?", 1, 10).First(&original).Error; err != nil || original.State != "ready" || !original.RetainUntil.Equal(old.CreatedAt.Add(c.DefaultPolicy().Retention)) {
		t.Fatal(original, err)
	}
	// A new user's challenge uses the new limits, and no text input can bypass the click gate.
	update = r.Update{ID: 13, UserID: 20, ChatID: 20, Private: true, MessageID: 13, Text: "consultation"}
	if _, err := service.Handle(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	var fresh d.Challenge
	if err := db.Where("bot_id = ? AND user_id = ?", 1, 20).First(&fresh).Error; err != nil || fresh.State != "pending" || fresh.MaxAttempts != 1 || !fresh.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal(fresh, err)
	}
}

// TestBotPolicyExpiredChallengeCannotBeRevived checks that longer new TTL/attempt policies are not retroactive.
func TestBotPolicyExpiredChallengeCannotBeRevived(t *testing.T) {
	store, db := fixture(t)
	if err := db.AutoMigrate(&d.BotPolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	policy := c.DefaultPolicy()
	policy.ChallengeTTL = time.Minute
	policy.MaxAttempts = 1
	if _, err := store.SavePolicy(ctx, 1, 9, 0, policy, now); err != nil {
		t.Fatal(err)
	}
	adapters := policyIngressAdapters{}
	store.cipher = adapters
	service := supportapp.New(store, adapters, adapters, adapters).WithPolicies(store)
	service.DeliveryReady = adapters
	service.Now = func() time.Time { return now }
	update := r.Update{ID: 30, UserID: 30, ChatID: 30, Private: true, MessageID: 30, Text: "question"}
	if _, err := service.Handle(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	var original d.Challenge
	if err := db.Where("bot_id = ? AND user_id = ?", 1, 30).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	policy.ChallengeTTL = time.Hour
	policy.MaxAttempts = 10
	if _, err := store.SavePolicy(ctx, 1, 9, 1, policy, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	update.ID = 31
	update.CallbackID = "click"
	update.CallbackData = "support:" + c.VerificationAnswer(original, original.AnswerIndex)
	if _, err := service.Handle(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	var current d.Challenge
	if err := db.First(&current, original.ID).Error; err != nil || current.State != "expired" || current.MaxAttempts != 1 || !current.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatal(current, err)
	}
}

// operationFixture creates authoritative routes on a single SQLite connection.
func operationFixture(t *testing.T) (*Store, *gorm.DB, r.Update, time.Time) {
	t.Helper()
	store, db := fixture(t)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}, &d.OperationReference{}, &d.OperationAudit{}))
	must(db.Create(&gd.Target{ID: 1, Purpose: "support", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -100, OccupancySlot: "-100", Observation: gd.Observation{Generation: 1, RouteVersion: 1, BotTelegramID: 100, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, IsForum: true, CanManageTopics: true, State: "ready", ObservedAt: &now}}).Error)
	must(db.Create(&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 1, Version: 1}).Error)
	must(db.Create(&d.Session{ID: 1, BotID: 1, UserID: 10, ChatID: 10, State: "open", Version: 1}).Error)
	must(db.Create(&d.TopicMapping{ID: 1, OperationID: 1, BotID: 1, SessionID: 1, TargetID: 1, ChatID: -100, ThreadID: 20, RouteVersion: 1, State: "open", Version: 1}).Error)
	return store, db, r.Update{ID: 3, UserID: 55, ChatID: -100, ThreadID: 20, IsTopicMessage: true, ChatType: "supergroup", CallbackID: "callback"}, now
}

// TestOperationsCASAuditAndReopen covers concurrent replay and fresh consultation.
func TestOperationsCASAuditAndReopen(t *testing.T) {
	store, db, update, now := operationFixture(t)
	ctx := context.Background()
	ref, err := store.PrepareOperation(ctx, 1, update, 1, 1, c.OperationClose, "", strings.Repeat("a", 24), now)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := store.ApplyOperation(ctx, 1, update, ref.Ref, now); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	var count int64
	if err = db.Model(&d.OperationAudit{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
	source := r.Update{ID: 100, UserID: 10, ChatID: 10, MessageID: 100, Private: true, Text: "new consultation"}
	out, err := store.Accept(ctx, 1, source, "cipher", 6, "new-challenge", c.DefaultPolicy(), now)
	if err != nil || out.Session.State != "waiting_verification" || out.Challenge == nil {
		t.Fatal(out, err)
	}
	if _, err = store.Verify(ctx, 1, r.Update{ID: 101, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), now); err != nil {
		t.Fatal(err)
	}
}

// TestOperationStaleCASRouteAndActor verifies all persisted callback bindings.
func TestOperationStaleCASRouteAndActor(t *testing.T) {
	for _, cause := range []string{"route", "session", "actor", "topic", "expired"} {
		t.Run(cause, func(t *testing.T) {
			store, db, update, now := operationFixture(t)
			ctx := context.Background()
			ref, err := store.PrepareOperation(ctx, 1, update, 1, 1, c.OperationBlock, "", strings.Repeat("b", 24), now)
			if err != nil {
				t.Fatal(err)
			}
			switch cause {
			case "route":
				err = db.Model(&gd.Target{}).Where("id = 1").Update("route_version", 2).Error
			case "session":
				err = db.Model(&d.Session{}).Where("id = 1").Update("version", 2).Error
			case "actor":
				update.UserID = 56
			case "topic":
				update.ThreadID++
			case "expired":
				now = now.Add(6 * time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = store.ApplyOperation(ctx, 1, update, ref.Ref, now); !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrConflict) {
				t.Fatal(err)
			}
			var count int64
			if err = db.Model(&d.OperationAudit{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}

// TestOperationAuditFailureRollsBackMutation checks journal and mutation atomicity.
func TestOperationAuditFailureRollsBackMutation(t *testing.T) {
	store, db, update, now := operationFixture(t)
	ctx := context.Background()
	ref, err := store.PrepareOperation(ctx, 1, update, 1, 1, c.OperationBlock, "", strings.Repeat("c", 24), now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Callback().Create().Before("gorm:create").Register("audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_support_operation_audits" {
			tx.AddError(errors.New("audit unavailable"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ApplyOperation(ctx, 1, update, ref.Ref, now); err == nil {
		t.Fatal("audit failure ignored")
	}
	var session d.Session
	if err = db.First(&session, 1).Error; err != nil || session.Version != 1 {
		t.Fatal(session, err)
	}
	var count int64
	if err = db.Model(&d.SupportBlock{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
	loaded, err := store.Operation(ctx, 1, ref.Ref)
	if err != nil || loaded.ConsumedAt != nil {
		t.Fatal(loaded, err)
	}
}

// TestOperationBlockUnblockAndInternalNote ensures notes never enter delivery.
func TestOperationBlockUnblockAndInternalNote(t *testing.T) {
	store, db, update, now := operationFixture(t)
	ctx := context.Background()
	for index, action := range []string{c.OperationBlock, c.OperationUnblock, c.OperationNote} {
		note := ""
		if action == c.OperationNote {
			note = "encrypted-internal-note"
		}
		ref, err := store.PrepareOperation(ctx, 1, update, 1, 1, action, note, strings.Repeat(string(rune('d'+index)), 24), now)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.ApplyOperation(ctx, 1, update, ref.Ref, now); err != nil {
			t.Fatal(err)
		}
	}
	var block d.SupportBlock
	if err := db.First(&block).Error; err != nil || block.Enabled || block.Version != 2 {
		t.Fatal(block, err)
	}
	var count int64
	if err := db.Model(&d.Downstream{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("note escaped", count, err)
	}
	var audit d.OperationAudit
	if err := db.Where("action = ?", c.OperationNote).First(&audit).Error; err != nil || audit.NoteCipher != "encrypted-internal-note" {
		t.Fatal(audit, err)
	}
}

// TestCompetingOperationReferences permits exactly one session revision mutation.
func TestCompetingOperationReferences(t *testing.T) {
	store, db, update, now := operationFixture(t)
	ctx := context.Background()
	refs := make([]string, 2)
	for index, action := range []string{c.OperationBlock, c.OperationClose} {
		entry, err := store.PrepareOperation(ctx, 1, update, 1, 1, action, "", strings.Repeat(string(rune('x'+index)), 24), now)
		if err != nil {
			t.Fatal(err)
		}
		refs[index] = entry.Ref
	}
	results := make(chan error, 2)
	for _, ref := range refs {
		go func(ref string) { results <- store.ApplyOperation(ctx, 1, update, ref, now) }(ref)
	}
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&d.OperationAudit{}).Count(&count).Error; err != nil || successes != 1 || count != 1 {
		t.Fatal(successes, count, err)
	}
}

type adminResolutionAuthority struct{}

func (adminResolutionAuthority) AuthorizeSupportResolution(_ context.Context, actor, bot uint) error {
	if actor != 9 || bot != 1 {
		return c.ErrDenied
	}
	return nil
}

// TestAdminResolutionUsesPersistedProof exercises AdminService -> DownstreamResolver -> transactional CAS.
// Unknown/forged success must remain unknown; only a stored sent receipt completes the task.
func TestAdminResolutionUsesPersistedProof(t *testing.T) {
	store, db, now, source := downstreamAlbumFixture(t)
	source.MediaGroupID = ""
	ctx := context.Background()
	row, err := store.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.DownstreamRequest(ctx, 1, row.ID, now)
	if err != nil || request == nil {
		t.Fatal(err)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: request.ChatID, TelegramUserID: request.TelegramUserID, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "unknown", PayloadHeld: true}
	if err = db.Create(&proof).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReconcileDownstream(ctx, 1, row.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	if err = db.First(row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	query := NewAdmin(db)
	service := supportapp.NewAdmin(query, store.cipher, func(context.Context, uint, string, bool) error { return nil }, nil).WithDownstreams(query, &supportapp.DownstreamResolver{Store: store, Authorizer: adminResolutionAuthority{}}, func(context.Context, uint) (uint64, error) { return 1, nil })
	page, err := service.Downstreams(ctx, 9, c.AdminFilter{Page: 1, PageSize: 20, State: "unknown", BotID: 1})
	if err != nil || page.Total != 1 || page.Items[0].Version != row.Version {
		t.Fatal(page, err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cipher", "file_id", "payload", "claim", "token", "source_chat_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("metadata leak", secret)
		}
	}
	input := c.AdminResolutionInput{ExpectedVersion: row.Version, Decision: "delivered", Evidence: "ticket:confirmed", DeliveryID: proof.ID}
	if err = service.ResolveDownstream(ctx, 9, row.ID, input); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unproven success", err)
	}
	if err = db.First(row, row.ID).Error; err != nil || row.State != "unknown" || row.Version != input.ExpectedVersion {
		t.Fatal(row, err)
	}
	if err = db.Model(&proof).Updates(map[string]any{"status": "sent", "message_id": 100, "message_ids": "[100]"}).Error; err != nil {
		t.Fatal(err)
	}
	if err = service.ResolveDownstream(ctx, 9, row.ID, input); err != nil {
		t.Fatal(err)
	}
	if err = db.First(row, row.ID).Error; err != nil || row.State != "delivered" {
		t.Fatal(row, err)
	}
	if err = service.ResolveDownstream(ctx, 9, row.ID, input); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale CAS", err)
	}
	var audit d.ResolutionAudit
	if err = db.Where("resource = ? AND resource_id = ?", "downstream", row.ID).Take(&audit).Error; err != nil || audit.ActorID != 9 || audit.Evidence != "ticket:confirmed" {
		t.Fatal(audit, err)
	}
}

// TestAdminAlbumMembersKeepOriginalPayload exercises the real Accept/collectAlbum
// path, then reads each member through the authorized admin service. The album
// aggregate never replaces the original Update ciphertext or exposes references.
func TestAdminAlbumMembersKeepOriginalPayload(t *testing.T) {
	store, db := fixture(t)
	if err := db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}); err != nil {
		t.Fatal(err)
	}
	cipher := runtimeapp.NewManager(nil, nil, "synthetic-admin-album-key")
	ctx, now := context.Background(), time.Now().UTC()
	sources := []r.Update{
		{ID: 101, UserID: 10, ChatID: 10, Private: true, MessageID: 51, MessageKind: "photo", MediaGroupID: "private-album-reference", Caption: "photo caption", Media: []r.Media{{Type: "photo", FileID: "private-photo-file", FileUniqueID: "private-photo-unique"}}, CaptionEntities: []r.MessageEntity{{Type: "text_link", URL: "https://private.invalid"}}},
		{ID: 102, UserID: 10, ChatID: 10, Private: true, MessageID: 52, MessageKind: "video", MediaGroupID: "private-album-reference", Caption: "video caption", Media: []r.Media{{Type: "video", FileID: "private-video-file", FileUniqueID: "private-video-unique"}}},
	}
	var session uint
	encryptedMembers := make([]string, 0, len(sources))
	for _, source := range sources {
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		encrypted, err := cipher.Seal(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		encryptedMembers = append(encryptedMembers, encrypted)
		outcome, err := store.Accept(ctx, 1, source, encrypted, int64(len(raw)), "admin-album-challenge", c.DefaultPolicy(), now)
		if err != nil {
			t.Fatal(err)
		}
		session = outcome.Session.ID
	}
	var album d.Album
	if err := db.Where("session_id = ?", session).Take(&album).Error; err != nil {
		t.Fatal(err)
	}
	var members []d.AlbumItem
	if err := db.Where("album_id = ?", album.ID).Order("source_message_id ASC").Find(&members).Error; err != nil {
		t.Fatal(err)
	}
	if album.Count != 2 || len(members) != 2 {
		t.Fatal("album aggregation missing", album.Count, len(members))
	}
	audited := make(map[uint]string)
	service := supportapp.NewAdmin(NewAdmin(db), cipher, func(_ context.Context, actor uint, path string, exact bool) error {
		if actor != 9 || (path == c.AdminContentPath && !exact) {
			return c.ErrDenied
		}
		return nil
	}, func(_ context.Context, actor, sessionID, messageID uint, outcome string) error {
		if actor != 9 || sessionID != session {
			return c.ErrDenied
		}
		audited[messageID] = outcome
		return nil
	})
	for index, member := range members {
		var persisted d.BufferedMessage
		if err := db.First(&persisted, member.BufferedMessageID).Error; err != nil {
			t.Fatal(err)
		}
		if persisted.PayloadCipher != encryptedMembers[index] {
			t.Fatal("original ciphertext was replaced by an album envelope")
		}
		content, err := service.Content(ctx, 9, session, member.BufferedMessageID)
		if err != nil || !content.Available || content.Kind != sources[index].MessageKind || content.Caption != sources[index].Caption {
			t.Fatal(content, err)
		}
		if len(content.MediaKinds) != 1 || content.MediaKinds[0] != sources[index].MessageKind || audited[member.BufferedMessageID] != "viewed" {
			t.Fatal(content, audited)
		}
		assertAdminAlbumProjection(t, content)
	}
	page, err := service.Messages(ctx, 9, session, c.AdminFilter{Page: 1, PageSize: 20})
	if err != nil || page.Total != 2 {
		t.Fatal(page, err)
	}
	assertAdminAlbumProjection(t, page)
}

// assertAdminAlbumProjection checks serialized display/metadata DTOs for forbidden
// transport fields and synthetic secret values; t is the calling test and value
// is the returned DTO. It fails the test on leakage and has no return value.
func assertAdminAlbumProjection(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-", "private.invalid", "file_id", "file_unique_id", "media_group_id", "chat_id", "message_id", "entities", "payload_cipher", "claim", "token"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("album projection leaked %s", forbidden)
		}
	}
}

// TestAdminMetadataProjection tests current operational metadata without loading payloads.
func TestAdminMetadataProjection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = db.AutoMigrate(&d.Session{}, &d.BufferedMessage{}, &d.ReplyMapping{}, &d.Downstream{}, &d.SupportBlock{}, &d.TopicMapping{}, &gd.Target{}, &gd.ModuleConfig{}); err != nil {
		t.Fatal(err)
	}
	session := d.Session{BotID: 2, UserID: 9007199254740993, ChatID: 9007199254740993, State: "open", Version: 1}
	if err = db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	message := d.BufferedMessage{BotID: 2, UpdateID: 1, SessionID: session.ID, State: "ready", Sequence: 1, PayloadCipher: "secret-cipher-file-token", Claim: "secret-claim", RetainUntil: time.Now().Add(time.Hour)}
	if err = db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	store := NewAdmin(db)
	ctx := context.Background()
	filter := c.AdminFilter{Page: 1, PageSize: 20}
	sessions, total, err := store.Sessions(ctx, filter)
	if err != nil || total != 1 || len(sessions) != 1 {
		t.Fatal(sessions, total, err)
	}
	raw, _ := json.Marshal(sessions)
	if !strings.Contains(string(raw), `"user_id":"9007199254740993"`) {
		t.Fatal(string(raw))
	}
	messages, total, err := store.Messages(ctx, session.ID, filter)
	if err != nil || total != 1 || len(messages) != 1 {
		t.Fatal(messages, total, err)
	}
	raw, _ = json.Marshal(messages)
	for _, forbidden := range []string{"secret", "payload", "claim", "file_id", "token"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal(string(raw))
		}
	}
	if _, err = store.Payload(ctx, session.ID+1, message.ID); !errors.Is(err, c.ErrAdminNotFound) {
		t.Fatal("cross-session read", err)
	}
	payload, err := store.Payload(ctx, session.ID, message.ID)
	if err != nil || payload.Cipher != message.PayloadCipher {
		t.Fatal(payload, err)
	}
	if _, err = store.Session(ctx, session.ID+1); !errors.Is(err, c.ErrAdminNotFound) {
		t.Fatal(err)
	}
	filter.BotID = 99
	sessions, total, err = store.Sessions(ctx, filter)
	if err != nil || total != 0 || len(sessions) != 0 {
		t.Fatal(sessions, total, err)
	}
}
