// Package dblock provides transaction-local, dialect-aware row locking.
package dblock

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Row locks the row selected by query and returns missing when it does not exist.
// The caller must supply an open transaction and a trusted column name. SQLite
// needs a write statement to acquire its reserved lock; its affected-row count is
// deliberately ignored. MySQL/PostgreSQL use a locking current read instead.
func Row(query *gorm.DB, column string, missing error) error {
	if query.Dialector.Name() == "sqlite" {
		if err := query.Session(&gorm.Session{}).UpdateColumn(column, clause.Column{Name: column}).Error; err != nil {
			return err
		}
	}
	var row struct{ Present int }
	err := query.Session(&gorm.Session{}).Select("1 AS present").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && missing != nil {
		return missing
	}
	return err
}

// TextCast returns a portable SQL cast for a trusted, internal column expression.
// expression must never contain user input; returned SQL can be embedded in a query.
func TextCast(db *gorm.DB, expression string) string {
	kind := "TEXT"
	if db.Dialector.Name() == "mysql" {
		kind = "CHAR"
	}
	return "CAST(" + expression + " AS " + kind + ")"
}
