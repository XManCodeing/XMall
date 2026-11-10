package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"sort"
	"time"
)

// ReadyEncoder is a pure deterministic projection of the decrypted original
// consultation. It must not access DB/HTTP. Configure before concurrent use.
// The default supports plain text only; unsupported media is never flattened.
type ReadyEncoder func(r.Update, c.DeliveryBinding, string) (r.Message, error)

func (s *Store) WithReadyEncoder(encoder ReadyEncoder) *Store { s.encoder = encoder; return s }

// encodeReady reconstructs the original, authorized body, not arbitrary caller
// text. The optional adapter supplies media conversion as runtime evolves.
func (s *Store) encodeReady(source r.Update, binding c.DeliveryBinding, ref string) (r.Message, error) {
	if s.encoder != nil {
		return s.encoder(source, binding, ref)
	}
	if (source.MessageKind != "text" && source.MessageKind != "") || source.Text == "" || len(source.Media) != 0 || len(source.Entities) != 0 || source.HasProtectedContent || source.MediaGroupID != "" {
		return r.Message{}, c.ErrDenied
	}
	return r.Message{TargetType: r.TargetSupportGroup, BusinessRef: ref, ChatID: binding.ChatID, ThreadID: binding.ThreadID, RouteVersion: binding.RouteVersion, Purpose: "support_ready", Operation: "send", Text: source.Text}, nil
}

// ScanReady returns a bounded keyset page, including unresolved records for
// reconciliation. IDs, not offsets, prevent a busy consultation starving others.
func (s *Store) ScanReady(ctx context.Context, bot, after uint, limit int) ([]d.BufferedMessage, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrDenied
	}
	var rows []d.BufferedMessage
	err := s.db.WithContext(ctx).Where("NOT EXISTS (SELECT 1 FROM telegram_support_album_items ai JOIN telegram_support_albums album ON album.id = ai.album_id WHERE ai.buffered_message_id = telegram_support_messages.id AND album.leader_id <> telegram_support_messages.id)").Where("bot_id = ? AND id > ? AND state IN ?", bot, after, []string{"ready", "claimed", "sending", "unknown"}).Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// original validates persisted source ownership before decrypting its contents.
func (s *Store) original(tx *gorm.DB, message d.BufferedMessage) (r.Update, error) {
	var source r.Update
	if s.cipher == nil || message.PayloadCipher == "" {
		return source, c.ErrDenied
	}
	var session d.Session
	if e := tx.Where("id = ? AND bot_id = ? AND user_id = ? AND chat_id = ? AND state = ?", message.SessionID, message.BotID, message.UserID, message.ChatID, "open").First(&session).Error; e != nil {
		return source, c.ErrDenied
	}
	plain, e := s.cipher.Open(message.PayloadCipher)
	if e != nil || json.Unmarshal([]byte(plain), &source) != nil {
		return source, c.ErrDenied
	}
	if source.ID != message.UpdateID || source.UserID != message.UserID || source.ChatID != message.ChatID || !source.Private || source.ChatID != source.UserID || source.CallbackID != "" {
		return source, c.ErrDenied
	}
	return source, nil
}

// LoadReady derives the current binding from durable mappings. It does not grant
// delivery authorization or change the claim; PrepareDispatch rechecks it all.
func (s *Store) LoadReady(ctx context.Context, bot, id uint, generation uint64, claim string, now time.Time) (out c.PreparedReady, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		if e := tx.Where("id = ? AND bot_id = ? AND generation = ? AND claim = ? AND state = ? AND lease_until > ? AND retain_until > ?", id, bot, generation, claim, "claimed", now, now).First(&out.Message).Error; e != nil {
			return c.ErrConflict
		}
		var topics []d.TopicMapping
		if e := tx.Where("bot_id = ? AND session_id = ? AND state = ?", bot, out.Message.SessionID, "open").Order("id DESC").Limit(100).Find(&topics).Error; e != nil {
			return e
		}
		found := false
		for _, topic := range topics {
			e := s.checkRoute(tx, bot, topic.TargetID, bindingOf(topic), now)
			if errors.Is(e, c.ErrDenied) {
				continue
			}
			if e != nil {
				return e
			}
			if found {
				return c.ErrDenied
			}
			out.Binding = bindingOf(topic)
			found = true
		}
		if !found {
			return c.ErrDenied
		}
		var e error
		out.Source, e = s.original(tx, out.Message)
		if e != nil {
			return e
		}
		out.Sources, e = s.albumSources(tx, out.Message, now)
		return e
	})
	return
}

