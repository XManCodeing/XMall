package telegrambotrecovery

import (
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"gorm.io/gorm"
)

// destinationEmpty rejects historical as well as active target business data.
// Missing tables are errors, never interpreted as an empty production database.
func destinationEmpty(tx *gorm.DB, bot uint) error {
	for _, table := range []string{
		"telegram_bot_contacts", "telegram_bot_sessions", "telegram_bot_inbox", "telegram_bot_deliveries",
		"telegram_support_bot_policies", "telegram_broadcasts",
		"telegram_support_sessions", "telegram_support_topic_operations", "telegram_support_topic_mappings",
		"telegram_support_ready_dispatches", "telegram_support_reply_mappings", "telegram_support_resolution_audits",
		"telegram_membership_access_invites", "telegram_membership_access_joins", "telegram_membership_access_operations",
		"telegram_membership_access_members", "telegram_membership_reminders", "telegram_membership_identity_migrations",
	} {
		var count int64
		if err := tx.Table(table).Where("bot_id = ?", bot).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return c.ErrConflict
		}
	}
	for _, table := range []string{"telegram_group_targets", "telegram_group_routes", "telegram_bot_module_configs"} {
		var count int64
		if err := tx.Table(table).Where("bot_instance_id = ?", bot).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return c.ErrConflict
		}
	}
	var countDisasters int64
	if err := tx.Table("telegram_support_disaster_checkpoints").Where("source_bot_id = ? OR destination_bot_id = ?", bot, bot).Count(&countDisasters).Error; err != nil {
		return err
	}
	if countDisasters != 0 {
		return c.ErrConflict
	}
	return nil
}
