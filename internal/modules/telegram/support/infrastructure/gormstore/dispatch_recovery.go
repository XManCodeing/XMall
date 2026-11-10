package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/i18n"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// rejectedReplyEnvelope builds content-free feedback in the original authorized topic.
// row supplies trusted routing and source supplies only the locale/rejection category.
func rejectedReplyEnvelope(row d.Downstream, source r.Update) r.Message {
	reason := c.ContentRejection(source)
	if reason == "" {
		reason = "content_unsupported"
	}
	locale := "zh-CN"
	if strings.HasPrefix(source.Locale, "en") {
		locale = "en-US"
	} else if source.Locale == "zh-Hant" || source.Locale == "zh-TW" {
		locale = "zh-TW"
	}
	return r.Message{TargetType: r.TargetSupportGroup, Purpose: "support_reply_rejected", Operation: "send", TargetKind: "text", ChatID: row.SourceChatID, ThreadID: row.ThreadID, RouteVersion: row.RouteVersion, BusinessRef: fmt.Sprintf("support:reply-rejected:%d", row.ID), Text: i18n.T(locale, "support."+reason)}
}

// rejectedReplyRequest repairs a missing feedback delivery using the same stable key.
// tx must be the caller's transaction; nil means feedback was already enqueued.
func (s *Store) rejectedReplyRequest(tx *gorm.DB, row d.Downstream, now time.Time) (*r.DeliveryRequest, error) {
	if err := s.downstreamReference(tx, row, now); err != nil {
		return nil, err
	}
	plain, err := s.cipher.Open(row.SourceCipher)
	var source r.Update
	if err != nil || json.Unmarshal([]byte(plain), &source) != nil {
		return nil, c.ErrDenied
	}
	message := rejectedReplyEnvelope(row, source)
	var count int64
	if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", row.BotID).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: message.BusinessRef}).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, nil
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	payload, err := s.cipher.Seal(string(raw))
	if err != nil {
		return nil, err
	}
	return &r.DeliveryRequest{BotID: row.BotID, Key: message.BusinessRef, ChatID: row.SourceChatID, TelegramUserID: row.ActorID, PayloadCipher: payload, TargetType: string(r.TargetSupportGroup), Priority: r.PrioritySupport, Purpose: message.Purpose, Operation: "send", Status: "pending", AvailableAt: now}, nil
}

// authorizeRejectedReply proves persisted rejection and an unchanged open route.
// envelope cannot carry source content, media, buttons or arbitrary destination IDs.
func (s *Store) authorizeRejectedReply(ctx context.Context, bot uint, envelope r.Message, now time.Time) error {
	id, err := strconv.ParseUint(strings.TrimPrefix(envelope.BusinessRef, "support:reply-rejected:"), 10, 64)
	if err != nil || id == 0 || s.cipher == nil {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var row d.Downstream
		if err := tx.Where("id = ? AND bot_id = ? AND state = ? AND kind = ?", id, bot, "rejected", "reply").First(&row).Error; err != nil {
			return fmt.Errorf("rejection record unavailable: %w", c.ErrDenied)
		}
		if err := s.downstreamReference(tx, row, now); err != nil {
			return fmt.Errorf("rejection route unavailable: %w", err)
		}
		plain, err := s.cipher.Open(row.SourceCipher)
		var source r.Update
		if err != nil || json.Unmarshal([]byte(plain), &source) != nil {
			return fmt.Errorf("rejection source invalid: %w", c.ErrDenied)
		}
		if !reflect.DeepEqual(envelope, rejectedReplyEnvelope(row, source)) {
			return fmt.Errorf("rejection envelope mismatch: %w", c.ErrDenied)
		}
		return nil
	})
}

// supportTerminalAge is independent of the short, pre-verification Retention TTL.
const supportTerminalAge = 7 * 24 * time.Hour

// clearCipherPage selects at most 100 eligible rows, expanding source albums to
// complete units (at most 1000 sources). Query contains all dependency
// predicates; UpdateColumns deliberately preserves immutable timestamps/metadata.
// It returns the database error so the caller's transaction can roll back.
func clearCipherPage(query *gorm.DB, fields map[string]any) error {
	var ids []uint
	if err := query.Session(&gorm.Session{}).Order("id ASC").Limit(100).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Each album contains at most ten sources: expand a page to complete units,
	// bounded by 1000 rows, so a page boundary never leaves a half-cleared album.
	var err error
	ids, err = expandSupportCleanupPage(query, ids)
	if err != nil {
		return err
	}
	return query.Where("id IN ?", ids).UpdateColumns(fields).Error
}