// PrepareDispatch commits exact business preparation and sending state together.
// The subsequent runtime enqueue is idempotent by stable business key; a crash
// before it is repaired by PendingDispatch, without treating enqueue as sent.
func (s *Store) PrepareDispatch(ctx context.Context, bot, id uint, generation uint64, claim string, envelope r.Message, now time.Time) (request r.DeliveryRequest, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var message d.BufferedMessage
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND generation = ? AND claim = ? AND state = ? AND lease_until > ? AND retain_until > ?", id, bot, generation, claim, "claimed", now, now).First(&message).Error; e != nil {
			return c.ErrConflict
		}
		binding := c.DeliveryBinding{ChatID: envelope.ChatID, ThreadID: envelope.ThreadID, RouteVersion: envelope.RouteVersion}
		topic, e := s.trustedTopic(tx, bot, message.SessionID, binding, now)
		if e != nil {
			return e
		}
		source, e := s.original(tx, message)
		if e != nil {
			return e
		}
		expected, e := s.expectedReady(tx, message, source, binding, now)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(expected, envelope) || !validEnvelope(message, binding, envelope) {
			return c.ErrDenied
		}
		raw, e := json.Marshal(envelope)
		if e != nil {
			return e
		}
		encrypted, e := s.cipher.Seal(string(raw))
		if e != nil {
			return e
		}
		dispatch := d.ReadyDispatch{BotID: bot, MessageID: id, TopicID: topic.ID, PayloadCipher: encrypted, Version: 1, CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&dispatch).Error; e != nil {
			return e
		}
		message.State = "sending"
		message.TargetChatID = binding.ChatID
		message.TargetThreadID = binding.ThreadID
		message.RouteVersion = binding.RouteVersion
		message.UpdatedAt = now
		if e = tx.Save(&message).Error; e != nil {
			return e
		}
		if e = prepareAlbumFollowers(tx, message, now); e != nil {
			return e
		}
		request = dispatchRequest(message, dispatch, now)
		return nil
	})
	return
}

// validEnvelope constrains every destination and purpose independently of the
// pluggable content encoder. No customer-private target or alternate operation.
func validEnvelope(message d.BufferedMessage, binding c.DeliveryBinding, envelope r.Message) bool {
	return c.ValidTransferEnvelope(envelope) && envelope.TargetType == r.TargetSupportGroup && envelope.BusinessRef == c.ReadyBusinessRef(message.SessionID, message.ID) && envelope.Purpose == "support_ready" && envelope.Operation == "send" && envelope.ChatID == binding.ChatID && envelope.ThreadID == binding.ThreadID && envelope.RouteVersion == binding.RouteVersion && binding.ChatID < 0 && binding.ThreadID > 0 && binding.RouteVersion > 0
}
func dispatchRequest(message d.BufferedMessage, dispatch d.ReadyDispatch, now time.Time) r.DeliveryRequest {
	return r.DeliveryRequest{BotID: message.BotID, Key: c.ReadyBusinessRef(message.SessionID, message.ID), ChatID: message.TargetChatID, TelegramUserID: message.UserID, PayloadCipher: dispatch.PayloadCipher, TargetType: string(r.TargetSupportGroup), Priority: r.PrioritySupport, Purpose: "support_ready", Operation: "send", Status: "pending", AvailableAt: now}
}

