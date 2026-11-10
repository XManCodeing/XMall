package gormstore

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// AuthorizePanelCleanup checks durable positive Telegram receipts, never callback claims.
// Both messages must belong to the same bot/private recipient; the target cannot be active.
func (s *Store) AuthorizePanelCleanup(ctx context.Context, bot uint, telegram, target int64, proof c.ConfirmedPanelCleanup) error {
	if bot == 0 || telegram <= 0 || target <= 0 || proof.SourceDeliveryID == 0 {
		return c.ErrConflict
	}
	var source d.Delivery
	if err := s.db.WithContext(ctx).Where("id = ? AND bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND purpose = ? AND status = ?", proof.SourceDeliveryID, bot, telegram, telegram, "panel", "sent").First(&source).Error; err != nil {
		return err
	}
	if c.TargetType(source.TargetType).Normalize() != c.TargetShopPrivate {
		return c.ErrConflict
	}
	if source.MessageID <= 0 || source.MessageID == target || source.PreviousMessageID != target {
		return c.ErrConflict
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&d.Delivery{}).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND purpose = ? AND status = ? AND message_id = ?", bot, telegram, telegram, "panel", "sent", target).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return c.ErrConflict
	}
	var session d.Session
	if err := s.db.WithContext(ctx).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ?", bot, telegram, telegram).First(&session).Error; err != nil {
		return err
	}
	if session.PanelMessageID == target {
		return c.ErrConflict
	}
	return nil
}
