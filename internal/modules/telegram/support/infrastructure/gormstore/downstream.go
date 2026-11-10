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
	"strconv"
	"strings"
	"time"
)

// AcceptDownstream encrypts and deduplicates a validated native reply. Member
// HTTP checks belong to ReplyService, never to this transaction-bound store.
func (s *Store) AcceptDownstream(ctx context.Context, bot uint, source r.Update, note bool, now time.Time) (row *d.Downstream, err error) {
	if bot == 0 || source.ID <= 0 || source.MessageID <= 0 || !validDownstreamSource(source) || (!note && c.DownstreamCommand(source)) || len(source.MediaGroupID) > 128 || s.cipher == nil {
		return nil, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		// Lock the consultation before looking up source replay; all contention uses
		// the same writer connection on SQLite and the same row lock on PostgreSQL.
		mapping, resolveErr := s.resolveDownstreamSource(tx, bot, source, now)
		if resolveErr != nil {
			return resolveErr
		}
		if e := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ?", mapping.SessionID, bot).UpdateColumn("version", gorm.Expr("version + 1")).Error; e != nil {
			return e
		}
		kind := "reply"
		if note {
			kind = "note"
		}
		existing := d.Downstream{}
		e := tx.Where("bot_id = ? AND update_id = ?", bot, source.ID).First(&existing).Error
		if e == nil {
			if existing.Kind != kind || existing.ActorID != source.UserID || existing.SourceMessageID != source.MessageID || existing.ReplyMappingID != mapping.ID || existing.SessionID != mapping.SessionID || existing.TopicID != mapping.TopicID || existing.UserID != mapping.UserID || existing.RouteVersion != mapping.RouteVersion {
				return c.ErrDenied
			}
			plain, openErr := s.cipher.Open(existing.SourceCipher)
			var saved r.Update
			if openErr != nil || json.Unmarshal([]byte(plain), &saved) != nil || !reflect.DeepEqual(saved, source) {
				return c.ErrDenied
			}
			row = &existing
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		candidate := d.Downstream{BotID: bot, UpdateID: source.ID, SessionID: mapping.SessionID, TopicID: mapping.TopicID, ReplyMappingID: mapping.ID, ActorID: source.UserID, SourceChatID: source.ChatID, SourceMessageID: source.MessageID, ThreadID: source.ThreadID, RouteVersion: mapping.RouteVersion, UserID: mapping.UserID, ChatID: mapping.UserID, Kind: kind, State: "pending", Version: 1, CreatedAt: now, UpdatedAt: now}
		if note {
			candidate.State = "internal"
		}
		if e = s.downstreamReference(tx, candidate, now); e != nil {
			return e
		}
		raw, e := json.Marshal(source)
		if e != nil {
			return e
		}
		if len(raw) > 512*1024 {
			return c.ErrDenied
		}
		candidate.SourceBytes = int64(len(raw))
		candidate.SourceCipher, e = s.cipher.Seal(string(raw))
		if e != nil {
			return e
		}
		if e = tx.Create(&candidate).Error; e != nil {
			return e
		}
		if !note && source.MediaGroupID != "" && c.ContentRejection(source) == "" {
			if e = collectDownstreamAlbum(tx, &candidate, source, now); e != nil {
				return e
			}
		} else if !note {
			envelope, e := downstreamEnvelope(candidate, source)
			if errors.Is(e, r.ErrInvalidTransfer) || c.ContentRejection(source) != "" {
				candidate.State = "rejected"
				envelope = rejectedReplyEnvelope(candidate, source)
			} else if e != nil {
				return e
			}
			raw, e = json.Marshal(envelope)
			if e != nil {
				return e
			}
			candidate.EnvelopeCipher, e = s.cipher.Seal(string(raw))
			if e != nil {
				return e
			}
			if e = tx.Save(&candidate).Error; e != nil {
				return e
			}
		}
		row = &candidate
		return nil
	})
	return
}

