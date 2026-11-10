package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	cardsecretdomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	cartdomain "github.com/XManCodeing/XMall/internal/modules/cart/domain"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	channel "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	settingsstore "github.com/XManCodeing/XMall/internal/modules/settings/infrastructure/gormstore"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	deliverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Fingerprint only schema inputs, not locking, ledger/runner mechanics or progress
// logging. The old whole-package v1 fixture is retained for audit; the scoped v1
// fixture was captured before the progress-only baseline call substitution below.
// Future model/baseline/data changes require a new target AND its incremental step.
const legacyWholePackageV1Fingerprint = "14a135bc9b1cc99bc28c03a1f4a5a8f27e928ce371bdd75e9c11df8e24ab578a"

// XMall module rename changes the source fingerprint (including imports), not
// database schema. Preserve the pre-rename v1 fingerprint for audit; no migration
// step or schemaVersion change is required for this package-path-only change.
const preXMallScopedV1Fingerprint = "85555e2b375b75da42e9e25d8a68dcc8cae4f41e34217ea211472b3cac20d899"

// The GitHub owner rename also changes imports only. Retain the previous XMall
// baseline for audit; database schema and schemaVersion remain unchanged.
const preOwnerRenameScopedV1Fingerprint = "f5c4c4d544768db1ce8eb2768d082671c6476aca3ddf8635830ae2f751d3b281"

func TestSupportV13UpgradePreservesIngressAndPhysicalTopicOwnership(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(fmt.Sprintf("fresh=%v", fresh), func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "support13.sqlite"))
			baseline := func(tx *gorm.DB) error {
				if err := migrateTelegramSupport(tx); err != nil {
					return err
				}
				return migrateTelegramSupportDelivery(tx)
			}
			if !fresh {
				// Frozen pre-v13 transport shape: new receipt columns must be
				// added without rewriting legacy/unknown external outcomes.
				if err := db.Exec(`CREATE TABLE telegram_bot_deliveries (id INTEGER PRIMARY KEY, bot_id INTEGER, key TEXT, status TEXT, message_id INTEGER, payload_cipher TEXT, payload_held NUMERIC)`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`INSERT INTO telegram_bot_deliveries (id, bot_id, key, status, message_id, payload_cipher, payload_held) VALUES (17, 2, 'legacy-unknown', 'unknown', 91, 'synthetic-receipt-cipher', 1)`).Error; err != nil {
					t.Fatal(err)
				}
				if err := migrateTelegramSupport(db); err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&supportdomain.Session{ID: 1, BotID: 2, UserID: 3, ChatID: 3, State: "open", Version: 1}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&supportdomain.BufferedMessage{ID: 1, BotID: 2, UpdateID: 4, SessionID: 1, Sequence: 1, State: "unknown", PayloadCipher: "synthetic-preserved-cipher"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("INSERT INTO schema_migrations VALUES (12, 'synthetic')").Error; err != nil {
					t.Fatal(err)
				}
				steps := append([]migrationStep(nil), schemaSteps[:12]...)
				original := steps[11].run
				steps[11].run = func(tx *gorm.DB) error {
					if err := original(tx); err != nil {
						return err
					}
					return errors.New("synthetic interruption")
				}
				if err := runSchemaMigrations(db, 13, baseline, steps...); err == nil {
					t.Fatal("interruption ignored")
				}
				var count int64
				if err := db.Table("schema_migrations").Where("version = ?", 13).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("partial upgrade marked complete: %d %v", count, err)
				}
			}
			for range 2 {
				if err := runSchemaMigrations(db, 13, baseline, schemaSteps[:12]...); err != nil {
					t.Fatal(err)
				}
			}
			for _, model := range []any{&supportdomain.TopicOperation{}, &supportdomain.TopicMapping{}, &supportdomain.ReadyDispatch{}, &supportdomain.ReplyMapping{}, &supportdomain.ResolutionAudit{}, &supportdomain.Album{}, &supportdomain.AlbumItem{}, &supportdomain.Downstream{}, &supportdomain.DownstreamAlbum{}, &supportdomain.SupportBlock{}, &supportdomain.OperationReference{}, &supportdomain.OperationAudit{}, &supportdomain.OperationControl{}} {
				if !db.Migrator().HasTable(model) {
					t.Fatalf("missing %T", model)
				}
				assertModelIndexesExist(t, db, model)
			}
			if !fresh {
				var saved supportdomain.BufferedMessage
				if err := db.First(&saved, 1).Error; err != nil {
					t.Fatal(err)
				}
				if saved.State != "unknown" || saved.PayloadCipher != "synthetic-preserved-cipher" {
					t.Fatal("upgrade rewrote unknown consultation")
				}
			}
			if !db.Migrator().HasTable(&supportdomain.BotPolicy{}) || !db.Migrator().HasColumn(&supportdomain.BotPolicy{}, "version") {
				t.Fatal("missing per-bot policy CAS schema")
			}
			for _, column := range []string{"message_ids", "receipt_claim"} {
				if !db.Migrator().HasColumn(&runtimedomain.Delivery{}, column) {
					t.Fatalf("missing receipt column %s", column)
				}
			}
			if !fresh {
				var legacy runtimedomain.Delivery
				if err := db.First(&legacy, 17).Error; err != nil {
					t.Fatal(err)
				}
				if legacy.Status != "unknown" || legacy.MessageID != 91 || legacy.PayloadCipher != "synthetic-receipt-cipher" || !legacy.PayloadHeld || len(legacy.MessageIDs) != 0 || legacy.ReceiptClaim != "" {
					t.Fatal("migration invented receipt evidence or lost unknown payload")
				}
			}
			receipt := runtimedomain.Delivery{BotID: 2, Key: "album-receipt", Status: "unknown", MessageID: 11, MessageIDs: []int64{11, 13}, ReceiptClaim: "synthetic-claim", PayloadHeld: true}
			if err := db.Create(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			var stored runtimedomain.Delivery
			if err := db.First(&stored, receipt.ID).Error; err != nil {
				t.Fatal(err)
			}
			if len(stored.MessageIDs) != 2 || stored.MessageIDs[0] != 11 || stored.MessageIDs[1] != 13 || stored.ReceiptClaim != "synthetic-claim" {
				t.Fatal("ordered partial receipt not preserved")
			}
			topic := supportdomain.TopicMapping{OperationID: 1, BotID: 2, ChatID: -100, ThreadID: 7, SessionID: 1, State: "deleted"}
			if err := db.Create(&topic).Error; err != nil {
				t.Fatal(err)
			}
			duplicate := supportdomain.TopicMapping{OperationID: 2, BotID: 2, ChatID: -100, ThreadID: 7, SessionID: 2, State: "open"}
			if err := db.Create(&duplicate).Error; err == nil {
				t.Fatal("historical topic reassigned")
			}
		})
	}
}

// TestSupportV13OperationEvidenceSurvivesReplay checks immutable staff evidence
// and short-reference ownership across additive migration replays.
func TestSupportV13OperationEvidenceSurvivesReplay(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "operations13.sqlite"))
	if err := migrateTelegramSupportDelivery(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	reference := supportdomain.OperationReference{Ref: "synthetic-operation-ref", BotID: 2, SessionID: 3, TopicID: 4,
		ActorID: 9007199254740993, ChatID: -1001234567890, ThreadID: 6, RouteVersion: 7, Generation: 8,
		SessionVersion: 9, Action: "note", NoteCipher: "synthetic-note-cipher", ExpiresAt: now.Add(time.Minute), ConsumedAt: &now, CreatedAt: now}
	if err := db.Create(&reference).Error; err != nil {
		t.Fatal(err)
	}
	audit := supportdomain.OperationAudit{ReferenceID: reference.ID, BotID: reference.BotID, SessionID: reference.SessionID,
		ActorID: reference.ActorID, ChatID: reference.ChatID, Action: reference.Action, NoteCipher: reference.NoteCipher, CreatedAt: now}
	if err := db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramSupportDelivery(db); err != nil {
			t.Fatal(err)
		}
	}
	var saved supportdomain.OperationReference
	if err := db.First(&saved, reference.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ActorID != reference.ActorID || saved.ChatID != reference.ChatID || saved.Generation != 8 || saved.SessionVersion != 9 || saved.NoteCipher != reference.NoteCipher || saved.ConsumedAt == nil || !saved.ConsumedAt.Equal(now) {
		t.Fatal("migration altered operation ownership or encrypted evidence")
	}
	var savedAudit supportdomain.OperationAudit
	if err := db.First(&savedAudit, audit.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedAudit.ActorID != audit.ActorID || savedAudit.NoteCipher != audit.NoteCipher || savedAudit.ReferenceID != reference.ID {
		t.Fatal("migration altered immutable audit evidence")
	}
	reference.ID = 0
	if err := db.Create(&reference).Error; err == nil {
		t.Fatal("duplicate operation capability accepted")
	}
	audit.ID = 0
	if err := db.Create(&audit).Error; err == nil {
		t.Fatal("duplicate audit for consumed reference accepted")
	}
}

// TestSupportV13PolicyAndControlReplay preserves CAS settings and actor-bound menus.
func TestSupportV13PolicyAndControlReplay(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "policy-controls13.sqlite"))
	if err := migrateTelegramSupportDelivery(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	policy := supportdomain.BotPolicy{BotID: 9, Version: 3, MaxMessages: 12, MaxBytes: 65536,
		MaxAttempts: 2, RetentionSeconds: 3600, ChallengeTTLSeconds: 120, CooldownSeconds: 60, UpdatedBy: 7}
	control := supportdomain.OperationControl{BotID: 9, UpdateID: 88, Ref: "synthetic-control-reference", ActorID: 9007199254740993,
		ChatID: -1001234567890, ThreadID: 5, SessionVersion: 6, RouteVersion: 7, Generation: 8,
		PayloadCipher: "synthetic-control-cipher", ExpiresAt: now.Add(time.Minute)}
	for _, row := range []any{&policy, &control} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateTelegramSupportDelivery(db); err != nil {
		t.Fatal(err)
	}
	var saved supportdomain.BotPolicy
	if err := db.First(&saved, "bot_id = ?", policy.BotID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Version != 3 || saved.MaxMessages != 12 || saved.MaxAttempts != 2 || saved.UpdatedBy != 7 {
		t.Fatal("migration reset explicit policy or CAS ownership")
	}
	var savedControl supportdomain.OperationControl
	if err := db.First(&savedControl, control.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedControl.ActorID != control.ActorID || savedControl.Generation != 8 || savedControl.PayloadCipher != control.PayloadCipher || !savedControl.ExpiresAt.Equal(control.ExpiresAt) {
		t.Fatal("migration rewrote bound operation control")
	}
	policy.Version = 4
	if err := db.Create(&policy).Error; err == nil {
		t.Fatal("duplicate bot policy accepted")
	}
	control.ID = 0
	control.Ref = "different-ref-same-update"
	if err := db.Create(&control).Error; err == nil {
		t.Fatal("duplicate source update created a second control")
	}
}

// TestSupportV13DownstreamColumns preserves unknown outcomes when adding album metadata.
// The fixture models the earlier downstream schema; no destination evidence is invented.
func TestSupportV13DownstreamColumns(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "downstream13.sqlite"))
	if err := db.Exec(`CREATE TABLE telegram_support_downstreams (id INTEGER PRIMARY KEY, bot_id INTEGER, update_id INTEGER, session_id INTEGER, state TEXT, source_cipher TEXT, envelope_cipher TEXT, delivery_id INTEGER, version INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO telegram_support_downstreams VALUES (1, 2, 3, 4, 'unknown', 'synthetic-source', 'synthetic-envelope', 7, 5)`).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramSupportDelivery(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"album_id", "source_bytes", "destination_message_id"} {
		if !db.Migrator().HasColumn(&supportdomain.Downstream{}, column) {
			t.Fatalf("missing downstream column %s", column)
		}
	}
	var saved supportdomain.Downstream
	if err := db.First(&saved, 1).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "unknown" || saved.SourceCipher != "synthetic-source" || saved.EnvelopeCipher != "synthetic-envelope" || saved.DeliveryID != 7 || saved.Version != 5 || saved.AlbumID != 0 || saved.SourceBytes != 0 || saved.DestinationMessageID != 0 {
		t.Fatalf("downstream evidence changed: id=%d state=%s", saved.ID, saved.State)
	}
	assertModelIndexesExist(t, db, &supportdomain.Downstream{})
	assertModelIndexesExist(t, db, &supportdomain.DownstreamAlbum{})
}

