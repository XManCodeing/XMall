package gormstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"gorm.io/gorm"
)

// sessionProjection builds a metadata-only relation. All correlated subqueries
// execute in SQL, so page size never introduces application-level N+1 queries.
// now is captured once per request; expiry and total use the identical instant.
// ReplyMapping.CreatedAt is immutable delivery acknowledgement, unlike a customer
// message's UpdatedAt. A delivered reply's acknowledgement clock clears receipts
// at or before it; failed/unknown replies and notes cannot clear waiting state.
func (s *AdminStore) sessionProjection(ctx context.Context, now time.Time) *gorm.DB {
	db := s.db.WithContext(ctx)
	reply := "SELECT reply.updated_at FROM telegram_support_downstreams reply WHERE reply.session_id = sessions.id AND reply.bot_id = sessions.bot_id AND reply.kind = 'reply' AND reply.state = 'delivered' ORDER BY reply.updated_at DESC LIMIT 1"
	customer := "SELECT receipt.created_at FROM telegram_support_reply_mappings receipt JOIN telegram_support_messages message ON message.id = receipt.buffered_message_id AND message.bot_id = receipt.bot_id AND message.session_id = receipt.session_id WHERE receipt.session_id = sessions.id AND receipt.bot_id = sessions.bot_id AND message.state = 'delivered'"
	since := customer + " AND ((" + reply + ") IS NULL OR receipt.created_at > (" + reply + ")) ORDER BY receipt.created_at ASC LIMIT 1"
	columns := "sessions.*, (" + reply + ") AS last_reply_delivered_at, (" + customer + " ORDER BY receipt.created_at DESC LIMIT 1) AS last_customer_delivered_at, (" + since + ") AS unreplied_since"
	columns += ", CASE WHEN EXISTS (" + customer + " AND ((" + reply + ") IS NULL OR receipt.created_at > (" + reply + "))) THEN 1 ELSE 0 END AS unreplied"
	columns += ", CASE WHEN block.id IS NOT NULL THEN 1 ELSE 0 END AS blocked, block.expires_at AS block_expires_at"
	for _, direction := range []string{"upstream", "downstream"} {
		table, extra := "telegram_support_messages", ""
		if direction == "downstream" {
			table, extra = "telegram_support_downstreams", " AND problem.kind = 'reply'"
		}
		for _, state := range []string{"failed", "unknown"} {
			columns += fmt.Sprintf(", (SELECT COUNT(*) FROM %s problem WHERE problem.session_id = sessions.id AND problem.bot_id = sessions.bot_id AND problem.state = '%s'%s) AS %s_%s", table, state, extra, direction, state)
		}
	}
	// A link is navigation, not authorization. Require one unambiguous, retained
	// current mapping and the module's current target/chat/version. Never use titles.
	topics := db.Table("telegram_support_topic_mappings AS mapping").
		Joins("JOIN telegram_group_targets AS target ON target.id = mapping.target_id AND target.bot_instance_id = mapping.bot_id AND target.telegram_chat_id = mapping.chat_id AND target.route_version = mapping.route_version").
		Joins("JOIN telegram_bot_module_configs AS module ON module.target_id = target.id AND module.bot_instance_id = target.bot_instance_id AND module.partition = ?", "support").
		Where("target.purpose = ? AND mapping.state IN ? AND mapping.chat_id < ? AND mapping.thread_id > 0", "support", []string{"open", "closed"}, int64(-1000000000000)).
		Select("mapping.session_id, mapping.bot_id, MIN(mapping.chat_id) AS topic_chat_id, MIN(mapping.thread_id) AS topic_thread_id, MIN(mapping.state) AS topic_state").
		Group("mapping.session_id, mapping.bot_id").Having("COUNT(*) = 1")
	columns += ", COALESCE(topic.topic_chat_id, 0) AS topic_chat_id, COALESCE(topic.topic_thread_id, 0) AS topic_thread_id, COALESCE(topic.topic_state, '') AS topic_state"
	// Select explicit session columns before joining; never read challenge/locale/body.
	sessions := db.Table("telegram_support_sessions").Select(sessionColumns)
	return db.Table("(?) AS sessions", sessions).
		Joins("LEFT JOIN telegram_support_blocks AS block ON block.bot_id = sessions.bot_id AND block.user_id = sessions.user_id AND block.enabled = ? AND (block.expires_at IS NULL OR block.expires_at > ?)", true, now).
		Joins("LEFT JOIN (?) AS topic ON topic.session_id = sessions.id AND topic.bot_id = sessions.bot_id", topics).Select(columns)
}

