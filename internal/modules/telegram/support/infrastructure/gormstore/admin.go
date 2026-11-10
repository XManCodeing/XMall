package gormstore

import (
	"context"
	"errors"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Policy reads only this bot's policy. Only a missing row falls back; missing tables/corruption fail closed.
func (s *Store) Policy(ctx context.Context, bot uint) (c.Policy, uint64, error) {
	if s == nil || s.db == nil {
		return c.Policy{}, 0, c.ErrAdminUnavailable
	}
	if bot == 0 {
		return c.Policy{}, 0, c.ErrAdminInvalid
	}
	var row d.BotPolicy
	err := s.db.WithContext(ctx).Where("bot_id = ?", bot).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.DefaultPolicy(), 0, nil
	}
	if err != nil {
		return c.Policy{}, 0, err
	}
	policy, err := (c.AdminPolicy{MaxMessages: row.MaxMessages, MaxBytes: row.MaxBytes, MaxAttempts: row.MaxAttempts, RetentionSeconds: row.RetentionSeconds, ChallengeTTLSeconds: row.ChallengeTTLSeconds, CooldownSeconds: row.CooldownSeconds}).Policy()
	if err != nil || row.Version == 0 {
		return c.Policy{}, 0, c.ErrPolicyInvalid
	}
	return policy, row.Version, nil
}

// WithPolicyRecoveryGuard binds policy writes to the runtime's parent fence lock.
// guard receives the active transaction and bot ID and returns a conflict when fenced.
func (s *Store) WithPolicyRecoveryGuard(guard func(*gorm.DB, uint) error) *Store {
	s.policyRecoveryGuard = guard
	return s
}

// SavePolicy atomically creates version one or updates an exact existing version.
// First-write conflicts are resolved by the bot primary key; no historical records are touched.
func (s *Store) SavePolicy(ctx context.Context, bot, actor uint, expected uint64, policy c.Policy, now time.Time) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, c.ErrAdminUnavailable
	}
	if bot == 0 || actor == 0 {
		return 0, c.ErrDenied
	}
	if !policy.Valid() || expected >= uint64(1<<63-1) || policy.Retention%time.Second != 0 || policy.ChallengeTTL%time.Second != 0 || policy.Cooldown%time.Second != 0 {
		return 0, c.ErrPolicyInvalid
	}
	dto := c.PolicyDTO(policy)
	row := d.BotPolicy{BotID: bot, Version: expected + 1, MaxMessages: dto.MaxMessages, MaxBytes: dto.MaxBytes, MaxAttempts: dto.MaxAttempts, RetentionSeconds: dto.RetentionSeconds, ChallengeTTLSeconds: dto.ChallengeTTLSeconds, CooldownSeconds: dto.CooldownSeconds, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.policyRecoveryGuard != nil {
			if err := s.policyRecoveryGuard(tx, bot); err != nil {
				return err
			}
		}
		var result *gorm.DB
		if expected == 0 {
			result = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}}, DoNothing: true}).Create(&row)
		} else {
			result = tx.Model(&d.BotPolicy{}).Where("bot_id = ? AND version = ?", bot, expected).Updates(map[string]any{"version": row.Version, "max_messages": row.MaxMessages, "max_bytes": row.MaxBytes, "max_attempts": row.MaxAttempts, "retention_seconds": row.RetentionSeconds, "challenge_ttl_seconds": row.ChallengeTTLSeconds, "cooldown_seconds": row.CooldownSeconds, "updated_by": actor, "updated_at": now})
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return row.Version, nil
}

var _ c.PolicyStore = (*Store)(nil)

// AdminStore projects session/message metadata from schema 12 and downstream tasks
// from schema 13; sensitive payloads are loaded only through the scoped Payload port.
type AdminStore struct{ db *gorm.DB }

// NewAdmin creates a read repository from the application's database connection.
func NewAdmin(db *gorm.DB) *AdminStore { return &AdminStore{db: db} }

const sessionColumns = "id, bot_id, user_id, chat_id, state, version, created_at, updated_at"
const messageColumns = "id, session_id, bot_id, sequence, state, retain_until, created_at, updated_at"

