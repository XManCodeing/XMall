package gormstore

import (
	"fmt"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateRecoveryTargets creates the v22 composite-key receipt table and copies v19-v21 receipts.
// db must be the migration transaction. The old table remains read-only for rollback/audit;
// copying is bounded, idempotent and preserves every original task/version/release proof.
func MigrateRecoveryTargets(db *gorm.DB) error {
	if err := db.AutoMigrate(&d.RecoveryFence{}); err != nil {
		return err
	}
	const legacy = "telegram_group_recovery_fences"
	if !db.Migrator().HasTable(legacy) {
		return nil
	}
	var after uint
	for {
		var rows []d.RecoveryFence
		if err := db.Table(legacy).Where("task_id > ?", after).Order("task_id ASC").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.TaskID == 0 || row.TargetID == 0 {
				return fmt.Errorf("group recovery receipt has invalid identity")
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			var stored d.RecoveryFence
			if err := db.Where("task_id = ? AND target_id = ?", row.TaskID, row.TargetID).Take(&stored).Error; err != nil {
				return err
			}
			if stored.TaskID != row.TaskID || stored.TargetID != row.TargetID || stored.OldBotID != row.OldBotID || stored.NewBotID != row.NewBotID || stored.OldChatID != row.OldChatID || stored.NewChatID != row.NewChatID || stored.InitialVersion != row.InitialVersion || stored.InitialRouteVersion != row.InitialRouteVersion || stored.FrozenVersion != row.FrozenVersion || stored.ReplacedVersion < row.ReplacedVersion || stored.ReleasedVersion < row.ReleasedVersion {
				return fmt.Errorf("group recovery migration receipt conflict: task=%d target=%d", row.TaskID, row.TargetID)
			}
			after = row.TaskID
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