// PendingDispatch may recover the pre-enqueue crash only when there is still no
// runtime delivery. Existing failed/unknown runtime attempts are never replaced.
func (s *Store) PendingDispatch(ctx context.Context, bot, id uint, generation uint64, now time.Time) (request *r.DeliveryRequest, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ? AND state = ?", id, bot, "sending").First(&message).Error; e != nil {
			return c.ErrConflict
		}
		var dispatch d.ReadyDispatch
		if e := tx.Where("message_id = ? AND bot_id = ?", id, bot).First(&dispatch).Error; e != nil {
			return c.ErrDenied
		}
		var count int64
		if e := countReadyDelivery(tx, bot, c.ReadyBusinessRef(message.SessionID, id), &count); e != nil {
			return e
		}
		if count != 0 {
			return nil
		}
		if _, e := s.trustedTopic(tx, bot, message.SessionID, messageBinding(message), now); e != nil {
			return e
		}
		if _, e := s.original(tx, message); e != nil {
			return e
		}
		value := dispatchRequest(message, dispatch, now)
		request = &value
		return nil
	})
	return
}
func messageBinding(message d.BufferedMessage) c.DeliveryBinding {
	return c.DeliveryBinding{ChatID: message.TargetChatID, ThreadID: message.TargetThreadID, RouteVersion: message.RouteVersion}
}

var _ c.Dispatches = (*Store)(nil)

// RejectReady revalidates claimed source content before producing durable feedback.
// It never touches prepared/unknown attempts; an album is rejected atomically.
func (s *Store) RejectReady(ctx context.Context, bot, id uint, generation uint64, claim string, now time.Time) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := runtimeFence(tx, bot, generation); err != nil {
			return err
		}
		var leader d.BufferedMessage
		if err := tx.Where("bot_id = ? AND id = ? AND state = ? AND generation = ? AND claim = ? AND lease_until > ?", bot, id, "claimed", generation, claim, now).First(&leader).Error; err != nil {
			return c.ErrConflict
		}
		ids, err := expandSupportCleanupPage(tx.Model(&d.BufferedMessage{}), []uint{id})
		if err != nil {
			return err
		}
		var rows []d.BufferedMessage
		if err = tx.Where("id IN ? AND bot_id = ? AND session_id = ?", ids, bot, leader.SessionID).Find(&rows).Error; err != nil {
			return err
		}
		reason := ""
		sources := make([]r.Update, 0, len(rows))
		for _, row := range rows {
			if row.State != "ready" && row.ID != leader.ID {
				return c.ErrDenied
			}
			var prepared int64
			if err = tx.Model(&d.ReadyDispatch{}).Where("message_id = ?", row.ID).Count(&prepared).Error; err != nil {
				return err
			}
			if prepared != 0 || row.DeliveryID != 0 {
				return fmt.Errorf("rejected source has prepared dispatch: %w", c.ErrDenied)
			}
			if err = countReadyDelivery(tx, bot, c.ReadyBusinessRef(row.SessionID, row.ID), &prepared); err != nil {
				return err
			}
			if prepared != 0 {
				return fmt.Errorf("rejected source has runtime attempt: %w", c.ErrDenied)
			}
			source, err := s.original(tx, row)
			if err != nil {
				return err
			}
			sources = append(sources, source)
			if rejected := c.ContentRejection(source); rejected != "" {
				reason = rejected
			}
		}
		if reason == "" && len(sources) > 0 && sources[0].MediaGroupID != "" {
			sort.Slice(sources, func(left, right int) bool { return sources[left].MessageID < sources[right].MessageID })
			if _, err := c.AlbumContents(sources); errors.Is(err, r.ErrInvalidTransfer) {
				reason = "content_unsupported"
			}
		}
		if reason == "" {
			return c.ErrDenied
		}
		return tx.Model(&d.BufferedMessage{}).Where("id IN ?", ids).Updates(map[string]any{"state": "rejected", "reason": reason, "payload_cipher": "", "feedback_pending": true, "claim": "", "lease_until": nil, "updated_at": now}).Error
	})
}

// countReadyDelivery quotes the reserved key column through GORM's dialect.
// tx is the active transaction; count receives matching durable runtime attempts.
func countReadyDelivery(tx *gorm.DB, bot uint, key string, count *int64) error {
	return tx.Table("telegram_bot_deliveries").Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Count(count).Error
}

