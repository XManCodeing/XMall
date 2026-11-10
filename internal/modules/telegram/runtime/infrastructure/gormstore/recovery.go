package gormstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BusinessDelivery returns persisted queue facts by exact key, including non-delivery terminal states.
// A missing platform receipt never becomes sent, even if an adapter returned HTTP success.
func (s *Store) BusinessDelivery(ctx context.Context, bot uint, key string) (c.InviteDeliveryStatus, error) {
	if bot == 0 || key == "" {
		return c.InviteDeliveryStatus{}, c.ErrConflict
	}
	var row d.Delivery
	err := s.db.WithContext(ctx).Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return c.InviteDeliveryStatus{State: "not_requested"}, nil
	}
	if err != nil {
		return c.InviteDeliveryStatus{}, err
	}
	state := row.Status
	if state == "sent" && row.MessageID <= 0 {
		state = "unknown"
	}
	return c.InviteDeliveryStatus{Operation: key, State: state, MessageID: row.MessageID}, nil
}

// RetryFailedDelivery accepts a trusted immutable source and current generation.
// It performs no send and returns ErrConflict unless a complete, unclaimed failed
// row has no platform observations. The caller owns business CAS and audit.
func (s *Store) RetryFailedDelivery(ctx context.Context, in c.DeliveryRetry) error {
	if in.BotID == 0 || in.DeliveryID == 0 || in.Generation == 0 || in.Now.IsZero() || in.PayloadCipher == "" {
		return c.ErrConflict
	}
	if (in.TargetType != string(c.TargetSupportGroup) || in.Purpose != "support_ready") &&
		(in.TargetType != string(c.TargetSupportPrivate) || in.Purpose != "support_reply") {
		return c.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fenceGeneration(tx, in.BotID, in.Generation); err != nil {
			return err
		}
		var instance d.Instance
		if err := tx.Where("id = ? AND enabled = ? AND mode = ? AND state = ?", in.BotID, true, "internal", "running").First(&instance).Error; err != nil {
			return c.ErrStale
		}
		var row d.Delivery
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ?", in.DeliveryID, in.BotID).First(&row).Error; err != nil {
			return notFound(err)
		}
		if !retryMatches(row, in) {
			return c.ErrConflict
		}
		updates := map[string]any{"status": "retry", "available_at": in.Now.UTC(), "last_error": "", "updated_at": in.Now.UTC()}
		if in.ReplacementPayloadCipher != "" {
			if row.LastError != "topic_missing" || row.TargetType != string(c.TargetSupportGroup) || row.Purpose != "support_ready" {
				return c.ErrConflict
			}
			updates["payload_cipher"] = in.ReplacementPayloadCipher
		}
		return result(tx.Model(&d.Delivery{}).Where("id = ? AND status = ? AND attempts = ? AND updated_at = ?", row.ID, "failed", row.Attempts, row.UpdatedAt).
			Updates(updates), c.ErrConflict)
	})
}

// retryMatches checks the entire server-owned source binding and terminal result.
// It returns false for unknown or any partial receipt, including legacy singles.
func retryMatches(row d.Delivery, in c.DeliveryRetry) bool {
	return row.Status == "failed" && row.Claim == "" && row.ClaimedUntil == nil && row.MessageID == 0 && len(row.MessageIDs) == 0 &&
		row.Key == in.Key && row.TargetType == in.TargetType && row.Purpose == in.Purpose && row.Operation == "send" &&
		row.ChatID == in.ChatID && row.TelegramUserID == in.TelegramUserID && row.PayloadCipher == in.PayloadCipher &&
		row.BroadcastID == 0 && row.UserID == 0 && row.PanelEpoch == "" && row.PanelRevision == 0 && row.TargetMessageID == 0 && row.PreviousMessageID == 0 &&
		!row.CreatedAt.After(in.Now.Add(time.Second))
}

// InviteDelivery reads persistent delivery evidence without decoding secret payloads.
// Runtime key namespaces preserve invite association even after payload retirement.
func (s *Store) InviteDelivery(ctx context.Context, bot, invite uint) (c.InviteDeliveryStatus, error) {
	if bot == 0 || invite == 0 {
		return c.InviteDeliveryStatus{}, c.ErrConflict
	}
	var row d.Delivery
	err := s.db.WithContext(ctx).Where("bot_id = ?", bot).Where(clause.Like{Column: clause.Column{Name: "key"}, Value: fmt.Sprintf("membership-invite:%d:%%", invite)}).Order("id DESC").First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return c.InviteDeliveryStatus{State: "not_requested"}, nil
	}
	if err != nil {
		return c.InviteDeliveryStatus{}, err
	}
	state := row.Status
	switch state {
	case "pending", "retry":
		state = "queued"
	case "processing":
		state = "sending"
	case "sent":
		state = "unknown"
		if row.MessageID > 0 {
			state = "delivered"
		}
	case "failed", "unknown", "superseded":
	default:
		state = "unknown"
	}
	return c.InviteDeliveryStatus{Operation: row.Key, State: state, MessageID: row.MessageID}, nil
}

// guardInviteEnqueue serializes duplicate panel notifications under the existing Bot lock.
// Same-operation replay is idempotent; other pending/unknown deliveries cannot be replayed.
func guardInviteEnqueue(tx *gorm.DB, row *d.Delivery) error {
	if !strings.HasPrefix(row.Key, "membership-invite:") {
		return nil
	}
	parts := strings.Split(row.Key, ":")
	if len(parts) != 5 || (row.Purpose != "panel" && !(row.Purpose == "membership_proactive" && parts[2] == "proactive" && parts[4] == "ready" && row.Operation == "send")) {
		return c.ErrConflict
	}
	var blocked int64
	err := tx.Model(&d.Delivery{}).Where("bot_id = ? AND status IN ?", row.BotID, []string{"pending", "retry", "processing", "unknown"}).Where(clause.Like{Column: clause.Column{Name: "key"}, Value: "membership-invite:" + parts[1] + ":%"}).Where(clause.Neq{Column: clause.Column{Name: "key"}, Value: row.Key}).Count(&blocked).Error
	if err != nil {
		return err
	}
	if blocked != 0 {
		return c.ErrConflict
	}
	return nil
}

var _ c.DeliveryRecoveryStore = (*Store)(nil)