// query creates a bounded, parameterized metadata query; invalid input fails closed.
func (s *AdminStore) query(ctx context.Context, table string, filter c.AdminFilter) (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, c.ErrAdminUnavailable
	}
	if filter.Page < 1 || filter.Page > 1000000 || filter.PageSize < 1 || filter.PageSize > 100 {
		return nil, c.ErrAdminInvalid
	}
	query := s.db.WithContext(ctx).Table(table)
	if filter.BotID != 0 {
		query = query.Where("bot_id = ?", filter.BotID)
	}
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	return query, nil
}

// Sessions returns only whitelisted columns and a total matching the supplied filters.
func (s *AdminStore) Sessions(ctx context.Context, filter c.AdminFilter) ([]c.AdminSession, int64, error) {
	rows := []c.AdminSession{}
	query, err := s.sessionQuery(ctx, filter)
	if err != nil {
		return rows, 0, err
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return rows, 0, err
	}
	var projections []adminSessionProjection
	err = query.Order("id DESC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Scan(&projections).Error
	for _, projection := range projections {
		rows = append(rows, sessionDTO(projection))
	}
	return rows, total, err
}

// Session returns metadata or a stable not-found error without loading private message data.
func (s *AdminStore) Session(ctx context.Context, session uint) (c.AdminSession, error) {
	query, err := s.sessionQuery(ctx, c.AdminFilter{Page: 1, PageSize: 1})
	if err != nil {
		return c.AdminSession{}, err
	}
	var row adminSessionProjection
	err = query.Where("id = ?", session).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrAdminNotFound
	}
	return sessionDTO(row), err
}

// Messages scopes every query to its owning session and excludes encrypted payload columns.
func (s *AdminStore) Messages(ctx context.Context, session uint, filter c.AdminFilter) ([]c.AdminMessage, int64, error) {
	rows := []c.AdminMessage{}
	query, err := s.query(ctx, "telegram_support_messages", filter)
	if err != nil {
		return rows, 0, err
	}
	query = query.Where("session_id = ?", session)
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return rows, 0, err
	}
	err = query.Select(messageColumns).Order("sequence ASC, id ASC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Scan(&rows).Error
	return rows, total, err
}

// Payload retrieves a single internal envelope only when both local ownership IDs match.
func (s *AdminStore) Payload(ctx context.Context, session, message uint) (c.AdminPayload, error) {
	var row c.AdminPayload
	if s == nil || s.db == nil {
		return row, c.ErrAdminUnavailable
	}
	err := s.db.WithContext(ctx).Table("telegram_support_messages AS messages").
		Select("messages.payload_cipher, messages.retain_until").
		Joins("JOIN telegram_support_sessions AS sessions ON sessions.id = messages.session_id AND sessions.bot_id = messages.bot_id").
		Where("messages.session_id = ? AND messages.id = ?", session, message).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrAdminNotFound
	}
	return row, err
}

const downstreamColumns = "id, bot_id, session_id, kind, state, version, delivery_id, created_at, updated_at"

// Downstreams returns a filtered page of whitelisted task metadata; no decrypt is performed.
func (s *AdminStore) Downstreams(ctx context.Context, filter c.AdminFilter) ([]c.AdminDownstream, int64, error) {
	rows := []c.AdminDownstream{}
	query, err := s.query(ctx, "telegram_support_downstreams", filter)
	if err != nil {
		return rows, 0, err
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return rows, 0, err
	}
	err = query.Select(downstreamColumns).Order("id DESC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Scan(&rows).Error
	return rows, total, err
}

// Downstream loads the server-owned bot and CAS version without source/envelope payloads.
func (s *AdminStore) Downstream(ctx context.Context, id uint) (c.AdminDownstream, error) {
	var row c.AdminDownstream
	if s == nil || s.db == nil {
		return row, c.ErrAdminUnavailable
	}
	err := s.db.WithContext(ctx).Table("telegram_support_downstreams").Select(downstreamColumns).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrAdminNotFound
	}
	return row, err
}

const topicOperationColumns = "id, bot_id, session_id, target_id, route_version, action, state, version, created_at, updated_at"
const upstreamColumns = "messages.id, messages.bot_id, messages.session_id, messages.state, COALESCE(dispatch.version, 0) AS version, messages.delivery_id, messages.created_at, messages.updated_at"