// collectAlbum registers sources during Accept's existing session transaction.
// Window=2s, <=10 items, <=512KiB encrypted-input accounting. No external I/O.
func collectAlbum(tx *gorm.DB, message *d.BufferedMessage, source r.Update, now time.Time) error {
	if source.MediaGroupID == "" || (message.State != "ready" && message.State != "buffered") {
		return nil
	}
	if len(source.MediaGroupID) > 128 {
		return c.ErrDenied
	}
	var album d.Album
	err := tx.Where("bot_id = ? AND chat_id = ? AND media_group_id = ?", message.BotID, source.ChatID, source.MediaGroupID).First(&album).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		album = d.Album{BotID: message.BotID, ChatID: source.ChatID, MediaGroupID: source.MediaGroupID, SessionID: message.SessionID, LeaderID: message.ID, State: "collecting", ClosesAt: now.Add(2 * time.Second), CreatedAt: now, UpdatedAt: now}
		if err = tx.Create(&album).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if album.SessionID != message.SessionID || album.State != "collecting" || !now.Before(album.ClosesAt) || album.Count >= 10 || album.Bytes+message.Bytes > 512*1024 {
		message.State = "rejected"
		message.Reason = "album_late_or_limit"
		message.PayloadCipher = ""
		message.FeedbackPending = true
		return tx.Save(message).Error
	}
	if source.MessageID <= 0 {
		return c.ErrDenied
	}
	if err = tx.Create(&d.AlbumItem{AlbumID: album.ID, BufferedMessageID: message.ID, SourceMessageID: source.MessageID, CreatedAt: now}).Error; err != nil {
		return err
	}
	return tx.Model(&album).Updates(map[string]any{"count": album.Count + 1, "bytes": album.Bytes + message.Bytes, "updated_at": now}).Error
}