func TestSupportV13ReplacesUniqueDeliveryMappingIndex(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "support13-index.sqlite"))
	model := &supportdomain.ReplyMapping{}
	if err := db.AutoMigrate(model); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(model, "idx_telegram_support_reply_mappings_delivery_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_telegram_support_reply_mappings_delivery_id ON telegram_support_reply_mappings (delivery_id)").Error; err != nil {
		t.Fatal(err)
	}
	first := supportdomain.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 10, BufferedMessageID: 1, DeliveryID: 7}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramSupportDelivery(db); err != nil {
			t.Fatal(err)
		}
	}
	second := supportdomain.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 11, BufferedMessageID: 2, DeliveryID: 7}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("album receipt cannot map multiple sources: %v", err)
	}
	duplicate := supportdomain.ReplyMapping{BotID: 1, ChatID: -100, MessageID: 10, BufferedMessageID: 3, DeliveryID: 8}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("trusted destination uniqueness lost")
	}
	var saved supportdomain.ReplyMapping
	if err := db.First(&saved, first.ID).Error; err != nil || saved.MessageID != 10 || saved.DeliveryID != 7 {
		t.Fatalf("historical mapping changed: %+v %v", saved, err)
	}
	assertModelIndexesExist(t, db, model)
}

func TestSupportV12UpgradeBaselineAndRestart(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "support.sqlite"))
		if !fresh {
			if e := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; e != nil {
				t.Fatal(e)
			}
			if e := db.Exec("INSERT INTO schema_migrations VALUES (11, 'old')").Error; e != nil {
				t.Fatal(e)
			}
		}
		baseline := func(db *gorm.DB) error { return migrateTelegramSupport(db) }
		for range 2 {
			if e := runSchemaMigrations(db, 12, baseline, schemaSteps[:11]...); e != nil {
				t.Fatal(e)
			}
		}
		for _, model := range []any{&supportdomain.Session{}, &supportdomain.Challenge{}, &supportdomain.BufferedMessage{}} {
			if !db.Migrator().HasTable(model) {
				t.Fatalf("missing %T", model)
			}
		}
		if e := db.Create(&supportdomain.Session{BotID: 1, UserID: 2, ChatID: 2, State: "waiting_verification", Version: 1}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&supportdomain.Session{BotID: 1, UserID: 2, ChatID: 2, State: "open", Version: 1}).Error; e == nil {
			t.Fatal("duplicate customer slot")
		}
	}
}

func TestTelegramGroupsV11UpgradeAndRestart(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "groups.sqlite"))
		if !fresh {
			if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO schema_migrations VALUES (10, 'synthetic')").Error; err != nil {
				t.Fatal(err)
			}
		}
		baseline := func(tx *gorm.DB) error { return migrateTelegramGroups(tx) }
		if !fresh {
			steps := append([]migrationStep(nil), schemaSteps[:10]...)
			original := steps[len(steps)-1].run
			steps[len(steps)-1].run = func(tx *gorm.DB) error {
				if err := original(tx); err != nil {
					return err
				}
				return errors.New("interrupted")
			}
			if err := runSchemaMigrations(db, 11, baseline, steps...); err == nil {
				t.Fatal("interruption ignored")
			}
		}
		for range 2 {
			if err := runSchemaMigrations(db, 11, baseline, schemaSteps[:10]...); err != nil {
				t.Fatal(err)
			}
		}
		for _, model := range []any{&groupsdomain.Target{}, &groupsdomain.Route{}, &groupsdomain.Operation{}, &groupsdomain.ModuleConfig{}} {
			if !db.Migrator().HasTable(model) {
				t.Fatalf("missing %T", model)
			}
		}
		if err := db.Create(&groupsdomain.Target{Name: "a", Purpose: "support", Version: 1, RouteVersion: 1, TelegramChatID: -100, OccupancySlot: "chat:-100"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&groupsdomain.Target{Name: "b", Purpose: "membership", BotInstanceID: 2, Version: 1, RouteVersion: 1, TelegramChatID: -100, OccupancySlot: "chat:-100"}).Error; err == nil {
			t.Fatal("physical slot uniqueness missing")
		}
	}
}

// TestPayloadRetentionV9UpgradeRollbackAndRepeat checks historical baseline upgrade, rollback and sentinels in t's temporary DB.
func TestPayloadRetentionV9UpgradeRollbackAndRepeat(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "retention-v9.sqlite"))
	fixture, err := os.ReadFile("testdata/v9_sqlite.sql")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(fixture)) != "ef76ef9f9ecbf3ccb391181d04f45052ef7ccadebef7bc8e46890552b736a11c" {
		t.Fatalf("historical v9 fixture unreadable or changed: %v", err)
	}
	if err := db.Exec(string(fixture)).Error; err != nil {
		t.Fatal(err)
	}
	var baselineValid bool
	if err := db.Raw(`SELECT (SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%')=72 AND (SELECT COUNT(*) FROM schema_migrations)=1 AND (SELECT MAX(version) FROM schema_migrations)=9`).Scan(&baselineValid).Error; err != nil || !baselineValid {
		t.Fatalf("incomplete historical baseline: %v", err)
	}
	for _, query := range []string{
		"INSERT INTO users (id,email,password_hash) VALUES (71,'synthetic-v9@example.invalid','synthetic-not-a-password')",
		"INSERT INTO wallet_accounts (id,user_id,balance) VALUES (71,71,123.45)",
		"INSERT INTO user_oauth_identities (id,user_id,provider,provider_user_id) VALUES (71,71,'telegram','9007199254740993')",
		"INSERT INTO telegram_bot_inbox (id,bot_id,update_id,payload_cipher,status,claim,attempts) VALUES (1,7,99,'inbox-cipher','processing','original-claim',2)",
		"INSERT INTO telegram_bot_deliveries (id,bot_id,`key`,chat_id,payload_cipher,status,claim,attempts) VALUES (1,7,'legacy',-100,'delivery-cipher','unknown','original-claim',2)",
	} {
		if err := db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseline := func(*gorm.DB) error { t.Fatal("unexpected baseline"); return nil }
	steps := append([]migrationStep(nil), schemaSteps...)
	injected := errors.New("synthetic interrupted v10")
	steps[8].run = func(tx *gorm.DB) error {
		if err := migrateTelegramPayloadRetention(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, schemaVersion, baseline, steps...); !errors.Is(err, injected) {
		t.Fatalf("rollback: %v", err)
	}
	if db.Migrator().HasColumn(&runtimedomain.Delivery{}, "target_type") {
		t.Fatal("SQLite DDL escaped rollback")
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 9 {
		t.Fatalf("ledger: %d %v", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
			t.Fatal(err)
		}
		assertHistoricalV9Sentinels(t, db)
	}
	var inbox runtimedomain.Inbox
	var delivery runtimedomain.Delivery
	for _, row := range []any{&inbox, &delivery} {
		if err := db.First(row, 1).Error; err != nil {
			t.Fatal(err)
		}
	}
	if inbox.PayloadHeld || inbox.PayloadRetainUntil != nil || inbox.PayloadCipher != "inbox-cipher" || inbox.Claim != "original-claim" || inbox.Attempts != 2 {
		t.Fatalf("inbox lost: %+v", inbox)
	}
	if delivery.TargetType != "shop_private" || delivery.PayloadHeld || delivery.PayloadRetainUntil != nil || delivery.PayloadCipher != "delivery-cipher" || delivery.Status != "unknown" || delivery.Claim != "original-claim" || delivery.ChatID != -100 {
		t.Fatalf("delivery lost: %+v", delivery)
	}
	until := time.Now().UTC().Add(time.Hour)
	if err := db.Model(&delivery).Updates(map[string]interface{}{"target_type": "future_unknown_target", "payload_held": true, "payload_retain_until": until}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramPayloadRetention(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&delivery, 1).Error; err != nil {
		t.Fatal(err)
	}
	if delivery.TargetType != "future_unknown_target" || !delivery.PayloadHeld || !delivery.PayloadRetainUntil.Equal(until) {
		t.Fatal("replay rewrote target/retention")
	}
	if err := db.Exec("INSERT INTO telegram_bot_inbox (bot_id,update_id,payload_cipher,status) VALUES (7,99,'dup','pending')").Error; err == nil {
		t.Fatal("inbox unique key lost")
	}
	if err := db.Exec("INSERT INTO telegram_bot_deliveries (bot_id,`key`) VALUES (7,'legacy')").Error; err == nil {
		t.Fatal("delivery unique key lost")
	}
	// A valid v9 plan must reach the ledger check, not fail plan-length validation.
	if err := runSchemaMigrations(db, 9, baseline, schemaSteps[:8]...); err == nil || !strings.Contains(err.Error(), "unknown or noncontiguous schema migration version 10 (target 9)") {
		t.Fatalf("old binary did not reject the newer ledger: %v", err)
	}
}

func TestMediaCacheMigrationFromV7AndRepeat(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "media.sqlite"))
	if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at TIMESTAMP NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations (version,completed_at) VALUES (7,CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { t.Fatal("baseline must not run"); return nil }, schemaSteps...); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable(&runtimedomain.MediaCache{}) {
		t.Fatal("cache table absent")
	}
	for _, bot := range []uint{1, 2} {
		if err := db.Create(&runtimedomain.MediaCache{BotID: bot, PathHash: "path", ContentHash: "content", FileID: "file"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&runtimedomain.MediaCache{BotID: 1, PathHash: "path", ContentHash: "content", FileID: "other"}).Error; err == nil {
		t.Fatal("duplicate cache key accepted")
	}
}

func TestPanelMigrationFromV6PreservesSessionAndQueue(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "panel-v6.sqlite"))
	if err := db.Exec(`CREATE TABLE telegram_bot_sessions (id integer PRIMARY KEY, bot_id integer NOT NULL, telegram_user_id integer NOT NULL, chat_id integer NOT NULL, version integer NOT NULL, state text, data_cipher text, expires_at datetime, created_at datetime, updated_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO telegram_bot_sessions (id,bot_id,telegram_user_id,chat_id,version,state,data_cipher) VALUES (1,1,9,9,4,'product','synthetic-cipher')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO schema_migrations VALUES (6,'synthetic')`).Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { t.Fatal("baseline must not run"); return nil }, schemaSteps...); err != nil {
			t.Fatal(err)
		}
	}
	var session runtimedomain.Session
	if err := db.First(&session, 1).Error; err != nil {
		t.Fatal(err)
	}
	if session.Version != 4 || session.State != "product" || session.DataCipher != "synthetic-cipher" || session.PanelMessageID != 0 || session.PanelRevision != 0 {
		t.Fatalf("%+v", session)
	}
	for _, column := range []string{"purpose", "operation", "panel_epoch", "panel_revision", "target_message_id", "previous_message_id", "message_kind"} {
		if !db.Migrator().HasColumn(&runtimedomain.Delivery{}, column) {
			t.Fatal(column)
		}
	}
}

