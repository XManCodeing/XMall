package gormstore

import (
	"context"
	"errors"
	"strings"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"testing"
	"time"
)

// TestSupportKeySQLDialects checks real checkpoint and delivery-proof queries.
// t owns an in-memory connection; server dialects only generate SQL, never execute it.
func TestSupportKeySQLDialects(t *testing.T) {
	_, local := fixture(t)
	pool, err := local.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []gorm.Dialector{
		mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{Conn: pool}),
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
			if err != nil {
				t.Fatal(err)
			}
			var statement string
			var values []any
			if err := db.Callback().Query().After("gorm:query").Register("test:key_sql", func(tx *gorm.DB) {
				statement = tx.Statement.SQL.String()
				values = tx.Statement.Vars
			}); err != nil {
				t.Fatal(err)
			}
			quoted := "`key`"
			if dialect.Name() == "postgres" {
				quoted = `"key"`
			}
			store := &Store{db: db}
			for _, query := range []struct {
				name string
				run  func() error
			}{
				{"reject_ready_guard", func() error {
					var count int64
					return countReadyDelivery(db, 1, "checkpoint-key", &count)
				}},
				{"checkpoint", func() error {
					_, err := store.DisasterContinuationProgress(context.Background(), "checkpoint-key")
					return err
				}},
				{"delivery_proof", func() error {
					_, err := recoveryProof(db, 1, "checkpoint-key")
					if errors.Is(err, c.ErrDenied) { // DryRun cannot produce a real proof row.
						return nil
					}
					return err
				}},
			} {
				t.Run(query.name, func(t *testing.T) {
					statement, values = "", nil
					if err := query.run(); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(statement, quoted+" = ") || strings.Contains(statement, "checkpoint-key") {
						t.Fatalf("key must be quoted and parameterized: %s", statement)
					}
					bound := false
					for _, value := range values {
						if value == "checkpoint-key" {
							bound = true
						}
					}
					if !bound {
						t.Fatal("missing key parameter")
					}
				})
			}
		})
	}
}

// TestDefaultRouteProjection exercises production's same-connection SQL using
// actual groups models, including expired capability and route/mode mismatch.
func TestDefaultRouteProjection(t *testing.T) {
	s, db := fixture(t)
	now := time.Now().UTC()
	ctx := context.Background()
	if e := db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}); e != nil {
		t.Fatal(e)
	}
	target := gd.Target{ID: 1, Purpose: "support", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -100, OccupancySlot: "-100", Observation: gd.Observation{Generation: 1, RouteVersion: 1, BotTelegramID: 100, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, IsForum: true, CanManageTopics: true, State: "ready", ObservedAt: &now}}
	if e := db.Create(&target).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 1, Version: 1}).Error; e != nil {
		t.Fatal(e)
	}
	session := d.Session{BotID: 1, UserID: 10, ChatID: 10, State: "open"}
	if e := db.Create(&session).Error; e != nil {
		t.Fatal(e)
	}
	operation, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "initial", now, time.Second)
	if e != nil || !operation.Execute {
		t.Fatal(operation, e)
	}
	for _, change := range []map[string]any{{"route_version": 2}, {"obs_generation": 9}, {"obs_is_forum": false}, {"obs_can_manage_topics": false}, {"enabled": false}, {"obs_observed_at": now.Add(-5 * time.Minute)}} {
		if e = db.Model(&gd.Target{}).Where("id = 1").Updates(change).Error; e != nil {
			t.Fatal(e)
		}
		expected := c.ErrDenied
		if change["obs_generation"] != nil || change["obs_observed_at"] != nil {
			expected = g.ErrCapabilityPending
		}
		if _, e = s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "denied", now, time.Second); !errors.Is(e, expected) {
			t.Fatal(change, e)
		}
		if e = db.Model(&gd.Target{}).Where("id = 1").Updates(map[string]any{"route_version": 1, "obs_generation": 1, "obs_is_forum": true, "obs_can_manage_topics": true, "enabled": true, "obs_observed_at": now}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = db.Model(&gd.ModuleConfig{}).Where("bot_instance_id = 1").Update("enabled", false).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "module-off", now, time.Second); e != c.ErrDenied {
		t.Fatal(e)
	}
}

