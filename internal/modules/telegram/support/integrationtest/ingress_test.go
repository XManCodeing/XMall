package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	store "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// ingressRaceFence mutates durable runtime state after the lease check, before BEGIN.
// This models a replacement/recovery racing a previously accepted readiness snapshot.
type ingressRaceFence struct{ mutate func() error }

// Check performs the test-only race; it returns persistence failures to the caller.
func (f ingressRaceFence) Check(context.Context, uint, uint64) error { return f.mutate() }

// TestProductionReadinessAndAtomicIngress verifies real route projections and both
// ingress methods against recovery/generation changes without calling Telegram.
func TestProductionReadinessAndAtomicIngress(t *testing.T) {
	fixture := newGuestGate(t)
	db, repo := fixture.db, fixture.repo
	mustGuest(t, db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}))
	now := time.Now().UTC()
	mustGuest(t, db.Model(&rd.Instance{}).Where("id = 1").Update("last_seen_at", now).Error)
	target := gd.Target{ID: 1, Purpose: "support", Enabled: true, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -100, OccupancySlot: "support-test", Observation: gd.Observation{RouteVersion: 1, BotTelegramID: 123, Generation: 1, ChatType: "supergroup", IsMember: true, IsForum: true, MemberStatus: "administrator", CanManageTopics: true, State: "ready", ObservedAt: &now}}
	mustGuest(t, db.Create(&target).Error)
	mustGuest(t, db.Create(&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 1}).Error)
	repo.WithReadyEncoder(c.EncodeReadyTransfer).WithPolicyRecoveryGuard(func(tx *gorm.DB, bot uint) error { return rs.New(tx).GuardRecoveryWrite(tx.Statement.Context, bot) })
	capability := store.DeliveryCapability{Store: repo}
	fixture.service.DeliveryReady = capability
	ctx := r.WithExecutionFence(context.Background(), guestFence{})
	if ready, err := fixture.service.ReadyEnabled(ctx, 1); err != nil || !ready {
		t.Fatal(ready, err)
	}
	update := r.Update{ID: 1, Private: true, UserID: 42, ChatID: 42, MessageID: 1, Text: "consultation"}
	if _, err := repo.Accept(context.Background(), 1, update, "encrypted", 9, "ref", c.DefaultPolicy(), now); !errors.Is(err, r.ErrStale) {
		t.Fatal(err)
	}
	out, err := repo.Accept(ctx, 1, update, "encrypted", 9, "ref", c.DefaultPolicy(), now)
	mustGuest(t, err)
	for _, change := range []map[string]any{{"obs_can_manage_topics": false}, {"recovery_task_id": 8}, {"obs_observed_at": now.Add(-6 * time.Minute)}, {"obs_generation": 2}} {
		mustGuest(t, db.Model(&gd.Target{}).Where("id = 1").Updates(change).Error)
		if ready, err := capability.Ready(ctx, 1, 1); err != nil || ready {
			t.Fatal(ready, err)
		}
		mustGuest(t, db.Save(&target).Error)
	}
	for _, change := range []map[string]any{{"generation": 2}, {"recovery_task_id": 7}, {"state": "stopped"}} {
		race := r.WithExecutionFence(context.Background(), ingressRaceFence{mutate: func() error { return db.Model(&rd.Instance{}).Where("id = 1").Updates(change).Error }})
		if _, err = repo.Verify(race, 1, r.Update{UserID: 42, ChatID: 42}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), now); err == nil {
			t.Fatal("verification escaped runtime fence")
		}
		if ready, err := capability.Ready(ctx, 1, 1); err != nil || ready {
			t.Fatal(ready, err)
		}
		mustGuest(t, db.Model(&rd.Instance{}).Where("id = 1").Updates(map[string]any{"generation": 1, "recovery_task_id": 0, "state": "running"}).Error)
		update.ID++
		if _, err = repo.Accept(race, 1, update, "encrypted", 9, "next", c.DefaultPolicy(), now); err == nil {
			t.Fatal("accept escaped runtime fence")
		}
		mustGuest(t, db.Model(&rd.Instance{}).Where("id = 1").Updates(map[string]any{"generation": 1, "recovery_task_id": 0, "state": "running"}).Error)
	}
	var count int64
	mustGuest(t, db.Model(&d.BufferedMessage{}).Count(&count).Error)
	if count != 1 {
		t.Fatal("raced input was persisted", count)
	}
	var challenge d.Challenge
	mustGuest(t, db.First(&challenge).Error)
	if challenge.State != "pending" {
		t.Fatal(challenge.State)
	}
	verified, err := repo.Verify(ctx, 1, r.Update{UserID: 42, ChatID: 42}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), now)
	mustGuest(t, err)
	if verified.Reason != "passed" {
		t.Fatal(verified.Reason)
	}
	update.ID++
	continued, err := repo.Accept(ctx, 1, update, "encrypted", 9, "continued", c.DefaultPolicy(), now)
	mustGuest(t, err)
	if continued.Session.ID != out.Session.ID || continued.Session.State != "open" || (continued.Challenge != nil && continued.Challenge.ID != out.Challenge.ID) {
		t.Fatal("subsequent consultation lost its verified session")
	}
	mustGuest(t, db.Migrator().DropTable(&gd.ModuleConfig{}))
	if ready, err := capability.Ready(ctx, 1, 1); err == nil || ready {
		t.Fatal("database error hidden", ready, err)
	}
}