// TrustedTelegramHistoryBotIDs adds only a SELECT over existing runtime/order
// tables in userauth's gormstore; no model, DDL or migration data changes. The
// conservative fingerprint includes store method bodies too. Retain the previous
// v9 input for audit, as with the import-only renames above; schemaVersion stays 9.
const preTelegramHistoryProvenanceV9Fingerprint = "22881cd60a935140182a7e9377984210d93280c5ca69d9f4ed7fa6d28d5bdac3"

// Pre-guard v11 fingerprint retained for audit. v11 is unreleased; this change
// adds route-transition/read guards only, with no schema or migration change.
const preGroupRouteGuardV11Fingerprint = "7b0d8f5fbc1c7f412bb175b0b1966a1c34241ca7971523e92499b6f9a5fba2bb"

// Preserve the previous unreleased v11 source hash: the module read projection
// now returns configured/effective flags in a contract DTO. Domain models and
// migration DDL are unchanged; the fingerprint also covers store method bodies.
const preModuleEnableProjectionV11Fingerprint = "7cb6d249cb7915e2d13e9ca7e3b920d39accfc1c49732dc29d5d77830d4c4120"

// preBotSafetyStageV21Fingerprint retains the unreleased source baseline; no DDL changed.
const preBotSafetyStageV21Fingerprint = "c812cdeea95a65bff73774876046a30e5ad1a97549fdabb242c89c55b9dd8840"

var schemaFingerprints = map[int64]string{
	// v48 records idle-scan safety only; existing schemas and historical records remain unchanged.
	48: "e2fe2af591fa7bed27b79be0e505e9299d3e46d8ebd05c4f6f2d80a3df63b503",
	// v47 adds refund retry leases/backoff without replaying money or rewriting original commands.
	47: "98b9255f32bad66fc069b57aa1ae477a29d1cb93e008a477b393b3de354e5211",
	// v46 records audit locking/recovery safety without changing tables or historical data.
	46: "bf3830bb440796057fb9a817e357e6480729c000db79818e870ca00db6de5093",
	// v45 records store safety corrections without DDL; preserve all earlier registrations.
	45: "c14169ae90cd248fc10e49c5429fa45fc3fa0af0b1c9b5804b9966d40b5b7f20",
	// v44 binds refund decisions to exact lifecycle tasks without backfilling legacy evidence.
	44: "13149f63848d2758630ee0aa82bed7d96a07015c31a037edb1091edfd18456db",
	// v43 records read-only admin group names; schema and historical rows are unchanged.
	43: "57fd048fa4dc6bfe70b5725c623944fb04e05e741b2d4f60d232c58cc23815d2",
	// v42 adds permanent unique product share codes.
	42: "41fd2c1e9c66aa7a99561ee157b06af920f8268e49e883ef9c4b0014f5f8ed9e",
	// v41 records bot-bound encrypted share encoder injection without schema or historical rewrites.
	41: "9462784dc10d1dcdc5348b0aae10b07bbaa664ccae698a12ed9b70abb643ddaf",
	// v40 records read-only membership share projection without DDL or historical rewrites.
	40: "421627361b53e18275285cbd054879bd5e6fb002215def20f3b61ba3e750a1f4",
	// v39 records verified callback cleanup and definite missing-topic recovery without DDL.
	39: "60793f4a92b86b5d504a157cbed23d88ab86bd53e66c228f17afb77d96092a37",
	// v38 adds daily arithmetic verification, customer identity and shared inquiry controls.
	38: "a7860e8fee8ed33ee69b53f927bce47ced750e9a0cafd712035a38f83a13726a",
	// v37 records direct topic reply routing without DDL or historical message replay.
	37: "dec5030b73afbc6b117c56bcf0c037256d87c72aca4bf6180cce60a9fc7287ee",
	// v36 records support ingress checks without DDL or historical message rewrites.
	36: "3b4f134f0749366cdfc676a49dc909d3f6d1b48fea6d5db69e4387740d2b39d6",
	// v35 records order-scoped evidence and task provenance reads without rewriting stored data.
	35: "f84e642719eba8f1deb14f68197c7508e02552a9a4e01933ca4451823bd699bf",
	// v34 records lifecycle renewal protection and filtered reads without DDL or history rewrites.
	34: "907b3e7b5ec5f2ea3cfc957f8ab5952a7bf31e0ed7252b28c9ee4324b9aea6f6",
	// v33 records fixed UTC+08 calendar reads without rewriting stored instants.
	33: "e8d7e8cc0fb28344ba7d076ec972a978d6453801e4feb0514f8e6950f4efbfb3",
	// v32 records authoritative numbered reads; no schema or historical data changes.
	32: "e7c34055662d4ab50ab95e7b4bda9fb046313dbb569d0431d9978ac004ac1ede",
	// v31 records membership order evidence and invitation-abuse notification safety without data rewrites.
	31: "d368ddcaac8cb91ae5851c5e5327ac8038f182209fde3d88f428a4ade3a4e0b1",
	// v30 adds durable checkpoints for independent live and historical delivery scans.
	30: "c79a3fe201bd864712cacf48a10b4cada237bf3811e80e7c6cc26e67c06c9473",
	// v29 registers durable proactive delivery responsibilities in upgrades and fresh baselines.
	29: "38738d050d432f14fc842146ec3d792852ee5fa4efa7d5339d59bdae62900465",
	// v28 adds durable refund commands for databases already at v27.
	28: "742181fd374be1b02c9becda39251cde17f1413a084c4d482875ad41ec040938",
	// v27 adds explicit invitation terms and independent cleanup persistence.
	27: "42d72806cac0d651dd5e2b07e117eca54e5eb5e491ea1ffbce518f0d9b87049b",
	// v26 records guest panel cleanup and support blocking safety in the store-source
	// contract. No models or DDL changed; its replay-safe step preserves historical data.
	26: "5dfc6a39eb1b58ccaff0326afd9ac4915bbe2f537affc18e7486182a9a825bfd",
	// v25 is still under local acceptance. Cross-bot attention reads change the conservative
	// store-source hash only; no models, tables or migration steps are changed.
	25: "c1d6c2542517fdfbe7cddf97a5409d6b833ef1c9c2aaeb4e127d65d8ebb40b85",
	// v24 adds immutable manual receipt audits, evidence claims and late-receipt anomalies.
	// ClaimEvidence query/locking changes are covered by the conservative store-source hash; no DDL changed.
	// Previous v24 fingerprint: 23b82dae4407eed865ee2b4fee642e64fc9653a7d0161772a97d7b633e9dc95d.
	// Product operations and batched SKU/config queries change store-source hashing only; no model/DDL changed.
	// Previous v24 fingerprint: 8cd4eab029b30e7c39ad450a243b4e0d266bb110280617f52189bb578af9df7f.
	// Membership sale-state validation and receipt guards change store-source hashing only; no model or DDL changed.
	// Previous v24 fingerprint: 26be908c930f2dffd8e79a25e4b1f2776c95482be4b6eee8f364edf4c42d6d50.
	// Session cleanup now compares UTC timestamps; store-source hashing changes without model/DDL changes.
	// Previous v24 fingerprint: 4ef7905ddb23fb4b9efe155254b3cba3fa031de4488355032690285d1bd4cfaf.
	// SQL JSON scanners now accept SQLite TEXT; no column/model/DDL changes.
	// Previous v24 fingerprint: 75a6bc776db82cf5be73c37117b1a552d3f84cdbee5223f6a8adbe370e07e91c.
	24: "8cd87b6d55d9b7ea7f6e74b94045dee01b71c0724129a58375d6360a40318485",
	// v23 adds counted downstream sources and bounded restoration scan/receipt cursors.
	// Qualify the existing v19 CHECK column for MySQL reserved-word compatibility;
	// column/constraint semantics are unchanged, so existing v23 databases need no DDL.
	// Previous fingerprint: e6aa1cf8e6bd752fe520c2ea67ca874fa6fe479ed06b4d4d2349141493ab665f.
	// Query-only dialect quoting also participates in this conservative source hash;
	// no model/DDL change. Previous: 75e17d29c70663a03626e57c6e25a385a97ba783bc2ac11aa13858f27845545e.
	// ObserveTopic only removes a duplicate root-pool execution check inside its
	// transaction; models and DDL are unchanged. Restoring that check reproduces
	// previous fingerprint 81d56eedfde5d9563a8857e571a78184292a6096af9b106df2940f354821cc12.
	23: "65add365508a146c131d1df1cde4bcf288447a2435b963bf354178c61a1b9784",
	// v22 adds per-target recovery receipts and preserves the legacy parent receipts.
	// The unreleased fingerprint also covers task-bound membership queue/query code.
	22: "64ff52e25617860201bb81bc2f41f8ad4560d023a2af3b415a037f63eadcdb74",
	// v21 remains unchanged at the SQL level: bot recovery adds SnapshotJSON DTOs
	// and transaction write guards only. Preserve its pre-stage source hash below.
	21: "64826eae46496ad25738757025af61e3e72aeec5847be1489030e39376f47469",
	// v20 adds reviewed beneficiary mappings and local support disaster checkpoints.
	20: "528178aee289753da3c8bf47ac34a323e422ddf69b4f4d3e3d6be5c5519194b6",
	// v19 adds recovery task checkpoints and persistent group route fences.
	19: "103297ff67b2a060185a75c3b789f51f554757d553869bfb74e762a5e47e7799",
	// v18 增加生命周期、退款处置与提醒持久状态；保留既有 v17 历史指纹。
	18: "834ef5f8273aec35132832464ae418cac2747c0e6567a28c1ee6c05a57779373",
	// v17 新增申请式邀请、审批任务、租约结果与已知成员；升级和中断重放另有回归。
	17: "aff4ea2a2570bd2a67513a5bee4873fa6b4cfeaf63bab0d110a051c939589fb7",
	// v16 新增权益来源、撤权、投影、可靠付款事件、恢复任务与项级履约回执。
	16: "90e3a6187bd168436f8b2b18172f8642293ef328a3db31b2b6b3031c101ccddd",
	// v15 尚未发布：同步安全群名读取、事务观测校验及纯领域权益聚合后的源码指纹。
	// 指纹覆盖 domain/gormstore 方法体；本轮未增加持久模型或 DDL，升级/重放测试独立验证。
	15: "f51f969117a9087526a55b9aef87cbd884b16c718bec0c337ac5d9975d275f72",
	14: "21adf1e40ebb5fab2927e42ffc446a13e5a2184416b2126dd79f7231da2874f9",
	// v13 includes the unreleased support models and admin task projections.
	// The conservative source fingerprint also includes gormstore query bodies.
	13: "98836094eafb85e75f43361a4d9e984f0b75ed4775ff6a9f0aad41003cfac4d8",
	12: "e807e7d1e3dfad65f1a86ea13f26fa000c750c5a0cabe7b99877a3eee40ed651",
	11: "8442141a3308fff62f50594ba6d6c4740c612b1531b8d146bb55b8666865d68c",
	10: "f4a1cd99e22a9d8d894650b162220d134d1b41a46aa2f88e723a21cbc1eb6385",
	9:  "2bd81621083efdcbe8f3bd16882afa4d3069c57f565e9127b390f9bcb37c150e",
	8:  "f99f3860c45ec866cd471e776b231f79f31a6e79c8fcd70b768b147fd69212e2",
	7:  "0dc026aff0197e4c279c5343c2cb0572e99fbeccf6d86e95ac4178d519f55379",
	1:  "6aa3cd137e927bbeee42774a013fec690c0930f262434d2c686dc6464f7a6c26",
	2:  "3b601bf8773d9f82915578189f724ded072ff20a4803aaae6f7898af12f11600",
	// Final unreleased v3 includes broadcast retry and procurement recipient store logic.
	// Both are covered by the existing v3 model migration; no deployed version is rewritten.
	3: "da6480dffd796c57a13bc40d6b0f3f743b21a9a3d99216e0b37978ae4e42a5db",
	4: "29e6bbdf957d8c52063fa619d7aff50ee3eaf6ac9b492632ac818dad16f5ad10",
	5: "63934f8dc6a6b0ed97b9ca46269fcd4a08ea4eeaf8a13a2226c5bd73163a509f",
	// v6 retires ownership from fresh schemas; upgrades retain existing data and
	// obsolete columns/tables without creating or backfilling new snapshots.
	6: "63c365beac07c1f5ba17da1b005467bea6118a33553f742e32fd9f649f1e84fd",
}

