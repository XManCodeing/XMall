package routerecovery

import (
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const legacyRuns = "telegram_membership_route_recovery_runs"
const legacyEvidence = "telegram_membership_route_recovery_evidence"

// MigrateBotRecovery creates new multi-target tables without altering legacy primary
// keys. db is the caller's migration handle; no global connection or HTTP is used.
// Copying is keyset-paged and insert-only, so MySQL DDL commits and partial retries
// cannot reset newer recovery progress. Legacy tables remain read-only audit facts.
func MigrateBotRecovery(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("membership recovery migration database missing")
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		return err
	}
	if db.Migrator().HasTable(legacyRuns) {
		var after uint
		for {
			var rows []Run
			if err := db.Table(legacyRuns).Where("task_id > ?", after).Order("task_id").Limit(100).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if err := copyLegacyRun(db, row); err != nil {
					return err
				}
				after = row.TaskID
			}
			if len(rows) < 100 {
				break
			}
		}
	}
	if !db.Migrator().HasTable(legacyEvidence) {
		return nil
	}
	return copyLegacyEvidence(db)
}

// copyLegacyRun preserves immutable single-Bot identity but never upgrades a scan
// into cross-Bot completion. Existing destination progress is validated, not overwritten.
func copyLegacyRun(db *gorm.DB, row Run) error {
	if row.TaskID == 0 || row.TargetID == 0 || row.BotID == 0 {
		return fmt.Errorf("membership legacy run identity invalid")
	}
	row.OldBotID, row.NewBotID = 0, 0
	row.OldVersion, row.NewVersion, row.OldGeneration, row.NewGeneration = 0, 0, 0, 0
	row.Verified, row.CompletionReceipt = false, ""
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	var saved Run
	if err := db.Where("task_id = ? AND target_id = ?", row.TaskID, row.TargetID).Take(&saved).Error; err != nil {
		return err
	}
	if saved.BotID != row.BotID || saved.OldChatID != row.OldChatID || saved.NewChatID != row.NewChatID || saved.OldRouteVersion != row.OldRouteVersion || saved.NewRouteVersion != row.NewRouteVersion || saved.Generation != row.Generation || saved.Version < row.Version || saved.OldBotID != 0 || saved.NewBotID != 0 {
		return fmt.Errorf("membership legacy run mapping conflict: task=%d target=%d", row.TaskID, row.TargetID)
	}
	return nil
}

// copyLegacyEvidence keyset-pages the frozen three-column key. Each evidence target
// must match its original task run, preventing a corrupt row from crossing targets.
func copyLegacyEvidence(db *gorm.DB) error {
	var after Evidence
	for {
		var rows []Evidence
		query := db.Table(legacyEvidence)
		if after.TaskID != 0 {
			query = query.Where("task_id > ? OR (task_id = ? AND kind > ?) OR (task_id = ? AND kind = ? AND reference > ?)", after.TaskID, after.TaskID, after.Kind, after.TaskID, after.Kind, after.Reference)
		}
		if err := query.Order("task_id, kind, reference").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.TaskID == 0 || row.TargetID == 0 || row.Kind == "" || row.Reference == "" {
				return fmt.Errorf("membership legacy evidence identity invalid")
			}
			var parent Run
			if err := db.Table(legacyRuns).Where("task_id = ?", row.TaskID).Take(&parent).Error; err != nil {
				return err
			}
			if parent.TargetID != row.TargetID {
				return fmt.Errorf("membership legacy evidence target conflict: task=%d", row.TaskID)
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			var saved Evidence
			if err := db.Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ?", row.TaskID, row.TargetID, row.Kind, row.Reference).Take(&saved).Error; err != nil {
				return err
			}
			if saved.SourceID != row.SourceID || saved.OrderID != row.OrderID || saved.OrderItemID != row.OrderItemID || saved.TelegramID != row.TelegramID {
				return fmt.Errorf("membership legacy evidence source conflict: task=%d", row.TaskID)
			}
			after = row
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
