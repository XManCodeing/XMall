package identitymigration

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// groupSnapshot selects only current routing metadata, not capability secrets or contacts.
type groupSnapshot struct {
	ID             uint
	Purpose        string
	Enabled        bool
	RecoveryTaskID uint
	Version        uint64
	RouteVersion   uint64
	BotInstanceID  uint
	TelegramChatID int64
}

// readScope verifies target, old identity provenance, current version and destination conflicts.
// It returns the original effective period, never a recalculated grant starting at migration time.
func readScope(tx *gorm.DB, account uint, request request, targetID uint, at time.Time) (*c.IdentityMigrationTarget, error) {
	var group groupSnapshot
	err := tx.Table("telegram_group_targets").Select("id, purpose, enabled, recovery_task_id, version, route_version, bot_instance_id, telegram_chat_id").Where("id = ?", targetID).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, c.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if group.Purpose != "membership" || !group.Enabled || group.RecoveryTaskID != 0 || group.BotInstanceID == 0 || group.TelegramChatID >= 0 || group.Version == 0 || group.RouteVersion == 0 {
		return nil, c.ErrCapability
	}
	var projection d.LedgerProjection
	err = tx.Select("beneficiary_telegram_id, target_id, version").Where("beneficiary_telegram_id = ? AND target_id = ?", request.oldID, targetID).Take(&projection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, c.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if expected, exists := request.versions[targetID]; exists && expected != projection.Version {
		return nil, fmt.Errorf("source entitlement version changed: %w", c.ErrConflict)
	}
	scope := d.EntitlementScope{BeneficiaryTelegramID: request.oldID, TargetID: targetID}
	calculated, sources, err := calculate(tx, account, scope, at)
	if err != nil {
		return nil, err
	}
	if !calculated.Effective.Active {
		return nil, fmt.Errorf("original entitlement inactive: %w", c.ErrConflict)
	}
	if err := destinationConflict(tx, request.newID, group); err != nil {
		return nil, err
	}
	return &c.IdentityMigrationTarget{TargetID: strconv.FormatUint(uint64(targetID), 10), EntitlementVersion: strconv.FormatUint(projection.Version, 10), GroupVersion: strconv.FormatUint(group.Version, 10), RouteVersion: strconv.FormatUint(group.RouteVersion, 10), BotID: strconv.FormatUint(uint64(group.BotInstanceID), 10), ChatID: strconv.FormatInt(group.TelegramChatID, 10), Kind: calculated.Effective.Kind, ExpiresAt: calculated.Effective.ExpiresAt, LimitedExpiresAt: calculated.LimitedExpiresAt, Sources: sources}, nil
}

// calculate adapts persisted source and revocation facts to the existing pure domain calculator.
// Every source in the old scope must belong to account, including revoked/future facts.
func calculate(tx *gorm.DB, account uint, scope d.EntitlementScope, at time.Time) (d.EntitlementCalculation, []c.IdentityMigrationSource, error) {
	var empty d.EntitlementCalculation
	var rows []d.LedgerSource
	if err := tx.Select("id, order_item_id, target_id, operation, beneficiary_telegram_id, account_id, paid_at, kind, months").Where("beneficiary_telegram_id = ? AND target_id = ?", scope.BeneficiaryTelegramID, scope.TargetID).Order("paid_at, order_item_id, id").Find(&rows).Error; err != nil {
		return empty, nil, err
	}
	if len(rows) == 0 {
		return empty, nil, c.ErrUntrustedIdentity
	}
	itemIDs := make([]uint, 0, len(rows))
	sources := make([]d.EntitlementSource, 0, len(rows))
	for _, row := range rows {
		if row.AccountID != account || row.Operation != "grant" {
			return empty, nil, c.ErrUntrustedIdentity
		}
		itemIDs = append(itemIDs, row.OrderItemID)
		sources = append(sources, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
	}
	var revoked []d.LedgerRevocation
	// Include tombstones by source key even when their identity disagrees: fail closed.
	if err := tx.Where("target_id = ? AND (beneficiary_telegram_id = ? OR order_item_id IN ?)", scope.TargetID, scope.BeneficiaryTelegramID, itemIDs).Find(&revoked).Error; err != nil {
		return empty, nil, err
	}
	keys := make([]d.SourceKey, 0, len(revoked))
	revokedItems := make(map[uint]bool, len(revoked))
	for _, row := range revoked {
		keys = append(keys, d.SourceKey{EntitlementScope: d.EntitlementScope{BeneficiaryTelegramID: row.BeneficiaryTelegramID, TargetID: row.TargetID}, OrderItemID: row.OrderItemID})
		revokedItems[row.OrderItemID] = true
	}
	calculated, err := d.RecalculateEntitlement(scope, sources, keys, at)
	if err != nil {
		return empty, nil, err
	}
	snapshots := make([]c.IdentityMigrationSource, 0, len(rows))
	for _, row := range rows {
		snapshots = append(snapshots, c.IdentityMigrationSource{ID: strconv.FormatUint(uint64(row.ID), 10), OrderItemID: strconv.FormatUint(uint64(row.OrderItemID), 10), PaidAt: row.PaidAt.UTC(), Kind: row.Kind, Months: row.Months, Revoked: revokedItems[row.OrderItemID]})
	}
	return calculated, snapshots, nil
}

// destinationConflict rejects even historical destination facts rather than silently merging.
// A member row on the target OR current physical chat blocks regardless of status/bot;
// it is only local conflict evidence, not a claim that the new identity is actually in Telegram.
func destinationConflict(tx *gorm.DB, newID int64, group groupSnapshot) error {
	var aliases []uint
	if err := tx.Table("telegram_group_targets").Where("id = ? OR telegram_chat_id = ?", group.ID, group.TelegramChatID).Pluck("id", &aliases).Error; err != nil {
		return err
	}
	for _, model := range []any{&d.LedgerSource{}, &d.LedgerRevocation{}} {
		var count int64
		if err := tx.Model(model).Where("beneficiary_telegram_id = ? AND target_id IN ?", newID, aliases).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("destination entitlement facts exist: %w", c.ErrConflict)
		}
	}
	var count int64
	// Version/calculation time alone are serialization metadata created by previews.
	// Any effective or historical term remains a conflict, even without source rows.
	if err := tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id IN ? AND (active = ? OR kind <> ? OR expires_at IS NOT NULL OR limited_expires_at IS NOT NULL)", newID, aliases, true, "").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("destination projection facts exist: %w", c.ErrConflict)
	}
	if err := tx.Model(&d.AccessMember{}).Where("telegram_id = ? AND (target_id IN ? OR chat_id = ?)", newID, aliases, group.TelegramChatID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("destination known member conflict: %w", c.ErrConflict)
	}
	return nil
}