// Frozen v5 fixture: runtime no longer owns this historical table. Keep its
// complete shape here so upgrades prove old snapshots survive without depending
// on removed runtime domain types.
type legacyBotOwnership struct {
	LockVersion    uint64
	AttestedAt     *time.Time
	RepairRevision uint64
	ID             uint   `gorm:"primaryKey"`
	ChannelID      uint   `gorm:"uniqueIndex:idx_bot_ownership"`
	TelegramID     int64  `gorm:"uniqueIndex:idx_bot_ownership"`
	State          string `gorm:"size:24"`
	SnapshotCipher string `gorm:"type:text"`
}

func (legacyBotOwnership) TableName() string { return "telegram_bot_ownership" }

func TestOwnershipRetirementPreservesDeployedV1ThroughV5(t *testing.T) {
	for _, version := range []int64{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "retired.sqlite"))
			client := channel.Client{Name: "legacy", ChannelType: "telegram_bot", ChannelKey: "legacy", BotToken: "synthetic-cipher", ChannelSecret: "synthetic-secret"}
			bot := runtimedomain.Instance{TelegramID: 123, TokenCipher: "synthetic", Mode: "internal", Enabled: true}
			history := legacyBotOwnership{ID: 2, ChannelID: 1, State: "unresolved", SnapshotCipher: "historical-cipher", RepairRevision: 7, LockVersion: 9}
			if err := runSchemaMigrations(db, version, func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&channel.Client{}); err != nil {
					return err
				}
				if err := migrateTelegramRuntime(tx); err != nil {
					return err
				}
				if err := tx.Create(&client).Error; err != nil {
					return err
				}
				if version < 5 {
					if err := tx.Migrator().DropColumn(&channel.Client{}, "revision"); err != nil {
						return err
					}
				}
				if err := tx.Create(&bot).Error; err != nil {
					return err
				}
				// Deployed schemas may retain linkage columns removed from runtime.
				if !tx.Migrator().HasColumn(&bot, "channel_client_id") {
					if err := tx.Exec("ALTER TABLE telegram_bot_instances ADD COLUMN channel_client_id INTEGER").Error; err != nil {
						return err
					}
				}
				if !tx.Migrator().HasColumn(&bot, "cutover_completed") {
					if err := tx.Exec("ALTER TABLE telegram_bot_instances ADD COLUMN cutover_completed NUMERIC").Error; err != nil {
						return err
					}
				}
				if err := tx.Exec("UPDATE telegram_bot_instances SET channel_client_id = ?, cutover_completed = ? WHERE id = ?", client.ID, true, bot.ID).Error; err != nil {
					return err
				}
				if version == 5 {
					if err := tx.AutoMigrate(&legacyBotOwnership{}); err != nil {
						return err
					}
					return tx.Create(&history).Error
				}
				return nil
			}, schemaSteps[:version-1]...); err != nil {
				t.Fatal(err)
			}
			baseline := func(*gorm.DB) error { return errors.New("unexpected baseline") }
			for attempt := 0; attempt < 2; attempt++ {
				if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
					t.Fatal(err)
				}
			}
			var preserved channel.Client
			if err := db.First(&preserved, client.ID).Error; err != nil {
				t.Fatal(err)
			}
			if preserved.Revision != 1 {
				t.Fatalf("channel CAS revision was not upgraded: %d", preserved.Revision)
			}
			if preserved.BotToken != client.BotToken || preserved.ChannelSecret != client.ChannelSecret || preserved.ChannelKey != client.ChannelKey {
				t.Fatal("upgrade changed legacy channel credentials")
			}
			var preservedBot runtimedomain.Instance
			if err := db.First(&preservedBot, bot.ID).Error; err != nil || preservedBot.TelegramID != bot.TelegramID || preservedBot.TokenCipher != bot.TokenCipher || !preservedBot.Enabled {
				t.Fatalf("upgrade changed bot record: %+v, %v", preservedBot, err)
			}
			var linkage struct {
				ChannelClientID  uint
				CutoverCompleted bool
			}
			if err := db.Table("telegram_bot_instances").Select("channel_client_id", "cutover_completed").Where("id = ?", bot.ID).Scan(&linkage).Error; err != nil || linkage.ChannelClientID != client.ID || !linkage.CutoverCompleted {
				t.Fatalf("upgrade destroyed historical linkage: %+v, %v", linkage, err)
			}
			if version == 5 {
				var rows []legacyBotOwnership
				if err := db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0] != history {
					t.Fatalf("upgrade rewrote ownership history: %+v, %v", rows, err)
				}
			} else if db.Migrator().HasTable(&legacyBotOwnership{}) {
				t.Fatal("upgrade manufactured retired ownership snapshots")
			}
			var versions []int64
			if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
				t.Fatal(err)
			}
			if len(versions) != int(schemaVersion-version+1) {
				t.Fatalf("wrong ledger: %v", versions)
			}
			for i, got := range versions {
				if got != version+int64(i) {
					t.Fatalf("noncontiguous ledger: %v", versions)
				}
			}
		})
	}
}

func TestTelegramUpgradeFromV1AndV2(t *testing.T) {
	for _, scenario := range []struct {
		version int64
		retry   bool
	}{{1, false}, {1, true}, {2, false}, {2, true}} {
		t.Run(fmt.Sprintf("from_v%d_retry_%t", scenario.version, scenario.retry), func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "telegram.sqlite"))
			if err := runSchemaMigrations(db, scenario.version, func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&paymentdomain.Payment{}, &broadcastdomain.Broadcast{}); err != nil {
					return err
				}
				for _, payment := range []paymentdomain.Payment{
					{OrderID: 42, ChannelID: 1, ProviderType: "epay", ChannelType: "alipay", InteractionMode: "redirect", Amount: money.FromDecimal(decimal.NewFromInt(12)), Currency: "CNY", Status: "pending", PayURL: "https://example.test/pay"},
					{OrderID: 42, ChannelID: 1, ProviderType: "epay", ChannelType: "alipay", InteractionMode: "redirect", Amount: money.FromDecimal(decimal.NewFromInt(12)), Currency: "CNY", Status: "failed"},
				} {
					if err := tx.Create(&payment).Error; err != nil {
						return err
					}
				}
				if err := tx.Create(&broadcastdomain.Broadcast{Title: "legacy", RecipientType: "all", MessageHTML: "legacy message", Status: "completed", SuccessCount: 2}).Error; err != nil {
					return err
				}
				// Remove every new payment/broadcast field, not just source IDs.
				if err := tx.Migrator().DropIndex(&paymentdomain.Payment{}, "idx_payment_order_operation"); err != nil {
					return err
				}
				for _, column := range []string{"operation_key", "operation_hash", "creation_state", "source_bot_id", "source_telegram_id", "source_private_chat_id"} {
					if err := tx.Migrator().DropColumn(&paymentdomain.Payment{}, column); err != nil {
						return err
					}
				}
				for _, column := range []string{"bot_id", "delivery_version", "payload_cipher", "unknown_count"} {
					if err := tx.Migrator().DropColumn(&broadcastdomain.Broadcast{}, column); err != nil {
						return err
					}
				}
				if scenario.version == 2 {
					if err := migrateTelegramRuntime(tx); err != nil {
						return err
					}
					for _, column := range []string{"telegram_user_id", "user_id", "broadcast_id"} {
						if err := tx.Migrator().DropColumn(&runtimedomain.Delivery{}, column); err != nil {
							return err
						}
					}
				}
				return tx.Exec("CREATE TABLE retained_legacy (id INTEGER PRIMARY KEY, value TEXT)").Error
			}, schemaSteps[:scenario.version-1]...); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO retained_legacy (id,value) VALUES (1,'preserved')").Error; err != nil {
				t.Fatal(err)
			}
			baseline := func(*gorm.DB) error { t.Fatal("versioned database ran baseline"); return nil }
			if scenario.retry {
				steps := append([]migrationStep(nil), schemaSteps...)
				injected := errors.New("injected v3 failure after DDL")
				steps[1].run = func(tx *gorm.DB) error {
					if err := migrateTelegramBusinessDelivery(tx); err != nil {
						return err
					}
					return injected
				}
				if err := runSchemaMigrations(db, schemaVersion, baseline, steps...); !errors.Is(err, injected) {
					t.Fatalf("failure = %v", err)
				}
				var completed int64
				if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&completed).Error; err != nil {
					t.Fatal(err)
				}
				if completed != scenario.version {
					t.Fatalf("failed SQLite attempt committed version %d", completed)
				}
			}
			if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
				t.Fatal(err)
			}
			if db.Migrator().HasTable(&legacyBotOwnership{}) {
				t.Fatal("upgrade manufactured retired ownership table")
			}
			for _, column := range []string{"channel_client_id", "cutover_completed"} {
				if db.Migrator().HasColumn(&runtimedomain.Instance{}, column) {
					t.Fatalf("upgrade manufactured retired linkage column %s", column)
				}
			}
			for _, model := range []interface{}{
				&runtimedomain.Instance{}, &runtimedomain.Contact{}, &runtimedomain.Inbox{}, &runtimedomain.Session{}, &runtimedomain.Delivery{},
				&userauthdomain.TelegramHistoryGrant{}, &deliverycontract.Event{}, &deliverycontract.OrderSource{}, &deliverycontract.OrderOperation{},
				&paymentdomain.Payment{}, &broadcastdomain.Broadcast{},
			} {
				stmt := &gorm.Statement{DB: db}
				if err := stmt.Parse(model); err != nil {
					t.Fatal(err)
				}
				for _, field := range stmt.Schema.Fields {
					if field.DBName != "" && !field.IgnoreMigration && !db.Migrator().HasColumn(model, field.DBName) {
						t.Fatalf("missing %T column %s", model, field.DBName)
					}
				}
				for _, index := range stmt.Schema.ParseIndexes() {
					if !db.Migrator().HasIndex(model, index.Name) {
						t.Fatalf("missing %T index %s", model, index.Name)
					}
				}
				if !db.Migrator().HasTable(model) {
					t.Fatalf("missing Telegram table %T", model)
				}
			}
			for _, column := range []string{"source_bot_id", "source_telegram_id", "source_private_chat_id"} {
				if !db.Migrator().HasColumn(&paymentdomain.Payment{}, column) {
					t.Fatalf("missing payment column %s", column)
				}
			}
			if !db.Migrator().HasColumn(&broadcastdomain.Broadcast{}, "bot_id") {
				t.Fatal("missing broadcast bot_id")
			}
			var payments []paymentdomain.Payment
			if err := db.Order("id").Find(&payments).Error; err != nil {
				t.Fatal(err)
			}
			if len(payments) != 2 || payments[0].PayURL != "https://example.test/pay" || payments[0].OperationKey != nil || payments[1].OperationKey != nil || !payments[0].Amount.Decimal.Equal(decimal.NewFromInt(12)) {
				t.Fatalf("legacy payments changed: %+v", payments)
			}
			var broadcast broadcastdomain.Broadcast
			if err := db.First(&broadcast).Error; err != nil {
				t.Fatal(err)
			}
			if broadcast.MessageHTML != "legacy message" || broadcast.SuccessCount != 2 || broadcast.DeliveryVersion != 0 || broadcast.UnknownCount != 0 || broadcast.PayloadCipher != "" {
				t.Fatalf("legacy broadcast changed: %+v", broadcast)
			}
			// The nullable key must preserve legacy duplicate order IDs while enforcing
			// uniqueness for newly keyed payment operations after an upgrade.
			key := "migration-test"
			if err := db.Model(&payments[0]).Update("operation_key", key).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&payments[1]).Update("operation_key", key).Error; err == nil {
				t.Fatal("payment operation unique index is ineffective")
			}
			var value string
			if err := db.Raw("SELECT value FROM retained_legacy WHERE id = 1").Scan(&value).Error; err != nil || value != "preserved" {
				t.Fatalf("legacy row changed: %q %v", value, err)
			}
			var versions []int64
			if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
				t.Fatal(err)
			}
			if len(versions) != int(schemaVersion-scenario.version+1) || versions[0] != scenario.version || versions[len(versions)-1] != schemaVersion {
				t.Fatal(versions)
			}
			if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
				t.Fatal(err)
			}
			// Replay v3 directly as well: MySQL may retain DDL after a failed step.
			if err := migrateTelegramBusinessDelivery(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestGuestSafetyV26UpgradePreservesData verifies the marker, interrupted upgrade
// rollback and replay using t's disposable SQLite database; no real DB is accessed.
func TestGuestSafetyV26UpgradePreservesData(t *testing.T) {
	for range 2 {
		// A nil connection proves the behavior-only step never issues DDL or DML.
		if err := migrateTelegramGuestSafety(nil); err != nil {
			t.Fatalf("safety marker must be a repeatable no-op: %v", err)
		}
	}
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "guest-safety-v26.sqlite"))
	for _, statement := range []string{
		"CREATE TABLE safety_evidence (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)",
		"INSERT INTO safety_evidence VALUES (1, 'synthetic-preserved-panel-session-block')",
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (25, 'synthetic-v25')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	var schemaBefore string
	if err := db.Raw("SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master ORDER BY name)").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { return errors.New("versioned database must not run baseline") }
	steps := append([]migrationStep(nil), schemaSteps[:25]...)
	original := steps[24].run
	interrupted := errors.New("synthetic v26 interruption")
	steps[24].run = func(tx *gorm.DB) error {
		if err := original(tx); err != nil {
			return err
		}
		return interrupted
	}
	if err := runSchemaMigrations(db, 26, baseline, steps...); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted upgrade: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 25 {
		t.Fatalf("interrupted upgrade advanced ledger: version=%d err=%v", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, 26, baseline, schemaSteps[:25]...); err != nil {
			t.Fatal(err)
		}
	}
	var schemaAfter, payload, completedAt string
	if err := db.Raw("SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master ORDER BY name)").Scan(&schemaAfter).Error; err != nil || schemaAfter != schemaBefore {
		t.Fatalf("behavior-only upgrade changed schema: %v", err)
	}
	if err := db.Raw("SELECT payload FROM safety_evidence WHERE id = 1").Scan(&payload).Error; err != nil || payload != "synthetic-preserved-panel-session-block" {
		t.Fatalf("behavior-only upgrade changed historical evidence: %v", err)
	}
	if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version = 25").Scan(&completedAt).Error; err != nil || completedAt != "synthetic-v25" {
		t.Fatalf("upgrade changed v25 ledger: %v", err)
	}
	var count int64
	if err := db.Table("schema_migrations").Where("version = ?", 26).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("replay must record v26 exactly once: count=%d err=%v", count, err)
	}
}

