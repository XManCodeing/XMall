package dblock

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Transaction retries only database contention after a complete rollback.
// db must be unbound for retries; nested callers defer retry to their outer owner.
// fn must contain only database effects and rebuild all evidence on every attempt.
func Transaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	attempts := 4
	if _, bound := db.Statement.ConnPool.(gorm.TxCommitter); bound {
		attempts = 1
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := db.WithContext(ctx).Transaction(fn)
		if err == nil || attempt+1 >= attempts || !contention(err) {
			return err
		}
		slog.WarnContext(ctx, "database transaction contention retry", "attempt", attempt+1)
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// contention recognizes driver codes, never business conflicts or arbitrary text.
func contention(err error) bool {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		return mysqlError.Number == 1213 || mysqlError.Number == 1205
	}
	var postgresError interface{ SQLState() string }
	if errors.As(err, &postgresError) {
		return postgresError.SQLState() == "40001" || postgresError.SQLState() == "40P01"
	}
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		return sqliteError.Code()&255 == 5 || sqliteError.Code()&255 == 6
	}
	return false
}