// expandSupportCleanupPage expands source IDs to bounded album units using the
// same transaction. Non-source models retain their original page unchanged.
func expandSupportCleanupPage(query *gorm.DB, ids []uint) ([]uint, error) {
	tx := query.Session(&gorm.Session{NewDB: true})
	var extra []uint
	var err error
	switch query.Statement.Model.(type) {
	case *d.BufferedMessage:
		albums := tx.Model(&d.AlbumItem{}).Select("album_id").Where("buffered_message_id IN ?", ids)
		err = tx.Model(&d.AlbumItem{}).Where("album_id IN (?)", albums).Pluck("buffered_message_id", &extra).Error
	case *d.Downstream:
		albums := tx.Model(&d.Downstream{}).Select("album_id").Where("id IN ? AND album_id <> 0 AND state <> ?", ids, "rejected")
		// Rejected late/overflow rows are not album members and are not bounded
		// by the ten-item limit. They must keep their own finite cleanup page.
		err = tx.Model(&d.Downstream{}).Where("album_id IN (?) AND state <> ?", albums, "rejected").Pluck("id", &extra).Error
	}
	return append(ids, extra...), err
}

// safeSupportMessages scopes terminal ciphertext to rows without unfinished
// transport dependencies. Cancelled/expired dispatches remain conservative: only
// delivery confirmation makes a prepared immutable envelope safe to forget.
// tx is the caller transaction; cutoff is the terminal retention deadline.
func safeSupportMessages(tx *gorm.DB, bot uint, cutoff time.Time) *gorm.DB {
	query := tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND state IN ? AND updated_at <= ?", bot, []string{"delivered", "cancelled", "expired", "rejected"}, cutoff)
	query = query.Where("NOT EXISTS (SELECT 1 FROM telegram_support_ready_dispatches p WHERE p.message_id = telegram_support_messages.id AND telegram_support_messages.state <> 'delivered')")
	// A rejected source must not forget a runtime attempt whose ID was never
	// copied back after a crash. Stable business keys cover that dependency too.
	businessKey := "'support:ready:' || CAST(telegram_support_messages.session_id AS TEXT) || ':' || CAST(telegram_support_messages.id AS TEXT)"
	if tx.Dialector.Name() == "mysql" {
		businessKey = "CONCAT('support:ready:', telegram_support_messages.session_id, ':', telegram_support_messages.id)"
	}
	query = query.Where("state <> 'rejected' OR NOT EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.bot_id = telegram_support_messages.bot_id AND p.key = " + businessKey + ")")
	return query.Where("NOT EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.id = telegram_support_messages.delivery_id AND (p.payload_held = ? OR p.payload_retain_until > ? OR p.status NOT IN ?))", true, cutoff.Add(supportTerminalAge), []string{"sent", "superseded"})
}

// safeSupportAlbumMessages additionally excludes the entire upstream album when
// any sibling is not terminal/old enough or still has a transport dependency.
// The returned query can be reused for sources and their prepared envelopes.
func safeSupportAlbumMessages(tx *gorm.DB, bot uint, cutoff time.Time) *gorm.DB {
	eligible := safeSupportMessages(tx, bot, cutoff).Select("telegram_support_messages.id")
	blocked := tx.Table("telegram_support_album_items AS sibling").Select("1").Joins("JOIN telegram_support_album_items own ON own.album_id = sibling.album_id").Where("own.buffered_message_id = telegram_support_messages.id").Where("sibling.buffered_message_id NOT IN (?)", eligible)
	return safeSupportMessages(tx, bot, cutoff).Where("NOT EXISTS (?)", blocked)
}

// expirableSupportMessages retains the original temporary TTL, but never ages
// out one member while another member needs its source for proof verification.
// Returns a transaction-bound query; prepared dispatches are never TTL-expired.
func expirableSupportMessages(tx *gorm.DB, bot uint, now time.Time) *gorm.DB {
	eligible := tx.Model(&d.BufferedMessage{}).Select("id").Where("bot_id = ? AND legacy_task_id = 0 AND state IN ? AND retain_until <= ?", bot, []string{"buffered", "ready", "claimed"}, now).Where("NOT EXISTS (SELECT 1 FROM telegram_support_ready_dispatches p WHERE p.message_id = telegram_support_messages.id)")
	blocked := tx.Table("telegram_support_album_items AS sibling").Select("1").Joins("JOIN telegram_support_album_items own ON own.album_id = sibling.album_id").Where("own.buffered_message_id = telegram_support_messages.id").Where("sibling.buffered_message_id NOT IN (?)", eligible)
	return tx.Model(&d.BufferedMessage{}).Where("id IN (?)", eligible).Where("NOT EXISTS (?)", blocked)
}

