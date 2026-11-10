package identitymigration

import (
	"context"
	"strconv"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
)

// PrepareInvites rechecks reviewed input before retiring old invitations on tx.
// retire only persists cleanup work; no platform call may run inside this transaction.
// Unknown approvals and overlapping migrations remain hard conflicts.
func (store *Store) PrepareInvites(ctx context.Context, input c.IdentityMigrationCommit, at time.Time, retire func(*gorm.DB, uint, int64, uint64) error) error {
	request, err := normalize(input.IdentityMigrationInput, at)
	if err != nil {
		return err
	}
	if retire == nil || input.ActorAdminID == 0 || !input.HumanVerified || len(request.versions) != len(request.targets) || len(input.ExpectedRoutes) != len(request.targets) || len(input.ExpectedGroups) != len(request.targets) {
		return c.ErrInvalid
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		preview := &c.IdentityMigrationPreview{}
		if err := readAccount(tx, input.AccountID, request.newID, preview); err != nil {
			return err
		}
		if !preview.BindingMatchesNewID || preview.BindingVersion != input.ExpectedBindingVersion {
			return c.ErrUntrustedIdentity
		}
		for _, target := range request.targets {
			if err := dblock.Row(tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id = ? AND version = ?", request.oldID, target, request.versions[target]), "version", c.ErrConflict); err != nil {
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
			var count int64
			if err := tx.Model(&d.IdentityMigration{}).Where("target_id = ? AND (old_telegram_id IN ? OR new_telegram_id IN ?)", target, []int64{request.oldID, request.newID}, []int64{request.oldID, request.newID}).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return c.ErrConflict
			}
			// Existing revoke work is inspected by the cleanup state machine, never
			// recreated. All other unfinished effects must settle before retirement.
			if err := tx.Model(&d.AccessOperation{}).Where("target_id = ? AND telegram_id IN ? AND kind <> ? AND state NOT IN ?", target, []int64{request.oldID, request.newID}, "revoke", []string{"succeeded", "failed"}).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return c.ErrConflict
			}
			if err := retire(tx, target, request.oldID, request.versions[target]); err != nil {
				return err
			}
		}
		return nil
	})
}