// downstreamReference validates every persisted link, including customer identity
// and the exact original delivery, without granting authority to supplied text.
func (s *Store) downstreamReference(tx *gorm.DB, row d.Downstream, now time.Time) error {
	topic, e := s.trustedTopic(tx, row.BotID, row.SessionID, c.DeliveryBinding{ChatID: row.SourceChatID, ThreadID: row.ThreadID, RouteVersion: row.RouteVersion}, now)
	if e != nil {
		return e
	}
	if topic.ID != row.TopicID {
		return c.ErrDenied
	}
	if row.ReplyMappingID == 0 {
		mapping, err := s.resolveDownstreamTopic(tx, row.BotID, r.Update{ChatID: row.SourceChatID, ThreadID: row.ThreadID}, now)
		if err != nil || mapping.TopicID != row.TopicID || mapping.SessionID != row.SessionID || mapping.UserID != row.UserID || row.ChatID != row.UserID {
			return c.ErrDenied
		}
		return nil
	}
	var mapping d.ReplyMapping
	if e = tx.Where("id = ? AND bot_id = ? AND session_id = ? AND topic_id = ? AND user_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ?", row.ReplyMappingID, row.BotID, row.SessionID, row.TopicID, row.UserID, row.SourceChatID, row.ThreadID, row.RouteVersion).First(&mapping).Error; e != nil {
		return c.ErrDenied
	}
	var original d.BufferedMessage
	if e = tx.Where("id = ? AND bot_id = ? AND session_id = ? AND user_id = ? AND state = ? AND delivery_id = ?", mapping.BufferedMessageID, row.BotID, row.SessionID, row.UserID, "delivered", mapping.DeliveryID).First(&original).Error; e != nil {
		return c.ErrDenied
	}
	if original.ChatID != row.ChatID || messageBinding(original) != bindingOf(topic) {
		return c.ErrDenied
	}
	return s.checkSupportBlock(tx, row.BotID, row.UserID, now)
}

// downstreamEnvelope is a deterministic projection, with no untrusted private
// destination or reply ID. Group reply IDs are never reused in the customer chat.
func downstreamEnvelope(row d.Downstream, source r.Update) (r.Message, error) {
	if source.ID != row.UpdateID || source.UserID != row.ActorID || source.ChatID != row.SourceChatID || source.MessageID != row.SourceMessageID || source.ThreadID != row.ThreadID || source.MediaGroupID != "" {
		return r.Message{}, c.ErrDenied
	}
	content, err := r.TransferContentFromUpdate(source)
	if err != nil || content.HasProtectedContent {
		return r.Message{}, r.ErrInvalidTransfer
	}
	return c.TransferEnvelope(r.TargetSupportPrivate, c.DownstreamBusinessRef(row.ID), "support_reply", row.ChatID, 0, row.RouteVersion, 0, []r.TransferContent{content}), nil
}

// CheckDownstream validates durable body and source but does NOT call membership
// HTTP. Only ReplyService.AuthorizeTarget is a complete outbound authorizer.
func (s *Store) CheckDownstream(ctx context.Context, bot uint, envelope r.Message, now time.Time) (out *d.Downstream, err error) {
	ref := strings.TrimPrefix(envelope.BusinessRef, "support:reply:")
	parsed, e := strconv.ParseUint(ref, 10, strconv.IntSize)
	if e != nil || parsed == 0 || c.DownstreamBusinessRef(uint(parsed)) != envelope.BusinessRef || !c.ValidTransferEnvelope(envelope) || envelope.Transfer == nil || s.cipher == nil {
		return nil, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var row d.Downstream
		if e := tx.Where("id = ? AND bot_id = ? AND state = ? AND kind = ?", uint(parsed), bot, "pending", "reply").First(&row).Error; e != nil {
			return c.ErrDenied
		}
		if e := s.downstreamReference(tx, row, now); e != nil {
			return e
		}
		plain, e := s.cipher.Open(row.SourceCipher)
		var source r.Update
		if e != nil || json.Unmarshal([]byte(plain), &source) != nil {
			return c.ErrDenied
		}
		expected, e := s.expectedDownstream(tx, row, now)
		if e != nil || !reflect.DeepEqual(expected, envelope) {
			return c.ErrDenied
		}
		plain, e = s.cipher.Open(row.EnvelopeCipher)
		var saved r.Message
		if e != nil || json.Unmarshal([]byte(plain), &saved) != nil || !reflect.DeepEqual(saved, envelope) {
			return c.ErrDenied
		}
		out = &row
		return nil
	})
	return
}