// clearTerminalSupportPayloads applies one finite page per ciphertext family.
// bot scopes every update; now is the trusted maintenance clock. Errors abort
// the existing Expire transaction. No receipt, mapping or audit metadata changes.
func clearTerminalSupportPayloads(tx *gorm.DB, bot uint, now time.Time) error {
	cutoff := now.Add(-supportTerminalAge)
	messages := safeSupportAlbumMessages(tx, bot, cutoff)
	if err := clearCipherPage(messages.Where("payload_cipher <> ?", ""), map[string]any{"payload_cipher": ""}); err != nil {
		return err
	}
	dispatches := tx.Model(&d.ReadyDispatch{}).Where("bot_id = ? AND payload_cipher <> ?", bot, "").Where("message_id IN (?)", safeSupportAlbumMessages(tx, bot, cutoff).Select("id"))
	if err := clearCipherPage(dispatches, map[string]any{"payload_cipher": ""}); err != nil {
		return err
	}
	if err := clearDownstreamPayloads(tx, bot, cutoff); err != nil {
		return err
	}
	audits := tx.Model(&d.OperationAudit{}).Where("bot_id = ? AND created_at <= ? AND note_cipher <> ?", bot, cutoff, "")
	if err := clearCipherPage(audits, map[string]any{"note_cipher": ""}); err != nil {
		return err
	}
	controls := tx.Model(&d.OperationControl{}).Where("bot_id = ? AND payload_cipher <> ? AND expires_at <= ?", bot, "", now)
	// Expired controls cannot pass checkControlBinding, including on inbox
	// replay; retain update/ref tombstones rather than creating a fresh menu.
	if err := clearCipherPage(controls, map[string]any{"payload_cipher": ""}); err != nil {
		return err
	}
	references := tx.Model(&d.OperationReference{}).Where("bot_id = ? AND note_cipher <> ? AND (consumed_at IS NOT NULL OR expires_at <= ?)", bot, "", now)
	return clearCipherPage(references, map[string]any{"note_cipher": ""})
}

// clearDownstreamPayloads clears acknowledged replies and internal notes after
// seven days; failed/pending/unknown and held proofs remain retryable. Albums
// are eligible only when every member satisfies the same retention/dependency
// rules. tx and cutoff come from Expire; errors propagate without partial commit.
func clearDownstreamPayloads(tx *gorm.DB, bot uint, cutoff time.Time) error {
	// SQLite/PostgreSQL concatenate with ||; MySQL treats || as logical OR
	// unless a non-default SQL mode is enabled. Never rely on deployment modes.
	businessKey := "'support:reply:' || CAST(telegram_support_downstreams.id AS TEXT)"
	if tx.Dialector.Name() == "mysql" {
		businessKey = "CONCAT('support:reply:', CAST(telegram_support_downstreams.id AS CHAR))"
	}
	eligible := func() *gorm.DB {
		return tx.Model(&d.Downstream{}).Where("bot_id = ? AND updated_at <= ? AND state IN ?", bot, cutoff, []string{"delivered", "internal", "cancelled", "rejected"}).Where("NOT EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.id = telegram_support_downstreams.delivery_id AND (p.payload_held = ? OR p.payload_retain_until > ? OR p.status NOT IN ?))", true, cutoff.Add(supportTerminalAge), []string{"sent", "superseded"}).Where("state NOT IN ('cancelled', 'rejected') OR delivery_id = 0 AND envelope_cipher = ''").Where("state <> 'rejected' OR NOT EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.bot_id = telegram_support_downstreams.bot_id AND p.key = " + businessKey + ")")
	}
	blocked := tx.Table("telegram_support_downstreams AS sibling").Select("1").Where("sibling.album_id = telegram_support_downstreams.album_id AND sibling.id NOT IN (?)", eligible().Select("id"))
	// downstreamParts explicitly excludes rejected rows; DownstreamRequest never
	// queues them. Only unprepared non-leaders with no runtime row are independent
	// tombstones. A rejected leader is retained rather than trusting its state.
	query := eligible().Where("state <> 'rejected' OR NOT EXISTS (SELECT 1 FROM telegram_support_downstream_albums a WHERE a.id = telegram_support_downstreams.album_id AND a.leader_id = telegram_support_downstreams.id)").Where("state = 'rejected' OR album_id = 0 OR NOT EXISTS (?)", blocked).Where("source_cipher <> '' OR envelope_cipher <> ''")
	return clearCipherPage(query, map[string]any{"source_cipher": "", "envelope_cipher": ""})
}