type modules struct{}

func (modules) Modules(context.Context, uint) ([]g.ModuleView, error) {
	return []g.ModuleView{{ModuleConfig: gd.ModuleConfig{Partition: "support", Enabled: true, TargetID: 1}, EffectiveEnabled: true}}, nil
}

type capability struct{}

func (capability) Ready(context.Context, uint, uint) (bool, error) { return true, nil }

type deliveries struct {
	t    *testing.T
	db   *gorm.DB
	fail bool
	rows map[string]r.DeliveryRequest
}

func (q *deliveries) EnqueueRequest(_ context.Context, r *r.DeliveryRequest) error {
	var count int64
	if err := q.db.Model(&d.BufferedMessage{}).Count(&count).Error; err != nil {
		q.t.Fatal(err)
	}
	if count == 0 {
		q.t.Fatal("challenge queued before consultation commit")
	}
	if q.fail {
		return errors.New("injected enqueue crash")
	}
	q.rows[r.Key] = *r
	return nil
}
func TestDurableMediaRetryGateAndRelease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "support.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	if err = db.AutoMigrate(&rd.Instance{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.Album{}, &d.AlbumItem{}, &d.SupportBlock{}, &d.ReadyDispatch{}, &d.Downstream{}, &d.OperationReference{}, &d.OperationControl{}, &d.DownstreamAlbum{}, &d.OperationAudit{}, &rd.Delivery{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&rd.Instance{ID: 1, TelegramID: 123, Generation: 1, Enabled: true, Mode: "internal", State: "running"}).Error; err != nil {
		t.Fatal(err)
	}
	cipher := runtimeapp.NewManager(nil, nil, "test-only-support-secret")
	repo := store.New(db, cipher)
	q := &deliveries{t: t, db: db, rows: map[string]r.DeliveryRequest{}}
	service := a.New(repo, cipher, modules{}, q)
	ctx := context.Background()
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }
	update := r.Update{ID: 1, Private: true, Type: "message", ChatType: "private", UserID: 42, ChatID: 42, MessageID: 11, MessageKind: "photo", Caption: "album caption", CaptionEntities: []r.MessageEntity{{Type: "bold", Offset: 0, Length: 5}}, MediaGroupID: "album-1", Media: []r.Media{{Type: "photo", FileID: "small", Width: 90, Height: 90}, {Type: "photo", FileID: "large", Width: 1280, Height: 1280}}}
	if _, err = service.Handle(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&d.Session{}).Count(&count)
	if count != 0 {
		t.Fatal("disabled service consumed message")
	}
	service.DeliveryReady = capability{}
	q.fail = true
	if _, err = service.Handle(ctx, 1, update); err != nil {
		t.Fatal("notification failure retried durable intake", err)
	}
	var row d.BufferedMessage
	db.First(&row)
	if row.PayloadCipher == "" || row.PayloadCipher == update.Caption {
		t.Fatal("missing encryption")
	}
	raw, err := cipher.Open(row.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	var recovered r.Update
	if err = json.Unmarshal([]byte(raw), &recovered); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered, update) {
		t.Fatalf("media changed: %+v", recovered)
	}
	// Simulate process reconstruction after commit, before challenge enqueue.
	service = a.New(repo, cipher, modules{}, q)
	service.DeliveryReady = capability{}
	service.Now = func() time.Time { return now }
	q.fail = false
	if err = service.Reconcile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(q.rows) != 1 {
		t.Fatal("independent recovery did not enqueue challenge")
	}
	for range 2 {
		if _, err = service.Handle(ctx, 1, update); err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&d.BufferedMessage{}).Count(&count)
	if count != 1 || len(q.rows) != 1 {
		t.Fatalf("replay rows=%d deliveries=%d", count, len(q.rows))
	}
	var ch d.Challenge
	db.First(&ch)
	for _, delivery := range q.rows {
		raw, err := cipher.Open(delivery.PayloadCipher)
		if err != nil {
			t.Fatal(err)
		}
		var m r.Message
		json.Unmarshal([]byte(raw), &m)
		if err = service.AuthorizeTarget(ctx, 1, m); err != nil {
			t.Fatal(err)
		}
		if err = service.AuthorizeTarget(ctx, 2, m); err == nil {
			t.Fatal("cross-bot authorization")
		}
		m.ChatID = 43
		if err = service.AuthorizeTarget(ctx, 1, m); err == nil {
			t.Fatal("cross-user authorization")
		}
	}
	click := r.Update{ID: 2, Private: true, Type: "callback_query", ChatType: "private", UserID: 42, ChatID: 42, MessageID: 12, CallbackID: "cb", CallbackData: "support:" + c.VerificationAnswer(ch, ch.AnswerIndex)}
	q.fail = true
	if _, err = service.Handle(ctx, 1, click); err == nil {
		t.Fatal("cleanup enqueue failure was swallowed")
	}
	q.fail = false
	for range 2 {
		if _, err = service.Handle(ctx, 1, click); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.rows) != 2 {
		t.Fatal("inbox replay did not recover exactly one cleanup")
	}
	rows, err := repo.Ready(ctx, 1, row.SessionID, 20, now)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ready=%v err=%v", rows, err)
	}
	if rows[0].ReleasedAt == nil {
		t.Fatal("release not persisted")
	}
	if !c.DefaultPolicy().Valid() {
		t.Fatal("invalid defaults")
	}
}