// DownstreamRequest repairs only missing runtime rows; existing attempts and
// unknown outcomes remain owned by runtime and are never replaced here.
func (s *Store) DownstreamRequest(ctx context.Context, bot, id uint, now time.Time) (out *r.DeliveryRequest, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var row d.Downstream
		if e := tx.Where("id = ? AND bot_id = ? AND kind = ? AND state IN ?", id, bot, "reply", []string{"pending", "collecting", "album_item", "rejected"}).First(&row).Error; e != nil {
			return c.ErrDenied
		}
		if row.State == "album_item" {
			return nil
		}
		if row.State == "rejected" {
			if row.EnvelopeCipher == "" {
				return nil
			}
			out, err = s.rejectedReplyRequest(tx, row, now)
			return err
		}
		if row.AlbumID != 0 {
			ready, e := s.freezeDownstreamAlbum(tx, &row, now)
			if e != nil || !ready {
				return e
			}
		}
		if e := s.downstreamReference(tx, row, now); e != nil {
			return e
		}
		var count int64
		if e := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.DownstreamBusinessRef(id)}).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			return nil
		}
		out = &r.DeliveryRequest{BotID: bot, Key: c.DownstreamBusinessRef(id), ChatID: row.ChatID, TelegramUserID: row.UserID, PayloadCipher: row.EnvelopeCipher, TargetType: string(r.TargetSupportPrivate), Priority: r.PrioritySupport, Purpose: "support_reply", Operation: "send", Status: "pending", AvailableAt: now}
		return nil
	})
	return
}

// ScanDownstream uses a bounded ID cursor, including unknown for sent recovery.
func (s *Store) ScanDownstream(ctx context.Context, bot, after uint, limit int) ([]d.Downstream, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrDenied
	}
	var rows []d.Downstream
	e := s.db.WithContext(ctx).Where("bot_id = ? AND id > ? AND kind = ? AND state IN ?", bot, after, "reply", []string{"pending", "unknown", "collecting", "rejected"}).Where("state <> ? OR (delivery_id = 0 AND envelope_cipher <> '')", "rejected").Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, e
}