func TestSchemaVersionCoversModelsAndMigrations(t *testing.T) {
	root := filepath.Clean("../../../..")
	hash := sha256.New()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		include := strings.HasPrefix(name, "internal/bootstrap/database/migrations/") ||
			strings.Contains(name, "/domain/") || strings.Contains(name, "/infrastructure/gormstore/") ||
			strings.HasPrefix(name, "internal/shared/money/") || strings.HasPrefix(name, "internal/shared/jsonmap/")
		if !include {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Exclude only the explicit version constant to avoid circular hashes.
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					if len(value.Names) == 1 && value.Names[0].Name == "schemaVersion" {
						value.Values = []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "0"}}
					}
				}
			}
		}
		if strings.HasSuffix(name, "/migrations/registry.go") {
			file.Decls = schemaRegistryDeclarations(file.Decls)
		}
		var canonical bytes.Buffer
		if err := format.Node(&canonical, fset, file); err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", name, canonical.Bytes())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if expected, ok := schemaFingerprints[schemaVersion]; !ok || expected != actual {
		t.Fatalf("schema inputs changed: increment schemaVersion and add its fingerprint; version=%d fingerprint=%s", schemaVersion, actual)
	}
}

// Keep unknown/new declarations by default: schema helpers must not silently
// escape the gate. Only explicitly identified runner infrastructure is excluded.
func schemaRegistryDeclarations(decls []ast.Decl) []ast.Decl {
	runnerOnly := map[string]bool{
		"AutoMigrate": true, "validateMigrationSteps": true, "runSchemaMigrations": true,
		"validateMigrationForeignKeys": true, "withMigrationLock": true,
		"Commit": true, "Rollback": true, "migrateModelsWithProgress": true,
		"sqliteMigrationTx": true, "migrationStep": true,
		"schemaVersion": true, "migrationLockName": true, "migrationAdvisoryKey": true,
	}
	var result []ast.Decl
	for _, decl := range decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			if runnerOnly[node.Name.Name] {
				continue
			}
			// The wrapper only adds per-table progress. Canonicalize its call to
			// the original AutoMigrate call; retain the complete model list.
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "migrateModelsWithProgress" && len(call.Args) > 0 {
					call.Fun = &ast.SelectorExpr{X: call.Args[0], Sel: ast.NewIdent("AutoMigrate")}
					call.Args = call.Args[1:]
				}
				return true
			})
		case *ast.GenDecl:
			if node.Tok == token.IMPORT {
				continue
			}
			var specs []ast.Spec
			for _, spec := range node.Specs {
				skip := false
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					skip = runnerOnly[spec.Name.Name]
				case *ast.ValueSpec:
					skip = len(spec.Names) == 1 && runnerOnly[spec.Names[0].Name]
				}
				if !skip {
					specs = append(specs, spec)
				}
			}
			node.Specs = specs
			if len(specs) == 0 {
				continue
			}
		}
		result = append(result, decl)
	}
	return result
}

func TestSchemaFingerprintExcludesRunnerButIncludesSchemaInputs(t *testing.T) {
	const source = `package migrations
const schemaVersion = 1
var schemaSteps = []migrationStep{}
func runSchemaMigrations() { lock() }
func migrateModelsWithProgress() { progress() }
func migrateSchema(db DB) { db.AutoMigrate(&Model{}); backfill() }
func backfill() { updateRows() }
`
	canonical := func(source string) string {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "registry.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		file.Decls = schemaRegistryDeclarations(file.Decls)
		var out bytes.Buffer
		if err := format.Node(&out, fset, file); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	before := canonical(source)
	for _, change := range [][2]string{{"lock()", "newLock()"}, {"progress()", "moreProgress()"}, {"schemaVersion = 1", "schemaVersion = 2"}, {"db.AutoMigrate(&Model{})", "migrateModelsWithProgress(db, &Model{})"}} {
		if canonical(strings.Replace(source, change[0], change[1], 1)) != before {
			t.Fatalf("runner-only change affected fingerprint: %v", change)
		}
	}
	for _, change := range [][2]string{{"&Model{}", "&NewModel{}"}, {"updateRows()", "differentRows()"}, {"[]migrationStep{}", "[]migrationStep{{version: 2}}"}} {
		if canonical(strings.Replace(source, change[0], change[1], 1)) == before {
			t.Fatalf("schema input escaped fingerprint: %v", change)
		}
	}
}

type migrationSQLRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *migrationSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *migrationSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

func openVersionRunnerDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1) // A hidden fallback to the global pool would deadlock.
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestVersionRunnerFirstRepeatFailureAndUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.sqlite")
	db := openVersionRunnerDB(t, path)
	if err := db.Exec("CREATE TABLE legacy_data (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO legacy_data VALUES (1, 10)").Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected migration failure")
	calls := 0
	migrate := func(tx *gorm.DB) error {
		calls++
		return tx.Transaction(func(inner *gorm.DB) error {
			if err := inner.Exec("UPDATE legacy_data SET value = value + 1").Error; err != nil {
				return err
			}
			if calls == 1 {
				return failure
			}
			return nil
		})
	}
	if err := runSchemaMigrations(db, 1, migrate); !errors.Is(err, failure) {
		t.Fatalf("failure lost: %v", err)
	}
	var value int
	if err := db.Raw("SELECT value FROM legacy_data").Scan(&value).Error; err != nil || value != 10 {
		t.Fatalf("rollback value=%d err=%v", value, err)
	}
	if err := runSchemaMigrations(db, 1, migrate); err != nil {
		t.Fatal(err)
	}
	// Reopen independently: completion must survive the original connection.
	reopened := openVersionRunnerDB(t, path)
	recorder := &migrationSQLRecorder{Interface: logger.Default}
	if err := runSchemaMigrations(reopened.Session(&gorm.Session{Logger: recorder}), 1, migrate); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("completed version reran: %d calls", calls)
	}
	if len(recorder.statements) != 2 {
		t.Fatalf("fast path must issue only ledger DDL/read via GORM: %v", recorder.statements)
	}
	for _, statement := range recorder.statements {
		if strings.Contains(statement, "sqlite_master") || strings.Contains(statement, "legacy_data") {
			t.Fatalf("fast path scanned schema/data: %s", statement)
		}
	}
	if err := runSchemaMigrations(reopened, 2, func(*gorm.DB) error { t.Fatal("upgrade reran baseline"); return nil }, migrationStep{2, "increment legacy value", migrate}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("upgrade skipped: %d", calls)
	}
	if err := reopened.Raw("SELECT value FROM legacy_data").Scan(&value).Error; err != nil || value != 12 {
		t.Fatalf("upgrade value=%d err=%v", value, err)
	}
	if err := runSchemaMigrations(reopened, 1, migrate); err == nil {
		t.Fatal("downgrade accepted")
	}
	var fk int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil || fk != 1 {
		t.Fatalf("foreign keys not restored: %d %v", fk, err)
	}
}

func TestVersionRunnerConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	first, second := openVersionRunnerDB(t, path), openVersionRunnerDB(t, path)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	migrate := func(tx *gorm.DB) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return tx.Transaction(func(inner *gorm.DB) error { return inner.Exec("CREATE TABLE applied (id INTEGER)").Error })
	}
	results := make(chan error, 2)
	go func() { results <- runSchemaMigrations(first, 1, migrate) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first runner did not acquire lock")
	}
	go func() { results <- runSchemaMigrations(second, 1, migrate) }()
	select {
	case err := <-results:
		t.Fatalf("second runner bypassed held lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("parallel baseline calls=%d", calls.Load())
	}
}

func TestVersionRunnerCrossProcess(t *testing.T) {
	if path := os.Getenv("XMALL_MIGRATION_TEST_DB"); path != "" {
		db := openVersionRunnerDB(t, path)
		if err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
			time.Sleep(100 * time.Millisecond)
			return tx.Exec("INSERT INTO executions (id) VALUES (1)").Error
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "processes.sqlite")
	db := openVersionRunnerDB(t, path)
	if err := db.Exec("CREATE TABLE executions (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 3)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestVersionRunnerCrossProcess$", "-test.timeout=30s")
		commands[i].Env = append(os.Environ(), "XMALL_MIGRATION_TEST_DB="+path, "TEST_MYSQL_DSN=", "TEST_POSTGRES_DSN=")
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outputs[i].String())
		}
	}
	var count int
	if err := db.Raw("SELECT COUNT(*) FROM executions").Scan(&count).Error; err != nil || count != 1 {
		t.Fatalf("executions=%d err=%v", count, err)
	}
}

func TestVersionRunnerPanicReleasesLockAndRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panic.sqlite")
	db := openVersionRunnerDB(t, path)
	func() {
		defer func() {
			if recover() != "injected panic" {
				t.Error("migration panic was swallowed")
			}
		}()
		_ = runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE incomplete (id INTEGER)").Error; err != nil {
				t.Fatal(err)
			}
			panic("injected panic")
		})
	}()
	if db.Migrator().HasTable("incomplete") {
		t.Fatal("panic committed partial migration")
	}
	retry := openVersionRunnerDB(t, path)
	if err := runSchemaMigrations(retry, 1, func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE complete (id INTEGER)").Error }); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRunnerUpgradeFailureKeepsPreviousVersion(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "retry-upgrade.sqlite"))
	if err := runSchemaMigrations(db, 1, func(*gorm.DB) error { return nil }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("upgrade failed")
	if err := runSchemaMigrations(db, 2, migrateSchema, migrationStep{2, "failed upgrade", func(*gorm.DB) error { return failure }}); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	var current int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&current).Error; err != nil || current != 1 {
		t.Fatalf("failed upgrade recorded: %d %v", current, err)
	}
	if err := runSchemaMigrations(db, 2, migrateSchema, migrationStep{2, "retry upgrade", func(*gorm.DB) error { return nil }}); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRunnerFreshSnapshotSkipsHistoricalSteps(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "fresh-current.sqlite"))
	baselineCalls := 0
	baseline := func(tx *gorm.DB) error {
		baselineCalls++
		return tx.Exec("CREATE TABLE current_model (id INTEGER PRIMARY KEY, new_column TEXT)").Error
	}
	steps := []migrationStep{
		{2, "old add column", func(tx *gorm.DB) error { return tx.Exec("ALTER TABLE current_model ADD COLUMN new_column TEXT").Error }},
		{3, "old data conversion", func(*gorm.DB) error { t.Fatal("fresh baseline ran historical step"); return nil }},
	}
	for i := 0; i < 2; i++ {
		if err := runSchemaMigrations(db, 3, baseline, steps...); err != nil {
			t.Fatal(err)
		}
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations").Scan(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if baselineCalls != 1 || len(versions) != 1 || versions[0] != 3 {
		t.Fatalf("baseline=%d ledger=%v", baselineCalls, versions)
	}
	steps = append(steps, migrationStep{4, "next upgrade", func(tx *gorm.DB) error { return tx.Exec("ALTER TABLE current_model ADD COLUMN next_column TEXT").Error }})
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if baselineCalls != 1 {
		t.Fatal("snapshot baseline repeated on upgrade")
	}
}

func TestVersionRunnerRejectsInvalidPlansBeforeDatabaseAccess(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "invalid-plan.sqlite"))
	noop := func(*gorm.DB) error { return nil }
	for _, tc := range []struct {
		name   string
		target int64
		steps  []migrationStep
	}{
		{"missing", 3, []migrationStep{{2, "two", noop}}},
		{"duplicate", 3, []migrationStep{{2, "two", noop}, {2, "two again", noop}}},
		{"unordered", 3, []migrationStep{{3, "three", noop}, {2, "two", noop}}},
		{"gap", 3, []migrationStep{{2, "two", noop}, {4, "four", noop}}},
		{"nil callback", 2, []migrationStep{{2, "two", nil}}},
		{"empty name", 2, []migrationStep{{2, "", noop}}},
		{"unexpected step", 1, []migrationStep{{2, "two", noop}}},
		{"invalid target", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runSchemaMigrations(db, tc.target, noop, tc.steps...); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	if db.Migrator().HasTable("schema_migrations") {
		t.Fatal("invalid plan accessed database")
	}
}

func TestVersionRunnerRejectsUnknownAndGappedLedger(t *testing.T) {
	for _, versions := range [][]int64{{0}, {4}, {1, 3}} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "unknown.sqlite"))
		if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
			t.Fatal(err)
		}
		for _, version := range versions {
			if err := db.Exec("INSERT INTO schema_migrations VALUES (?, '')", version).Error; err != nil {
				t.Fatal(err)
			}
		}
		unexpected := func(*gorm.DB) error { t.Fatal("invalid ledger ran migration"); return nil }
		if err := runSchemaMigrations(db, 3, unexpected, migrationStep{2, "two", unexpected}, migrationStep{3, "three", unexpected}); err == nil {
			t.Fatalf("accepted ledger %v", versions)
		}
	}
}

func TestVersionRunnerSQLiteMultiStepFailureRollsBackAttempt(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "multi-step.sqlite"))
	if err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE changes (version INTEGER PRIMARY KEY)").Error }); err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { t.Fatal("existing DB reran baseline"); return nil }
	calls := map[int64]int{}
	failure := errors.New("step three failed")
	step := func(version int64) migrationStep {
		return migrationStep{version, fmt.Sprintf("step %d", version), func(tx *gorm.DB) error {
			calls[version]++
			if err := tx.Exec("INSERT INTO changes VALUES (?)", version).Error; err != nil {
				return err
			}
			if version == 3 && calls[version] == 1 {
				return failure
			}
			return nil
		}}
	}
	steps := []migrationStep{step(2), step(3), step(4)}
	if err := runSchemaMigrations(db, 4, baseline, steps...); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	var current, count int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT COUNT(*) FROM changes").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if current != 1 || count != 0 || calls[4] != 0 {
		t.Fatalf("partial SQLite commit: version=%d count=%d calls=%v", current, count, calls)
	}
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if calls[2] != 2 || calls[3] != 2 || calls[4] != 1 {
		t.Fatalf("retry calls=%v", calls)
	}
	// All successfully committed steps stay skipped on the next upgrade.
	steps = append(steps, step(5))
	if err := runSchemaMigrations(db, 5, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if calls[2] != 2 || calls[3] != 2 || calls[4] != 1 || calls[5] != 1 {
		t.Fatalf("completed steps repeated: %v", calls)
	}
}

// A tiny database/sql driver exercises MySQL's real runner/lock branch without
// opening sockets. It models autocommitted ledger writes, not MySQL DDL semantics.
type migrationLedgerConnector struct {
	versions []int64
	locked   bool
}
type migrationLedgerDriver struct{}
type migrationLedgerConn struct{ state *migrationLedgerConnector }
type migrationLedgerRows struct {
	values []driver.Value
	index  int
}

func (c *migrationLedgerConnector) Connect(context.Context) (driver.Conn, error) {
	return &migrationLedgerConn{state: c}, nil
}
func (*migrationLedgerConnector) Driver() driver.Driver { return migrationLedgerDriver{} }
func (migrationLedgerDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}
func (*migrationLedgerConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*migrationLedgerConn) Close() error { return nil }
func (*migrationLedgerConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *migrationLedgerConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.state.locked {
		return nil, errors.New("ledger write without owning lock")
	}
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS schema_migrations"):
	case strings.HasPrefix(query, "INSERT INTO schema_migrations"):
		c.state.versions = append(c.state.versions, args[0].Value.(int64))
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}
func (c *migrationLedgerConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	rows := &migrationLedgerRows{}
	switch query {
	case "SELECT GET_LOCK(?, 0)":
		if c.state.locked {
			return nil, errors.New("lock already owned")
		}
		c.state.locked = true
		rows.values = []driver.Value{int64(1)}
	case "SELECT RELEASE_LOCK(?)":
		if !c.state.locked {
			return nil, errors.New("lock not owned")
		}
		c.state.locked = false
		rows.values = []driver.Value{int64(1)}
	case "SELECT version FROM schema_migrations ORDER BY version":
		if !c.state.locked {
			return nil, errors.New("ledger read without lock")
		}
		for _, version := range c.state.versions {
			rows.values = append(rows.values, version)
		}
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	return rows, nil
}
func (*migrationLedgerRows) Columns() []string { return []string{"version"} }
func (*migrationLedgerRows) Close() error      { return nil }
func (r *migrationLedgerRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.index]
	r.index++
	return nil
}

func TestVersionRunnerMySQLRetryPreservesCompletedSteps(t *testing.T) {
	state := &migrationLedgerConnector{versions: []int64{1}}
	pool := sql.OpenDB(state)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { t.Fatal("versioned MySQL reran baseline"); return nil }
	failure := errors.New("injected step failure")
	calls := map[int64]int{}
	step := func(version int64) migrationStep {
		return migrationStep{version, fmt.Sprintf("step %d", version), func(tx *gorm.DB) error {
			if _, ok := tx.Statement.ConnPool.(*sql.Conn); !ok || !state.locked {
				t.Fatal("step lost pinned lock connection")
			}
			calls[version]++
			if version == 3 && calls[version] == 1 {
				return failure
			}
			return nil
		}}
	}
	steps := []migrationStep{step(2), step(3), step(4)}
	if err := runSchemaMigrations(db, 4, baseline, steps...); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if fmt.Sprint(state.versions) != "[1 2]" || state.locked || calls[4] != 0 {
		t.Fatalf("state=%+v calls=%v", state, calls)
	}
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.versions) != "[1 2 3 4]" || state.locked || calls[2] != 1 || calls[3] != 2 || calls[4] != 1 {
		t.Fatalf("retry state=%+v calls=%v", state, calls)
	}
}

func TestVersionRunnerRejectsForeignKeyViolationsWithoutCompletion(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "foreign-keys.sqlite"))
	err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
		for _, query := range []string{
			"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
			"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id))",
			"INSERT INTO child VALUES (1, 99)",
		} {
			if err := tx.Exec(query).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key validation") {
		t.Fatalf("invalid migration accepted: %v", err)
	}
	if db.Migrator().HasTable("schema_migrations") || db.Migrator().HasTable("child") {
		t.Fatal("invalid baseline committed")
	}
}

func TestVersionRunnerUsesLockedDBWithoutReplacingGlobal(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "baseline.sqlite"))
	previous := gormdb.DB
	gormdb.DB = nil
	t.Cleanup(func() { gormdb.DB = previous })
	originalLogger := db.Logger
	originalPool := db.Statement.ConnPool
	if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(&legacyBotOwnership{}) {
		t.Fatal("fresh baseline created retired ownership table")
	}
	for _, column := range []string{"channel_client_id", "cutover_completed"} {
		if db.Migrator().HasColumn(&runtimedomain.Instance{}, column) {
			t.Fatalf("fresh baseline created retired linkage column %s", column)
		}
	}
	if !db.Migrator().HasTable(&channel.Client{}) {
		t.Fatal("fresh baseline removed supported channel clients")
	}
	if gormdb.DB != nil || db.Logger != originalLogger || db.Statement.ConnPool != originalPool {
		t.Fatal("runner changed business DB/logger/connection pool")
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { t.Fatal("full baseline repeated"); return nil }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
}

func setupSKUMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := gormdb.DB
	t.Cleanup(func() {
		gormdb.DB = previousDB
	})
	dsn := fmt.Sprintf("file:sku_migration_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func setupRegistryMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := gormdb.DB
	t.Cleanup(func() {
		gormdb.DB = previousDB
	})
	dsn := fmt.Sprintf("file:registry_migration_test_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func modelIndexNames(t *testing.T, db *gorm.DB, model interface{}) []string {
	t.Helper()
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		t.Fatalf("parse model indexes: %v", err)
	}
	indexes := statement.Schema.ParseIndexes()
	names := make([]string, 0, len(indexes))
	for _, index := range indexes {
		names = append(names, index.Name)
	}
	if len(names) == 0 {
		t.Fatalf("model %T does not declare any indexes", model)
	}
	return names
}

