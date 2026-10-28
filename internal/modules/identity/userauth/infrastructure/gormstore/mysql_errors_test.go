package gormstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	userauthapp "github.com/homles698698-lab/XMall/internal/modules/identity/userauth/application"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestMySQLTransactionRetryRollsBackWholeAttempt(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if err := db.Exec("CREATE TABLE retry_probe (attempt INTEGER)").Error; err != nil {
				t.Fatal(err)
			}
			attempts := 0
			err = New(db).WithinTransaction(context.Background(), func(port userauthapp.AuthTransaction) error {
				attempts++
				tx := port.(*transaction).db
				if err := tx.Exec("INSERT INTO retry_probe (attempt) VALUES (?)", attempts).Error; err != nil {
					return err
				}
				if attempts < 3 {
					return fmt.Errorf("write: %w", &mysql.MySQLError{Number: number})
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var rows []int
			if err := db.Table("retry_probe").Pluck("attempt", &rows).Error; err != nil {
				t.Fatal(err)
			}
			if attempts != 3 || len(rows) != 1 || rows[0] != 3 {
				t.Fatalf("attempts=%d committed=%v", attempts, rows)
			}
			attempts = 0
			err = New(db).WithinTransaction(context.Background(), func(userauthapp.AuthTransaction) error {
				attempts++
				return &mysql.MySQLError{Number: number}
			})
			if err == nil || attempts != 3 {
				t.Fatalf("retry must stop at three attempts: attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestRetryableTransactionDriverErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"mysql deadlock", &mysql.MySQLError{Number: 1213}, true},
		{"mysql lock timeout", &mysql.MySQLError{Number: 1205}, true},
		{"mysql wrapped deadlock", fmt.Errorf("transaction: %w", &mysql.MySQLError{Number: 1213}), true},
		{"mysql duplicate", &mysql.MySQLError{Number: 1062}, false},
		{"mysql connection loss", &mysql.MySQLError{Number: 2013}, false},
		{"postgres deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"postgres serialization", &pgconn.PgError{Code: "40001"}, true},
		{"postgres duplicate", &pgconn.PgError{Code: "23505"}, false},
		{"sqlite busy", errors.New("database is locked"), true},
		{"sqlite locked", errors.New("database table is locked"), true},
		{"unrelated", errors.New("connection reset"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableTransactionError(tc.err); got != tc.want {
				t.Fatalf("isRetryableTransactionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
