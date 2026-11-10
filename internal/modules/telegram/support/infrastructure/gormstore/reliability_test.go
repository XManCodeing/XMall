package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func TestGuessedReferencesChargeCallerAndSurviveRestart(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	victim, e := s.Accept(ctx, 1, r.Update{ID: 1, UserID: 10, ChatID: 10}, "cipher", 6, "victim", p, now)
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= p.MaxAttempts; i++ {
		out, e := s.Verify(ctx, 1, r.Update{ID: int64(i + 20), UserID: 20, ChatID: 20}, c.VerificationAnswer(*victim.Challenge, victim.Challenge.AnswerIndex), p, now)
		if e != nil {
			t.Fatal(e)
		}
		if i == p.MaxAttempts && out.Reason != "cooldown" {
			t.Fatal(out)
		}
	}
	s = New(db)
	out, e := s.Verify(ctx, 1, r.Update{ID: 10, UserID: 20, ChatID: 20}, "unknown", p, now)
	if e != nil || out.Reason != "cooldown" {
		t.Fatal(out, e)
	}
	var owner d.Session
	db.First(&owner, victim.Session.ID)
	if owner.Attempts != 0 || !owner.CooldownUntil.IsZero() {
		t.Fatal("victim punished")
	}
	out, e = s.Verify(ctx, 1, r.Update{ID: 11, UserID: 10, ChatID: 10}, c.VerificationAnswer(*victim.Challenge, victim.Challenge.AnswerIndex), p, now)
	if e != nil || out.Reason != "passed" {
		t.Fatal(out, e)
	}
	out, e = s.Verify(ctx, 1, r.Update{ID: 12, UserID: 20, ChatID: 20}, "unknown", p, now.Add(p.Cooldown))
	if e != nil || out.Reason != "invalid" {
		t.Fatal(out, e)
	}
}
func TestReadyClaimLeaseUnknownAndOrdering(t *testing.T) {
	s, db := fixture(t)
	target := c.DeliveryBinding{ChatID: -100, ThreadID: 10, RouteVersion: 1}
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	out, e := s.Accept(ctx, 1, r.Update{ID: 1, UserID: 10, ChatID: 10}, "cipher", 6, "ref", p, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Accept(ctx, 1, r.Update{ID: 2, UserID: 10, ChatID: 10}, "cipher2", 7, "ref2", p, now); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, 1, r.Update{ID: 3, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), p, now); e != nil {
		t.Fatal(e)
	}
	installTestTopic(t, s, db, out.Session.ID, target)
	m, e := s.ClaimReady(ctx, 1, out.Session.ID, 1, "first", now, time.Minute)
	if e != nil || m == nil || m.Sequence != 1 {
		t.Fatal(m, e)
	}
	if e = s.AckReady(ctx, 1, m.ID, 1, "first", "delivered", 1, now); e != c.ErrConflict {
		t.Fatal("ack before send", e)
	}
	if e := db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 2).Error; e != nil {
		t.Fatal(e)
	}
	if n, e := s.ClaimReady(ctx, 1, out.Session.ID, 2, "other", now, time.Minute); e != nil || n != nil {
		t.Fatal("concurrent claim", n, e)
	}
	n, e := s.ClaimReady(ctx, 1, out.Session.ID, 2, "new", now.Add(time.Minute), time.Minute)
	if e != nil || n == nil || n.ID != m.ID {
		t.Fatal(n, e)
	}
	if e = s.BeginSend(ctx, 1, n.ID, 1, "first", target, now); e != c.ErrConflict {
		t.Fatal("stale fence", e)
	}
	if e = s.BeginSend(ctx, 1, n.ID, 2, "new", target, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 3).Error; e != nil {
		t.Fatal(e)
	}
	if n, e = s.ClaimReady(ctx, 1, out.Session.ID, 3, "third", now.Add(2*time.Minute), time.Minute); e != nil || n != nil {
		t.Fatal("unknown retried", n, e)
	}
	rows, e := s.Ready(ctx, 1, out.Session.ID, 20, now)
	if e != nil || len(rows) != 1 || rows[0].Sequence != 2 {
		t.Fatal(rows, e)
	}
	if n, e = s.ClaimReady(ctx, 1, out.Session.ID, 3, "third", now.Add(3*time.Minute), time.Minute); e != nil || n != nil {
		t.Fatal("overtook unknown", n, e)
	}
}
func TestExpirationRegistersFeedbackBeforeClearing(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := c.DefaultPolicy()
	out, e := s.Accept(ctx, 1, r.Update{ID: 1, UserID: 10, ChatID: 10, Locale: "en-US"}, "cipher", 6, "ref", p, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Expire(ctx, 1, now.Add(p.Retention)); e != nil {
		t.Fatal(e)
	}
	var row d.BufferedMessage
	db.Where("session_id = ?", out.Session.ID).First(&row)
	if row.PayloadCipher != "" || !row.FeedbackPending || row.State != "expired" {
		t.Fatal(row)
	}
	notices, e := New(db).Notices(ctx, 1, 50, now.Add(p.Retention))
	if e != nil || len(notices) != 2 {
		t.Fatal(notices, e)
	}
	for _, n := range notices {
		if n.Reason != "expired" || n.UserID != 10 {
			t.Fatal(n)
		}
	}
}

// testRoute uses its transaction to prove MaxOpenConns=1 never escapes tx.
type testRoute struct {
	tx      *gorm.DB
	binding c.DeliveryBinding
}

func (reader testRoute) CheckSupportRoute(ctx context.Context, bot, target uint, binding c.DeliveryBinding, now time.Time) error {
	var count int64
	if e := reader.tx.WithContext(ctx).Model(&rd.Instance{}).Where("id = ? AND enabled = ? AND state = ?", bot, true, "running").Count(&count).Error; e != nil {
		return e
	}
	if count != 1 || target != 1 || binding.ChatID != reader.binding.ChatID || binding.RouteVersion != reader.binding.RouteVersion {
		return c.ErrDenied
	}
	return nil
}
func allowTestRoute(s *Store, binding c.DeliveryBinding) {
	s.WithRouteReader(func(tx *gorm.DB) c.RouteReader { return testRoute{tx, binding} })
}
func installTestTopic(t *testing.T, s *Store, db *gorm.DB, session uint, binding c.DeliveryBinding) d.TopicMapping {
	t.Helper()
	allowTestRoute(s, binding)
	topic := d.TopicMapping{OperationID: session, BotID: 1, SessionID: session, TargetID: 1, ChatID: binding.ChatID, ThreadID: binding.ThreadID, RouteVersion: binding.RouteVersion, State: "open", Version: 1}
	if e := db.Create(&topic).Error; e != nil {
		t.Fatal(e)
	}
	return topic
}

// eventTestFence represents a supervised receiver; no network client is used.
type eventTestFence struct{}

func (eventTestFence) Check(context.Context, uint, uint64) error { return nil }

// transactionCheckingFence models the runtime fence's independent store read.
// db is the single-connection fixture; calls counts checks made before the transaction.
type transactionCheckingFence struct {
	db    *gorm.DB
	calls int
}

// Check validates bot/generation through the root pool and rejects transaction-held calls.
// It returns an error rather than blocking on the fixture's sole connection.
func (fence *transactionCheckingFence) Check(ctx context.Context, bot uint, generation uint64) error {
	pool, err := fence.db.DB()
	if err != nil {
		return err
	}
	if pool.Stats().InUse != 0 {
		return fmt.Errorf("execution fence called while transaction holds the only connection")
	}
	fence.calls++
	var instance rd.Instance
	return fence.db.WithContext(ctx).Where("id = ? AND generation = ?", bot, generation).First(&instance).Error
}

// TestObserveTopicChecksExecutionBeforeTransaction prevents a root-pool read or
// Redis renewal inside the transaction; t supplies an isolated single-connection fixture.
func TestObserveTopicChecksExecutionBeforeTransaction(t *testing.T) {
	store, db := fixture(t)
	allowTestRoute(store, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	fence := &transactionCheckingFence{db: db}
	ctx := r.WithExecutionFence(context.Background(), fence)
	now := time.Now().UTC().Truncate(time.Second)
	event := c.TopicEvent{BotID: 1, TargetID: 1, RouteVersion: 1, Generation: 1, Update: r.Update{
		ID: 100, Type: "message", MessageKind: "service", MessageID: 8,
		ChatID: -100, ChatType: "supergroup", ThreadID: 42, IsTopicMessage: true,
		TopicService: &r.TopicServiceEvent{State: "closed", Date: now.Unix()},
	}}
	if err := store.ObserveTopic(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	if fence.calls != 1 {
		t.Fatalf("execution checks = %d, want 1 before transaction", fence.calls)
	}
}

func TestTopicServiceOrderingAndLocalOperationBarrier(t *testing.T) {
	store, db := fixture(t)
	ctx := r.WithExecutionFence(context.Background(), eventTestFence{})
	now := time.Now().UTC().Truncate(time.Second)
	allowTestRoute(store, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	session := d.Session{BotID: 1, UserID: 10, ChatID: 10, State: "open"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "create", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishTopic(ctx, 1, job.Operation.ID, 1, "create", c.TopicResult{State: "succeeded", ThreadID: 42}, now); err != nil {
		t.Fatal(err)
	}
	event := c.TopicEvent{BotID: 1, TargetID: 1, RouteVersion: 1, Generation: 1, Update: r.Update{ID: 100, Type: "message", MessageKind: "service", MessageID: 8,
		ChatID: -100, ChatType: "supergroup", ThreadID: 42, IsTopicMessage: true, TopicService: &r.TopicServiceEvent{State: "closed", Date: now.Add(time.Second).Unix()}}}
	observe := func(id int64, state string, date time.Time) {
		t.Helper()
		event.Update.ID = id
		event.Update.TopicService = &r.TopicServiceEvent{State: state, Date: date.Unix()}
		if err := store.ObserveTopic(ctx, event, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	load := func(state string) d.TopicMapping {
		t.Helper()
		var mapping d.TopicMapping
		if err := db.First(&mapping).Error; err != nil {
			t.Fatal(err)
		}
		if mapping.State != state {
			t.Fatalf("state %s want %s", mapping.State, state)
		}
		return mapping
	}
	observe(100, "closed", now.Add(time.Second))
	load("closed")
	observe(102, "open", now.Add(2*time.Second))
	observe(101, "closed", now.Add(time.Second))
	observe(102, "closed", now.Add(3*time.Second))
	mapping := load("open")
	observe(103, "closed", now.Add(4*time.Second))
	mapping = load("closed")
	job, err = store.BeginTopicAction(ctx, 1, mapping.ID, mapping.Version, 1, "reopen", "reopen", now.Add(10*time.Second), time.Minute)
	if err != nil || !job.Execute {
		t.Fatal(job, err)
	}
	observe(104, "closed", now.Add(11*time.Second)) // pending action cannot be overwritten
	load("reopen_from_closed")
	if err = store.FinishTopic(ctx, 1, job.Operation.ID, 1, "reopen", c.TopicResult{State: "succeeded"}, now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	observe(104, "closed", now.Add(11*time.Second)) // durable replay ignored after finish
	observe(105, "closed", now.Add(12*time.Second)) // equal-second stale event ignored
	load("open")
	observe(106, "closed", now.Add(13*time.Second))
	load("closed")
	if err = store.ObserveTopic(context.Background(), event, now.Add(time.Minute)); err == nil {
		t.Fatal("missing fence accepted")
	}
	for _, change := range []func(*c.TopicEvent){
		func(event *c.TopicEvent) { event.Generation = 99 },
		func(event *c.TopicEvent) { event.BotID = 2 },
		func(event *c.TopicEvent) { event.Update.ChatID = -200 },
		func(event *c.TopicEvent) { event.RouteVersion = 2 },
	} {
		invalid := event
		change(&invalid)
		if err = store.ObserveTopic(ctx, invalid, now.Add(time.Minute)); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	load("closed")
	// A different physical thread never creates or attaches a mapping.
	event.Update.ThreadID = 999
	if err = store.ObserveTopic(ctx, event, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Model(&d.TopicMapping{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestTopicEventsNeverInferUnknownCreationOrCrossPhysicalScope(t *testing.T) {
	store, db := fixture(t)
	ctx := r.WithExecutionFence(context.Background(), eventTestFence{})
	now := time.Now().UTC().Truncate(time.Second)
	allowTestRoute(store, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	for index, scope := range []struct {
		bot  uint
		chat int64
	}{{1, -100}, {2, -100}, {1, -200}} {
		operation := d.TopicOperation{BotID: scope.bot, SessionID: uint(index + 1), TargetID: 1, RouteVersion: 1, ChatID: scope.chat,
			State: "succeeded", Action: "create", Generation: 1, Version: 1, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		if err := db.Create(&operation).Error; err != nil {
			t.Fatal(err)
		}
		mapping := d.TopicMapping{OperationID: operation.ID, BotID: scope.bot, SessionID: operation.SessionID, TargetID: 1, RouteVersion: 1,
			ChatID: scope.chat, ThreadID: 42, State: "open", Version: 1}
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
	}
	unknown := d.TopicOperation{BotID: 1, SessionID: 9, TargetID: 1, RouteVersion: 1, ChatID: -100, State: "unknown", Action: "create", Generation: 1, Version: 1}
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	event := c.TopicEvent{BotID: 1, TargetID: 1, RouteVersion: 1, Generation: 1, Update: r.Update{ID: 100, Type: "message", MessageKind: "service", MessageID: 8,
		ChatID: -100, ChatType: "supergroup", ThreadID: 42, IsTopicMessage: true, TopicService: &r.TopicServiceEvent{State: "closed", Date: now.Unix()}}}
	if err := store.ObserveTopic(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	event.Update.ThreadID = 999
	if err := store.ObserveTopic(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	var mappings []d.TopicMapping
	if err := db.Order("id ASC").Find(&mappings).Error; err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 3 || mappings[0].State != "closed" || mappings[1].State != "open" || mappings[2].State != "open" {
		t.Fatal("physical scope escaped or unknown create attached", mappings)
	}
	if err := db.First(&unknown, unknown.ID).Error; err != nil || unknown.State != "unknown" {
		t.Fatal(unknown, err)
	}
}

// TestOrphanTopicClaimsExpireWithoutMessages covers bounded local recovery only.
func TestOrphanTopicClaimsExpireWithoutMessages(t *testing.T) {
	store, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for index := 1; index <= 3; index++ {
		row := d.TopicOperation{BotID: 1, SessionID: uint(index), TargetID: 1, RouteVersion: 1, ChatID: -100,
			State: "creating", Action: "create", Version: 1, Generation: 1, Claim: fmt.Sprintf("claim-%d", index), LeaseUntil: now.Add(-time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ExpireTopicClaims(ctx, 1, 999, now, 2); err == nil {
		t.Fatal("stale generation accepted")
	}
	if err := store.ExpireTopicClaims(ctx, 1, 1, now, 2); err != nil {
		t.Fatal(err)
	}
	var rows []d.TopicOperation
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "unknown" || rows[1].State != "unknown" || rows[2].State != "creating" {
		t.Fatal("claim recovery was not bounded")
	}
	if rows[0].Version != 2 || rows[0].Claim != "claim-1" || rows[0].Generation != 1 {
		t.Fatal("claim provenance lost")
	}
	if err := store.ExpireTopicClaims(ctx, 1, 1, now, 2); err != nil {
		t.Fatal(err)
	}
	var first d.TopicOperation
	if err := db.First(&first, rows[0].ID).Error; err != nil || first.Version != 2 {
		t.Fatal("unknown operation was mutated twice", err)
	}
}

func TestTopicCreationUnknownCASAndLifecycle(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	allowTestRoute(s, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	session := d.Session{BotID: 1, UserID: 10, ChatID: 10, State: "open"}
	if e := db.Create(&session).Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mutex sync.Mutex
	executions := 0
	var acquired c.TopicClaim
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			got, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, fmt.Sprintf("claim-%d", index), now, time.Second)
			if e != nil {
				t.Error(e)
				return
			}
			if got.Execute {
				mutex.Lock()
				executions++
				acquired = got
				mutex.Unlock()
			}
		}(index)
	}
	wg.Wait()
	if executions != 1 {
		t.Fatal("creation executions", executions)
	}
	expired, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "restart", now.Add(time.Second), time.Second)
	if e != nil || expired.Execute || expired.Operation.State != "unknown" {
		t.Fatal(expired, e)
	}
	resolution := c.Resolution{ActorID: 8, ExpectedVersion: expired.Operation.Version, Decision: "succeeded", ThreadID: 42, Evidence: "ticket:123"}
	if e = s.ResolveTopic(ctx, 1, acquired.Operation.ID, resolution, now.Add(2*time.Second)); e != nil {
		t.Fatal(e)
	}
	if e = s.ResolveTopic(ctx, 1, acquired.Operation.ID, resolution, now); e != c.ErrConflict {
		t.Fatal("stale resolution", e)
	}
	var topic d.TopicMapping
	if e = db.First(&topic).Error; e != nil {
		t.Fatal(e)
	}
	for _, action := range []string{"close", "reopen", "delete"} {
		operation, e := s.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, action, action, now, time.Minute)
		if e != nil || !operation.Execute {
			t.Fatal(operation, e)
		}
		if e = s.FinishTopic(ctx, 1, operation.Operation.ID, 1, action, c.TopicResult{State: "succeeded"}, now); e != nil {
			t.Fatal(e)
		}
		if e = db.First(&topic, topic.ID).Error; e != nil {
			t.Fatal(e)
		}
	}
	if topic.State != "deleted" {
		t.Fatal(topic)
	}
	retry, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "replacement", now, time.Minute)
	if e != nil || !retry.Execute || retry.Operation.ID == acquired.Operation.ID || retry.Operation.SlotVersion != 1 {
		t.Fatal("confirmed deletion did not allocate an isolated slot", retry, e)
	}
	var audits int64
	db.Model(&d.ResolutionAudit{}).Count(&audits)
	if audits != 1 {
		t.Fatal(audits)
	}
}

func preparedFixture(t *testing.T) (*Store, *gorm.DB, d.BufferedMessage, r.DeliveryRequest, r.Message, time.Time) {
	t.Helper()
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.cipher = app.NewManager(nil, nil, "support-reliability-test")
	source := r.Update{ID: 1, UserID: 10, ChatID: 10, Private: true, MessageID: 9, MessageKind: "text", Text: "original"}
	raw, e := json.Marshal(source)
	if e != nil {
		t.Fatal(e)
	}
	encrypted, e := s.cipher.Seal(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.Accept(ctx, 1, source, encrypted, int64(len(raw)), "ref", c.DefaultPolicy(), now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, 1, r.Update{ID: 2, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), now); e != nil {
		t.Fatal(e)
	}
	binding := c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}
	installTestTopic(t, s, db, out.Session.ID, binding)
	message, e := s.ClaimReady(ctx, 1, out.Session.ID, 1, "claim", now, time.Second)
	if e != nil || message == nil {
		t.Fatal(message, e)
	}
	loaded, e := s.LoadReady(ctx, 1, message.ID, 1, "claim", now)
	if e != nil || loaded.Binding != binding || loaded.Source.Text != source.Text {
		t.Fatal(loaded, e)
	}
	envelope, e := s.encodeReady(loaded.Source, binding, c.ReadyBusinessRef(message.SessionID, message.ID))
	if e != nil {
		t.Fatal(e)
	}
	forged := envelope
	forged.Text = "arbitrary"
	if _, e = s.PrepareDispatch(ctx, 1, message.ID, 1, "claim", forged, now); e != c.ErrDenied {
		t.Fatal("forged source", e)
	}
	request, e := s.PrepareDispatch(ctx, 1, message.ID, 1, "claim", envelope, now)
	if e != nil {
		t.Fatal(e)
	}
	return s, db, *message, request, envelope, now
}

func TestDispatchRestartProofAndNativeReply(t *testing.T) {
	s, db, message, request, envelope, now := preparedFixture(t)
	ctx := context.Background()
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != nil {
		t.Fatal(e)
	}
	envelope.Text = "injected"
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal(e)
	}
	envelope.Text = "original"
	if e := db.Model(&rd.Instance{}).Where("id = 1").Update("generation", 2).Error; e != nil {
		t.Fatal(e)
	}
	restarted := New(db, s.cipher)
	allowTestRoute(restarted, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	pending, e := restarted.PendingDispatch(ctx, 1, message.ID, 2, now.Add(time.Minute))
	if e != nil || pending == nil || pending.Key != request.Key {
		t.Fatal(pending, e)
	}
	if got, e := restarted.ClaimReady(ctx, 1, message.SessionID, 2, "new", now.Add(time.Minute), time.Minute); e != nil || got != nil {
		t.Fatal(got, e)
	}
	proof := rd.Delivery{BotID: request.BotID, Key: request.Key, ChatID: request.ChatID, TelegramUserID: request.TelegramUserID, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "pending"}
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := restarted.ReconcileReady(ctx, 1, message.ID, 2, now.Add(time.Minute)); e != nil || state != "sending" {
		t.Fatal(state, e)
	}
	if e = restarted.AuthorizeTargetAt(ctx, 1, envelope, now.Add(time.Minute)); e != nil {
		t.Fatal("lease expired blocked queued send", e)
	}
	if e = db.Model(&proof).Updates(map[string]any{"status": "sent", "message_id": 77}).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := restarted.ReconcileReady(ctx, 1, message.ID, 2, now.Add(2*time.Minute)); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	if state, e := restarted.ReconcileReady(ctx, 1, message.ID, 2, now.Add(3*time.Minute)); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	reply := r.Update{Type: "message", ID: 44, UserID: 90, ChatID: -100, ThreadID: 20, IsTopicMessage: true, Reply: &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 20}}
	mapping, e := restarted.ResolveNativeReply(ctx, 1, reply, now)
	if e != nil || mapping == nil || mapping.UserID != 10 {
		t.Fatal(mapping, e)
	}
	reply.Reply.MessageID = 78
	if mapping, e = restarted.ResolveNativeReply(ctx, 1, reply, now); e != nil || mapping == nil || mapping.ID != 0 || mapping.UserID != 10 {
		t.Fatal("same-topic unregistered reply rejected", e)
	}
	reply.Reply = nil
	if mapping, e = restarted.ResolveNativeReply(ctx, 1, reply, now); e != nil || mapping == nil || mapping.ID != 0 || mapping.UserID != 10 {
		t.Fatal("topic route not resolved", e)
	}
}

func TestDispatchUnknownResolutionAndRouteProtection(t *testing.T) {
	s, db, message, request, envelope, now := preparedFixture(t)
	ctx := context.Background()
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: request.ChatID, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "unknown"}
	if e := db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileReady(ctx, 1, message.ID, 1, now); e != nil || state != "unknown" {
		t.Fatal(state, e)
	}
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal("unknown authorized", e)
	}
	resolution := c.Resolution{ActorID: 8, ExpectedVersion: 1, Decision: "failed", DeliveryID: proof.ID, Evidence: "ticket:clear-failure"}
	if e := s.ResolveReady(ctx, 1, message.ID, 1, resolution, now); e != c.ErrDenied {
		t.Fatal("unknown treated as failure", e)
	}
	if e := db.Model(&proof).Update("status", "failed").Error; e != nil {
		t.Fatal(e)
	}
	if e := s.ResolveReady(ctx, 1, message.ID, 1, resolution, now); e != nil {
		t.Fatal(e)
	}
	if e := s.ResolveReady(ctx, 1, message.ID, 1, resolution, now); e != c.ErrConflict {
		t.Fatal(e)
	}
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal("resolved failure must require explicit retry", e)
	}
	allowTestRoute(s, c.DeliveryBinding{ChatID: -200, RouteVersion: 2})
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal("historical route authorized", e)
	}
	if _, e := s.ScanReady(ctx, 1, 0, 101); e != c.ErrDenied {
		t.Fatal("unbounded scan", e)
	}
}