// TopicOperations reads a bounded page of operation metadata for one bot.
func (s *AdminStore) TopicOperations(ctx context.Context, filter c.AdminFilter) ([]c.AdminTopicOperation, int64, error) {
	rows := []c.AdminTopicOperation{}
	if filter.BotID == 0 {
		return rows, 0, c.ErrAdminInvalid
	}
	query, err := s.query(ctx, "telegram_support_topic_operations", filter)
	if err != nil {
		return rows, 0, err
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return rows, 0, err
	}
	err = query.Select(topicOperationColumns).Order("id DESC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Scan(&rows).Error
	return rows, total, err
}

// TopicOperation returns only local operation identifiers and finite state/version metadata.
func (s *AdminStore) TopicOperation(ctx context.Context, id uint) (c.AdminTopicOperation, error) {
	var row c.AdminTopicOperation
	if s == nil || s.db == nil {
		return row, c.ErrAdminUnavailable
	}
	err := s.db.WithContext(ctx).Table("telegram_support_topic_operations").Select(topicOperationColumns).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrAdminNotFound
	}
	if err == nil {
		err = s.topicMappingSnapshot(ctx, &row)
	}
	return row, err
}

// Upstreams joins only CAS metadata; absent dispatches have version zero and cannot resolve.
func (s *AdminStore) Upstreams(ctx context.Context, filter c.AdminFilter) ([]c.AdminUpstream, int64, error) {
	rows := []c.AdminUpstream{}
	if filter.BotID == 0 {
		return rows, 0, c.ErrAdminInvalid
	}
	// Apply filters before joining to avoid ambiguous bot_id columns.
	query, err := s.query(ctx, "telegram_support_messages", filter)
	if err != nil {
		return rows, 0, err
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return rows, 0, err
	}
	err = s.db.WithContext(ctx).Table("(?) AS messages", query.Select("id, bot_id, session_id, state, delivery_id, created_at, updated_at")).Joins("LEFT JOIN telegram_support_ready_dispatches AS dispatch ON dispatch.message_id = messages.id AND dispatch.bot_id = messages.bot_id").Select(upstreamColumns).Order("messages.id DESC").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Scan(&rows).Error
	return rows, total, err
}

// Upstream uses buffered-message ownership, never the unrelated dispatch primary key.
func (s *AdminStore) Upstream(ctx context.Context, id uint) (c.AdminUpstream, error) {
	var row c.AdminUpstream
	if s == nil || s.db == nil {
		return row, c.ErrAdminUnavailable
	}
	err := s.db.WithContext(ctx).Table("telegram_support_messages AS messages").Joins("LEFT JOIN telegram_support_ready_dispatches AS dispatch ON dispatch.message_id = messages.id AND dispatch.bot_id = messages.bot_id").Select(upstreamColumns).Where("messages.id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrAdminNotFound
	}
	return row, err
}

// AdminTopicResolver adds current runtime/route fencing around the existing topic CAS.
// Embedded worker ports are unchanged; only the admin resolver uses this adapter.
type AdminTopicResolver struct{ *Store }

