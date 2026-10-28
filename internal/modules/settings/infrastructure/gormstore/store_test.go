package settingsstore

import (
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSettingsKeyQuotedAcrossDialects(t *testing.T) {
	cases := []struct {
		name      string
		dialector gorm.Dialector
		quoted    string
	}{
		{"mysql", mysql.New(mysql.Config{DSN: "unused:unused@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true}), "`settings`.`key`"},
		{"postgres", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"}), `"settings"."key"`},
		{"sqlite", sqlite.Open(":memory:"), "`settings`.`key`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(tc.dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
			if err != nil {
				t.Fatal(err)
			}
			if pool, err := db.DB(); err == nil {
				t.Cleanup(func() { _ = pool.Close() })
			}
			var queries []string
			if err := db.Callback().Query().After("gorm:query").Register("test:capture_settings_sql", func(tx *gorm.DB) {
				queries = append(queries, tx.Statement.SQL.String())
			}); err != nil {
				t.Fatal(err)
			}
			store := New(db)
			if _, _, err := store.GetByKey("site"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Upsert("site", jsonmap.JSON{"title": "test"}); err != nil {
				t.Fatal(err)
			}
			if len(queries) != 2 {
				t.Fatalf("want two lookups, got %v", queries)
			}
			for _, query := range queries {
				if !strings.Contains(query, tc.quoted+" = ") {
					t.Fatalf("key predicate must be dialect-quoted: %s", query)
				}
			}
		})
	}
}

func TestSettingsUpsertRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&SettingRecord{}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	if _, found, err := store.GetByKey("site"); err != nil || found {
		t.Fatalf("missing setting: found=%v err=%v", found, err)
	}
	for _, title := range []string{"first", "updated"} {
		if _, err := store.Upsert("site", jsonmap.JSON{"title": title}); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.GetByKey("site")
		if err != nil || !found || got["title"] != title {
			t.Fatalf("round trip: got=%v found=%v err=%v", got, found, err)
		}
	}
}
