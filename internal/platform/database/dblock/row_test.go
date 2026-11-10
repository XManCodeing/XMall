package dblock

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	mysqlerr "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlRecorder captures dry-run statements without connecting to external databases.
type sqlRecorder struct {
	logger.Interface
	statements []string
}

// Trace saves generated SQL; arguments are synthetic test data only.
func (record *sqlRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	record.statements = append(record.statements, statement)
}

// TestRowDialects proves server dialects use FOR UPDATE, not affected-row guards.
func TestRowDialects(t *testing.T) {
	for _, dialect := range []gorm.Dialector{
		mysql.New(mysql.Config{DSN: "test:test@tcp(localhost:3306)/test", SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{DSN: "host=localhost user=test dbname=test"}),
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			recorder := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
			db, err := gorm.Open(dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: recorder})
			if err != nil {
				t.Fatal(err)
			}
			if err := Row(db.Table("locks").Where("id = ? AND version = ?", 1, 2), "version", errors.New("missing")); err != nil {
				t.Fatal(err)
			}
			if len(recorder.statements) != 1 || !strings.Contains(recorder.statements[0], "FOR UPDATE") || strings.Contains(recorder.statements[0], "UPDATE `locks`") {
				t.Fatal(recorder.statements)
			}
			kind := "TEXT"
			if dialect.Name() == "mysql" {
				kind = "CHAR"
			}
			if cast := TextCast(db, "identity.id"); cast != "CAST(identity.id AS "+kind+")" {
				t.Fatal(cast)
			}
		})
	}
}

// TestRowSQLite checks existence without changing the guarded value or leaking query state.
func TestRowSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "lock.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE locks (id INTEGER PRIMARY KEY, version INTEGER); INSERT INTO locks VALUES (1, 7)").Error; err != nil {
		t.Fatal(err)
	}
	missing := errors.New("missing")
	err = db.Transaction(func(tx *gorm.DB) error {
		query := tx.Table("locks").Where("id = ?", 1)
		if err := Row(query, "version", missing); err != nil {
			return err
		}
		var version int
		if err := query.Select("version").Scan(&version).Error; err != nil {
			return err
		}
		if version != 7 {
			t.Fatalf("guard mutated version: %d", version)
		}
		if err := Row(tx.Table("locks").Where("id = ?", 2), "version", missing); !errors.Is(err, missing) {
			t.Fatalf("missing=%v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestTransactionRetriesOnlyContention verifies rollback, bounded retry and cancellation.
func TestTransactionRetriesOnlyContention(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "retry.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	err = Transaction(context.Background(), db, func(*gorm.DB) error {
		attempts++
		if attempts < 3 {
			return &mysqlerr.MySQLError{Number: 1213}
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	conflict := errors.New("business version changed")
	attempts = 0
	err = Transaction(context.Background(), db, func(*gorm.DB) error { attempts++; return conflict })
	if !errors.Is(err, conflict) || attempts != 1 {
		t.Fatalf("business retry=%d %v", attempts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Transaction(ctx, db, func(*gorm.DB) error { t.Fatal("called after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