// ResolveTopic validates trusted generation and physical ownership in the SAME transaction.
// A failed decision records no effect and never creates a mapping; retired routes may be closed out.
func (s AdminTopicResolver) ResolveTopic(ctx context.Context, bot, id uint, in c.Resolution, now time.Time) error {
	if s.Store == nil || in.Generation == 0 {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := runtimeFence(tx, bot, in.Generation); err != nil {
			return err
		}
		var operation d.TopicOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND version = ?", id, bot, in.ExpectedVersion).Take(&operation).Error; err != nil {
			return c.ErrConflict
		}
		bound := *s.Store
		bound.db = tx
		if in.Decision == "succeeded" {
			if in.ThreadID <= 0 {
				return c.ErrDenied
			}
			binding := c.DeliveryBinding{ChatID: operation.ChatID, ThreadID: in.ThreadID, RouteVersion: operation.RouteVersion}
			if err := bound.operationRoute(tx, d.TopicMapping{BotID: bot, TargetID: operation.TargetID, ChatID: binding.ChatID, ThreadID: binding.ThreadID, RouteVersion: binding.RouteVersion}, now); err != nil {
				return err
			}
			var count int64
			query := tx.Model(&d.TopicMapping{}).Where("bot_id = ? AND chat_id = ? AND thread_id = ?", bot, operation.ChatID, in.ThreadID)
			if operation.Action == "create" {
				if err := query.Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return c.ErrConflict
				}
			} else {
				if err := query.Where("operation_id = ? AND session_id = ? AND target_id = ? AND route_version = ?", id, operation.SessionID, operation.TargetID, operation.RouteVersion).Count(&count).Error; err != nil {
					return err
				}
				if count != 1 {
					return c.ErrDenied
				}
			}
		} else if in.Decision != "failed" || in.ThreadID != 0 {
			return c.ErrDenied
		}
		return bound.ResolveTopic(ctx, bot, id, in, now)
	})
}

var _ c.AdminRecoveryStore = (*AdminStore)(nil)
var _ c.Topics = AdminTopicResolver{}
var _ c.AdminDownstreamStore = (*AdminStore)(nil)
var _ c.AdminReadStore = (*AdminStore)(nil)

// PrepareOperation snapshots one authoritative topic/session and creates a short
// reference. Caller has checked live membership; no network I/O occurs here.
func (s *Store) PrepareOperation(ctx context.Context, bot uint, update r.Update, route, generation uint64, action, noteCipher, ref string, now time.Time) (out d.OperationReference, err error) {
	if !operationActor(bot, update) || !c.ValidOperation(action) || len(ref) != 24 || (action == c.OperationNote) != (noteCipher != "") {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var topic d.TopicMapping
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state = ?", bot, update.ChatID, update.ThreadID, route, "open").First(&topic).Error; e != nil {
			return c.ErrDenied
		}
		if e := s.operationRoute(tx, topic, now); e != nil {
			return e
		}
		var session d.Session
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ?", topic.SessionID, bot).First(&session).Error; e != nil {
			return c.ErrDenied
		}
		if session.State == "closed" && action != c.OperationUnblock {
			return c.ErrDenied
		}
		out = d.OperationReference{Ref: ref, BotID: bot, SessionID: session.ID, TopicID: topic.ID, ActorID: update.UserID, ChatID: topic.ChatID, ThreadID: topic.ThreadID, RouteVersion: route, Generation: generation, SessionVersion: session.Version, Action: action, NoteCipher: noteCipher, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now}
		return tx.Create(&out).Error
	})
	return out, err
}

// Operation loads only the opaque reference scoped to the executing bot.
func (s *Store) Operation(ctx context.Context, bot uint, ref string) (out d.OperationReference, err error) {
	if bot == 0 || len(ref) != 24 {
		return out, c.ErrDenied
	}
	err = s.db.WithContext(ctx).Where("bot_id = ? AND ref = ?", bot, ref).First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = c.ErrDenied
	}
	return out, err
}