func assertModelIndexesExist(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	for _, name := range modelIndexNames(t, db, model) {
		if !db.Migrator().HasIndex(model, name) {
			t.Errorf("model %T index %s does not exist", model, name)
		}
	}
}

func dropModelIndexes(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	for _, name := range modelIndexNames(t, db, model) {
		if !db.Migrator().HasIndex(model, name) {
			continue
		}
		if err := db.Migrator().DropIndex(model, name); err != nil {
			t.Fatalf("drop model %T index %s: %v", model, name, err)
		}
	}
}

func TestEnsureProductSKUMigrationBackfillLegacyData(t *testing.T) {
	db := setupSKUMigrationTestDB(t)

	if err := db.AutoMigrate(
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&orderdomain.OrderItem{},
		&cartdomain.Item{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
		&settingsstore.SettingRecord{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	product := &productdomain.Product{
		CategoryID:        1,
		Slug:              "sku-migration-legacy",
		TitleJSON:         jsonmap.JSON{"zh-CN": "历史商品"},
		PriceAmount:       money.FromDecimal(decimal.NewFromInt(128)),
		PurchaseType:      "member",
		FulfillmentType:   "manual",
		ManualStockTotal:  20,
		ManualStockLocked: 3,
		ManualStockSold:   5,
		IsActive:          true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	now := time.Now()
	orderItem := &orderdomain.OrderItem{
		OrderID:         1,
		ProductID:       product.ID,
		SKUID:           0,
		TitleJSON:       jsonmap.JSON{"zh-CN": "历史商品"},
		UnitPrice:       product.PriceAmount,
		Quantity:        1,
		TotalPrice:      product.PriceAmount,
		FulfillmentType: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(orderItem).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}

	cartItem := &cartdomain.Item{
		UserID:          1001,
		ProductID:       product.ID,
		SKUID:           0,
		Quantity:        2,
		FulfillmentType: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(cartItem).Error; err != nil {
		t.Fatalf("create cart item failed: %v", err)
	}

	batch := &cardsecretdomain.Batch{
		ProductID:  product.ID,
		SKUID:      0,
		BatchNo:    "SKU-MIGRATION-BATCH-001",
		Source:     "manual",
		TotalCount: 1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := db.Create(batch).Error; err != nil {
		t.Fatalf("create card secret batch failed: %v", err)
	}

	secret := &cardsecretdomain.Secret{
		ProductID: product.ID,
		SKUID:     0,
		BatchID:   &batch.ID,
		Secret:    "CARD-001",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secret).Error; err != nil {
		t.Fatalf("create card secret failed: %v", err)
	}

	if err := ensureProductSKUMigration(); err != nil {
		t.Fatalf("ensure sku migration failed: %v", err)
	}

	var sku productdomain.ProductSKU
	if err := db.Where("product_id = ? AND sku_code = ?", product.ID, productdomain.DefaultSKUCode).First(&sku).Error; err != nil {
		t.Fatalf("query default sku failed: %v", err)
	}
	if !sku.PriceAmount.Decimal.Equal(product.PriceAmount.Decimal) {
		t.Fatalf("default sku price mismatch want %s got %s", product.PriceAmount.String(), sku.PriceAmount.String())
	}
	if sku.ManualStockTotal != product.ManualStockTotal || sku.ManualStockLocked != product.ManualStockLocked || sku.ManualStockSold != product.ManualStockSold {
		t.Fatalf("default sku stock snapshot mismatch")
	}

	var gotOrderItem orderdomain.OrderItem
	if err := db.First(&gotOrderItem, orderItem.ID).Error; err != nil {
		t.Fatalf("reload order item failed: %v", err)
	}
	if gotOrderItem.SKUID != sku.ID {
		t.Fatalf("order item sku_id want %d got %d", sku.ID, gotOrderItem.SKUID)
	}

	var gotCartItem cartdomain.Item
	if err := db.First(&gotCartItem, cartItem.ID).Error; err != nil {
		t.Fatalf("reload cart item failed: %v", err)
	}
	if gotCartItem.SKUID != sku.ID {
		t.Fatalf("cart item sku_id want %d got %d", sku.ID, gotCartItem.SKUID)
	}

	var gotBatch cardsecretdomain.Batch
	if err := db.First(&gotBatch, batch.ID).Error; err != nil {
		t.Fatalf("reload card secret batch failed: %v", err)
	}
	if gotBatch.SKUID != sku.ID {
		t.Fatalf("card secret batch sku_id want %d got %d", sku.ID, gotBatch.SKUID)
	}

	var gotSecret cardsecretdomain.Secret
	if err := db.First(&gotSecret, secret.ID).Error; err != nil {
		t.Fatalf("reload card secret failed: %v", err)
	}
	if gotSecret.SKUID != sku.ID {
		t.Fatalf("card secret sku_id want %d got %d", sku.ID, gotSecret.SKUID)
	}

	if err := ensureProductSKUMigration(); err != nil {
		t.Fatalf("ensure sku migration second run failed: %v", err)
	}

	var skuCount int64
	if err := db.Model(&productdomain.ProductSKU{}).Where("product_id = ?", product.ID).Count(&skuCount).Error; err != nil {
		t.Fatalf("count product sku failed: %v", err)
	}
	if skuCount != 1 {
		t.Fatalf("idempotent check failed: sku count want 1 got %d", skuCount)
	}
}

func TestMappingExternalIDLengthPreflightPreservesOversizedValues(t *testing.T) {
	for _, field := range []struct{ table, column string }{
		{"product_mappings", "upstream_product_external_id"},
		{"sku_mappings", "upstream_sku_external_id"},
	} {
		t.Run(field.table, func(t *testing.T) {
			db := setupSKUMigrationTestDB(t)
			if err := validateMappingExternalIDLengths(db); err != nil {
				t.Fatalf("empty database preflight: %v", err)
			}
			if err := db.Exec("CREATE TABLE " + field.table + " (id INTEGER PRIMARY KEY, " + field.column + " TEXT)").Error; err != nil {
				t.Fatal(err)
			}
			value := strings.Repeat("界", 256)
			if err := db.Table(field.table).Create(map[string]interface{}{"id": 1, field.column: value}).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateMappingExternalIDLengths(db); err == nil || !strings.Contains(err.Error(), "no rows changed") {
				t.Fatalf("expected actionable oversized ID failure, got %v", err)
			}
			var preserved string
			if err := db.Table(field.table).Select(field.column).Scan(&preserved).Error; err != nil || preserved != value {
				t.Fatalf("opaque ID changed: %q, err=%v", preserved, err)
			}
			if err := db.Table(field.table).Where("id = 1").Update(field.column, strings.Repeat("界", 255)).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateMappingExternalIDLengths(db); err != nil {
				t.Fatalf("255 Unicode characters must fit: %v", err)
			}
		})
	}
}

func TestMigrateCartSKUUniqueIndex(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&cartdomain.Item{}); err != nil {
		t.Fatalf("auto migrate cart item failed: %v", err)
	}

	// 构造历史唯一索引，验证迁移函数会移除该索引并保留新索引。
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_cart_user_product ON cart_items(user_id, product_id)").Error; err != nil {
		t.Fatalf("create legacy index failed: %v", err)
	}

	if err := migrateCartSKUUniqueIndex(); err != nil {
		t.Fatalf("migrate cart unique index failed: %v", err)
	}

	if db.Migrator().HasIndex(&cartdomain.Item{}, "idx_cart_user_product") {
		t.Fatalf("legacy unique index idx_cart_user_product should be dropped")
	}
	if !db.Migrator().HasIndex(&cartdomain.Item{}, "idx_cart_user_product_sku") {
		t.Fatalf("new unique index idx_cart_user_product_sku should exist")
	}
}

func TestEnsureUserOAuthIdentityUserProviderUniqueIndexFailsClosedOnLegacyDuplicates(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&externalidentitydomain.Identity{}); err != nil {
		t.Fatalf("auto migrate identity: %v", err)
	}
	now := time.Now()
	first := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "legacy-effective",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	second := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "legacy-hidden",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("create first legacy identity: %v", err)
	}
	if err := db.Create(second).Error; err != nil {
		t.Fatalf("create duplicate legacy identity: %v", err)
	}

	err := ensureUserOAuthIdentityUserProviderUniqueIndex()
	if err == nil {
		t.Fatalf("expected explicit duplicate preflight failure")
	}
	for _, expected := range []string{
		userOAuthIdentityUserProviderUniqueIndex,
		"1 duplicate user/provider group",
		"user_id=42",
		`provider="telegram"`,
		"count=2",
		"SELECT id,user_id,provider,provider_user_id",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("migration error %q does not include %q", err, expected)
		}
	}
	if db.Migrator().HasIndex(
		&userOAuthIdentityUserProviderIndexSchema{},
		userOAuthIdentityUserProviderUniqueIndex,
	) {
		t.Fatalf("index %s must not exist after dirty preflight failure", userOAuthIdentityUserProviderUniqueIndex)
	}

	var identities []externalidentitydomain.Identity
	if err := db.Where("user_id = ? AND provider = ?", 42, "telegram").
		Order("id ASC").
		Find(&identities).Error; err != nil {
		t.Fatalf("load deduplicated identities: %v", err)
	}
	if len(identities) != 2 || identities[0].ID != first.ID || identities[1].ID != second.ID {
		t.Fatalf("dirty migration changed credentials: %+v", identities)
	}

	// After an operator resolves the duplicate deliberately, the same migration
	// creates the invariant and remains idempotent.
	if err := db.Delete(second).Error; err != nil {
		t.Fatalf("resolve duplicate explicitly: %v", err)
	}
	if err := ensureUserOAuthIdentityUserProviderUniqueIndex(); err != nil {
		t.Fatalf("ensure unique index after resolution: %v", err)
	}
	if err := ensureUserOAuthIdentityUserProviderUniqueIndex(); err != nil {
		t.Fatalf("idempotent ensure unique index: %v", err)
	}
	if !db.Migrator().HasIndex(
		&userOAuthIdentityUserProviderIndexSchema{},
		userOAuthIdentityUserProviderUniqueIndex,
	) {
		t.Fatalf("missing index %s", userOAuthIdentityUserProviderUniqueIndex)
	}

	duplicate := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "must-fail",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(duplicate).Error; err == nil {
		t.Fatalf("expected database unique constraint for same user/provider")
	}
	otherUser := &externalidentitydomain.Identity{
		UserID: 43, Provider: "telegram", ProviderUserID: "other-user",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(otherUser).Error; err != nil {
		t.Fatalf("same provider on another user should remain valid: %v", err)
	}
}

type racingNamedIndexMigrator struct {
	hasIndexResults []bool
	hasIndexCalls   int
	createCalls     int
	createErr       error
}

func (m *racingNamedIndexMigrator) HasIndex(interface{}, string) bool {
	index := m.hasIndexCalls
	m.hasIndexCalls++
	if index >= len(m.hasIndexResults) {
		return false
	}
	return m.hasIndexResults[index]
}

func (m *racingNamedIndexMigrator) CreateIndex(interface{}, string) error {
	m.createCalls++
	return m.createErr
}

func TestCreateIndexConvergingOnExistingHandlesOnlyConcurrentWinner(t *testing.T) {
	ddlErr := errors.New("index already exists")
	migrator := &racingNamedIndexMigrator{
		hasIndexResults: []bool{false, true},
		createErr:       ddlErr,
	}
	if err := createIndexConvergingOnExisting(migrator, &struct{}{}, "idx_target"); err != nil {
		t.Fatalf("concurrent winner should converge: %v", err)
	}
	if migrator.createCalls != 1 || migrator.hasIndexCalls != 2 {
		t.Fatalf("calls = create:%d has:%d, want 1/2", migrator.createCalls, migrator.hasIndexCalls)
	}
}

func TestCreateIndexConvergingOnExistingPreservesDDLFailure(t *testing.T) {
	ddlErr := errors.New("duplicate rows prevent unique index")
	migrator := &racingNamedIndexMigrator{
		hasIndexResults: []bool{false, false},
		createErr:       ddlErr,
	}
	err := createIndexConvergingOnExisting(migrator, &struct{}{}, "idx_target")
	if !errors.Is(err, ddlErr) {
		t.Fatalf("error = %v, want original DDL error", err)
	}
}

// testBehaviorOnlyMigration runs registered steps through target twice on t's isolated database.
// previous is the frozen legacy version; failures report any schema, evidence or ledger mutation.
func testBehaviorOnlyMigration(t *testing.T, previous, target int64) {
	t.Helper()
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "behavior-only.sqlite"))
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"CREATE TABLE audit_history_sentinel (id INTEGER PRIMARY KEY, evidence TEXT NOT NULL)",
		"INSERT INTO audit_history_sentinel VALUES (1, 'keep-original-evidence')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed legacy fixture: %v", err)
		}
	}
	if err := db.Exec("INSERT INTO schema_migrations VALUES (?, 'historical')", previous).Error; err != nil {
		t.Fatal(err)
	}
	const schemaQuery = "SELECT name || ':' || COALESCE(sql, '') FROM sqlite_master ORDER BY name"
	var before, after []string
	if err := db.Raw(schemaQuery).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { return errors.New("behavior-only upgrade must not run baseline") }
	var firstCompleted string
	for attempt := range 2 {
		if err := runSchemaMigrations(db, target, baseline, schemaSteps[:target-1]...); err != nil {
			t.Fatalf("apply/replay v%d marker: %v", target, err)
		}
		var completed string
		if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version = ?", target).Scan(&completed).Error; err != nil || completed == "" {
			t.Fatalf("missing target timestamp: %q %v", completed, err)
		}
		if attempt == 0 {
			firstCompleted = completed
		} else if completed != firstCompleted {
			t.Fatal("replay changed target migration timestamp")
		}
	}
	if err := db.Raw(schemaQuery).Scan(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("behavior-only v%d changed schema: before=%v after=%v err=%v", target, before, after, err)
	}
	var evidence, completed string
	if err := db.Raw("SELECT evidence FROM audit_history_sentinel WHERE id = 1").Scan(&evidence).Error; err != nil || evidence != "keep-original-evidence" {
		t.Fatalf("historical evidence changed: %q %v", evidence, err)
	}
	if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version = ?", previous).Scan(&completed).Error; err != nil || completed != "historical" {
		t.Fatalf("historical migration changed: %q %v", completed, err)
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{previous, target}) {
		t.Fatalf("expected exact behavior-only ledger: %v %v", versions, err)
	}
}

