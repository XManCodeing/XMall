package routerecovery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store owns only local persistence. No network dependency can enter its transaction.
type Store struct{ db *gorm.DB }

// New validates db and returns the module's narrow recovery port; it performs no migration.
func New(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("membership route recovery database: %w", c.ErrInvalid)
	}
	return &Store{db: db}, nil
}

// RecoverRoute scans the complete fenced scope and durably records every liability.
// Reinvocation rescans local evidence only; it never repeats an unknown platform operation.
func (store *Store) RecoverRoute(ctx context.Context, request Request) (out Result, err error) {
	out = Result{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion}
	if err = validate(request); err != nil {
		return out, err
	}
	at := time.Now().UTC()
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fence(tx, request); err != nil {
			return err
		}
		run, err := lockRun(tx, request)
		if err != nil {
			return err
		}
		// Mark prior evidence unknown first. Deleted facts must never silently disappear.
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ?", request.TaskID, request.TargetID).Update("state", "unknown").Error; err != nil {
			return err
		}
		scope := scanRequest{Request: request, OldBotID: request.BotID, NewBotID: request.BotID}
		for _, scan := range []func(*gorm.DB, scanRequest, time.Time) error{scanLedger, scanInvites, scanOperations, scanMembers, scanJoins, scanMigrations, scanReceipts} {
			if err := scan(tx, scope, at); err != nil {
				return err
			}
		}
		var remaining, unknown int64
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", request.TaskID, request.TargetID, "remaining").Count(&remaining).Error; err != nil {
			return err
		}
		if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", request.TaskID, request.TargetID, "unknown").Count(&unknown).Error; err != nil {
			return err
		}
		out.Remaining, out.Unknown = uint64(remaining), uint64(unknown)
		out.Verified = remaining == 0 && unknown == 0
		updated := tx.Model(&Run{}).Where("task_id = ? AND target_id = ? AND version = ? AND generation = ?", run.TaskID, run.TargetID, run.Version, request.Generation).Updates(map[string]any{"version": run.Version + 1, "verified": out.Verified, "remaining": out.Remaining, "unknown": out.Unknown, "scanned_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		out.Verified = false
		slog.WarnContext(ctx, "membership route recovery scan rejected", "task", request.TaskID)
		return out, err
	}
	slog.InfoContext(ctx, "membership route recovery scanned", "task", request.TaskID, "remaining", out.Remaining, "unknown", out.Unknown)
	return out, nil
}

// validate rejects incomplete/cross-purpose inputs before any write; no empty scope is proof.
func validate(request Request) error {
	if request.TaskID == 0 || request.TargetID == 0 || request.BotID == 0 || request.Generation == 0 || request.Purpose != "membership" || request.OldChatID >= 0 || request.NewChatID >= 0 || request.OldChatID == request.NewChatID || request.OldRouteVersion == 0 || request.NewRouteVersion != request.OldRouteVersion+1 || request.NewRouteVersion == 0 {
		return c.ErrInvalid
	}
	return nil
}

// lockRun persists request as a permanent idempotency identity and rejects any rebinding.
// Generation is part of the identity; stale runtime owners cannot refresh evidence.
func lockRun(tx *gorm.DB, request Request) (*Run, error) {
	row := Run{TaskID: request.TaskID, TargetID: request.TargetID, BotID: request.BotID, OldChatID: request.OldChatID, NewChatID: request.NewChatID, OldRouteVersion: request.OldRouteVersion, NewRouteVersion: request.NewRouteVersion, Generation: request.Generation, Version: 1}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	var saved Run
	if err := tx.First(&saved, "task_id = ? AND target_id = ?", request.TaskID, request.TargetID).Error; err != nil {
		return nil, err
	}
	if saved.TargetID != row.TargetID || saved.BotID != row.BotID || saved.OldChatID != row.OldChatID || saved.NewChatID != row.NewChatID || saved.OldRouteVersion != row.OldRouteVersion || saved.NewRouteVersion != row.NewRouteVersion || saved.Generation != row.Generation {
		return nil, c.ErrConflict
	}
	return &saved, nil
}

// record upserts one local evidence fact; request scopes it and at is the server scan time.
func record(tx *gorm.DB, request scanRequest, at time.Time, row Evidence) error {
	row.TaskID, row.TargetID, row.ScannedAt = request.TaskID, request.TargetID, at
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

var _ Recoverer = (*Store)(nil)