// AuthorizeTarget is called immediately before EVERY runtime external attempt.
// It verifies the durable dispatch body, original consultation and current route;
// a valid-looking business reference alone never authorizes arbitrary text.
func (s *Store) AuthorizeTarget(ctx context.Context, bot uint, envelope r.Message) error {
	return s.AuthorizeTargetAt(ctx, bot, envelope, time.Now().UTC())
}

// AuthorizeTargetAt is the clock-explicit variant used by deterministic workers
// and tests. Clock input is trusted infrastructure, never client input.
func (s *Store) AuthorizeTargetAt(ctx context.Context, bot uint, envelope r.Message, now time.Time) error {
	if envelope.Purpose == "support_reply_rejected" {
		return s.authorizeRejectedReply(ctx, bot, envelope, now)
	}
	session, id, err := parseReadyRef(envelope.BusinessRef)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND state = ?", id, bot, session, "sending").First(&message).Error; e != nil {
			return c.ErrDenied
		}
		binding := messageBinding(message)
		if !validEnvelope(message, binding, envelope) {
			return c.ErrDenied
		}
		topic, e := s.trustedTopic(tx, bot, session, binding, now)
		if e != nil {
			return e
		}
		var dispatch d.ReadyDispatch
		if e = tx.Where("message_id = ? AND bot_id = ? AND topic_id = ?", id, bot, topic.ID).First(&dispatch).Error; e != nil {
			return c.ErrDenied
		}
		if s.cipher == nil {
			return c.ErrDenied
		}
		plain, e := s.cipher.Open(dispatch.PayloadCipher)
		if e != nil {
			return c.ErrDenied
		}
		var saved r.Message
		if json.Unmarshal([]byte(plain), &saved) != nil || !reflect.DeepEqual(saved, envelope) {
			return c.ErrDenied
		}
		source, e := s.original(tx, message)
		if e != nil {
			return e
		}
		expected, e := s.expectedReady(tx, message, source, binding, now)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(expected, envelope) {
			return c.ErrDenied
		}
		return nil
	})
}

// parseReadyRef accepts only canonical positive IDs; no padding or suffixes.
func parseReadyRef(ref string) (uint, uint, error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 4 || parts[0] != "support" || parts[1] != "ready" {
		return 0, 0, c.ErrDenied
	}
	session, e := strconv.ParseUint(parts[2], 10, strconv.IntSize)
	if e != nil || session == 0 {
		return 0, 0, c.ErrDenied
	}
	message, e := strconv.ParseUint(parts[3], 10, strconv.IntSize)
	if e != nil || message == 0 || c.ReadyBusinessRef(uint(session), uint(message)) != ref {
		return 0, 0, c.ErrDenied
	}
	return uint(session), uint(message), nil
}

// ReconcileReady uses the current true runtime generation, not the obsolete
// business claim. Sent receipts complete after restart; unknown retains its source.
// Only a terminal topic_missing proof permits rebinding the same failed delivery.
func (s *Store) ReconcileReady(ctx context.Context, bot, id uint, generation uint64, now time.Time) (state string, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ? AND state IN ?", id, bot, []string{"sending", "unknown", "failed", "delivered"}).First(&message).Error; e != nil {
			return c.ErrConflict
		}
		state = message.State
		if state == "delivered" {
			return nil
		}
		var proof deliveryProof
		e := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.ReadyBusinessRef(message.SessionID, id)}).First(&proof).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			var count int64
			if e = tx.Model(&d.ReadyDispatch{}).Where("bot_id = ? AND message_id = ?", bot, id).Count(&count).Error; e != nil {
				return e
			}
			// A prepared dispatch has never bypassed runtime: missing runtime row means
			// safe re-enqueue. Legacy BeginSend alone cannot prove no external effect.
			if count == 1 {
				return nil
			}
			if message.State == "sending" && message.LeaseUntil != nil && !now.Before(*message.LeaseUntil) {
				state = "unknown"
				return tx.Model(&message).Updates(map[string]any{"state": state, "error_class": "lease_expired", "updated_at": now}).Error
			}
			return nil
		}
		if e != nil {
			return e
		}
		if e = s.envelopeProof(tx, message, proof); e != nil {
			return e
		}
		if proof.Status == "failed" && proof.LastError == "topic_missing" {
			recovered, recoveryErr := s.recoverMissingTopic(tx, message, proof, generation, now)
			state = "topic_missing"
			if recovered {
				state = "sending"
			}
			return recoveryErr
		}
		if proof.Status == "sent" {
			if e = s.sentProof(tx, message, proof.ID); e != nil {
				return e
			}
			if e = s.registerReply(tx, message, proof.ID, now); e != nil {
				return e
			}
			state = "delivered"
			return tx.Model(&message).Updates(map[string]any{"state": state, "delivery_id": proof.ID, "claim": "", "lease_until": nil, "updated_at": now}).Error
		}
		if message.State == "delivered" {
			return c.ErrDenied
		}
		if proof.Status == "unknown" {
			state = "unknown"
		} else if proof.Status == "failed" {
			state = "failed"
		}
		// Runtime owns retries; failed is visible and never silently re-enqueued.
		return tx.Model(&message).Updates(map[string]any{"state": state, "delivery_id": proof.ID, "updated_at": now}).Error
	})
	return
}

