package gormstore

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homles698698-lab/XMall/internal/constants"
	siteconnectioncontract "github.com/homles698698-lab/XMall/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/homles698698-lab/XMall/internal/modules/siteconnection/domain"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupSiteConnectionStoreTest(t *testing.T) (*gorm.DB, *Store) {
	t.Helper()
	dsn := fmt.Sprintf("file:siteconnection_store_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&siteconnectiondomain.Connection{}); err != nil {
		t.Fatalf("migrate site connection: %v", err)
	}
	return db, New(db)
}

func TestHTTPConfigConcurrentSaveHasSingleWinner(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	conn := &siteconnectiondomain.Connection{Name: "draft", BaseURL: "https://example.com", Protocol: constants.ConnectionProtocolGenericHTTP, Status: constants.ConnectionStatusActive}
	if err := store.Create(conn); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var failures atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			ok, err := store.UpdateHTTPConfig(conn.ID, 0, "ciphertext")
			if err != nil {
				failures.Add(1)
			}
			if ok {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 || failures.Load() != 0 {
		t.Fatalf("winners=%d errors=%d", winners.Load(), failures.Load())
	}
	got, err := store.GetByID(conn.ID)
	if err != nil || got.HTTPConfigRevision != 1 || got.Status != constants.ConnectionStatusDisabled {
		t.Fatalf("saved: %#v %v", got, err)
	}
}

func TestStoreHTTPConfigPersistenceAndActiveProtocolFilter(t *testing.T) {
	_, store := setupSiteConnectionStoreTest(t)
	conn := &siteconnectiondomain.Connection{Name: "draft", BaseURL: "https://example.com", Protocol: constants.ConnectionProtocolGenericHTTP, Status: constants.ConnectionStatusActive, HTTPConfigEncrypted: "encrypted-config"}
	if err := store.Create(conn); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByID(conn.ID)
	if err != nil || got.HTTPConfigEncrypted != "encrypted-config" {
		t.Fatalf("roundtrip: %#v %v", got, err)
	}
	if ok, err := store.UpdateHTTPConfig(got.ID, 0, "updated-encrypted-config"); err != nil || !ok {
		t.Fatalf("update config: %v %v", ok, err)
	}
	// A stale generic CRUD/Ping must not restore old configuration or activation.
	got.Name = "renamed"
	if err = store.Update(got); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetByID(conn.ID)
	if err != nil || got.HTTPConfigEncrypted != "updated-encrypted-config" || got.HTTPConfigRevision != 1 || got.Status != constants.ConnectionStatusDisabled {
		t.Fatalf("updated roundtrip: %#v %v", got, err)
	}
	if ok, err := store.UpdateHTTPConfig(got.ID, 0, "stale"); err != nil || ok {
		t.Fatalf("stale config accepted: %v %v", ok, err)
	}
	if ok, err := store.SetStatusIfRevision(got.ID, 0, constants.ConnectionStatusActive); err != nil || ok {
		t.Fatalf("stale activation accepted: %v %v", ok, err)
	}
	if ok, err := store.SetStatusIfRevision(got.ID, 1, constants.ConnectionStatusActive); err != nil || !ok {
		t.Fatalf("activation: %v %v", ok, err)
	}
	active, err := store.ListActive()
	if err != nil || len(active) != 1 {
		t.Fatalf("ready generic missing from active processing: %#v %v", active, err)
	}
}

func TestStoreDeleteHidesConnectionFromEveryReadPath(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	connection := &siteconnectiondomain.Connection{
		Name:               "primary upstream",
		BaseURL:            "https://upstream.example.com",
		ApiKey:             "upstream-key",
		ApiSecret:          "encrypted-secret",
		Protocol:           constants.ConnectionProtocolDujiaoNext,
		Status:             constants.ConnectionStatusActive,
		RetryMax:           5,
		RetryIntervals:     "[30,60,300]",
		ExchangeRate:       decimal.NewFromInt(1),
		PriceMarkupPercent: decimal.Zero,
		PriceRoundingMode:  "none",
	}
	if err := store.Create(connection); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	if err := store.Delete(connection.ID); err != nil {
		t.Fatalf("delete connection: %v", err)
	}

	if got, err := store.GetByID(connection.ID); err != nil || got != nil {
		t.Fatalf("GetByID after delete = %#v, %v; want nil, nil", got, err)
	}
	if got, err := store.GetByApiKey(connection.ApiKey); err != nil || got != nil {
		t.Fatalf("GetByApiKey after delete = %#v, %v; want nil, nil", got, err)
	}
	listed, total, err := store.List(siteconnectioncontract.ListFilter{})
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if total != 0 || len(listed) != 0 {
		t.Fatalf("list after delete = total %d rows %d; want empty", total, len(listed))
	}
	active, err := store.ListActive()
	if err != nil {
		t.Fatalf("list active connections: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active list after delete has %d rows; want empty", len(active))
	}

	var persisted siteconnectiondomain.Connection
	if err := db.Where("id = ?", connection.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted deleted connection: %v", err)
	}
	if persisted.DeletedAt == nil {
		t.Fatal("delete must persist deleted_at instead of physically removing the row")
	}
}

// The workbench migration adds an optional encrypted column without replacing
// existing protocol identifiers or credentials. Only an in-memory DB is used.
func TestHTTPConfigMigrationPreservesLegacyConnection(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	if err := db.Exec(`INSERT INTO site_connections
		(name, base_url, api_key, api_secret, protocol, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy upstream", "https://upstream.example.com", "legacy-key",
		"legacy-encrypted-secret", constants.ConnectionProtocolDujiaoNext,
		constants.ConnectionStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"http_config_encrypted", "http_config_revision"} {
		if db.Migrator().HasColumn(&siteconnectiondomain.Connection{}, column) {
			if err := db.Migrator().DropColumn(&siteconnectiondomain.Connection{}, column); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 2 {
		if err := db.AutoMigrate(&siteconnectiondomain.Connection{}); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := store.GetByApiKey("legacy-key")
	if err != nil || conn == nil {
		t.Fatalf("legacy connection disappeared: %v", err)
	}
	if conn.ApiSecret != "legacy-encrypted-secret" || conn.Protocol != constants.ConnectionProtocolDujiaoNext || conn.Status != constants.ConnectionStatusActive {
		t.Fatal("migration changed legacy connection credentials, protocol or status")
	}
	if conn.HTTPConfigRevision != 0 {
		t.Fatal("migration changed draft revision")
	}
	if !db.Migrator().HasColumn(&siteconnectiondomain.Connection{}, "http_config_encrypted") || !db.Migrator().HasColumn(&siteconnectiondomain.Connection{}, "http_config_revision") {
		t.Fatal("encrypted configuration or revision column missing")
	}
	if err := db.Model(conn).Update("http_config_encrypted", "opaque-http-config-ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	conn, err = store.GetByID(conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"legacy-encrypted-secret", "opaque-http-config-ciphertext", "http_config_encrypted"} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("connection API representation exposes encrypted credentials")
		}
	}
}
