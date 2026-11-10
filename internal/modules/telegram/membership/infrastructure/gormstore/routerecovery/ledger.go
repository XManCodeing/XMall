package routerecovery

import (
	"fmt"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// scanLedger recalculates original terms using the existing domain calculator, without
// changing sources, historical orders, projections or outbox. Stable TargetID is the
// destination mapping, so late paid facts continue resolving through the replacement route.
func scanLedger(tx *gorm.DB, request scanRequest, at time.Time) error {
	var sources []d.LedgerSource
	if err := tx.Where("target_id = ?", request.TargetID).Order("id").Find(&sources).Error; err != nil {
		return err
	}
	byIdentity := map[int64][]d.LedgerSource{}
	for _, source := range sources {
		byIdentity[source.BeneficiaryTelegramID] = append(byIdentity[source.BeneficiaryTelegramID], source)
	}
	for identity, rows := range byIdentity {
		if err := ledgerScope(tx, request, at, identity, rows); err != nil {
			return err
		}
	}
	// Orphan projections are not evidence of an entitlement and must not pass empty scans.
	var projections []d.LedgerProjection
	if err := tx.Where("target_id = ?", request.TargetID).Find(&projections).Error; err != nil {
		return err
	}
	for _, projection := range projections {
		if _, exists := byIdentity[projection.BeneficiaryTelegramID]; exists {
			continue
		}
		if err := record(tx, request, at, Evidence{Kind: "projection", Reference: fmt.Sprint(projection.BeneficiaryTelegramID), State: "unknown"}); err != nil {
			return err
		}
	}
	return scanDeliveries(tx, request, at)
}

// ledgerScope records source/order identity and original effective expiry per beneficiary.
// Invalid or absent projection proof remains unknown; database failures abort the scan.
func ledgerScope(tx *gorm.DB, request scanRequest, at time.Time, identity int64, rows []d.LedgerSource) error {
	scope := d.EntitlementScope{BeneficiaryTelegramID: identity, TargetID: request.TargetID}
	facts := make([]d.EntitlementSource, 0, len(rows))
	state := "resolved"
	for _, row := range rows {
		if row.Operation != "grant" || row.AccountID == 0 || row.OrderID == 0 || row.OrderItemID == 0 {
			state = "unknown"
		}
		facts = append(facts, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
	}
	var revoked []d.LedgerRevocation
	if err := tx.Where("target_id = ?", request.TargetID).Find(&revoked).Error; err != nil {
		return err
	}
	keys := []d.SourceKey{}
	for _, row := range revoked {
		if row.BeneficiaryTelegramID == identity {
			keys = append(keys, d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID})
			continue
		}
		for _, source := range rows {
			if source.OrderItemID == row.OrderItemID {
				state = "unknown"
			}
		}
	}
	calculation, err := d.RecalculateEntitlement(scope, facts, keys, at)
	if err != nil {
		state = "unknown"
	}
	var projections []d.LedgerProjection
	if err := tx.Where("target_id = ? AND beneficiary_telegram_id = ?", request.TargetID, identity).Find(&projections).Error; err != nil {
		return err
	}
	var version uint64
	if len(projections) != 1 || projections[0].Version == 0 {
		state = "unknown"
	} else {
		version = projections[0].Version
	}
	for _, row := range rows {
		entry := Evidence{Kind: "source", Reference: fmt.Sprint(row.ID), State: state, SourceID: row.ID, OrderID: row.OrderID, OrderItemID: row.OrderItemID, TelegramID: identity, EntitlementVersion: version, TermKind: calculation.Effective.Kind, ExpiresAt: calculation.Effective.ExpiresAt}
		if err := record(tx, request, at, entry); err != nil {
			return err
		}
	}
	return nil
}

// scanDeliveries conservatively inventories all Bot panel deliveries because invitation
// references live inside encrypted payloads. Without a typed runtime reconciliation proof,
// unresolved panel deliveries cannot be safely attributed away from membership.
func scanDeliveries(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []struct {
		ID     uint
		Status string
	}
	if err := tx.Table("telegram_bot_deliveries").Select("id, status").Where("bot_id IN ? AND purpose = ?", []uint{request.OldBotID, request.NewBotID}, "panel").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "unknown"
		switch row.Status {
		case "sent", "failed", "superseded":
			state = "resolved"
		case "pending", "retry":
			state = "remaining"
		}
		if err := record(tx, request, at, Evidence{Kind: "delivery", Reference: fmt.Sprint(row.ID), State: state}); err != nil {
			return err
		}
	}
	return nil
}