// albumSources reconstructs every original under one transaction. Followers may
// never be independently scheduled; leader remains the ordered session blocker.
func (s *Store) albumSources(tx *gorm.DB, message d.BufferedMessage, now time.Time) ([]r.Update, error) {
	var item d.AlbumItem
	e := tx.Where("buffered_message_id = ?", message.ID).First(&item).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var album d.Album
	if e = tx.First(&album, item.AlbumID).Error; e != nil {
		return nil, e
	}
	if album.LeaderID != message.ID || now.Before(album.ClosesAt) || album.Count < 1 || album.Count > 10 {
		return nil, c.ErrConflict
	}
	var items []d.AlbumItem
	if e = tx.Where("album_id = ?", album.ID).Order("source_message_id ASC").Find(&items).Error; e != nil {
		return nil, e
	}
	if len(items) != album.Count {
		return nil, c.ErrDenied
	}
	sources := make([]r.Update, 0, len(items))
	for _, part := range items {
		var original d.BufferedMessage
		if e = tx.Where("id = ? AND bot_id = ? AND session_id = ? AND state IN ?", part.BufferedMessageID, message.BotID, message.SessionID, []string{"ready", "claimed", "sending", "unknown"}).First(&original).Error; e != nil {
			return nil, c.ErrDenied
		}
		source, e := s.original(tx, original)
		if e != nil {
			return nil, e
		}
		if source.MediaGroupID != album.MediaGroupID || source.ChatID != album.ChatID || source.MessageID != part.SourceMessageID {
			return nil, c.ErrDenied
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// expectedReady deterministically rebuilds either one source or a bounded album.
func (s *Store) expectedReady(tx *gorm.DB, message d.BufferedMessage, source r.Update, binding c.DeliveryBinding, now time.Time) (r.Message, error) {
	sources, e := s.albumSources(tx, message, now)
	if e != nil {
		return r.Message{}, e
	}
	var expected r.Message
	if len(sources) == 0 {
		expected, e = s.encodeReady(source, binding, c.ReadyBusinessRef(message.SessionID, message.ID))
	} else {
		sort.Slice(sources, func(left, right int) bool { return sources[left].MessageID < sources[right].MessageID })
		expected, e = c.EncodeReadyAlbum(sources, binding, c.ReadyBusinessRef(message.SessionID, message.ID))
	}
	if e != nil {
		return r.Message{}, e
	}
	return s.readyDispatchVersion(tx, message, expected)
}

// readyDispatchVersion preserves already-prepared legacy typed sends without
// trusting supplied source IDs. Only a durable encrypted dispatch opts out of
// forwarding; the remaining body is still compared with original updates.
func (s *Store) readyDispatchVersion(tx *gorm.DB, message d.BufferedMessage, expected r.Message) (r.Message, error) {
	var dispatch d.ReadyDispatch
	err := tx.Where("bot_id = ? AND message_id = ?", message.BotID, message.ID).First(&dispatch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return expected, nil
	}
	if err != nil {
		return r.Message{}, err
	}
	plain, err := s.cipher.Open(dispatch.PayloadCipher)
	var saved r.Message
	if err != nil || json.Unmarshal([]byte(plain), &saved) != nil {
		return r.Message{}, c.ErrDenied
	}
	if expected.Transfer != nil && saved.Transfer != nil && saved.Transfer.SourceChatID == 0 && len(saved.Transfer.SourceMessageIDs) == 0 {
		expected.Transfer.SourceChatID, expected.Transfer.SourceMessageIDs = 0, nil
	}
	return expected, nil
}

// prepareAlbumFollowers pins every original to the leader's immutable target.
// No follower owns another runtime delivery; one album equals one attempt.
func prepareAlbumFollowers(tx *gorm.DB, leader d.BufferedMessage, now time.Time) error {
	var album d.Album
	e := tx.Where("leader_id = ? AND bot_id = ?", leader.ID, leader.BotID).First(&album).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	var items []d.AlbumItem
	if e = tx.Where("album_id = ?", album.ID).Find(&items).Error; e != nil {
		return e
	}
	for _, item := range items {
		if item.BufferedMessageID == leader.ID {
			continue
		}
		result := tx.Model(&d.BufferedMessage{}).Where("id = ? AND state = ? AND bot_id = ? AND session_id = ?", item.BufferedMessageID, "ready", leader.BotID, leader.SessionID).Updates(map[string]any{"state": "sending", "target_chat_id": leader.TargetChatID, "target_thread_id": leader.TargetThreadID, "route_version": leader.RouteVersion, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
	}
	return tx.Model(&album).Updates(map[string]any{"state": "prepared", "updated_at": now}).Error
}

// registerAlbumReplies validates the complete ordered receipt before registering
// any item. Partial observations never create trusted reply mappings or release.
func (s *Store) registerAlbumReplies(tx *gorm.DB, leader d.BufferedMessage, proof deliveryProof, topic d.TopicMapping, now time.Time) (bool, error) {
	var album d.Album
	e := tx.Where("leader_id = ? AND bot_id = ?", leader.ID, leader.BotID).First(&album).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	var items []d.AlbumItem
	if e = tx.Where("album_id = ?", album.ID).Order("source_message_id ASC").Find(&items).Error; e != nil {
		return true, e
	}
	if len(items) != album.Count || len(proof.MessageIDs) != len(items) {
		return true, c.ErrDenied
	}
	for index, item := range items {
		destination := proof.MessageIDs[index]
		mapping := d.ReplyMapping{BotID: leader.BotID, ChatID: topic.ChatID, MessageID: destination, ThreadID: topic.ThreadID, TopicID: topic.ID, SessionID: leader.SessionID, BufferedMessageID: item.BufferedMessageID, DeliveryID: proof.ID, UserID: leader.UserID, RouteVersion: topic.RouteVersion, CreatedAt: now}
		if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mapping).Error; e != nil {
			return true, e
		}
		var saved d.ReplyMapping
		if e = tx.Where("bot_id = ? AND chat_id = ? AND message_id = ?", leader.BotID, topic.ChatID, destination).First(&saved).Error; e != nil {
			return true, e
		}
		if saved.BufferedMessageID != item.BufferedMessageID || saved.DeliveryID != proof.ID || saved.TopicID != topic.ID || saved.SessionID != leader.SessionID || saved.UserID != leader.UserID {
			return true, c.ErrDenied
		}
		if e = tx.Model(&item).Update("destination_message_id", destination).Error; e != nil {
			return true, e
		}
		if item.BufferedMessageID != leader.ID {
			if e = tx.Model(&d.BufferedMessage{}).Where("id = ? AND bot_id = ? AND session_id = ?", item.BufferedMessageID, leader.BotID, leader.SessionID).Updates(map[string]any{"state": "delivered", "delivery_id": proof.ID, "updated_at": now}).Error; e != nil {
				return true, e
			}
		}
	}
	if e = tx.Model(&album).Updates(map[string]any{"state": "delivered", "updated_at": now}).Error; e != nil {
		return true, e
	}
	return true, releaseProof(tx, leader.BotID, proof.ID)
}
