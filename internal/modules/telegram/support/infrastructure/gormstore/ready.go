package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"time"
)

// Lock the authoritative runtime row first, using the same transaction/connection.
// This no-op write serializes SQLite and row-locks PostgreSQL/MySQL against restart.
func runtimeFence(tx *gorm.DB, bot uint, generation uint64) error {
	if bot == 0 || generation == 0 {
		return c.ErrDenied
	}
	q := tx.Table("telegram_bot_instances").Where("id = ? AND generation = ? AND enabled = ? AND mode = ? AND state = ? AND recovery_task_id = 0", bot, generation, true, "internal", "running").UpdateColumn("generation", gorm.Expr("generation"))
	if q.Error != nil {
		return q.Error
	}
	// MySQL may report zero changed rows for a no-op write; verify the locked
	// predicate instead of interpreting RowsAffected as matched rows.
	var count int64
	if e := tx.Table("telegram_bot_instances").Where("id = ? AND generation = ? AND enabled = ? AND mode = ? AND state = ? AND recovery_task_id = 0", bot, generation, true, "internal", "running").Count(&count).Error; e != nil {
		return e
	}
	if count != 1 {
		return c.ErrConflict
	}
	return nil
}
func (s *Store) ClaimReady(ctx context.Context, bot, session uint, generation uint64, claim string, now time.Time, ttl time.Duration) (row *d.BufferedMessage, err error) {
	if claim == "" || len(claim) > 64 || ttl < time.Second || ttl > 5*time.Minute {
		return nil, c.ErrDenied
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		lock := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ?", session, bot).Updates(map[string]any{"version": gorm.Expr("version + 1"), "consumer_generation": generation})
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return c.ErrDenied
		}
		var m d.BufferedMessage
		e := tx.Where("bot_id = ? AND session_id = ? AND legacy_task_id = 0 AND state IN ?", bot, session, []string{"buffered", "ready", "claimed", "sending", "unknown", "failed"}).Order("sequence ASC").First(&m).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		var albumItem d.AlbumItem
		albumErr := tx.Where("buffered_message_id = ?", m.ID).First(&albumItem).Error
		if albumErr == nil {
			var album d.Album
			if e := tx.First(&album, albumItem.AlbumID).Error; e != nil {
				return e
			}
			if album.LeaderID != m.ID || now.Before(album.ClosesAt) {
				return nil
			}
		} else if !errors.Is(albumErr, gorm.ErrRecordNotFound) {
			return albumErr
		}
		if m.State == "sending" && m.LeaseUntil != nil && !now.Before(*m.LeaseUntil) {
			var prepared int64
			if e := tx.Model(&d.ReadyDispatch{}).Where("message_id = ? AND bot_id = ?", m.ID, bot).Count(&prepared).Error; e != nil {
				return e
			}
			// Runtime owns the prepared attempt's lease. Business lease expiry
			// cannot invalidate an asynchronously queued, otherwise valid send.
			if prepared == 1 {
				return nil
			}
			return tx.Model(&m).Updates(map[string]any{"state": "unknown", "error_class": "lease_expired"}).Error
		}
		if m.State == "unknown" || m.State == "sending" || m.State == "buffered" || m.State == "failed" {
			return nil
		}
		if m.State == "claimed" && (m.LeaseUntil == nil || now.Before(*m.LeaseUntil)) {
			return nil
		}
		if !now.Before(m.RetainUntil) {
			return tx.Model(&m).Updates(map[string]any{"state": "expired", "reason": "expired", "payload_cipher": "", "feedback_pending": true}).Error
		}
		until := now.Add(ttl)
		m.State = "claimed"
		m.Claim = claim
		m.Generation = generation
		m.LeaseUntil = &until
		if e = tx.Save(&m).Error; e != nil {
			return e
		}
		row = &m
		return nil
	})
	return
}
func (s *Store) BeginSend(ctx context.Context, bot, id uint, generation uint64, claim string, target c.DeliveryBinding, now time.Time) error {
	if target.ChatID >= 0 || target.ThreadID <= 0 || target.RouteVersion == 0 {
		return c.ErrDenied
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ?", id, bot).First(&message).Error; e != nil {
			return c.ErrDenied
		}
		if _, e := s.trustedTopic(tx, bot, message.SessionID, target, now); e != nil {
			return e
		}
		result := tx.Model(&d.BufferedMessage{}).Where("id = ? AND bot_id = ? AND generation = ? AND claim = ? AND state = ? AND lease_until > ? AND retain_until > ?", id, bot, generation, claim, "claimed", now, now).Updates(map[string]any{"state": "sending", "target_chat_id": target.ChatID, "target_thread_id": target.ThreadID, "route_version": target.RouteVersion})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
}

