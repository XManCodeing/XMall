package identitymigration

import (
	"errors"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// Mapping reads the explicit target mapping for identity on tx; nil means no migration.
// It never consults website bindings and never traverses identity chains.
func Mapping(tx *gorm.DB, identity int64, target uint) (*d.IdentityMigration, error) {
	var rows []d.IdentityMigration
	if err := tx.Where("target_id = ? AND (old_telegram_id = ? OR new_telegram_id = ?)", target, identity, identity).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, c.ErrConflict
	}
	return &rows[0], nil
}

// Scope resolves the stable ledger root for identity/target using only tx.
// All grants and revocations still persist the original source identity.
func Scope(tx *gorm.DB, identity int64, target uint) (int64, error) {
	mapping, err := Mapping(tx, identity, target)
	if err != nil || mapping == nil {
		return identity, err
	}
	return mapping.OldTelegramID, nil
}

// Sources returns original facts plus their tombstones for a mapped scope.
// account ownership is checked before any combination; callers normalize only calculator copies.
func Sources(tx *gorm.DB, identity int64, target uint) ([]d.LedgerSource, []d.LedgerRevocation, *d.IdentityMigration, error) {
	mapping, err := Mapping(tx, identity, target)
	if err != nil {
		return nil, nil, nil, err
	}
	identities := []int64{identity}
	if mapping != nil {
		identities = []int64{mapping.OldTelegramID, mapping.NewTelegramID}
	}
	var sources []d.LedgerSource
	if err := tx.Where("beneficiary_telegram_id IN ? AND target_id = ?", identities, target).Order("id").Find(&sources).Error; err != nil {
		return nil, nil, nil, err
	}
	var revoked []d.LedgerRevocation
	if err := tx.Where("beneficiary_telegram_id IN ? AND target_id = ?", identities, target).Find(&revoked).Error; err != nil {
		return nil, nil, nil, err
	}
	if mapping != nil {
		for _, source := range sources {
			if source.AccountID != mapping.AccountID {
				return nil, nil, nil, c.ErrUntrustedIdentity
			}
		}
	}
	sourceIdentity := make(map[uint]int64, len(sources))
	for _, source := range sources {
		sourceIdentity[source.OrderItemID] = source.BeneficiaryTelegramID
	}
	for _, tombstone := range revoked {
		if original, exists := sourceIdentity[tombstone.OrderItemID]; exists && original != tombstone.BeneficiaryTelegramID {
			return nil, nil, nil, d.ErrEntitlementScope
		}
	}
	return sources, revoked, mapping, nil
}

// SourceMatches validates immutable source provenance against an explicit current mapping.
// Returns false for unrelated identities and routes, including targets added after migration.
func SourceMatches(tx *gorm.DB, sourceID, target uint, identity int64) (bool, error) {
	var source d.LedgerSource
	err := tx.First(&source, "id = ? AND target_id = ?", sourceID, target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if source.BeneficiaryTelegramID == identity {
		return true, nil
	}
	mapping, err := Mapping(tx, identity, target)
	return mapping != nil && mapping.NewTelegramID == identity && mapping.OldTelegramID == source.BeneficiaryTelegramID && mapping.AccountID == source.AccountID, err
}

// Advance atomically switches current benefit only on a newer trusted actual-member observation.
// Approval, invitation creation and administrative identity verification are never admission evidence.
func Advance(tx *gorm.DB, input c.AccessObservation) error {
	if !input.IsMember && (input.Status == "left" || input.Status == "restricted") {
		var unresolved int64
		if err := tx.Model(&d.AccessOperation{}).Where("kind = ? AND target_id = ? AND chat_id = ? AND telegram_id = ? AND state <> ?", "lifecycle", input.TargetID, input.ChatID, input.TelegramID, "succeeded").Count(&unresolved).Error; err != nil {
			return err
		}
		if unresolved != 0 {
			return nil
		}
		return tx.Model(&d.IdentityMigration{}).Where("target_id = ? AND old_telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND state = ? AND admitted_at < ?", input.TargetID, input.TelegramID, input.BotID, input.ChatID, input.RouteVersion, "cleanup_pending", input.ObservedAt).Updates(map[string]any{"state": "completed", "completed_at": input.ObservedAt, "version": gorm.Expr("version + 1")}).Error
	}
	if !input.IsMember || (input.Status != "member" && input.Status != "restricted" && input.Status != "administrator" && input.Status != "creator") {
		return nil
	}
	updated := tx.Model(&d.IdentityMigration{}).Where("target_id = ? AND new_telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND state = ? AND created_at < ?", input.TargetID, input.TelegramID, input.BotID, input.ChatID, input.RouteVersion, "awaiting_member", input.ObservedAt).
		Updates(map[string]any{"state": "cleanup_pending", "admitted_at": input.ObservedAt, "version": gorm.Expr("version + 1")})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return nil
	}
	mapping, err := Mapping(tx, input.TelegramID, input.TargetID)
	if err != nil {
		return err
	}
	if mapping == nil {
		return c.ErrConflict
	}
	return tx.Model(&d.LedgerProjection{}).Where("target_id = ? AND beneficiary_telegram_id = ?", input.TargetID, mapping.OldTelegramID).UpdateColumn("version", gorm.Expr("version + 1")).Error
}

// Complete records verified removal only after the existing lifecycle's successful final phase.
// Unknown effects and ban acknowledgements alone cannot complete the migration.
func Complete(tx *gorm.DB, operation *d.AccessOperation, outcome c.LifecycleResult, at time.Time) error {
	if outcome.Outcome != "succeeded" || outcome.Member == nil || outcome.Member.Outcome != c.AccessSucceeded || outcome.Member.IsMember || (outcome.Member.Status != "left" && outcome.Member.Status != "restricted") {
		return nil
	}
	return tx.Model(&d.IdentityMigration{}).Where("target_id = ? AND old_telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND state = ?", operation.TargetID, operation.TelegramID, operation.BotID, operation.ChatID, operation.RouteVersion, "cleanup_pending").Updates(map[string]any{"state": "completed", "completed_at": at, "version": gorm.Expr("version + 1")}).Error
}