// ApplyOperation atomically consumes a reference, performs session CAS, mutates
// support-only state and appends an immutable audit. Any failure rolls back all.
func (s *Store) ApplyOperation(ctx context.Context, bot uint, update r.Update, ref string, now time.Time) error {
	if !operationActor(bot, update) || update.CallbackID == "" || len(ref) != 24 {
		return c.ErrDenied
	}
	initial, err := s.Operation(ctx, bot, ref)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, initial.Generation); e != nil {
			return e
		}
		var entry d.OperationReference
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND ref = ?", bot, ref).First(&entry).Error; e != nil {
			return c.ErrDenied
		}
		if entry.ActorID != update.UserID || entry.ChatID != update.ChatID || entry.ThreadID != update.ThreadID || !now.Before(entry.ExpiresAt) {
			return c.ErrDenied
		}
		var topic d.TopicMapping
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND session_id = ? AND bot_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state = ?", entry.TopicID, entry.SessionID, bot, entry.ChatID, entry.ThreadID, entry.RouteVersion, "open").First(&topic).Error; e != nil {
			return c.ErrDenied
		}
		if e := s.operationRoute(tx, topic, now); e != nil {
			return e
		}
		if entry.ConsumedAt != nil {
			return nil
		}
		changed := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND version = ?", entry.SessionID, bot, entry.SessionVersion).UpdateColumn("version", gorm.Expr("version + 1"))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		var session d.Session
		if e := tx.First(&session, entry.SessionID).Error; e != nil {
			return e
		}
		if session.State == "closed" && entry.Action != c.OperationUnblock {
			return c.ErrDenied
		}
		if e := applyStaffChange(tx, entry, session, now); e != nil {
			return e
		}
		audit := d.OperationAudit{ReferenceID: entry.ID, BotID: bot, SessionID: entry.SessionID, TopicID: entry.TopicID, ActorID: entry.ActorID, ChatID: entry.ChatID, ThreadID: entry.ThreadID, RouteVersion: entry.RouteVersion, Generation: entry.Generation, SessionVersion: entry.SessionVersion, Action: entry.Action, NoteCipher: entry.NoteCipher, CreatedAt: now}
		if e := tx.Create(&audit).Error; e != nil {
			return e
		}
		return tx.Model(&entry).Updates(map[string]any{"consumed_at": now, "note_cipher": ""}).Error
	})
}

// operationRoute locks the current route row before checking it, preventing a
// concurrent route swap between validation and commit. Custom readers must provide
// the same transactional locking contract and must be bound to the supplied tx.
func (s *Store) operationRoute(tx *gorm.DB, topic d.TopicMapping, now time.Time) error {
	if s.routes == nil {
		result := tx.Table("telegram_group_targets").Where("id = ? AND bot_instance_id = ? AND route_version = ?", topic.TargetID, topic.BotID, topic.RouteVersion).UpdateColumn("route_version", gorm.Expr("route_version"))
		if result.Error != nil {
			return result.Error
		}
	}
	return s.checkRoute(tx, topic.BotID, topic.TargetID, bindingOf(topic), now)
}

// applyStaffChange receives the already locked session; it never calls another
// service or starts a nested transaction. Close retains sending/unknown evidence.
func applyStaffChange(tx *gorm.DB, entry d.OperationReference, session d.Session, now time.Time) error {
	switch entry.Action {
	case c.OperationClose:
		if e := tx.Model(&session).Updates(map[string]any{"state": "closed", "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND bot_id = ? AND state IN ?", session.ID, session.BotID, []string{"buffered", "ready", "claimed"}).Updates(map[string]any{"state": "cancelled", "reason": "closed", "claim": "", "lease_until": nil, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Model(&d.Challenge{}).Where("session_id = ? AND bot_id = ? AND state = ?", session.ID, session.BotID, "pending").Updates(map[string]any{"state": "cancelled", "version": gorm.Expr("version + 1"), "updated_at": now}).Error
	case c.OperationBlock, c.OperationUnblock:
		enabled := entry.Action == c.OperationBlock
		if enabled {
			if err := cancelBlockedChallenges(tx, session.BotID, session.ID, now); err != nil {
				return err
			}
		}
		row := d.SupportBlock{BotID: session.BotID, UserID: session.UserID, Enabled: enabled, Version: 1, ActorID: entry.ActorID, Evidence: "staff_operation", CreatedAt: now, UpdatedAt: now}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "user_id"}}, DoUpdates: clause.Assignments(map[string]any{"enabled": enabled, "expires_at": nil, "version": gorm.Expr("version + 1"), "actor_id": entry.ActorID, "evidence": "staff_operation", "updated_at": now})}).Create(&row).Error
	case c.OperationNote:
		if entry.NoteCipher == "" {
			return c.ErrDenied
		}
		return nil
	default:
		return c.ErrDenied
	}
}

// operationActor rejects unaccountable/anonymous senders at the persistence edge.
func operationActor(bot uint, update r.Update) bool {
	return bot != 0 && !update.Private && update.UserID > 0 && update.ChatID < 0 && update.ThreadID > 0 && update.IsTopicMessage && update.SenderChat == nil && (update.ChatType == "supergroup" || update.ChatType == "")
}

var _ c.Operations = (*Store)(nil)