// ResolveReady records an audited CAS on an unknown business dispatch. Sent
// needs the real runtime proof; failed needs a runtime-owned terminal failure.
// It records failure only; explicit recovery is required before another attempt.
func (s *Store) ResolveReady(ctx context.Context, bot, id uint, generation uint64, in c.Resolution, now time.Time) error {
	if !validResolution(in) || (in.Decision != "delivered" && in.Decision != "failed") {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var dispatch d.ReadyDispatch
		if e := tx.Where("message_id = ? AND bot_id = ? AND version = ?", id, bot, in.ExpectedVersion).First(&dispatch).Error; e != nil {
			return c.ErrConflict
		}
		var message d.BufferedMessage
		if e := tx.Where("id = ? AND bot_id = ? AND state = ?", id, bot, "unknown").First(&message).Error; e != nil {
			return c.ErrConflict
		}
		var proof deliveryProof
		if e := tx.Table("telegram_bot_deliveries").Where("id = ? AND bot_id = ?", in.DeliveryID, bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.ReadyBusinessRef(message.SessionID, id)}).First(&proof).Error; e != nil {
			return c.ErrDenied
		}
		if in.Decision == "delivered" {
			if e := s.sentProof(tx, message, proof.ID); e != nil {
				return e
			}
			if e := s.registerReply(tx, message, proof.ID, now); e != nil {
				return e
			}
			message.State = "delivered"
		} else {
			if proof.Status != "failed" || proof.MessageID != 0 || len(proof.MessageIDs) != 0 {
				return c.ErrDenied
			}
			if e := s.envelopeProof(tx, message, proof); e != nil {
				return e
			}
			// Explicit proven failure permits the existing runtime row's retry only.
			message.State = "failed"
		}
		result := tx.Model(&dispatch).Where("version = ?", in.ExpectedVersion).Updates(map[string]any{"version": in.ExpectedVersion + 1, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
		message.DeliveryID = proof.ID
		message.UpdatedAt = now
		if e := tx.Save(&message).Error; e != nil {
			return e
		}
		return auditResolution(tx, bot, id, "ready", in, now)
	})
}

// recoverMissingTopic consumes persisted rejection evidence on tx. generation/now
// belong to the live worker; true means the same delivery was rebound and queued.
func (s *Store) recoverMissingTopic(tx *gorm.DB, message d.BufferedMessage, proof deliveryProof, generation uint64, now time.Time) (bool, error) {
	if proof.Status != "failed" || proof.LastError != "topic_missing" || !supportProofSource(proof) || s.recovery == nil {
		return false, c.ErrDenied
	}
	// Revalidate under the runtime lock: an administrator may have retried it.
	locked, err := recoveryProof(tx, message.BotID, proof.Key)
	if err != nil {
		return false, err
	}
	if locked.ID != proof.ID || locked.Status != "failed" || locked.LastError != "topic_missing" {
		return false, c.ErrConflict
	}
	if err = s.envelopeProof(tx, message, locked); err != nil {
		return false, err
	}
	source, err := s.original(tx, message)
	if err != nil {
		return false, err
	}
	expected, err := s.expectedReady(tx, message, source, messageBinding(message), now)
	if err != nil {
		return false, err
	}
	if err = s.matchRecoveryEnvelope(locked, expected); err != nil {
		return false, err
	}
	var previous d.TopicMapping
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND session_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ?", message.BotID, message.SessionID, message.TargetChatID, message.TargetThreadID, message.RouteVersion).Take(&previous).Error; err != nil {
		return false, err
	}
	if err = s.checkRoute(tx, message.BotID, previous.TargetID, bindingOf(previous), now); err != nil {
		return false, err
	}
	if err = markMissingTopic(tx, previous, generation, now); err != nil {
		return false, err
	}
	var replacement d.TopicMapping
	err = tx.Where("bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND route_version = ? AND state = ? AND id <> ?", message.BotID, message.SessionID, previous.TargetID, previous.ChatID, previous.RouteVersion, "open", previous.ID).Take(&replacement).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = s.trustedTopic(tx, message.BotID, message.SessionID, bindingOf(replacement), now); err != nil {
		return false, err
	}
	expected.ThreadID = replacement.ThreadID
	if expected.Transfer != nil {
		expected.Transfer.ThreadID = replacement.ThreadID
	}
	return true, s.rebindMissingTopic(tx, message, locked, replacement, expected, generation, now)
}

