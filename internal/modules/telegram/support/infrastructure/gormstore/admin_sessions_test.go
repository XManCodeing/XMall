package gormstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
)

// sessionOperationsFixture supplies current tables and deterministic metadata only.
// It returns the repository, DB and clock; failures abort this test, never a live DB.
func sessionOperationsFixture(t *testing.T) (*AdminStore, *gorm.DB, time.Time) {
	t.Helper()
	_, db := fixture(t)
	if err := db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	for _, row := range []any{
		&d.Session{ID: 1, BotID: 1, UserID: 9007199254740993, ChatID: 9007199254740993, State: "open", Version: 1},
		&d.Session{ID: 2, BotID: 1, UserID: 20, ChatID: 20, State: "closed", Version: 1},
		&d.Session{ID: 3, BotID: 2, UserID: 20, ChatID: 20, State: "open", Version: 1},
		&d.BufferedMessage{ID: 1, BotID: 1, UpdateID: 1, SessionID: 1, State: "delivered", PayloadCipher: "secret-body", UpdatedAt: now.Add(time.Hour)},
		&d.ReplyMapping{BotID: 1, ChatID: -1001234567890, MessageID: 42, BufferedMessageID: 1, SessionID: 1, CreatedAt: now},
		&d.BufferedMessage{BotID: 1, UpdateID: 2, SessionID: 1, State: "failed", ErrorClass: "secret-error"},
		&d.BufferedMessage{BotID: 1, UpdateID: 3, SessionID: 1, State: "unknown"},
		&d.BufferedMessage{BotID: 1, UpdateID: 4, SessionID: 2, State: "ready"},
		&d.Downstream{BotID: 1, UpdateID: 1, SessionID: 1, Kind: "note", State: "delivered", UpdatedAt: now.Add(time.Second)},
		&d.Downstream{BotID: 1, UpdateID: 2, SessionID: 1, Kind: "reply", State: "failed"},
		&d.Downstream{BotID: 1, UpdateID: 3, SessionID: 1, Kind: "reply", State: "unknown"},
		&d.SupportBlock{BotID: 1, UserID: 9007199254740993, Enabled: true},
		&d.SupportBlock{BotID: 1, UserID: 20, Enabled: true, ExpiresAt: &now},
		&gd.Target{ID: 1, BotInstanceID: 1, Purpose: "support", TelegramChatID: -1001234567890, RouteVersion: 2, OccupancySlot: "test"},
		&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", TargetID: 1},
		&d.TopicMapping{OperationID: 1, BotID: 1, SessionID: 1, TargetID: 1, ChatID: -1001234567890, ThreadID: 9007199254740993, RouteVersion: 2, State: "open"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return NewAdmin(db), db, now
}

// TestAdminSessionOperations checks reply semantics, privacy, expiry and SQL paging.
// Internal notes and unforwarded customer messages must never change waiting state.
func TestAdminSessionOperations(t *testing.T) {
	store, db, now := sessionOperationsFixture(t)
	ctx := context.Background()
	row, err := store.Session(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Unreplied || row.UnrepliedSince == nil || !row.UnrepliedSince.Equal(now) || row.LastReplyDeliveredAt != nil || !row.Blocked {
		t.Fatalf("unexpected projection: %+v", row)
	}
	if row.UpstreamFailed != 1 || row.UpstreamUnknown != 1 || row.DownstreamFailed != 1 || row.DownstreamUnknown != 1 {
		t.Fatal(row)
	}
	if row.TopicURL != "https://t.me/c/1234567890/9007199254740993" {
		t.Fatal(row.TopicURL)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "cipher", "payload", "error_class"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("metadata leaked", secret)
		}
	}
	yes, no := true, false
	for _, filter := range []c.AdminFilter{
		{Unreplied: &yes}, {Blocked: &yes}, {HasTopic: &yes}, {Failure: "any"}, {Failure: "upstream"}, {Failure: "downstream"},
	} {
		filter.Page, filter.PageSize = 1, 1
		rows, total, err := store.Sessions(ctx, filter)
		if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != 1 {
			t.Fatal(filter, rows, total, err)
		}
		filter.Page = 2
		rows, total, err = store.Sessions(ctx, filter)
		if err != nil || total != 1 || len(rows) != 0 {
			t.Fatal(rows, total, err)
		}
	}
	rows, total, err := store.Sessions(ctx, c.AdminFilter{Page: 1, PageSize: 20, BotID: 1, Blocked: &no, Unreplied: &no, Failure: "none"})
	if err != nil || total != 1 || rows[0].ID != 2 || rows[0].BlockExpiresAt != nil {
		t.Fatal(rows, total, err)
	}
	// Reply waiting is independent of session close; no non-delivered reply clears it.
	if err = db.Model(&d.Session{}).Where("id = 1").Update("state", "closed").Error; err != nil {
		t.Fatal(err)
	}
	row, err = store.Session(ctx, 1)
	if err != nil || !row.Unreplied {
		t.Fatal(row, err)
	}
	// Successful replies clear earlier receipts; a late message starts a new wait.
	reply := d.Downstream{BotID: 1, UpdateID: 4, SessionID: 1, Kind: "reply", State: "delivered", UpdatedAt: now.Add(time.Second)}
	if err = db.Create(&reply).Error; err != nil {
		t.Fatal(err)
	}
	row, err = store.Session(ctx, 1)
	if err != nil || row.Unreplied || row.UnrepliedSince != nil || row.LastReplyDeliveredAt == nil {
		t.Fatal(row, err)
	}
	if err = db.Create(&d.BufferedMessage{ID: 10, BotID: 1, UpdateID: 10, SessionID: 1, State: "delivered"}).Error; err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Second)
	if err = db.Create(&d.ReplyMapping{BotID: 1, ChatID: -1001234567890, MessageID: 43, BufferedMessageID: 10, SessionID: 1, CreatedAt: later}).Error; err != nil {
		t.Fatal(err)
	}
	row, err = store.Session(ctx, 1)
	if err != nil || !row.Unreplied || row.UnrepliedSince == nil || !row.UnrepliedSince.Equal(later) {
		t.Fatal(row, err)
	}
}

// TestAdminSessionTopicFences rejects retired, deleted, ambiguous and mismatched
// module topics. A disabled module may still navigate for group-side operations.
func TestAdminSessionTopicFences(t *testing.T) {
	for _, change := range []string{"route", "deleted", "module", "ambiguous", "bot", "chat"} {
		t.Run(change, func(t *testing.T) {
			store, db, _ := sessionOperationsFixture(t)
			var err error
			switch change {
			case "route":
				err = db.Model(&gd.Target{}).Where("id = 1").Update("route_version", 3).Error
			case "deleted":
				err = db.Model(&d.TopicMapping{}).Where("session_id = 1").Update("state", "deleted").Error
			case "module":
				err = db.Model(&gd.ModuleConfig{}).Where("bot_instance_id = 1").Update("target_id", 9).Error
			case "bot":
				err = db.Model(&gd.Target{}).Where("id = 1").Update("bot_instance_id", 2).Error
			case "chat":
				err = db.Model(&gd.Target{}).Where("id = 1").Update("telegram_chat_id", -1009999999999).Error
			case "ambiguous":
				err = db.Create(&d.TopicMapping{OperationID: 2, BotID: 1, SessionID: 1, TargetID: 1, ChatID: -1001234567890, ThreadID: 99, RouteVersion: 2, State: "closed"}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			row, err := store.Session(context.Background(), 1)
			if err != nil || row.TopicURL != "" || row.TopicThreadID != 0 {
				t.Fatal(row, err)
			}
		})
	}
}