// ReconcileDownstream requires the real current generation and exact sent
// receipt. Proof dependency release shares the final acknowledgement transaction.
func (s *Store) ReconcileDownstream(ctx context.Context, bot, id uint, generation uint64, now time.Time) (state string, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var row d.Downstream
		if e := tx.Where("id = ? AND bot_id = ? AND kind = ?", id, bot, "reply").First(&row).Error; e != nil {
			return c.ErrDenied
		}
		state = row.State
		if state == "rejected" {
			var proof deliveryProof
			err := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: fmt.Sprintf("support:reply-rejected:%d", id)}).First(&proof).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			return tx.Model(&row).Update("delivery_id", proof.ID).Error
		}
		if state == "delivered" {
			return nil
		}
		if state == "collecting" {
			return nil
		}
		if state != "pending" && state != "unknown" && state != "failed" {
			return c.ErrConflict
		}
		var proof deliveryProof
		e := tx.Table("telegram_bot_deliveries").Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.DownstreamBusinessRef(id)}).First(&proof).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if e = s.downstreamProof(row, proof); e != nil {
			return e
		}
		if proof.Status == "sent" {
			if e = s.finishDownstreamReceipt(tx, row, proof, now); e != nil {
				return e
			}
			state = "delivered"
		} else if proof.Status == "unknown" {
			state = "unknown"
		} else if proof.Status == "failed" {
			state = "failed"
		}
		if state == row.State && row.DeliveryID == proof.ID {
			return nil
		}
		return tx.Model(&row).Updates(map[string]any{"state": state, "delivery_id": proof.ID, "version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
	return
}

// downstreamProof matches the exact encrypted persisted reply and customer.
func (s *Store) downstreamProof(row d.Downstream, proof deliveryProof) error {
	if !supportProofSource(proof) || s.cipher == nil || proof.BotID != row.BotID || proof.Key != c.DownstreamBusinessRef(row.ID) || proof.ChatID != row.ChatID || proof.TelegramUserID != row.UserID || proof.TargetType != string(r.TargetSupportPrivate) || proof.Purpose != "support_reply" || proof.Operation != "send" {
		return c.ErrDenied
	}
	actual, e := s.cipher.Open(proof.PayloadCipher)
	if e != nil {
		return c.ErrDenied
	}
	expected, e := s.cipher.Open(row.EnvelopeCipher)
	if e != nil || expected != actual {
		return c.ErrDenied
	}
	return nil
}

var _ c.Downstreams = (*Store)(nil)

// collectDownstreamAlbum records a bounded album after each item's native reply
// has been verified. Same actor/session/topic are mandatory, avoiding mixed owners.
func collectDownstreamAlbum(tx *gorm.DB, row *d.Downstream, source r.Update, now time.Time) error {
	content, e := r.TransferContentFromUpdate(source)
	if e != nil || content.HasProtectedContent {
		return c.ErrDenied
	}
	switch content.Kind {
	case "photo", "video", "audio", "document":
	default:
		return c.ErrDenied
	}
	var album d.DownstreamAlbum
	e = tx.Where("bot_id = ? AND source_chat_id = ? AND media_group_id = ?", row.BotID, row.SourceChatID, source.MediaGroupID).First(&album).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		album = d.DownstreamAlbum{BotID: row.BotID, SourceChatID: row.SourceChatID, MediaGroupID: source.MediaGroupID, LeaderID: row.ID, SessionID: row.SessionID, TopicID: row.TopicID, ActorID: row.ActorID, RouteVersion: row.RouteVersion, State: "collecting", ClosesAt: now.Add(2 * time.Second), CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&album).Error; e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	// CAS serialization also covers two consultations trying the same source key.
	lock := tx.Model(&d.DownstreamAlbum{}).Where("id = ?", album.ID).UpdateColumn("count", gorm.Expr("count"))
	if lock.Error != nil {
		return lock.Error
	}
	if e = tx.First(&album, album.ID).Error; e != nil {
		return e
	}
	if album.SessionID != row.SessionID || album.TopicID != row.TopicID || album.ActorID != row.ActorID || album.RouteVersion != row.RouteVersion {
		return c.ErrDenied
	}
	var duplicate int64
	if e = tx.Model(&d.Downstream{}).Where("album_id = ? AND source_message_id = ?", album.ID, row.SourceMessageID).Count(&duplicate).Error; e != nil {
		return e
	}
	row.AlbumID = album.ID
	if duplicate > 0 || album.State != "collecting" || !now.Before(album.ClosesAt) || album.Count >= 10 || album.Bytes+row.SourceBytes > 512*1024 {
		row.State = "rejected"
		return tx.Save(row).Error
	}
	row.State = "album_item"
	if row.ID == album.LeaderID {
		row.State = "collecting"
	}
	if e = tx.Save(row).Error; e != nil {
		return e
	}
	return tx.Model(&album).Updates(map[string]any{"count": album.Count + 1, "bytes": album.Bytes + row.SourceBytes, "updated_at": now}).Error
}

// downstreamParts returns bounded ordered original items. It never includes late
// rejected items, and every item's destination and actor must match the leader.
func downstreamParts(tx *gorm.DB, row d.Downstream) ([]d.Downstream, error) {
	if row.AlbumID == 0 {
		return []d.Downstream{row}, nil
	}
	var album d.DownstreamAlbum
	if e := tx.Where("id = ? AND leader_id = ? AND bot_id = ?", row.AlbumID, row.ID, row.BotID).First(&album).Error; e != nil {
		return nil, c.ErrDenied
	}
	var parts []d.Downstream
	if e := tx.Where("album_id = ? AND state <> ?", album.ID, "rejected").Order("source_message_id ASC").Limit(11).Find(&parts).Error; e != nil {
		return nil, e
	}
	if len(parts) < 1 || len(parts) > 10 || len(parts) != album.Count {
		return nil, c.ErrDenied
	}
	last := int64(0)
	for _, part := range parts {
		if part.BotID != row.BotID || part.SessionID != row.SessionID || part.TopicID != row.TopicID || part.ActorID != row.ActorID || part.SourceChatID != row.SourceChatID || part.ThreadID != row.ThreadID || part.RouteVersion != row.RouteVersion || part.UserID != row.UserID || part.ChatID != row.ChatID || part.SourceMessageID <= last || part.Kind != "reply" {
			return nil, c.ErrDenied
		}
		last = part.SourceMessageID
	}
	return parts, nil
}

// expectedDownstream revalidates every encrypted source and native reference.
// Live membership is checked by application for their common immutable actor.
func (s *Store) expectedDownstream(tx *gorm.DB, row d.Downstream, now time.Time) (r.Message, error) {
	parts, e := downstreamParts(tx, row)
	if e != nil {
		return r.Message{}, e
	}
	sources := make([]r.Update, 0, len(parts))
	for _, part := range parts {
		if e = s.downstreamReference(tx, part, now); e != nil {
			return r.Message{}, e
		}
		plain, e := s.cipher.Open(part.SourceCipher)
		var source r.Update
		if e != nil || json.Unmarshal([]byte(plain), &source) != nil {
			return r.Message{}, c.ErrDenied
		}
		if source.ID != part.UpdateID || source.UserID != part.ActorID || source.ChatID != part.SourceChatID || source.MessageID != part.SourceMessageID || source.ThreadID != part.ThreadID || !validDownstreamSource(source) || c.DownstreamCommand(source) {
			return r.Message{}, c.ErrDenied
		}
		mapping, e := s.resolveDownstreamSource(tx, row.BotID, source, now)
		if e != nil || mapping.ID != part.ReplyMappingID || mapping.TopicID != part.TopicID || mapping.SessionID != part.SessionID || mapping.UserID != part.UserID || mapping.RouteVersion != part.RouteVersion {
			return r.Message{}, c.ErrDenied
		}
		sources = append(sources, source)
	}
	if row.AlbumID == 0 {
		return downstreamEnvelope(row, sources[0])
	}
	var album d.DownstreamAlbum
	if e = tx.First(&album, row.AlbumID).Error; e != nil {
		return r.Message{}, e
	}
	if now.Before(album.ClosesAt) {
		return r.Message{}, c.ErrConflict
	}
	for _, source := range sources {
		if source.MediaGroupID != album.MediaGroupID {
			return r.Message{}, c.ErrDenied
		}
	}
	// Reuse album content validation, not the private-source upstream forward encoder.
	// Staff replies are content copies and must never leak group source references.
	contents, e := c.AlbumContents(sources)
	if e != nil {
		return r.Message{}, e
	}
	return c.TransferEnvelope(r.TargetSupportPrivate, c.DownstreamBusinessRef(row.ID), "support_reply", row.ChatID, 0, row.RouteVersion, 0, contents), nil
}

// freezeDownstreamAlbum commits exact immutable content before runtime enqueue.
// The album lock shares the intake lock, making the window restart-safe.
func (s *Store) freezeDownstreamAlbum(tx *gorm.DB, row *d.Downstream, now time.Time) (bool, error) {
	lock := tx.Model(&d.DownstreamAlbum{}).Where("id = ? AND leader_id = ?", row.AlbumID, row.ID).UpdateColumn("count", gorm.Expr("count"))
	if lock.Error != nil {
		return false, lock.Error
	}
	var album d.DownstreamAlbum
	if e := tx.Where("id = ? AND leader_id = ?", row.AlbumID, row.ID).First(&album).Error; e != nil {
		return false, c.ErrDenied
	}
	if now.Before(album.ClosesAt) {
		return false, nil
	}
	if album.State != "collecting" {
		return row.State == "pending", nil
	}
	envelope, e := s.expectedDownstream(tx, *row, now)
	if e != nil {
		return false, e
	}
	raw, e := json.Marshal(envelope)
	if e != nil {
		return false, e
	}
	encrypted, e := s.cipher.Seal(string(raw))
	if e != nil {
		return false, e
	}
	row.EnvelopeCipher = encrypted
	row.State = "pending"
	row.Version++
	row.UpdatedAt = now
	changed := tx.Model(&d.Downstream{}).Where("id = ? AND state = ?", row.ID, "collecting").Updates(map[string]any{"envelope_cipher": encrypted, "state": "pending", "version": row.Version, "updated_at": now})
	if changed.Error != nil {
		return false, changed.Error
	}
	if changed.RowsAffected != 1 {
		return false, c.ErrConflict
	}
	return true, tx.Model(&album).Updates(map[string]any{"state": "prepared", "updated_at": now}).Error
}

// finishDownstreamReceipt stores per-source trusted destination IDs only from a
// complete sent receipt. Neither admin inputs nor partial results supply IDs.
func (s *Store) finishDownstreamReceipt(tx *gorm.DB, row d.Downstream, proof deliveryProof, now time.Time) error {
	if e := s.downstreamProof(row, proof); e != nil {
		return e
	}
	parts, e := downstreamParts(tx, row)
	if e != nil {
		return e
	}
	if proof.Status != "sent" || len(proof.MessageIDs) != len(parts) || proof.MessageID <= 0 || proof.MessageID != proof.MessageIDs[0] {
		return c.ErrDenied
	}
	var envelope r.Message
	plain, e := s.cipher.Open(row.EnvelopeCipher)
	if e != nil || json.Unmarshal([]byte(plain), &envelope) != nil || envelope.Transfer == nil || !c.ValidTransferEnvelope(envelope) || len(envelope.Transfer.Contents) != len(parts) {
		return c.ErrDenied
	}
	seen := map[int64]bool{}
	for _, id := range proof.MessageIDs {
		if id <= 0 || seen[id] {
			return c.ErrDenied
		}
		seen[id] = true
	}
	for index, part := range parts {
		if e = tx.Model(&d.Downstream{}).Where("id = ? AND bot_id = ?", part.ID, row.BotID).Updates(map[string]any{"destination_message_id": proof.MessageIDs[index], "delivery_id": proof.ID, "state": "delivered", "version": gorm.Expr("version + 1"), "updated_at": now}).Error; e != nil {
			return e
		}
	}
	if row.AlbumID != 0 {
		if e = tx.Model(&d.DownstreamAlbum{}).Where("id = ?", row.AlbumID).Updates(map[string]any{"state": "delivered", "updated_at": now}).Error; e != nil {
			return e
		}
	}
	return releaseProof(tx, row.BotID, proof.ID)
}

// ResolveDownstream applies a generation-fenced administrator CAS and audit in
// one transaction. Failed remains terminal until the explicit recovery endpoint
// requeues the SAME runtime row. Partial observations deny retry.
func (s *Store) ResolveDownstream(ctx context.Context, bot, id uint, generation uint64, in c.Resolution, now time.Time) error {
	if !validResolution(in) || in.DeliveryID == 0 || (in.Decision != "delivered" && in.Decision != "failed") {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var row d.Downstream
		if e := tx.Where("id = ? AND bot_id = ? AND version = ? AND state = ? AND kind = ?", id, bot, in.ExpectedVersion, "unknown", "reply").First(&row).Error; e != nil {
			return c.ErrConflict
		}
		var proof deliveryProof
		if e := tx.Table("telegram_bot_deliveries").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", in.DeliveryID).First(&proof).Error; e != nil {
			return c.ErrDenied
		}
		if e := s.downstreamProof(row, proof); e != nil {
			return e
		}
		changed := tx.Model(&d.Downstream{}).Where("id = ? AND version = ? AND state = ?", id, in.ExpectedVersion, "unknown").Updates(map[string]any{"version": in.ExpectedVersion + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		if in.Decision == "delivered" {
			if e := s.finishDownstreamReceipt(tx, row, proof, now); e != nil {
				return e
			}
		} else {
			if proof.Status != "failed" || proof.MessageID != 0 || len(proof.MessageIDs) != 0 {
				return c.ErrDenied
			}
			if _, e := s.expectedDownstream(tx, row, now); e != nil {
				return e
			}
			if e := tx.Model(&row).Updates(map[string]any{"state": "failed", "delivery_id": proof.ID, "updated_at": now}).Error; e != nil {
				return e
			}
		}
		return auditResolution(tx, bot, id, "downstream", in, now)
	})
}

// consumeDownstreamLiability accepts only a complete original-bot sent receipt.
// The caller owns the checkpoint lock/savepoint; no runtime send or live-topic
// authorization is attempted. A consumed ledger row decrements exactly one unit.
func (s *Store) consumeDownstreamLiability(tx *gorm.DB, plan d.RestorationCheckpoint, liability d.RestorationDownstreamLiability, now time.Time) error {
	var entry d.RestorationEntry
	if err := tx.Where("id = ? AND task_id = ? AND target_id = ? AND source_session_id = ? AND downstream_tracking_version = 1 AND downstream_scanned = ?", liability.EntryID, plan.TaskID, plan.TargetID, liability.SourceSessionID, true).Take(&entry).Error; err != nil {
		return c.ErrDenied
	}
	var row d.Downstream
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND kind = ?", liability.DownstreamID, plan.SourceBotID, entry.SourceSessionID, "reply").Take(&row).Error; err != nil {
		return c.ErrDenied
	}
	if liability.SourceVersion == 0 || liability.SourceVersion >= 1<<63-1 || liability.SourceBotID != plan.SourceBotID || row.TopicID != liability.TopicID || row.RouteVersion != liability.RouteVersion || row.AlbumID != liability.AlbumID || row.ID > entry.DownstreamUpperBound {
		return c.ErrDenied
	}
	var proof deliveryProof
	if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ? AND status = ?", row.BotID, "sent").Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.DownstreamBusinessRef(row.ID)}).Take(&proof).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrDenied
	} else if err != nil {
		return err
	}
	already := row.State == "delivered"
	if already {
		if row.Version != liability.SourceVersion+1 || row.DeliveryID != proof.ID || row.DestinationMessageID != proof.MessageID {
			return c.ErrDenied
		}
	} else if row.Version != liability.SourceVersion || row.State != liability.SourceState {
		return c.ErrDenied
	}
	if err := historicalDownstreamReference(tx, plan, row); err != nil {
		return err
	}
	if err := s.downstreamProof(row, proof); err != nil {
		return err
	}
	parts, err := downstreamParts(tx, row)
	if err != nil {
		return err
	}
	for _, part := range parts {
		if part.ID > entry.DownstreamUpperBound {
			return c.ErrDenied
		}
		if err = historicalDownstreamReference(tx, plan, part); err != nil {
			return err
		}
	}
	// finishDownstreamReceipt is also the full album proof validator. For a
	// previously persisted receipt validate in a nested savepoint, then restore
	// the business versions by rollback instead of applying the result twice.
	if already {
		if err = validatePersistedDownstreamReceipt(tx, s, row, proof, parts, now); err != nil {
			return err
		}
	} else if err = s.finishDownstreamReceipt(tx, row, proof, now); err != nil {
		return err
	}
	changed := tx.Model(&d.RestorationDownstreamLiability{}).Where("id = ? AND task_id = ? AND target_id = ? AND state = ? AND version = ?", liability.ID, plan.TaskID, plan.TargetID, "pending", liability.Version).Updates(map[string]any{"state": "consumed", "version": liability.Version + 1, "delivery_id": proof.ID, "consumed_business_version": liability.SourceVersion + 1, "consumed_at": now, "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return c.ErrConflict
	}
	changed = tx.Model(&d.RestorationEntry{}).Where("id = ? AND task_id = ? AND target_id = ? AND legacy >= 1", entry.ID, plan.TaskID, plan.TargetID).UpdateColumn("legacy", gorm.Expr("legacy - 1"))
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return c.ErrConflict
	}
	return tx.Create(&d.ResolutionAudit{BotID: row.BotID, Resource: "downstream_liability", ResourceID: liability.ID, ActorID: plan.ActorID, Version: liability.Version, Decision: "consumed", Evidence: plan.Evidence, CreatedAt: now}).Error
}