// markMissingTopic retires only an idle exact mapping on tx; generation/now
// identify the observer. Errors preserve uncertain operations and historical data.
func markMissingTopic(tx *gorm.DB, mapping d.TopicMapping, generation uint64, now time.Time) error {
	if mapping.State == "deleted" {
		return nil
	}
	if mapping.State != "open" && mapping.State != "closed" {
		return c.ErrConflict
	}
	var operation d.TopicOperation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND route_version = ? AND retired = ? AND state IN ?", mapping.OperationID, mapping.BotID, mapping.SessionID, mapping.TargetID, mapping.ChatID, mapping.RouteVersion, false, []string{"succeeded", "failed"}).Take(&operation).Error; err != nil {
		return c.ErrConflict
	}
	if err := tx.Model(&mapping).Updates(map[string]any{"state": "deleted", "version": mapping.Version + 1, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&operation).Updates(map[string]any{"action": "delete", "state": "succeeded", "generation": generation, "version": operation.Version + 1, "updated_at": now}).Error; err != nil {
		return err
	}
	slog.Info("support_topic_missing", "bot_id", mapping.BotID, "topic_id", mapping.ID)
	return nil
}

// rebindMissingTopic changes only the thread in a validated original envelope.
// tx commits runtime retry, dispatch and all album sources together; errors roll back.
func (s *Store) rebindMissingTopic(tx *gorm.DB, message d.BufferedMessage, proof deliveryProof, topic d.TopicMapping, envelope r.Message, generation uint64, now time.Time) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	encrypted, err := s.cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	port := s.recovery(tx)
	if port == nil {
		return c.ErrDenied
	}
	if err = port.RetryFailedDelivery(tx.Statement.Context, r.DeliveryRetry{BotID: message.BotID, DeliveryID: proof.ID, Generation: generation, Key: proof.Key, TargetType: proof.TargetType, Purpose: proof.Purpose, PayloadCipher: proof.PayloadCipher, ReplacementPayloadCipher: encrypted, ChatID: proof.ChatID, TelegramUserID: proof.TelegramUserID, Now: now}); err != nil {
		return err
	}
	changed := tx.Model(&d.ReadyDispatch{}).Where("bot_id = ? AND message_id = ?", message.BotID, message.ID).Updates(map[string]any{"topic_id": topic.ID, "payload_cipher": encrypted, "version": gorm.Expr("version + 1"), "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return c.ErrConflict
	}
	ids := []uint{message.ID}
	var album d.Album
	err = tx.Where("bot_id = ? AND leader_id = ?", message.BotID, message.ID).Take(&album).Error
	if err == nil {
		if err = tx.Model(&d.AlbumItem{}).Where("album_id = ?", album.ID).Pluck("buffered_message_id", &ids).Error; err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err = tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND session_id = ? AND id IN ?", message.BotID, message.SessionID, ids).Updates(map[string]any{"state": "sending", "target_thread_id": topic.ThreadID, "delivery_id": proof.ID, "updated_at": now}).Error; err != nil {
		return err
	}
	slog.Info("support_topic_delivery_requeued", "bot_id", message.BotID, "delivery_id", proof.ID, "topic_id", topic.ID)
	return nil
}

var _ r.TargetAuthorizer = (*Store)(nil)

// DeliveryRecoveryFactory binds runtime retry storage to the SAME transaction.
// Construction must not capture a global DB; this keeps retry and audit atomic.
type DeliveryRecoveryFactory func(*gorm.DB) r.DeliveryRecoveryStore

// WithDeliveryRecovery installs the transaction adapter before concurrent use.
// factory creates a runtime port; the returned store retains existing configuration.
func (s *Store) WithDeliveryRecovery(factory DeliveryRecoveryFactory) *Store {
	s.recovery = factory
	return s
}

