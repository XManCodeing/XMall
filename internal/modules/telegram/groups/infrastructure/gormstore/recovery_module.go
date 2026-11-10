package gormstore

import (
	"errors"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// guardRecoveryModule rejects rebinding, clearing or toggling either side of a
// fenced module reference. Target locks serialize this check with FreezeTarget;
// all reads use tx, including SQLite's single-connection configuration.
func guardRecoveryModule(tx *gorm.DB, config *d.ModuleConfig) error {
	var previous d.ModuleConfig
	err := tx.Where("bot_instance_id = ? AND partition = ?", config.BotInstanceID, config.Partition).First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var targets []d.Target
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", []uint{previous.TargetID, config.TargetID}).Order("id ASC").Find(&targets).Error; err != nil {
		return err
	}
	for _, target := range targets {
		if target.RecoveryTaskID != 0 {
			return c.ErrConflict
		}
	}
	return nil
}