// deliveryProof is a read-only projection of the existing runtime table. The
// encrypted envelope is authoritative for business ref and destination mapping.
type deliveryProof struct {
	ID, BotID                                                  uint
	Key, Status, TargetType, Purpose, Operation, PayloadCipher string
	ChatID, TelegramUserID, MessageID                          int64
	BroadcastID, UserID                                        uint
	PanelEpoch, Claim, LastError                               string
	PanelRevision                                              uint64
	TargetMessageID, PreviousMessageID                         int64
	ClaimedUntil                                               *time.Time
	MessageIDs                                                 []int64 `gorm:"serializer:json;type:text"`
}

func (s *Store) sentProof(tx *gorm.DB, m d.BufferedMessage, delivery uint) error {
	if delivery == 0 || s.cipher == nil {
		return c.ErrDenied
	}
	var proof deliveryProof
	if e := tx.Table("telegram_bot_deliveries").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", delivery).Take(&proof).Error; e != nil {
		return c.ErrDenied
	}
	if proof.Status != "sent" || proof.MessageID <= 0 {
		return c.ErrDenied
	}
	return s.envelopeProof(tx, m, proof)
}

// envelopeProof validates the immutable runtime destination and encrypted
// business envelope on the same transaction, without assuming transport success.
func (s *Store) envelopeProof(tx *gorm.DB, m d.BufferedMessage, proof deliveryProof) error {
	ref := c.ReadyBusinessRef(m.SessionID, m.ID)
	if !supportProofSource(proof) || s.cipher == nil || proof.BotID != m.BotID || proof.Key != ref || proof.TargetType != string(r.TargetSupportGroup) || proof.Purpose != "support_ready" || proof.Operation != "send" || proof.ChatID != m.TargetChatID || proof.TelegramUserID != m.UserID {
		return c.ErrDenied
	}
	plain, e := s.cipher.Open(proof.PayloadCipher)
	if e != nil {
		return c.ErrDenied
	}
	var message r.Message
	if json.Unmarshal([]byte(plain), &message) != nil {
		return c.ErrDenied
	}
	if !c.ValidTransferEnvelope(message) {
		return c.ErrDenied
	}
	if message.Transfer != nil && proof.Status == "sent" {
		if len(proof.MessageIDs) != len(message.Transfer.Contents) || len(proof.MessageIDs) == 0 || proof.MessageID != proof.MessageIDs[0] {
			return c.ErrDenied
		}
		seen := map[int64]bool{}
		for _, id := range proof.MessageIDs {
			if id <= 0 || seen[id] {
				return c.ErrDenied
			}
			seen[id] = true
		}
	}
	if message.BusinessRef != ref || message.TargetType != r.TargetSupportGroup || message.Purpose != "support_ready" || message.Operation != "send" || message.ChatID != m.TargetChatID || message.ThreadID != m.TargetThreadID || message.RouteVersion != m.RouteVersion {
		return c.ErrDenied
	}
	var dispatch d.ReadyDispatch
	e = tx.Where("bot_id = ? AND message_id = ?", m.BotID, m.ID).First(&dispatch).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	if e == nil {
		saved, openErr := s.cipher.Open(dispatch.PayloadCipher)
		var expected r.Message
		if openErr != nil || json.Unmarshal([]byte(saved), &expected) != nil || !reflect.DeepEqual(expected, message) {
			return c.ErrDenied
		}
	}
	return nil
}
func (s *Store) AckReady(ctx context.Context, bot, id uint, generation uint64, claim, state string, delivery uint, now time.Time) error {
	if state != "delivered" && state != "retry" && state != "failed" && state != "unknown" {
		return c.ErrDenied
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var m d.BufferedMessage
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND generation = ? AND claim = ? AND state = ? AND lease_until > ?", id, bot, generation, claim, "sending", now).First(&m).Error; e != nil {
			return c.ErrConflict
		}
		if state == "delivered" {
			if e := s.registerReply(tx, m, delivery, now); e != nil {
				return e
			}
		}
		var albumCount int64
		if e := tx.Model(&d.Album{}).Where("leader_id = ? AND bot_id = ?", m.ID, bot).Count(&albumCount).Error; e != nil {
			return e
		}
		if albumCount > 0 && state == "failed" {
			return c.ErrDenied
		} // Keep followers blocked until explicit album resolution.
		if state == "retry" || state == "failed" {
			var proof deliveryProof
			if e := tx.Table("telegram_bot_deliveries").Where("id = ? AND status = ? AND message_id = 0", delivery, "failed").First(&proof).Error; e != nil {
				return c.ErrDenied
			}
			if proof.MessageID != 0 || len(proof.MessageIDs) != 0 {
				return c.ErrDenied
			}
			if e := s.envelopeProof(tx, m, proof); e != nil {
				return e
			}
		}
		next := state
		if state == "retry" {
			// Keep the existing runtime delivery and dispatch: only its explicit
			// retry can resend, never manufacture a second queue entry.
			next = "sending"
		}
		return tx.Model(&m).Updates(map[string]any{"state": next, "delivery_id": delivery, "claim": "", "lease_until": nil, "updated_at": now}).Error
	})
}

var _ c.ReadyConsumer = (*Store)(nil)