// historicalDownstreamReference validates immutable source ownership without
// weakening trustedTopic's live execution rules. Historical mappings stay retired.
func historicalDownstreamReference(tx *gorm.DB, plan d.RestorationCheckpoint, row d.Downstream) error {
	var topic d.TopicMapping
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND route_version <= ? AND state = ?", row.TopicID, row.BotID, row.SessionID, plan.TargetID, row.SourceChatID, row.ThreadID, row.RouteVersion, plan.SourceRouteVersion, "retired").Take(&topic).Error; err != nil {
		return c.ErrDenied
	}
	if row.ReplyMappingID == 0 {
		var owner d.Session
		if err := tx.Where("id = ? AND bot_id = ? AND user_id = ? AND chat_id = ?", row.SessionID, row.BotID, row.UserID, row.ChatID).Take(&owner).Error; err != nil || row.UserID <= 0 || row.ChatID != row.UserID {
			return c.ErrDenied
		}
		return nil
	}
	var mapping d.ReplyMapping
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND topic_id = ? AND user_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ?", row.ReplyMappingID, row.BotID, row.SessionID, row.TopicID, row.UserID, row.SourceChatID, row.ThreadID, row.RouteVersion).Take(&mapping).Error; err != nil {
		return c.ErrDenied
	}
	var original d.BufferedMessage
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND user_id = ? AND state = ? AND delivery_id = ?", mapping.BufferedMessageID, row.BotID, row.SessionID, row.UserID, "delivered", mapping.DeliveryID).Take(&original).Error; err != nil {
		return c.ErrDenied
	}
	if original.ChatID != row.ChatID || messageBinding(original) != bindingOf(topic) {
		return c.ErrDenied
	}
	return nil
}