// RecoverDelivery checks server-owned evidence and advances one audited business
// revision. Inputs contain no platform receipt. Unknown is never retried; it is
// returned as manual review. Errors roll back the entire transport/business CAS.
func (s *Store) RecoverDelivery(ctx context.Context, bot, id uint, generation uint64, actor uint, direction, action string, in c.AdminDeliveryRecoveryInput, now time.Time) (out c.AdminDeliveryRecovery, err error) {
	if actor == 0 || id == 0 || in.ExpectedVersion == 0 || in.ExpectedVersion >= uint64(1<<63-1) || !c.ValidRecoveryEvidence(in.Evidence) || (direction != "upstream" && direction != "downstream") || (action != "reconcile" && action != "retry") {
		return out, c.ErrAdminInvalid
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		local := *s
		local.db = tx
		var proof deliveryProof
		var e error
		if direction == "upstream" {
			out, proof, e = local.recoverUpstream(tx, bot, id, in.ExpectedVersion, now)
		} else {
			out, proof, e = local.recoverDownstream(tx, bot, id, in.ExpectedVersion, now)
		}
		if e != nil {
			return e
		}
		if action == "retry" {
			if out.State != "failed" || proof.Status != "failed" || s.recovery == nil {
				return c.ErrConflict
			}
			port := s.recovery(tx)
			if port == nil {
				return c.ErrAdminUnavailable
			}
			e = port.RetryFailedDelivery(ctx, r.DeliveryRetry{BotID: bot, DeliveryID: proof.ID, Generation: generation, Key: proof.Key, TargetType: proof.TargetType, Purpose: proof.Purpose, PayloadCipher: proof.PayloadCipher, ChatID: proof.ChatID, TelegramUserID: proof.TelegramUserID, Now: now})
			if e != nil {
				if errors.Is(e, r.ErrConflict) || errors.Is(e, r.ErrStale) || errors.Is(e, r.ErrNotFound) {
					return c.ErrConflict
				}
				return e
			}
			out.State = "pending"
			if direction == "upstream" {
				out.State = "sending"
			}
			out.RuntimeState, out.RetryQueued = "retry", true
		}
		if e = persistRecovery(tx, bot, id, direction, in.ExpectedVersion, out, now); e != nil {
			return e
		}
		out.Version = in.ExpectedVersion + 1
		out.ManualReview = out.State == "unknown"
		return auditResolution(tx, bot, id, direction, c.Resolution{ActorID: actor, ExpectedVersion: in.ExpectedVersion, Decision: action, Evidence: in.Evidence}, now)
	})
	return
}

// supportProofSource rejects unrelated broadcast/panel/commerce fields and
// impossible terminal observations. proof is loaded only from runtime storage;
// the boolean authorizes no send and is shared by legacy resolution paths.
func supportProofSource(proof deliveryProof) bool {
	if proof.BroadcastID != 0 || proof.UserID != 0 || proof.PanelEpoch != "" || proof.PanelRevision != 0 || proof.TargetMessageID != 0 || proof.PreviousMessageID != 0 {
		return false
	}
	if (proof.Status == "sent" || proof.Status == "failed" || proof.Status == "unknown") && (proof.Claim != "" || proof.ClaimedUntil != nil) {
		return false
	}
	return proof.Status != "failed" || (proof.MessageID == 0 && len(proof.MessageIDs) == 0)
}

// recoveryProof locks the one durable source row by bot/business key. Missing
// evidence is not proof of failure; the caller must retain the business state.
func recoveryProof(tx *gorm.DB, bot uint, key string) (deliveryProof, error) {
	var proof deliveryProof
	err := tx.Table("telegram_bot_deliveries").Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ?", bot).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).First(&proof).Error
	if err == nil && !supportProofSource(proof) {
		return proof, c.ErrDenied
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return proof, nil
	}
	return proof, err
}

