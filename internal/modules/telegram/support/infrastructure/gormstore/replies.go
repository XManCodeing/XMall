package gormstore

import (
	"context"
	"errors"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// registerReply derives all linkage from the original row and proven delivery.
// It runs inside the caller's transaction, before final business acknowledgement.
func (s *Store) registerReply(tx *gorm.DB, message d.BufferedMessage, delivery uint, now time.Time) error {
	if e := s.sentProof(tx, message, delivery); e != nil {
		return e
	}
	var proof deliveryProof
	if e := tx.Table("telegram_bot_deliveries").Where("id = ?", delivery).First(&proof).Error; e != nil {
		return e
	}
	var topic d.TopicMapping
	// Historical success must still be recorded after route retirement/close.
	if e := tx.Where("bot_id = ? AND session_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ?", message.BotID, message.SessionID, message.TargetChatID, message.TargetThreadID, message.RouteVersion).First(&topic).Error; e != nil {
		return c.ErrDenied
	}
	if handled, e := s.registerAlbumReplies(tx, message, proof, topic, now); handled || e != nil {
		return e
	}
	mapping := d.ReplyMapping{BotID: message.BotID, ChatID: message.TargetChatID, MessageID: proof.MessageID, ThreadID: message.TargetThreadID, TopicID: topic.ID, SessionID: message.SessionID, BufferedMessageID: message.ID, DeliveryID: delivery, UserID: message.UserID, RouteVersion: message.RouteVersion, CreatedAt: now}
	if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mapping).Error; e != nil {
		return e
	}
	var saved d.ReplyMapping
	if e := tx.Where("bot_id = ? AND chat_id = ? AND message_id = ?", mapping.BotID, mapping.ChatID, mapping.MessageID).First(&saved).Error; e != nil {
		return e
	}
	if saved.ThreadID != mapping.ThreadID || saved.TopicID != mapping.TopicID || saved.SessionID != mapping.SessionID || saved.BufferedMessageID != message.ID || saved.DeliveryID != delivery || saved.UserID != message.UserID || saved.RouteVersion != message.RouteVersion {
		return c.ErrDenied
	}
	return releaseProof(tx, message.BotID, delivery)
}

// releaseProof clears only the runtime dependency hold on the SAME connection,
// after proof and mappings are committed atomically by the surrounding transaction.
func releaseProof(tx *gorm.DB, bot, delivery uint) error {
	return tx.Table("telegram_bot_deliveries").Where("id = ? AND bot_id = ? AND status = ?", delivery, bot, "sent").UpdateColumn("payload_held", false).Error
}

// RegisterReply is idempotent and accepts only delivered rows with sent proof.
// It is intended for repair/backfill, not accepting an inbound claimed mapping.
func (s *Store) RegisterReply(ctx context.Context, bot, id uint, generation uint64, now time.Time) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ? AND state = ?", id, bot, "delivered").First(&message).Error; e != nil {
			return c.ErrDenied
		}
		return s.registerReply(tx, message, message.DeliveryID, now)
	})
}

// ResolveNativeReply resolves registered replies or same-topic unregistered staff
// messages. The unique current open consultation owns every destination.
// A topic-only result has ID zero and must never be persisted as a ReplyMapping.
// Caller MUST still check live staff membership before forwarding.
func (s *Store) ResolveNativeReply(ctx context.Context, bot uint, update r.Update, now time.Time) (out *d.ReplyMapping, err error) {
	if !validDownstreamSource(update) {
		return nil, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		mapping, resolveErr := s.resolveDownstreamSource(tx, bot, update, now)
		if resolveErr != nil {
			return resolveErr
		}
		out = &mapping
		return nil
	})
	return
}

// validDownstreamSource validates the observed group/topic and optional reply.
// Commands are checked separately so an explicit internal note can retain them.
func validDownstreamSource(update r.Update) bool {
	if update.Private || update.ChatID >= 0 || update.UserID <= 0 || update.SenderChat != nil || update.ThreadID <= 0 || !update.IsTopicMessage || update.Type != "message" || update.MessageKind == "service" || update.TopicService != nil {
		return false
	}
	return update.Reply == nil || (update.Reply.MessageID > 0 && update.Reply.ChatID == update.ChatID && (update.Reply.ThreadID == 0 || update.Reply.ThreadID == update.ThreadID))
}

// resolveDownstreamSource returns a proven message mapping or a topic-only
// destination. Unknown replies require an explicitly matching observed topic;
// known replies from another topic are rejected even when Telegram omits thread ID.
// tx is always the caller's connection; now governs route/block validity.
func (s *Store) resolveDownstreamSource(tx *gorm.DB, bot uint, update r.Update, now time.Time) (d.ReplyMapping, error) {
	var mapping d.ReplyMapping
	if bot == 0 || !validDownstreamSource(update) {
		return mapping, c.ErrDenied
	}
	if update.Reply == nil || update.Reply.MessageID == update.ThreadID {
		return s.resolveDownstreamTopic(tx, bot, update, now)
	}
	err := tx.Where("bot_id = ? AND chat_id = ? AND message_id = ?", bot, update.ChatID, update.Reply.MessageID).First(&mapping).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if update.Reply.ThreadID != update.ThreadID {
			return mapping, c.ErrDenied
		}
		return s.resolveDownstreamTopic(tx, bot, update, now)
	}
	if err != nil {
		return mapping, err
	}
	if mapping.ThreadID != update.ThreadID {
		return mapping, c.ErrDenied
	}
	row := d.Downstream{BotID: bot, SessionID: mapping.SessionID, TopicID: mapping.TopicID, ReplyMappingID: mapping.ID, SourceChatID: mapping.ChatID, ThreadID: mapping.ThreadID, RouteVersion: mapping.RouteVersion, UserID: mapping.UserID, ChatID: mapping.UserID}
	return mapping, s.downstreamReference(tx, row, now)
}

// resolveDownstreamTopic requires exactly one physical open mapping, then reuses
// trustedTopic for session, route, recovery isolation and support-block checks.
// The returned zero-ID mapping describes routing only, never a fabricated reply.
func (s *Store) resolveDownstreamTopic(tx *gorm.DB, bot uint, update r.Update, now time.Time) (d.ReplyMapping, error) {
	var topics []d.TopicMapping
	if err := tx.Where("bot_id = ? AND chat_id = ? AND thread_id = ? AND state = ?", bot, update.ChatID, update.ThreadID, "open").Limit(2).Find(&topics).Error; err != nil {
		return d.ReplyMapping{}, err
	}
	if len(topics) != 1 {
		return d.ReplyMapping{}, c.ErrDenied
	}
	topic, err := s.trustedTopic(tx, bot, topics[0].SessionID, bindingOf(topics[0]), now)
	if err != nil {
		return d.ReplyMapping{}, err
	}
	if topic.ID != topics[0].ID {
		return d.ReplyMapping{}, c.ErrDenied
	}
	var owner d.Session
	if err = tx.Where("id = ? AND bot_id = ? AND state = ?", topic.SessionID, bot, "open").First(&owner).Error; err != nil {
		return d.ReplyMapping{}, err
	}
	if owner.UserID <= 0 || owner.ChatID != owner.UserID {
		return d.ReplyMapping{}, c.ErrDenied
	}
	return d.ReplyMapping{BotID: bot, ChatID: topic.ChatID, ThreadID: topic.ThreadID, TopicID: topic.ID, SessionID: topic.SessionID, UserID: owner.UserID, RouteVersion: topic.RouteVersion}, nil
}

var _ c.NativeReplies = (*Store)(nil)