func TestBackfillPendingOrderRiskIPsHandlesLegacyNullsAndIPv6(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&orderdomain.Order{}); err != nil {
		t.Fatalf("auto migrate orders: %v", err)
	}

	pendingIPv4 := &orderdomain.Order{OrderNo: "risk-ipv4", Status: constants.OrderStatusPendingPayment, Currency: "USD", ClientIP: "1.2.3.4"}
	pendingIPv6 := &orderdomain.Order{OrderNo: "risk-ipv6", Status: constants.OrderStatusPendingPayment, Currency: "USD", ClientIP: "2001:db8:abcd:12::9"}
	paid := &orderdomain.Order{OrderNo: "risk-paid", Status: constants.OrderStatusPaid, Currency: "USD", ClientIP: "5.6.7.8"}
	for _, order := range []*orderdomain.Order{pendingIPv4, pendingIPv6, paid} {
		if err := db.Create(order).Error; err != nil {
			t.Fatalf("create order %s: %v", order.OrderNo, err)
		}
		if err := db.Exec("UPDATE orders SET risk_ip = NULL WHERE id = ?", order.ID).Error; err != nil {
			t.Fatalf("set legacy NULL risk_ip for %s: %v", order.OrderNo, err)
		}
	}

	if err := backfillPendingOrderRiskIPs(db); err != nil {
		t.Fatalf("backfill risk IPs: %v", err)
	}
	var gotIPv4, gotIPv6, gotPaid orderdomain.Order
	if err := db.First(&gotIPv4, pendingIPv4.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotIPv6, pendingIPv6.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotPaid, paid.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotIPv4.RiskIP != "1.2.3.4" || gotIPv6.RiskIP != "2001:db8:abcd:12::/64" {
		t.Fatalf("unexpected risk IP backfill: ipv4=%q ipv6=%q", gotIPv4.RiskIP, gotIPv6.RiskIP)
	}
	if gotPaid.RiskIP != "" {
		t.Fatalf("paid history must not be backfilled, got %q", gotPaid.RiskIP)
	}
}

func TestEnsureCartForeignKeyConstraintsPreservesRowsAndEnforcesReferences(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	for _, statement := range []string{
		`CREATE TABLE products (id integer PRIMARY KEY)`,
		`CREATE TABLE product_skus (id integer PRIMARY KEY)`,
		`INSERT INTO products (id) VALUES (1)`,
		`INSERT INTO product_skus (id) VALUES (2)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare cart schema: %v", err)
		}
	}
	if err := db.AutoMigrate(&cartdomain.Item{}); err != nil {
		t.Fatalf("prepare indexed cart schema: %v", err)
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (3, 4, 1, 2, 1)`).Error; err != nil {
		t.Fatalf("prepare cart row: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})

	if err := ensureCartForeignKeyConstraints(); err != nil {
		t.Fatalf("ensure cart constraints: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})
	var rowCount int64
	if err := db.Table("cart_items").Where("id = ?", 3).Count(&rowCount).Error; err != nil {
		t.Fatalf("count preserved cart rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("cart constraint migration lost existing row: count=%d", rowCount)
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (5, 6, 999, 2, 1)`).Error; err == nil {
		t.Fatal("cart product foreign key did not reject missing product")
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (7, 8, 1, 999, 1)`).Error; err == nil {
		t.Fatal("cart SKU foreign key did not reject missing SKU")
	}

	dropModelIndexes(t, db, &cartdomain.Item{})
	if err := ensureCartForeignKeyConstraints(); err != nil {
		t.Fatalf("repair cart indexes with existing constraints: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})
	for _, constraint := range []string{cartProductForeignKeyConstraint, cartSKUForeignKeyConstraint} {
		if !db.Migrator().HasConstraint(&cartItemConstraintSchema{}, constraint) {
			t.Errorf("cart index repair removed constraint %s", constraint)
		}
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (9, 4, 1, 2, 1)`).Error; err == nil {
		t.Fatal("repaired cart unique index did not reject duplicate user/product/SKU")
	}
}

func TestEnsureCartForeignKeyConstraintsRejectsOrphansBeforeSchemaChanges(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	for _, statement := range []string{
		`CREATE TABLE products (id integer PRIMARY KEY)`,
		`CREATE TABLE product_skus (id integer PRIMARY KEY)`,
		`CREATE TABLE cart_items (id integer PRIMARY KEY, user_id integer NOT NULL, product_id integer NOT NULL, sku_id integer NOT NULL, quantity integer NOT NULL)`,
		`INSERT INTO product_skus (id) VALUES (2)`,
		`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (3, 4, 999, 2, 1)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare orphaned cart schema: %v", err)
		}
	}

	err := ensureCartForeignKeyConstraints()
	if err == nil || !strings.Contains(err.Error(), cartProductForeignKeyConstraint) {
		t.Fatalf("expected descriptive product reference error, got %v", err)
	}
	for _, constraint := range []string{cartProductForeignKeyConstraint, cartSKUForeignKeyConstraint} {
		if db.Migrator().HasConstraint(&cartItemConstraintSchema{}, constraint) {
			t.Fatalf("orphan preflight left partial constraint %s", constraint)
		}
	}
}

type supersededProcurementOrderSchema struct {
	ID                  uint `gorm:"primarykey"`
	LocalOrderID        uint
	LocalOrderReference *supersededProcurementOrderReference `gorm:"foreignKey:LocalOrderID;references:ID;constraint:fk_procurement_orders_local_order_reference"`
}

func (supersededProcurementOrderSchema) TableName() string { return "procurement_orders" }

type supersededProcurementOrderReference struct {
	ID uint `gorm:"primarykey"`
}

func (supersededProcurementOrderReference) TableName() string { return "orders" }

type procurementOrderIndexSchema struct {
	ID                      uint       `gorm:"primarykey"`
	ConnectionID            uint       `gorm:"index"`
	LocalOrderID            uint       `gorm:"uniqueIndex:uidx_procurement_local_order"`
	NotificationPending     bool       `gorm:"index"`
	UpstreamOrderExternalID string     `gorm:"index"`
	LocalOrderNo            string     `gorm:"index"`
	UpstreamOrderNo         string     `gorm:"index"`
	Status                  string     `gorm:"index"`
	NextRetryAt             *time.Time `gorm:"index"`
	TraceID                 string     `gorm:"index"`
	CreatedAt               time.Time  `gorm:"index"`
	UpdatedAt               time.Time  `gorm:"index"`
	DeletedAt               *time.Time `gorm:"index"`
}

func (procurementOrderIndexSchema) TableName() string { return "procurement_orders" }

func TestProcurementUniqueMigrationRejectsDuplicatesWithoutDeletion(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	if err := db.Exec("CREATE TABLE procurement_orders (id INTEGER PRIMARY KEY, local_order_id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO procurement_orders (id,local_order_id) VALUES (1,7),(2,7)").Error; err != nil {
		t.Fatal(err)
	}
	if err := validateProcurementLocalUniqueness(db); err == nil {
		t.Fatal("duplicates must block migration")
	}
	var count int64
	db.Table("procurement_orders").Count(&count)
	if count != 2 {
		t.Fatalf("migration deleted rows: %d", count)
	}
}

func TestAutoMigrateReplacesSupersededProcurementConstraintName(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	if err := db.AutoMigrate(&supersededProcurementOrderSchema{}); err != nil {
		t.Fatalf("create superseded procurement schema failed: %v", err)
	}
	if err := db.AutoMigrate(&procurementOrderIndexSchema{}); err != nil {
		t.Fatalf("create indexed superseded procurement schema failed: %v", err)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})
	if !db.Migrator().HasConstraint(&supersededProcurementOrderSchema{}, supersededProcurementOrderForeignKeyConstraint) {
		t.Fatalf("test setup did not create superseded procurement constraint")
	}

	if err := AutoMigrate(); err != nil {
		t.Fatalf("auto migrate superseded schema failed: %v", err)
	}
	if db.Migrator().HasConstraint(&procurementdomain.Order{}, supersededProcurementOrderForeignKeyConstraint) {
		t.Errorf("superseded procurement constraint %s still exists", supersededProcurementOrderForeignKeyConstraint)
	}
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		t.Errorf("canonical procurement constraint %s does not exist", procurementOrderForeignKeyConstraint)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})

	dropModelIndexes(t, db, &procurementdomain.Order{})
	if err := ensureProcurementOrderForeignKeyConstraint(); err != nil {
		t.Fatalf("repair procurement indexes with canonical constraint: %v", err)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		t.Errorf("procurement index repair removed canonical constraint %s", procurementOrderForeignKeyConstraint)
	}
}