// recoverUpstream validates source, current route and dispatch CAS before using
// runtime evidence. Returns metadata and the locked proof; performs no send.
func (s *Store) recoverUpstream(tx *gorm.DB, bot, id uint, version uint64, now time.Time) (out c.AdminDeliveryRecovery, proof deliveryProof, err error) {
	var dispatch d.ReadyDispatch
	if err = tx.Where("bot_id = ? AND message_id = ? AND version = ?", bot, id, version).First(&dispatch).Error; err != nil {
		return out, proof, c.ErrConflict
	}
	var row d.BufferedMessage
	if err = tx.Where("bot_id = ? AND id = ? AND state IN ?", bot, id, []string{"sending", "unknown", "failed"}).First(&row).Error; err != nil {
		return out, proof, c.ErrConflict
	}
	if _, err = s.trustedTopic(tx, bot, row.SessionID, messageBinding(row), now); err != nil {
		return
	}
	proof, err = recoveryProof(tx, bot, c.ReadyBusinessRef(row.SessionID, id))
	if err != nil {
		return
	}
	out = recoveryView(id, row.State, row.DeliveryID, proof)
	if proof.ID == 0 {
		return
	}
	if err = s.envelopeProof(tx, row, proof); err != nil {
		return
	}
	var source r.Update
	source, err = s.original(tx, row)
	if err != nil {
		return
	}
	var expected r.Message
	expected, err = s.expectedReady(tx, row, source, messageBinding(row), now)
	if err != nil {
		return
	}
	if err = s.matchRecoveryEnvelope(proof, expected); err != nil {
		return
	}
	switch proof.Status {
	case "sent":
		err = s.registerReply(tx, row, proof.ID, now)
		out.State = "delivered"
	case "failed", "unknown":
		out.State = proof.Status
	}
	return
}

// recoverDownstream checks the complete persisted reply source and current topic.
// A sent receipt is accepted only by the existing per-source receipt registrar.
func (s *Store) recoverDownstream(tx *gorm.DB, bot, id uint, version uint64, now time.Time) (out c.AdminDeliveryRecovery, proof deliveryProof, err error) {
	var row d.Downstream
	if err = tx.Where("bot_id = ? AND id = ? AND version = ? AND kind = ? AND state IN ?", bot, id, version, "reply", []string{"pending", "unknown", "failed"}).First(&row).Error; err != nil {
		return out, proof, c.ErrConflict
	}
	if err = s.downstreamReference(tx, row, now); err != nil {
		return
	}
	proof, err = recoveryProof(tx, bot, c.DownstreamBusinessRef(id))
	if err != nil {
		return
	}
	out = recoveryView(id, row.State, row.DeliveryID, proof)
	if proof.ID == 0 {
		return
	}
	if err = s.downstreamProof(row, proof); err != nil {
		return
	}
	var expected r.Message
	expected, err = s.expectedDownstream(tx, row, now)
	if err != nil {
		return
	}
	if err = s.matchRecoveryEnvelope(proof, expected); err != nil {
		return
	}
	switch proof.Status {
	case "sent":
		err = s.finishDownstreamReceipt(tx, row, proof, now)
		out.State = "delivered"
	case "failed", "unknown":
		out.State = proof.Status
	}
	return
}

// matchRecoveryEnvelope compares runtime ciphertext with a fresh reconstruction
// of trusted source fields; valid-looking source IDs alone are insufficient.
func (s *Store) matchRecoveryEnvelope(proof deliveryProof, expected r.Message) error {
	plain, err := s.cipher.Open(proof.PayloadCipher)
	var saved r.Message
	if err != nil || json.Unmarshal([]byte(plain), &saved) != nil || !reflect.DeepEqual(saved, expected) {
		return c.ErrDenied
	}
	return nil
}

// recoveryView exposes only local references and finite evidence provenance.
// No payload, platform message IDs, claims or identity/routing fields escape.
func recoveryView(id uint, state string, delivery uint, proof deliveryProof) c.AdminDeliveryRecovery {
	out := c.AdminDeliveryRecovery{ID: id, State: state, DeliveryID: delivery, EvidenceSource: "none"}
	if proof.ID != 0 {
		out.DeliveryID, out.RuntimeState, out.EvidenceSource = proof.ID, proof.Status, "runtime_delivery"
	}
	return out
}

// persistRecovery advances the exact business revision within the proof lock.
// Receipt registration may already advance downstream version; only that known
// sent branch may use its resulting revision. Other concurrent writes conflict.
func persistRecovery(tx *gorm.DB, bot, id uint, direction string, version uint64, out c.AdminDeliveryRecovery, now time.Time) error {
	if direction == "upstream" {
		changed := tx.Model(&d.ReadyDispatch{}).Where("bot_id = ? AND message_id = ? AND version = ?", bot, id, version).Updates(map[string]any{"version": version + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		return tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND id = ?", bot, id).Updates(map[string]any{"state": out.State, "delivery_id": out.DeliveryID, "updated_at": now}).Error
	}
	expected := version
	if out.State == "delivered" {
		expected++
	}
	changed := tx.Model(&d.Downstream{}).Where("bot_id = ? AND id = ? AND version = ?", bot, id, expected).Updates(map[string]any{"state": out.State, "delivery_id": out.DeliveryID, "version": version + 1, "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}
