package dberror

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"mysql code without message", &mysql.MySQLError{Number: 1062}, true},
		{"mysql wrapped", fmt.Errorf("create: %w", &mysql.MySQLError{Number: 1062}), true},
		{"mysql unrelated with misleading message", &mysql.MySQLError{Number: 1146, Message: "unique table does not exist"}, false},
		{"mysql foreign key", &mysql.MySQLError{Number: 1452}, false},
		{"postgres wrapped", fmt.Errorf("create: %w", &pgconn.PgError{Code: "23505"}), true},
		{"postgres foreign key", &pgconn.PgError{Code: "23503", Message: "unique reference"}, false},
		{"translated duplicate", fmt.Errorf("create: %w", errors.New("duplicated key not allowed")), true},
		{"sqlite legacy", errors.New("UNIQUE constraint failed: users.email"), true},
		{"legacy duplicate", errors.New("duplicate key value violates unique constraint"), true},
		{"unrelated", errors.New("connection closed"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("IsUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
