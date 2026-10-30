package gormstore

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"time"
)

var _ c.PanelStore = (*Store)(nil)

func (s *Store) PreparePanel(ctx context.Context, item *d.Delivery, message *c.Message) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, item.BotID); err != nil {
			return err
		}
		var delivery d.Delivery
		if err := tx.Where("id = ? AND claim = ? AND status = ?", item.ID, item.Claim, "processing").First(&delivery).Error; err != nil {
			return c.ErrStale
		}
		var session d.Session
		if err := tx.Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ?", item.BotID, item.TelegramUserID, item.ChatID).First(&session).Error; err != nil {
			return err
		}
		if session.PanelEpoch != message.PanelEpoch || session.PanelRevision > message.PanelRevision {
			return c.ErrConflict
		}
		// A persisted target is authoritative. Callback data alone cannot redirect
		// edits to another message. Unknown creation may be claimed only from the
		// private callback source verified by the handler's epoch/action checks.
		var unknown int64
		if err := tx.Model(&d.Delivery{}).Where("bot_id = ? AND chat_id = ? AND purpose = ? AND panel_epoch = ? AND operation = ? AND status = ?", item.BotID, item.ChatID, "panel", message.PanelEpoch, "send", "unknown").Count(&unknown).Error; err != nil {
			return err
		}
		if (session.PanelState == "unknown" || (session.PanelState == "creating" && unknown > 0)) && message.TargetMessageID > 0 && session.PanelMessageEpoch == message.PanelEpoch {
			session.PanelMessageID = message.TargetMessageID
			if message.TargetKind == "photo" || message.TargetKind == "text" {
				session.PanelKind = message.TargetKind
			}
			session.PanelState = "ready"
			if err := tx.Model(&session).Updates(map[string]interface{}{"panel_message_id": session.PanelMessageID, "panel_kind": session.PanelKind, "panel_state": "ready"}).Error; err != nil {
				return err
			}
		}
		unknown = 0
		if err := tx.Model(&d.Delivery{}).Where("bot_id = ? AND chat_id = ? AND purpose = ? AND panel_epoch = ? AND operation = ? AND status IN ?", item.BotID, item.ChatID, "panel", message.PanelEpoch, "send", []string{"unknown", "processing"}).Where("id <> ?", item.ID).Count(&unknown).Error; err != nil {
			return err
		}
		known := session.PanelMessageID > 0 && session.PanelMessageEpoch == message.PanelEpoch && session.PanelState == "ready"
		if !known && (unknown > 0 || (session.PanelMessageEpoch == message.PanelEpoch && (session.PanelState == "creating" || session.PanelState == "unknown"))) {
			return c.ErrStale
		}
		message.Operation = "edit"
		message.TargetKind = session.PanelKind
		message.TargetMessageID = session.PanelMessageID
		if !known || (message.ReplacePanel && delivery.Attempts == 1) || (session.PanelKind == "photo" && len(message.Photo) == 0 && message.PhotoURL == "" && message.FileID == "") || session.PanelKind == "document" {
			message.Operation = "send"
			message.TargetMessageID = 0
		}
		// A retry must never migrate onto a different physical message.
		if delivery.Operation == "edit" && delivery.TargetMessageID > 0 && delivery.Attempts > 1 && (message.Operation != "edit" || delivery.TargetMessageID != message.TargetMessageID) {
			return c.ErrConflict
		}
		item.Operation, item.TargetMessageID = message.Operation, message.TargetMessageID
		item.MessageKind = "text"
		if len(message.Photo) > 0 || message.PhotoURL != "" || message.FileID != "" {
			item.MessageKind = "photo"
		}
		if message.ForceDocument {
			item.MessageKind = "document"
		}
		if message.Operation == "send" {
			item.PreviousMessageID = session.PanelMessageID
			if item.PreviousMessageID == 0 {
				item.PreviousMessageID = delivery.PreviousMessageID
			}
			if err := tx.Model(&session).Updates(map[string]interface{}{"panel_state": "creating", "panel_message_epoch": message.PanelEpoch}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&delivery).Updates(map[string]interface{}{"operation": item.Operation, "target_message_id": item.TargetMessageID, "previous_message_id": item.PreviousMessageID, "message_kind": item.MessageKind}).Error
	})
}

// FinishPanel never CASes on the rendering revision: successful older sends
// still yield the sole usable target for the next render. Only physical epoch
// ownership gates registration; the delivery result itself is always retained.
func (s *Store) FinishPanel(ctx context.Context, item *d.Delivery, status string, delivered c.DeliveryResult, detail string, available time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, item.BotID); err != nil {
			return err
		}
		var current d.Delivery
		if err := tx.First(&current, item.ID).Error; err != nil {
			return err
		}
		if current.Status == status && current.MessageID == delivered.MessageID && current.Claim == "" {
			return nil
		}
		if current.Status != "processing" || current.Claim != item.Claim {
			return c.ErrStale
		}
		if err := result(tx.Model(&d.Delivery{}).Where("id = ? AND status = ? AND claim = ?", item.ID, "processing", item.Claim).Updates(map[string]interface{}{"status": status, "message_id": delivered.MessageID, "claim": "", "claimed_until": nil, "last_error": safeError(detail), "available_at": readyAt(available)}), c.ErrStale); err != nil {
			return err
		}
		changes := map[string]interface{}{}
		if status == "sent" && delivered.MessageID > 0 {
			kind := item.MessageKind
			if delivered.MessageKind != "" {
				kind = delivered.MessageKind
			}
			changes = map[string]interface{}{"panel_message_id": delivered.MessageID, "panel_message_epoch": item.PanelEpoch, "panel_kind": kind, "panel_state": "ready", "panel_delivered_revision": item.PanelRevision}
		} else if item.Operation == "send" {
			switch status {
			case "unknown":
				changes["panel_state"] = "unknown"
			case "failed", "retry":
				changes["panel_state"] = ""
			}
		}
		if len(changes) == 0 {
			return nil
		}
		return tx.Model(&d.Session{}).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND panel_message_epoch = ?", item.BotID, item.TelegramUserID, item.ChatID, item.PanelEpoch).Updates(changes).Error
	})
}

// ReplacePanel only permits a definite invalid-target response to transition an
// edit to a fresh send; arbitrary API errors must not invoke this transition.
func (s *Store) ReplacePanel(ctx context.Context, item *d.Delivery) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, item.BotID); err != nil {
			return err
		}
		res := tx.Model(&d.Session{}).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND panel_epoch = ? AND panel_message_id = ?", item.BotID, item.TelegramUserID, item.ChatID, item.PanelEpoch, item.TargetMessageID).Updates(map[string]interface{}{"panel_message_id": 0, "panel_state": ""})
		if err := result(res, c.ErrConflict); err != nil {
			return err
		}
		return result(tx.Model(&d.Delivery{}).Where("id = ? AND claim = ? AND status = ?", item.ID, item.Claim, "processing").Updates(map[string]interface{}{"operation": "", "target_message_id": 0, "previous_message_id": item.TargetMessageID}), c.ErrStale)
	})
}