// sessionQuery applies server-side operational filters before both count and page.
// filter comes from the validated API boundary; repository callers are also checked.
func (s *AdminStore) sessionQuery(ctx context.Context, filter c.AdminFilter) (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, c.ErrAdminUnavailable
	}
	if filter.Page < 1 || filter.Page > 1000000 || filter.PageSize < 1 || filter.PageSize > 100 || !filter.ValidSessionOperations() {
		return nil, c.ErrAdminInvalid
	}
	query := s.db.WithContext(ctx).Table("(?) AS projection", s.sessionProjection(ctx, time.Now().UTC()))
	if filter.BotID != 0 {
		query = query.Where("bot_id = ?", filter.BotID)
	}
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	for column, value := range map[string]*bool{"unreplied": filter.Unreplied, "blocked": filter.Blocked} {
		if value != nil {
			numeric := 0
			if *value {
				numeric = 1
			}
			query = query.Where(column+" = ?", numeric)
		}
	}
	if filter.HasTopic != nil {
		if *filter.HasTopic {
			query = query.Where("topic_thread_id > 0")
		} else {
			query = query.Where("topic_thread_id = 0")
		}
	}
	switch filter.Failure {
	case "any":
		query = query.Where("upstream_failed + upstream_unknown + downstream_failed + downstream_unknown > 0")
	case "upstream":
		query = query.Where("upstream_failed + upstream_unknown > 0")
	case "downstream":
		query = query.Where("downstream_failed + downstream_unknown > 0")
	case "none":
		query = query.Where("upstream_failed + upstream_unknown + downstream_failed + downstream_unknown = 0")
	}
	return query, nil
}

// projectionTime scans SQL-derived timestamps across SQLite/MySQL/PostgreSQL.
// Database drivers may return time.Time, text or bytes for a scalar subquery.
type projectionTime struct{ Time *time.Time }

// Scan accepts one nullable driver timestamp and returns an error on malformed
// metadata; it never logs the source value or silently manufactures a time.
func (value *projectionTime) Scan(source any) error {
	if source == nil {
		value.Time = nil
		return nil
	}
	if instant, ok := source.(time.Time); ok {
		value.Time = &instant
		return nil
	}
	var text string
	switch raw := source.(type) {
	case string:
		text = raw
	case []byte:
		text = string(raw)
	default:
		return fmt.Errorf("unsupported support timestamp type %T", source)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if instant, err := time.Parse(layout, text); err == nil {
			value.Time = &instant
			return nil
		}
	}
	return fmt.Errorf("invalid support projection timestamp")
}

// adminSessionProjection overrides only computed timestamp scanners; private
// content is absent from both this scan target and the public DTO.
type adminSessionProjection struct {
	c.AdminSession          `gorm:"embedded"`
	UnrepliedSince          projectionTime `gorm:"column:unreplied_since;type:datetime"`
	LastCustomerDeliveredAt projectionTime `gorm:"column:last_customer_delivered_at;type:datetime"`
	LastReplyDeliveredAt    projectionTime `gorm:"column:last_reply_delivered_at;type:datetime"`
	BlockExpiresAt          projectionTime `gorm:"column:block_expires_at;type:datetime"`
}

// sessionDTO converts metadata and generates only numeric Telegram private-topic
// links; opening them still requires Telegram membership and exposes no text.
func sessionDTO(row adminSessionProjection) c.AdminSession {
	out := row.AdminSession
	out.UnrepliedSince, out.LastCustomerDeliveredAt = row.UnrepliedSince.Time, row.LastCustomerDeliveredAt.Time
	out.LastReplyDeliveredAt, out.BlockExpiresAt = row.LastReplyDeliveredAt.Time, row.BlockExpiresAt.Time
	if out.TopicChatID < -1000000000000 && out.TopicThreadID > 0 {
		channel := -(out.TopicChatID + 1000000000000)
		out.TopicURL = "https://t.me/c/" + strconv.FormatInt(channel, 10) + "/" + strconv.FormatInt(out.TopicThreadID, 10)
	} else {
		out.TopicChatID, out.TopicThreadID, out.TopicState = 0, 0, ""
	}
	return out
}
