// Package identitymigration provides read-only prechecks and explicitly reviewed beneficiary mappings.
package identitymigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"gorm.io/gorm"
)

// Store holds only the database dependency; it never calls Telegram or verifies human identity.
type Store struct{ db *gorm.DB }

// New returns a reader for db, rejecting nil; it does not migrate or modify tables.
func New(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("identity migration database: %w", c.ErrInvalid)
	}
	return &Store{db: db}, nil
}

// PreviewIdentityMigration validates input and reads all evidence on one transaction
// connection at server-supplied at. A successful snapshot is never execution permission.
// PostgreSQL uses repeatable-read/read-only; SQLite's transaction supplies its snapshot.
func (store *Store) PreviewIdentityMigration(ctx context.Context, input c.IdentityMigrationInput, at time.Time) (*c.IdentityMigrationPreview, error) {
	request, err := normalize(input, at)
	if err != nil {
		return nil, err
	}
	preview := &c.IdentityMigrationPreview{AccountID: input.AccountID, OldTelegramID: strconv.FormatInt(request.oldID, 10), NewTelegramID: strconv.FormatInt(request.newID, 10), ObservedAt: at.UTC(), Blockers: []string{}, Targets: []c.IdentityMigrationTarget{}}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := readAccount(tx, input.AccountID, request.newID, preview); err != nil {
			return err
		}
		for _, targetID := range request.targets {
			target, err := readScope(tx, input.AccountID, request, targetID, at.UTC())
			if err != nil {
				return fmt.Errorf("identity migration target %d: %w", targetID, err)
			}
			preview.Targets = append(preview.Targets, *target)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		// Fixed classifications only: database errors can contain bound identity values.
		slog.WarnContext(ctx, "membership identity migration precheck rejected", "class", errorClass(err))
		return nil, err
	}
	slog.InfoContext(ctx, "membership identity migration precheck read", "targets", len(preview.Targets), "blocked", len(preview.Blockers) > 0)
	return preview, nil
}

// errorClass maps err to a safe fixed log category without database error text.
func errorClass(err error) string {
	switch {
	case errors.Is(err, c.ErrConflict):
		return "conflict"
	case errors.Is(err, c.ErrUntrustedIdentity):
		return "identity"
	case errors.Is(err, c.ErrNotFound):
		return "not_found"
	default:
		return "read_failed"
	}
}

// readAccount checks active account and all numeric Telegram binding aliases using tx.
// Minimal column selection excludes contact/profile data; no global handle is used.
func readAccount(tx *gorm.DB, account uint, newID int64, preview *c.IdentityMigrationPreview) error {
	var user struct{ Status string }
	err := tx.Table("users").Select("status").Where("id = ? AND deleted_at IS NULL", account).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrUntrustedIdentity
	}
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(user.Status)) != "active" {
		return c.ErrUntrustedIdentity
	}
	rows, err := tx.Table("user_oauth_identities").Select("id, user_id, provider_user_id").Where("provider = ?", "telegram").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	ownCount := 0
	for rows.Next() {
		var bindingID, userID uint
		var raw string
		if err := rows.Scan(&bindingID, &userID, &raw); err != nil {
			return err
		}
		parsed, parseErr := decimal(raw, 63, false)
		if parseErr == nil && int64(parsed) == newID && userID != account {
			return fmt.Errorf("new identity bound to another account: %w", c.ErrConflict)
		}
		if userID == account {
			ownCount++
			preview.BindingMatchesNewID = parseErr == nil && int64(parsed) == newID
			preview.BindingVersion = strconv.FormatUint(uint64(bindingID), 10)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if ownCount != 1 {
		preview.BindingMatchesNewID = false
		preview.BindingVersion = "0"
	}
	if !preview.BindingMatchesNewID {
		preview.Blockers = append(preview.Blockers, "account_binding_mismatch")
	}
	return nil
}

var _ c.IdentityMigrationReader = (*Store)(nil)