// validatePersistedDownstreamReceipt requires every stored part to agree with
// the exact sent receipt. Validation-only savepoint rolls back helper writes.
func validatePersistedDownstreamReceipt(tx *gorm.DB, s *Store, row d.Downstream, proof deliveryProof, parts []d.Downstream, now time.Time) error {
	if len(parts) != len(proof.MessageIDs) {
		return c.ErrDenied
	}
	for index, part := range parts {
		if part.State != "delivered" || part.DeliveryID != proof.ID || part.DestinationMessageID != proof.MessageIDs[index] {
			return c.ErrDenied
		}
	}
	validated := errors.New("validated historical receipt")
	err := tx.Transaction(func(checkTx *gorm.DB) error {
		if e := s.finishDownstreamReceipt(checkTx, row, proof, now); e != nil {
			return e
		}
		return validated
	})
	if errors.Is(err, validated) {
		return nil
	}
	return err
}

var _ c.DownstreamResolutions = (*Store)(nil)

// checkSupportBlock affects support only and evaluates expiration at use time.
func (s *Store) checkSupportBlock(tx *gorm.DB, bot uint, user int64, now time.Time) error {
	var count int64
	if e := tx.Model(&d.SupportBlock{}).Where("bot_id = ? AND user_id = ? AND enabled = ? AND (expires_at IS NULL OR expires_at > ?)", bot, user, true, now).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return c.ErrDenied
	}
	return nil
}

