package routerecovery

import (
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"gorm.io/gorm"
)

// fence locks runtime and target predicates on tx, then validates both historical routes.
// No module domain is imported across boundaries and no global connection is consulted.
func fence(tx *gorm.DB, request Request) error {
	bot := tx.Table("telegram_bot_instances").Where("id = ? AND generation = ? AND mode = ? AND enabled = ? AND state = ?", request.BotID, request.Generation, "internal", true, "running")
	if err := matchedLock(bot, "generation"); err != nil {
		return err
	}
	group := tx.Table("telegram_group_targets").Where("id = ? AND recovery_task_id = ? AND purpose = ? AND enabled = ? AND bot_instance_id = ? AND telegram_chat_id = ? AND route_version = ?", request.TargetID, request.TaskID, "membership", true, request.BotID, request.NewChatID, request.NewRouteVersion)
	if err := matchedLock(group, "version"); err != nil {
		return err
	}
	for _, route := range []struct {
		chat    int64
		version uint64
		retired bool
	}{{request.OldChatID, request.OldRouteVersion, true}, {request.NewChatID, request.NewRouteVersion, false}} {
		query := tx.Table("telegram_group_routes").Where("target_id = ? AND bot_instance_id = ? AND telegram_chat_id = ? AND route_version = ?", request.TargetID, request.BotID, route.chat, route.version)
		if route.retired {
			query = query.Where("retired_at IS NOT NULL")
		} else {
			query = query.Where("retired_at IS NULL")
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
	}
	return nil
}

// matchedLock uses a no-op write to serialize the exact CAS predicate until commit.
// count is checked separately for drivers that report changed rather than matched rows.
func matchedLock(query *gorm.DB, column string) error {
	if err := query.UpdateColumn(column, gorm.Expr(column)).Error; err != nil {
		return err
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return c.ErrConflict
	}
	return nil
}

// accessScope includes all history on the stable target plus physical-route aliases.
// Wrong Bot/target/version rows remain visible and become unknown instead of disappearing.
func accessScope(tx *gorm.DB, request scanRequest) *gorm.DB {
	return tx.Where("target_id = ? OR chat_id IN ?", request.TargetID, []int64{request.OldChatID, request.NewChatID})
}

// routeState distinguishes current destination from historical cleanup and malformed scopes.
func routeState(request scanRequest, target, bot uint, chat int64, version uint64) string {
	if target != request.TargetID {
		return "unknown"
	}
	if bot == request.NewBotID && chat == request.NewChatID && version == request.NewRouteVersion {
		return "destination"
	}
	if bot == request.OldBotID && chat == request.OldChatID && version == request.OldRouteVersion {
		return "old"
	}
	return "unknown"
}
