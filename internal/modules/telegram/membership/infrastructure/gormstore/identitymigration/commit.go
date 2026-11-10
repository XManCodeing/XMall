package identitymigration

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Models returns the new mapping model for explicit migration registration; it performs no DDL.
func Models() []any { return []any{&d.IdentityMigration{}} }

// CommitIdentityMigration rechecks all preview evidence and creates mappings atomically on one connection.
// HumanVerified is an administrator attestation, not automated identity proof. No HTTP occurs here.
func (store *Store) CommitIdentityMigration(ctx context.Context, input c.IdentityMigrationCommit, at time.Time) error {
	request, err := normalize(input.IdentityMigrationInput, at)
	if err != nil {
		return err
	}
	if input.ActorAdminID == 0 || !input.HumanVerified || len(request.versions) != len(request.targets) || len(input.ExpectedRoutes) != len(request.targets) || len(input.ExpectedGroups) != len(request.targets) {
		return c.ErrInvalid
	}
	err = dblock.Transaction(ctx, store.db, func(tx *gorm.DB) error {
		// Serialize against paid grant, refund and another migration before reading evidence.
		for _, target := range request.targets {
			if err := dblock.Row(tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id = ? AND version = ?", request.oldID, target, request.versions[target]), "version", c.ErrConflict); err != nil {
				return err
			}
		}
		preview := &c.IdentityMigrationPreview{}
		if err := readAccount(tx, input.AccountID, request.newID, preview); err != nil {
			return err
		}
		if !preview.BindingMatchesNewID || preview.BindingVersion != input.ExpectedBindingVersion {
			return c.ErrUntrustedIdentity
		}
		for _, target := range request.targets {
			if err := commitTarget(tx, input, request, preview, target, at); err != nil {
				return err
			}
		}
		return nil
	})
	slog.InfoContext(ctx, "membership identity migration reviewed", "targets", len(request.targets), "committed", err == nil)
	return err
}

// commitTarget rejects overlapping mappings and unsettled old effects before persisting one exact target.
// Route and projection versions come from the reviewed preview, not current product configuration.
func commitTarget(tx *gorm.DB, input c.IdentityMigrationCommit, request request, preview *c.IdentityMigrationPreview, target uint, at time.Time) error {
	var count int64
	if err := tx.Model(&d.IdentityMigration{}).Where("target_id = ? AND (old_telegram_id IN ? OR new_telegram_id IN ?)", target, []int64{request.oldID, request.newID}, []int64{request.oldID, request.newID}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return c.ErrConflict
	}
	// Reserve/lock the destination before inspecting facts. A preview-only root is
	// reusable, but a concurrent grant must finish before the conflict check below.
	reservation := d.LedgerProjection{BeneficiaryTelegramID: request.newID, TargetID: target, CalculatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reservation).Error; err != nil {
		return err
	}
	if err := dblock.Row(tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id = ?", request.newID, target), "version", c.ErrConflict); err != nil {
		return err
	}
	snapshot, err := readScope(tx, input.AccountID, request, target, at)
	if err != nil {
		return err
	}
	key := strconv.FormatUint(uint64(target), 10)
	if input.ExpectedRoutes[key] != snapshot.RouteVersion || input.ExpectedGroups[key] != snapshot.GroupVersion {
		return c.ErrConflict
	}
	// An old in-flight/unknown approval can race admission switching; require explicit recovery first.
	if err := tx.Model(&d.AccessOperation{}).Where("target_id = ? AND telegram_id IN ? AND state NOT IN ?", target, []int64{request.oldID, request.newID}, []string{"succeeded", "failed"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return c.ErrConflict
	}
	if err := tx.Model(&d.AccessInvite{}).Where("target_id = ? AND telegram_id IN ? AND state NOT IN ? AND (term = 'long_term' OR expiry_trusted = false OR expires_at > ?)", target, []int64{request.oldID, request.newID}, []string{"revoked", "expired"}, at).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return c.ErrConflict
	}
	binding, err := strconv.ParseUint(preview.BindingVersion, 10, 64)
	if err != nil {
		return c.ErrInvalid
	}
	bot, err := strconv.ParseUint(snapshot.BotID, 10, 64)
	if err != nil {
		return c.ErrInvalid
	}
	chat, err := strconv.ParseInt(snapshot.ChatID, 10, 64)
	if err != nil {
		return c.ErrInvalid
	}
	route, err := strconv.ParseUint(snapshot.RouteVersion, 10, 64)
	if err != nil {
		return c.ErrInvalid
	}
	group, err := strconv.ParseUint(snapshot.GroupVersion, 10, 64)
	if err != nil {
		return c.ErrInvalid
	}
	row := d.IdentityMigration{AccountID: input.AccountID, TargetID: target, OldTelegramID: request.oldID, NewTelegramID: request.newID, ActorAdminID: input.ActorAdminID, BindingVersion: uint(binding), BotID: uint(bot), ChatID: chat, RouteVersion: route, GroupVersion: group, EntitlementVersion: request.versions[target], Version: 1, State: "awaiting_member", CreatedAt: at.UTC()}
	return tx.Create(&row).Error
}

var _ c.IdentityMigrationWriter = (*Store)(nil)