// SetSupportBlock applies a bot/customer-only CAS behind live member authorization.
// It changes neither commerce nor refund permissions. Evidence contains no body.
func (s *Store) SetSupportBlock(ctx context.Context, bot, session uint, actor int64, expected uint64, enabled bool, until *time.Time, evidence string, now time.Time) error {
	if actor <= 0 || evidence == "" || len(evidence) > 512 || (until != nil && !until.After(now)) {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var owner d.Session
		lock := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ?", session, bot).UpdateColumn("version", gorm.Expr("version + 1"))
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return c.ErrDenied
		}
		if e := tx.First(&owner, session).Error; e != nil {
			return e
		}
		if enabled {
			if err := cancelBlockedChallenges(tx, bot, session, now); err != nil {
				return err
			}
		}
		if expected == 0 {
			return tx.Create(&d.SupportBlock{BotID: bot, UserID: owner.UserID, Enabled: enabled, ExpiresAt: until, Version: 1, ActorID: actor, Evidence: evidence, CreatedAt: now, UpdatedAt: now}).Error
		}
		changed := tx.Model(&d.SupportBlock{}).Where("bot_id = ? AND user_id = ? AND version = ?", bot, owner.UserID, expected).Updates(map[string]any{"enabled": enabled, "expires_at": until, "version": expected + 1, "actor_id": actor, "evidence": evidence, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
}

// CloseConsultation cancels only unprepared work. Sending/unknown retain proof;
// authorization rejects closed sessions. Caller must authorize the staff actor.
func (s *Store) CloseConsultation(ctx context.Context, bot, session uint, expected uint64, now time.Time) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		changed := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND version = ? AND state <> ?", session, bot, expected, "closed").Updates(map[string]any{"state": "closed", "version": expected + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		if e := tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND bot_id = ? AND state IN ?", session, bot, []string{"buffered", "ready", "claimed"}).Updates(map[string]any{"state": "cancelled", "reason": "closed", "claim": "", "lease_until": nil, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Model(&d.Challenge{}).Where("session_id = ? AND bot_id = ? AND state = ?", session, bot, "pending").Updates(map[string]any{"state": "cancelled", "version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
}