func TestTopicDefiniteFailureRetryAndPhysicalOwnership(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	allowTestRoute(s, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	first := d.Session{BotID: 1, UserID: 10, ChatID: 10, State: "open"}
	second := d.Session{BotID: 1, UserID: 20, ChatID: 20, State: "open"}
	if e := db.Create(&first).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&second).Error; e != nil {
		t.Fatal(e)
	}
	operation, e := s.ClaimTopic(ctx, 1, first.ID, 1, 1, 1, -100, "first", now, time.Minute)
	if e != nil || !operation.Execute {
		t.Fatal(operation, e)
	}
	if e = s.FinishTopic(ctx, 1, operation.Operation.ID, 1, "first", c.TopicResult{State: "failed"}, now); e != nil {
		t.Fatal(e)
	}
	retry, e := s.ClaimTopic(ctx, 1, first.ID, 1, 1, 1, -100, "retry", now, time.Minute)
	if e != nil || !retry.Execute || retry.Operation.ID != operation.Operation.ID {
		t.Fatal(retry, e)
	}
	if e = s.FinishTopic(ctx, 1, retry.Operation.ID, 1, "first", c.TopicResult{State: "succeeded", ThreadID: 33}, now); e != c.ErrConflict {
		t.Fatal("stale response", e)
	}
	if e = s.FinishTopic(ctx, 1, retry.Operation.ID, 1, "retry", c.TopicResult{State: "succeeded", ThreadID: 33}, now); e != nil {
		t.Fatal(e)
	}
	other, e := s.ClaimTopic(ctx, 1, second.ID, 1, 1, 1, -100, "other", now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishTopic(ctx, 1, other.Operation.ID, 1, "other", c.TopicResult{State: "succeeded", ThreadID: 33}, now); e == nil {
		t.Fatal("physical topic reassigned")
	}
	var persisted d.TopicOperation
	if e = db.First(&persisted, other.Operation.ID).Error; e != nil || persisted.State != "creating" {
		t.Fatal(persisted, e)
	}
	if e = db.Model(&d.TopicMapping{}).Where("session_id = ?", first.ID).Update("state", "deleted").Error; e != nil {
		t.Fatal(e)
	}
	if e = s.FinishTopic(ctx, 1, other.Operation.ID, 1, "other", c.TopicResult{State: "succeeded", ThreadID: 33}, now); e == nil {
		t.Fatal("deleted topic reassigned")
	}
}

func TestTopicActionUnknownBlocksAndCanResolveNoEffect(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	allowTestRoute(s, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	session := d.Session{BotID: 1, UserID: 10, ChatID: 10, State: "open"}
	if e := db.Create(&session).Error; e != nil {
		t.Fatal(e)
	}
	created, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "create", now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishTopic(ctx, 1, created.Operation.ID, 1, "create", c.TopicResult{State: "succeeded", ThreadID: 31}, now); e != nil {
		t.Fatal(e)
	}
	var topic d.TopicMapping
	if e = db.First(&topic).Error; e != nil {
		t.Fatal(e)
	}
	closing, e := s.BeginTopicAction(ctx, 1, topic.ID, topic.Version, 1, "close", "close", now, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishTopic(ctx, 1, closing.Operation.ID, 1, "close", c.TopicResult{State: "unknown"}, now); e != nil {
		t.Fatal(e)
	}
	stuck, e := s.ClaimTopic(ctx, 1, session.ID, 1, 1, 1, -100, "again", now.Add(time.Minute), time.Minute)
	if e != nil || stuck.Execute || stuck.Mapping.State == "open" {
		t.Fatal(stuck, e)
	}
	resolution := c.Resolution{ActorID: 1, ExpectedVersion: stuck.Operation.Version, Decision: "failed", Evidence: "ticket:no-effect"}
	if e = s.ResolveTopic(ctx, 1, stuck.Operation.ID, resolution, now); e != nil {
		t.Fatal(e)
	}
	if e = db.First(&topic, topic.ID).Error; e != nil || topic.State != "open" {
		t.Fatal(topic, e)
	}
}

func TestNativeReplyRejectsUntrustedAndHistoricalReferences(t *testing.T) {
	s, db, message, _, _, now := preparedFixture(t)
	ctx := context.Background()
	// Directly forged registration cannot bootstrap trust without runtime sent proof.
	if e := s.RegisterReply(ctx, 1, message.ID, 1, now); e != c.ErrDenied {
		t.Fatal(e)
	}
	reference := &r.ReplyReference{ChatID: -100, MessageID: 99, ThreadID: 20}
	base := r.Update{Type: "message", ChatID: -100, UserID: 91, ThreadID: 20, IsTopicMessage: true, Reply: reference}
	if mapping, err := s.ResolveNativeReply(ctx, 1, base, now); err != nil || mapping == nil || mapping.ID != 0 {
		t.Fatal("same-topic unregistered reply did not use topic route", err)
	}
	cases := []r.Update{{Type: "message", Private: true, ChatID: 10, UserID: 10, Reply: reference}, {Type: "message", ChatID: -100, UserID: 91, ThreadID: 20, IsTopicMessage: true, Reply: reference, SenderChat: &r.ChatReference{ID: -100}}, {Type: "edited_message", ChatID: -100, UserID: 91, ThreadID: 20, IsTopicMessage: true, Reply: reference}}
	for _, update := range cases {
		if _, e := s.ResolveNativeReply(ctx, 1, update, now); e != c.ErrDenied {
			t.Fatal(update, e)
		}
	}
	// Even forged mapping rows must still match a delivered consultation and route.
	mapping := d.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 99, ThreadID: 20, TopicID: 1, SessionID: message.SessionID, BufferedMessageID: message.ID, DeliveryID: 1, UserID: 10, RouteVersion: 1}
	if e := db.Create(&mapping).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.ResolveNativeReply(ctx, 1, base, now); e != c.ErrDenied {
		t.Fatal(e)
	}
	if e := db.Model(&message).Updates(map[string]any{"state": "delivered", "delivery_id": 1}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&d.TopicMapping{}).Where("id = 1").Update("state", "closed").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.ResolveNativeReply(ctx, 1, base, now); e != c.ErrDenied {
		t.Fatal("closed native reply", e)
	}
}
