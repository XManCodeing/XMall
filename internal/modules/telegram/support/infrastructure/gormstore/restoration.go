package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RestorationGuardFactory creates a DB-only guard on the supplied transaction.
type RestorationGuardFactory func(*gorm.DB) c.RestorationGuard

// RecoveryDrainFactory supplies the existing runtime proof reader on the SAME
// transaction; it must never capture a global database connection.
type RecoveryDrainFactory func(*gorm.DB) r.RecoveryFenceStore

// WithRestorationGuard installs task/route authority before concurrent use.
func (s *Store) WithRestorationGuard(factory RestorationGuardFactory) *Store {
	s.restoration = factory
	return s
}

// WithRecoveryDrain installs actual runtime drain proof validation for retirement.
func (s *Store) WithRecoveryDrain(factory RecoveryDrainFactory) *Store {
	s.drains = factory
	return s
}

// restorationPlan reconstructs the immutable task contract from owned storage.
func restorationPlan(row d.RestorationCheckpoint) c.SupportRestoration {
	return c.SupportRestoration{TaskID: row.TaskID, ActorID: row.ActorID, Evidence: row.Evidence,
		Source:      c.RestorationRoute{BotID: row.SourceBotID, TargetID: row.TargetID, Version: row.SourceVersion, Generation: row.SourceGeneration, RouteVersion: row.SourceRouteVersion, ChatID: row.SourceChatID},
		Destination: c.RestorationRoute{BotID: row.DestinationBotID, TargetID: row.TargetID, Version: row.DestinationVersion, Generation: row.DestinationGeneration, RouteVersion: row.DestinationRouteVersion, ChatID: row.DestinationChatID}}
}

// checkRestoration fails closed on missing dependencies and stale task authority.
func (s *Store) checkRestoration(tx *gorm.DB, in c.SupportRestoration, now time.Time) error {
	if s.restoration == nil || now.IsZero() {
		return c.ErrDenied
	}
	guard := s.restoration(tx)
	if guard == nil {
		return c.ErrDenied
	}
	return guard.CheckSupportRestoration(tx.Statement.Context, in, now)
}

// BeginSupportRestoration snapshots sessions only after proven stop/drain and
// route switch. Source history remains owned by its original bot and session.
func (s *Store) BeginSupportRestoration(ctx context.Context, in c.SupportRestoration, now time.Time) (out c.RestorationProgress, err error) {
	if in.TaskID == 0 || in.ActorID == 0 || !c.ValidRecoveryEvidence(in.Evidence) ||
		in.Source.BotID == 0 || in.Destination.BotID == 0 || in.Source.TargetID == 0 || in.Source.TargetID != in.Destination.TargetID ||
		in.Source.Version == 0 || in.Destination.Version == 0 || in.Source.ChatID >= 0 || in.Destination.ChatID >= 0 ||
		in.Source.RouteVersion == 0 || in.Destination.RouteVersion <= in.Source.RouteVersion {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := s.checkRestoration(tx, in, now); e != nil {
			return e
		}
		var row d.RestorationCheckpoint
		e := tx.Where("task_id = ? AND target_id = ?", in.TaskID, in.Source.TargetID).Take(&row).Error
		if e == nil {
			if restorationPlan(row) != in {
				return c.ErrConflict
			}
			out, e = restorationProgress(tx, row)
			return e
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		row = d.RestorationCheckpoint{TaskID: in.TaskID, ActorID: in.ActorID, Evidence: in.Evidence, SourceBotID: in.Source.BotID, DestinationBotID: in.Destination.BotID, TargetID: in.Source.TargetID,
			SourceVersion: in.Source.Version, SourceGeneration: in.Source.Generation, SourceRouteVersion: in.Source.RouteVersion, SourceChatID: in.Source.ChatID,
			DestinationVersion: in.Destination.Version, DestinationGeneration: in.Destination.Generation, DestinationRouteVersion: in.Destination.RouteVersion, DestinationChatID: in.Destination.ChatID,
			Revision: 1, CreatedAt: now, UpdatedAt: now}
		if e = tx.Model(&d.Session{}).Where("bot_id = ?", row.SourceBotID).Select("COALESCE(MAX(id),0)").Scan(&row.UpperBound).Error; e != nil {
			return e
		}
		if e = tx.Create(&row).Error; e != nil {
			return e
		}
		out, e = restorationProgress(tx, row)
		return e
	})
	if err != nil {
		out = c.RestorationProgress{}
	}
	return
}

// restorationProgress aggregates only owned task rows; legacy is never hidden
// behind a completed page or newly created topic.
func restorationProgress(tx *gorm.DB, row d.RestorationCheckpoint) (out c.RestorationProgress, err error) {
	out = c.RestorationProgress{TaskID: row.TaskID, TargetID: row.TargetID, Revision: row.Revision, Cursor: row.Cursor, UpperBound: row.UpperBound, Scanned: row.Scanned}
	var groups []struct {
		State  string
		Count  uint64
		Legacy uint64
	}
	err = tx.Model(&d.RestorationEntry{}).Select("state, COUNT(*) AS count, COALESCE(SUM(legacy),0) AS legacy").Where("task_id = ? AND target_id = ?", row.TaskID, row.TargetID).Group("state").Scan(&groups).Error
	for _, group := range groups {
		out.Legacy += group.Legacy
		switch group.State {
		case "unknown":
			out.Pending += group.Count
			out.Unknown += group.Count
		case "restored":
			out.Restored += group.Count
		case "awaiting_consultation":
			out.AwaitingConsultation += group.Count
		default:
			out.Pending += group.Count
		}
	}
	if err == nil {
		var unscanned int64
		err = tx.Model(&d.RestorationEntry{}).Where("task_id = ? AND target_id = ? AND downstream_tracking_version = 1 AND downstream_scanned = ?", row.TaskID, row.TargetID, false).Count(&unscanned).Error
		out.Pending += uint64(unscanned)
	}
	return
}

// SupportRestorationProgress reads checkpoints without granting execution rights.
func (s *Store) SupportRestorationProgress(ctx context.Context, task uint, targets ...uint) (out c.RestorationProgress, err error) {
	if task == 0 {
		return out, c.ErrDenied
	}
	var row d.RestorationCheckpoint
	if err = loadRestoration(s.db.WithContext(ctx), task, targets, &row); err != nil {
		return
	}
	return restorationProgress(s.db.WithContext(ctx), row)
}

// AdvanceSupportRestoration processes one bounded session page atomically. A
// lost response is recovered by progress reads; stale revisions never reapply.
func (s *Store) AdvanceSupportRestoration(ctx context.Context, task uint, revision uint64, limit int, now time.Time, targets ...uint) (out c.RestorationProgress, err error) {
	if task == 0 || revision == 0 || revision >= 1<<63-1 || limit < 1 || limit > 100 {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var row d.RestorationCheckpoint
		if e := loadRestoration(tx, task, targets, &row); e != nil {
			return e
		}
		if e := s.checkRestoration(tx, restorationPlan(row), now); e != nil {
			return e
		}
		changed := tx.Model(&row).Where("revision = ?", revision).Updates(map[string]any{"revision": revision + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		if !row.Scanned {
			var sessions []d.Session
			if e := tx.Where("bot_id = ? AND id > ? AND id <= ?", row.SourceBotID, row.Cursor, row.UpperBound).Order("id ASC").Limit(limit).Find(&sessions).Error; e != nil {
				return e
			}
			for _, session := range sessions {
				if e := s.restoreSession(tx, row, session, now); e != nil {
					return e
				}
				row.Cursor = session.ID
			}
			row.Scanned = len(sessions) < limit || row.Cursor == row.UpperBound
		}
		if e := s.refreshRestorationSessions(tx, row, limit, now); e != nil {
			return e
		}
		if e := s.advanceDownstreamLiabilities(tx, &row, limit, now); e != nil {
			return e
		}
		if e := s.refreshReceiptedLegacy(tx, row, limit, now); e != nil {
			return e
		}
		if e := refreshUnattemptedLegacy(tx, row, limit, now); e != nil {
			return e
		}
		row.Revision = revision + 1
		row.UpdatedAt = now
		if e := tx.Save(&row).Error; e != nil {
			return e
		}
		var e error
		out, e = restorationProgress(tx, row)
		return e
	})
	if err != nil {
		out = c.RestorationProgress{}
	}
	return
}

// refreshRestorationSessions binds deferred histories only to independently
// verified destination consultations. It never inserts a session or a contact.
func (s *Store) refreshRestorationSessions(tx *gorm.DB, plan d.RestorationCheckpoint, limit int, now time.Time) error {
	var entries []d.RestorationEntry
	query := tx.Model(&d.RestorationEntry{}).Select("telegram_support_restoration_entries.*").
		Joins("JOIN telegram_support_sessions source ON source.id = telegram_support_restoration_entries.source_session_id").
		Joins("JOIN telegram_support_sessions destination ON destination.user_id = source.user_id AND destination.bot_id = ? AND destination.chat_id = source.user_id AND destination.state = ?", plan.DestinationBotID, "open").
		Where("telegram_support_restoration_entries.task_id = ? AND telegram_support_restoration_entries.target_id = ? AND telegram_support_restoration_entries.state = ?", plan.TaskID, plan.TargetID, "awaiting_consultation").
		Where("EXISTS (SELECT 1 FROM telegram_support_challenges c WHERE c.session_id = destination.id AND c.bot_id = destination.bot_id AND c.state = ? AND c.consumed_at IS NOT NULL)", "passed")
	if err := query.Order("telegram_support_restoration_entries.id ASC").Limit(limit).Find(&entries).Error; err != nil {
		return err
	}
	for _, entry := range entries {
		var source, destination d.Session
		if err := tx.First(&source, entry.SourceSessionID).Error; err != nil {
			return err
		}
		if err := tx.Where("bot_id = ? AND user_id = ? AND state = ?", plan.DestinationBotID, source.UserID, "open").Take(&destination).Error; err != nil {
			return err
		}
		if err := s.checkSupportBlock(tx, plan.DestinationBotID, destination.UserID, now); err != nil {
			return err
		}
		if err := tx.Model(&entry).Updates(map[string]any{"destination_session_id": destination.ID, "state": "pending", "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// restoreSession isolates only the exact old route. It never transfers message
// bodies, IDs, challenges or reachability. Cross-bot history requires an already
// independently verified destination consultation; otherwise it is deferred.
func (s *Store) restoreSession(tx *gorm.DB, plan d.RestorationCheckpoint, session d.Session, now time.Time) error {
	var count int64
	if err := s.authorizeSessionHistory(tx, plan, session.ID, now); err != nil {
		return err
	}
	if err := tx.Model(&d.TopicOperation{}).Where("bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ?", plan.SourceBotID, session.ID, plan.TargetID, plan.SourceRouteVersion).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	var unrelated int64
	if err := tx.Model(&d.TopicOperation{}).Where("bot_id = ? AND session_id = ? AND retired = ? AND (target_id <> ? OR route_version > ?)", plan.SourceBotID, session.ID, false, plan.TargetID, plan.SourceRouteVersion).Count(&unrelated).Error; err != nil {
		return err
	}
	if unrelated != 0 {
		return c.ErrConflict
	}
	entry := d.RestorationEntry{TaskID: plan.TaskID, TargetID: plan.TargetID, SourceSessionID: session.ID, State: "pending", CreatedAt: now, UpdatedAt: now}
	if err := retireRestorationSession(tx, plan, session.ID, &entry, now); err != nil {
		return err
	}
	if plan.SourceBotID == plan.DestinationBotID {
		entry.DestinationSessionID = session.ID
		if session.State != "open" {
			entry.State = "awaiting_consultation"
		}
	} else {
		var destination d.Session
		err := tx.Where("bot_id = ? AND user_id = ? AND chat_id = ? AND state = ?", plan.DestinationBotID, session.UserID, session.UserID, "open").Take(&destination).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			entry.State = "awaiting_consultation"
		} else if err != nil {
			return err
		} else {
			// Session state alone is not proof of a new bot's independent consultation.
			var passed int64
			if err = tx.Model(&d.Challenge{}).Where("bot_id = ? AND session_id = ? AND state = ? AND consumed_at IS NOT NULL", plan.DestinationBotID, destination.ID, "passed").Count(&passed).Error; err != nil {
				return err
			}
			if passed == 0 {
				entry.State = "awaiting_consultation"
			} else {
				entry.DestinationSessionID = destination.ID
			}
		}
	}
	return tx.Create(&entry).Error
}

// authorizeSessionHistory validates every distinct old route through the same
// task guard before any retirement; excess history fails closed, never truncates.
func (s *Store) authorizeSessionHistory(tx *gorm.DB, plan d.RestorationCheckpoint, session uint, now time.Time) error {
	var routes []struct {
		RouteVersion uint64
		ChatID       int64
	}
	if err := tx.Model(&d.TopicOperation{}).Distinct("route_version", "chat_id").Where("bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ?", plan.SourceBotID, session, plan.TargetID, plan.SourceRouteVersion).Limit(101).Scan(&routes).Error; err != nil {
		return err
	}
	if len(routes) > 100 {
		return c.ErrConflict
	}
	for _, route := range routes {
		candidate := restorationPlan(plan)
		candidate.Source.RouteVersion = route.RouteVersion
		candidate.Source.ChatID = route.ChatID
		if err := s.checkRestoration(tx, candidate, now); err != nil {
			return err
		}
	}
	return nil
}

// retireRestorationSession preserves old receipts and uncertainty. Pending work
// is marked legacy separately so fresh consultations are not head-of-line blocked.
func retireRestorationSession(tx *gorm.DB, plan d.RestorationCheckpoint, session uint, entry *d.RestorationEntry, now time.Time) error {
	scope := tx.Model(&d.TopicOperation{}).Where("bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ?", plan.SourceBotID, session, plan.TargetID, plan.SourceRouteVersion)
	var uncertain int64
	if err := scope.Session(&gorm.Session{}).Where("state IN ?", []string{"creating", "unknown"}).Count(&uncertain).Error; err != nil {
		return err
	}
	entry.Legacy = uint64(uncertain)
	if err := scope.Updates(map[string]any{"retired": true, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&d.TopicMapping{}).Where("bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ?", plan.SourceBotID, session, plan.TargetID, plan.SourceRouteVersion).Updates(map[string]any{"state": "retired", "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
		return err
	}
	messages := tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND session_id = ? AND state IN ?", plan.SourceBotID, session, []string{"buffered", "ready", "claimed", "sending", "unknown", "failed"})
	var pending int64
	if err := messages.Session(&gorm.Session{}).Count(&pending).Error; err != nil {
		return err
	}
	entry.Legacy += uint64(pending)
	if err := messages.Updates(map[string]any{"legacy_task_id": plan.TaskID, "feedback_pending": false}).Error; err != nil {
		return err
	}
	// Same-bot, never-prepared consultations keep their source in place and use
	// the ordinary ClaimReady/PrepareReady delivery path against the new topic.
	// Cross-bot source message/file identifiers are never reused.
	if plan.SourceBotID == plan.DestinationBotID {
		restored, err := restoreNeverPrepared(tx, plan, session, now)
		if err != nil {
			return err
		}
		entry.Legacy -= restored
	}
	entry.DownstreamTrackingVersion = 1
	if err := tx.Model(&d.Downstream{}).Where("bot_id = ? AND session_id = ?", plan.SourceBotID, session).Select("COALESCE(MAX(id),0)").Scan(&entry.DownstreamUpperBound).Error; err != nil {
		return err
	}
	entry.DownstreamScanned = entry.DownstreamUpperBound == 0
	return nil
}

// restoreNeverPrepared restores at most 100 proven unattempted single sources.
// Albums and excess rows remain explicit legacy, never partially reconstructed.
// The exact runtime business key check also protects legacy dispatch variants.
func restoreNeverPrepared(tx *gorm.DB, plan d.RestorationCheckpoint, session uint, now time.Time) (uint64, error) {
	var rows []d.BufferedMessage
	query := neverPreparedCandidates(tx, plan, now).Where("session_id = ?", session)
	if err := query.Order("id ASC").Limit(100).Find(&rows).Error; err != nil {
		return 0, err
	}
	var restored uint64
	for _, row := range rows {
		var attempts int64
		if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", plan.SourceBotID).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.ReadyBusinessRef(session, row.ID)}).Count(&attempts).Error; err != nil {
			return 0, err
		}
		if attempts != 0 {
			continue
		}
		changed := tx.Model(&row).Updates(map[string]any{"legacy_task_id": 0, "state": "ready", "claim": "", "lease_until": nil})
		if changed.Error != nil {
			return 0, changed.Error
		}
		restored += uint64(changed.RowsAffected)
	}
	return restored, nil
}

// neverPreparedCandidates excludes every durable attempt before pagination.
// SQL concatenation follows the existing dispatch cleanup dialect convention;
// the selected source remains on its original bot and is never copied.
func neverPreparedCandidates(tx *gorm.DB, plan d.RestorationCheckpoint, now time.Time) *gorm.DB {
	businessKey := "'support:ready:' || CAST(telegram_support_messages.session_id AS TEXT) || ':' || CAST(telegram_support_messages.id AS TEXT)"
	if tx.Dialector.Name() == "mysql" {
		businessKey = "CONCAT('support:ready:', CAST(telegram_support_messages.session_id AS CHAR), ':', CAST(telegram_support_messages.id AS CHAR))"
	}
	return tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND legacy_task_id = ? AND state IN ? AND delivery_id = 0 AND retain_until > ?", plan.SourceBotID, plan.TaskID, []string{"ready", "claimed"}, now).
		Where("NOT EXISTS (SELECT 1 FROM telegram_support_ready_dispatches p WHERE p.message_id = telegram_support_messages.id)").
		Where("NOT EXISTS (SELECT 1 FROM telegram_support_album_items a WHERE a.buffered_message_id = telegram_support_messages.id)").
		Where("NOT EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.bot_id = telegram_support_messages.bot_id AND p.key = " + businessKey + ")")
}

// refreshReceiptedLegacy retires only single-message liabilities with a verified
// immutable runtime sent receipt. plan is already authorized/drained by Advance;
// missing/invalid evidence stays isolated. Task marker and counter commit together.
func (s *Store) refreshReceiptedLegacy(tx *gorm.DB, plan d.RestorationCheckpoint, limit int, now time.Time) error {
	var cursor d.ResolutionAudit
	resource := "legacy_receipt_page"
	target := fmt.Sprint(plan.TargetID)
	err := tx.Where("bot_id = ? AND resource = ? AND resource_id = ? AND actor_id = ? AND decision = ?", plan.SourceBotID, resource, plan.TaskID, plan.ActorID, target).Order("id DESC").Take(&cursor).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var after uint64
	if cursor.ID != 0 {
		after, err = strconv.ParseUint(cursor.Evidence, 10, 64)
		if err != nil || cursor.Version > plan.Revision {
			return c.ErrConflict
		}
	}
	var messages []d.BufferedMessage
	businessKey := "'support:ready:' || CAST(telegram_support_messages.session_id AS TEXT) || ':' || CAST(telegram_support_messages.id AS TEXT)"
	if tx.Dialector.Name() == "mysql" {
		businessKey = "CONCAT('support:ready:', CAST(telegram_support_messages.session_id AS CHAR), ':', CAST(telegram_support_messages.id AS CHAR))"
	}
	if err := tx.Where("bot_id = ? AND legacy_task_id = ? AND id > ? AND state IN ?", plan.SourceBotID, plan.TaskID, after, []string{"sending", "unknown", "failed", "delivered"}).
		Where("EXISTS (SELECT 1 FROM telegram_bot_deliveries p WHERE p.bot_id = telegram_support_messages.bot_id AND p.key = "+businessKey+" AND p.status = ?)", "sent").
		Where("NOT EXISTS (SELECT 1 FROM telegram_support_album_items i WHERE i.buffered_message_id = telegram_support_messages.id) OR EXISTS (SELECT 1 FROM telegram_support_albums a WHERE a.leader_id = telegram_support_messages.id AND a.bot_id = telegram_support_messages.bot_id)").
		Where("EXISTS (SELECT 1 FROM telegram_support_restoration_entries e WHERE e.task_id = ? AND e.target_id = ? AND e.source_session_id = telegram_support_messages.session_id AND e.legacy > 0)", plan.TaskID, plan.TargetID).
		Order("id ASC").Limit(limit).Find(&messages).Error; err != nil {
		return err
	}
	for _, message := range messages {
		if err := s.authorizeSessionHistory(tx, plan, message.SessionID, now); err != nil {
			return err
		}
		// Isolate a malformed receipt/mapping without persisting any partial
		// mapping writes; database/authority errors still abort the page.
		err := tx.Transaction(func(itemTx *gorm.DB) error {
			return s.consumeLegacyReceipt(itemTx, plan, message, now)
		})
		if err != nil && !errors.Is(err, c.ErrDenied) {
			return err
		}
	}
	// The cursor is a scan observation, not a resolution. Invalid rows retain
	// their liabilities and are reconsidered after a bounded wrap-around.
	next := "0"
	if len(messages) == limit {
		next = fmt.Sprint(messages[len(messages)-1].ID)
	}
	return tx.Create(&d.ResolutionAudit{BotID: plan.SourceBotID, Resource: resource, ResourceID: plan.TaskID, ActorID: plan.ActorID, Decision: target, Version: plan.Revision + 1, Evidence: next, CreatedAt: now}).Error
}

// consumeLegacyReceipt checks one exact historical receipt on the page savepoint.
// It returns ErrDenied for invalid evidence and rolls back any partial mapping;
// only a full proof may consume the task marker and its matching liability.
func (s *Store) consumeLegacyReceipt(tx *gorm.DB, plan d.RestorationCheckpoint, message d.BufferedMessage, now time.Time) error {
	var proof deliveryProof
	err := tx.Table("telegram_bot_deliveries").Where("bot_id = ? AND status = ?", message.BotID, "sent").Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.ReadyBusinessRef(message.SessionID, message.ID)}).Take(&proof).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrDenied
	}
	if err != nil {
		return err
	}
	var topic d.TopicMapping
	if err = tx.Where("bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND route_version <= ? AND state = ?", message.BotID, message.SessionID, plan.TargetID, message.TargetChatID, message.TargetThreadID, message.RouteVersion, plan.SourceRouteVersion, "retired").Take(&topic).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrDenied
	} else if err != nil {
		return err
	}
	if err = s.sentProof(tx, message, proof.ID); err != nil {
		return err
	}
	ids, err := legacyReceiptMembers(tx, plan, message)
	if err != nil {
		return err
	}
	if err = s.registerReply(tx, message, proof.ID, now); err != nil {
		return err
	}
	changed := tx.Model(&d.BufferedMessage{}).Where("id IN ? AND legacy_task_id = ?", ids, plan.TaskID).Updates(map[string]any{"legacy_task_id": 0, "state": "delivered", "delivery_id": proof.ID, "claim": "", "lease_until": nil, "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != int64(len(ids)) {
		return c.ErrConflict
	}
	changed = tx.Model(&d.RestorationEntry{}).Where("task_id = ? AND target_id = ? AND source_session_id = ? AND legacy >= ?", plan.TaskID, plan.TargetID, message.SessionID, len(ids)).UpdateColumn("legacy", gorm.Expr("legacy - ?", len(ids)))
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// legacyReceiptMembers returns only task-marked sources of one complete album
// (at most ten) or the single source. Partial/mixed task ownership is not proof.
func legacyReceiptMembers(tx *gorm.DB, plan d.RestorationCheckpoint, leader d.BufferedMessage) ([]uint, error) {
	var album d.Album
	err := tx.Where("bot_id = ? AND leader_id = ?", leader.BotID, leader.ID).Take(&album).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []uint{leader.ID}, nil
	}
	if err != nil {
		return nil, err
	}
	if album.Count < 1 || album.Count > 10 || album.SessionID != leader.SessionID || album.ChatID != leader.ChatID {
		return nil, c.ErrDenied
	}
	var items []d.AlbumItem
	if err = tx.Where("album_id = ?", album.ID).Limit(11).Find(&items).Error; err != nil {
		return nil, err
	}
	if len(items) != album.Count {
		return nil, c.ErrDenied
	}
	ids := make([]uint, 0, len(items))
	foundLeader := false
	for _, item := range items {
		ids = append(ids, item.BufferedMessageID)
		foundLeader = foundLeader || item.BufferedMessageID == leader.ID
	}
	if !foundLeader {
		return nil, c.ErrDenied
	}
	var count int64
	if err = tx.Model(&d.BufferedMessage{}).Where("id IN ? AND bot_id = ? AND session_id = ? AND user_id = ? AND chat_id = ? AND legacy_task_id = ?", ids, leader.BotID, leader.SessionID, leader.UserID, leader.ChatID, plan.TaskID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != int64(len(ids)) {
		return nil, c.ErrDenied
	}
	return ids, nil
}

// refreshUnattemptedLegacy continues bounded same-bot never-prepared work after
// the initial scan. Other legacy is not retried or hidden by this counter update.
func refreshUnattemptedLegacy(tx *gorm.DB, plan d.RestorationCheckpoint, limit int, now time.Time) error {
	if plan.SourceBotID != plan.DestinationBotID {
		return nil
	}
	var entries []d.RestorationEntry
	if err := tx.Where("task_id = ? AND target_id = ? AND legacy > 0", plan.TaskID, plan.TargetID).Where("source_session_id IN (?)", neverPreparedCandidates(tx, plan, now).Select("session_id")).Order("id ASC").Limit(limit).Find(&entries).Error; err != nil {
		return err
	}
	for _, entry := range entries {
		restored, err := restoreNeverPrepared(tx, plan, entry.SourceSessionID, now)
		if err != nil {
			return err
		}
		if restored > entry.Legacy {
			return c.ErrConflict
		}
		if restored > 0 {
			if err = tx.Model(&entry).Update("legacy", entry.Legacy-restored).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ClaimRestorationTopic creates a normal topic operation under task authority.
// Existing creating/unknown operations never grant another external attempt.
func (s *Store) ClaimRestorationTopic(ctx context.Context, task uint, claim string, now time.Time, targets ...uint) (out c.RestorationTopic, err error) {
	if !validLease(claim, time.Minute) {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var plan d.RestorationCheckpoint
		if e := loadRestoration(tx, task, targets, &plan); e != nil {
			return e
		}
		out.Plan = restorationPlan(plan)
		if e := s.checkRestoration(tx, out.Plan, now); e != nil {
			return e
		}
		var entry d.RestorationEntry
		e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND target_id = ? AND state IN ?", task, plan.TargetID, []string{"pending", "creating", "retry_authorized"}).Order("id ASC").Take(&entry).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		out.EntryID = entry.ID
		if entry.OperationID != 0 {
			var operation d.TopicOperation
			if e = tx.First(&operation, entry.OperationID).Error; e != nil {
				return e
			}
			if entry.State == "retry_authorized" {
				out.Claim, e = claimRestorationRetry(tx, entry, operation, claim, now)
				return e
			}
			if operation.State == "creating" && !now.Before(operation.LeaseUntil) {
				if e = tx.Model(&operation).Updates(map[string]any{"state": "unknown", "version": gorm.Expr("version + 1")}).Error; e != nil {
					return e
				}
				if e = tx.Model(&entry).Update("state", "unknown").Error; e != nil {
					return e
				}
			}
			return nil
		}
		blocked, e := topicRetryBlocked(tx, plan.DestinationBotID, now)
		if e != nil || blocked {
			return e
		}
		var session d.Session
		if e = tx.Where("id = ? AND bot_id = ? AND state = ?", entry.DestinationSessionID, plan.DestinationBotID, "open").Take(&session).Error; e != nil {
			return c.ErrDenied
		}
		if e = s.checkSupportBlock(tx, plan.DestinationBotID, session.UserID, now); e != nil {
			return e
		}
		operation, e := nextTopicSlot(tx, plan.DestinationBotID, session.ID, plan.TargetID, plan.DestinationRouteVersion, plan.DestinationChatID)
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			if e != nil {
				return e
			}
			return bindExistingRestorationTopic(tx, &entry, operation, now)
		}
		operation = d.TopicOperation{BotID: plan.DestinationBotID, SessionID: session.ID, TargetID: plan.TargetID, ChatID: plan.DestinationChatID, RouteVersion: plan.DestinationRouteVersion, SlotVersion: operation.SlotVersion, State: "creating", Action: "create", Version: 1, Generation: plan.DestinationGeneration, Claim: claim, LeaseUntil: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&operation).Error; e != nil {
			return e
		}
		if e = tx.Model(&entry).Updates(map[string]any{"operation_id": operation.ID, "state": "creating", "updated_at": now}).Error; e != nil {
			return e
		}
		out.Claim = c.TopicClaim{Operation: operation, Execute: true}
		return nil
	})
	if err != nil {
		out = c.RestorationTopic{}
	}
	return
}

// bindExistingRestorationTopic links a topic created by independent fresh
// consultation. It never reassigns physical ownership or issues a second create.
func bindExistingRestorationTopic(tx *gorm.DB, entry *d.RestorationEntry, operation d.TopicOperation, now time.Time) error {
	state := "unknown"
	if operation.State == "succeeded" {
		var mapping d.TopicMapping
		if err := tx.Where("operation_id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND chat_id = ? AND route_version = ? AND state = ?", operation.ID, operation.BotID, entry.DestinationSessionID, operation.TargetID, operation.ChatID, operation.RouteVersion, "open").Take(&mapping).Error; err != nil {
			return c.ErrConflict
		}
		state = "restored"
	}
	return tx.Model(entry).Updates(map[string]any{"operation_id": operation.ID, "state": state, "updated_at": now}).Error
}

// FinishRestorationTopic commits the ordinary topic result/mapping plus recovery
// state atomically. Exact claim, task and route reject old or unrelated receipts.
func (s *Store) FinishRestorationTopic(ctx context.Context, task, entryID uint, claim string, result c.TopicResult, now time.Time) error {
	if result.State != "succeeded" && result.State != "failed" && result.State != "unknown" {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var plan d.RestorationCheckpoint
		var locator d.RestorationEntry
		if e := tx.Where("id = ? AND task_id = ?", entryID, task).Take(&locator).Error; e != nil {
			return c.ErrDenied
		}
		if e := loadRestoration(tx, task, []uint{locator.TargetID}, &plan); e != nil {
			return e
		}
		if e := s.checkRestoration(tx, restorationPlan(plan), now); e != nil {
			return e
		}
		var entry d.RestorationEntry
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND state IN ?", entryID, task, []string{"creating", "unknown"}).Take(&entry).Error; e != nil {
			return c.ErrConflict
		}
		var operation d.TopicOperation
		if e := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND route_version = ? AND claim = ? AND state IN ?", entry.OperationID, plan.DestinationBotID, entry.DestinationSessionID, plan.DestinationRouteVersion, claim, []string{"creating", "unknown"}).Take(&operation).Error; e != nil {
			return c.ErrConflict
		}
		if operation.State == "unknown" && result.State != "succeeded" {
			return c.ErrConflict
		}
		if e := applyTopicResult(tx, &operation, result, now); e != nil {
			return e
		}
		state := result.State
		if state == "succeeded" {
			state = "restored"
		}
		return tx.Model(&entry).Updates(map[string]any{"state": state, "updated_at": now}).Error
	})
}

// AuthorizeRestorationTopic verifies the durable exact create intent immediately
// before HTTP. It does not trust a caller-supplied session, claim or route alone.
func (s *Store) AuthorizeRestorationTopic(ctx context.Context, job c.RestorationTopic, now time.Time) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var plan d.RestorationCheckpoint
		if err := loadRestoration(tx, job.Plan.TaskID, []uint{job.Plan.Source.TargetID}, &plan); err != nil {
			return err
		}
		if restorationPlan(plan) != job.Plan || !job.Claim.Execute {
			return c.ErrDenied
		}
		if err := s.checkRestoration(tx, job.Plan, now); err != nil {
			return err
		}
		var entry d.RestorationEntry
		if err := tx.Where("id = ? AND task_id = ? AND operation_id = ? AND destination_session_id = ? AND state = ?", job.EntryID, plan.TaskID, job.Claim.Operation.ID, job.Claim.Operation.SessionID, "creating").Take(&entry).Error; err != nil {
			return c.ErrDenied
		}
		var operation d.TopicOperation
		if err := tx.Where("id = ? AND claim = ? AND state = ? AND retired = ? AND lease_until > ?", entry.OperationID, job.Claim.Operation.Claim, "creating", false, now).Take(&operation).Error; err != nil {
			return c.ErrDenied
		}
		if operation.BotID != plan.DestinationBotID || operation.ChatID != plan.DestinationChatID || operation.TargetID != plan.TargetID || operation.RouteVersion != plan.DestinationRouteVersion || operation.Action != "create" {
			return c.ErrDenied
		}
		var count int64
		if err := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND state = ?", operation.SessionID, operation.BotID, "open").Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrDenied
		}
		var owner d.Session
		if err := tx.First(&owner, operation.SessionID).Error; err != nil {
			return err
		}
		return s.checkSupportBlock(tx, operation.BotID, owner.UserID, now)
	})
}

// loadRestoration requires an explicit target when a parent has multiple plans.
// Legacy single-plan callers remain unambiguous; no target is silently selected.
func loadRestoration(tx *gorm.DB, task uint, targets []uint, row *d.RestorationCheckpoint) error {
	if task == 0 || len(targets) > 1 {
		return c.ErrDenied
	}
	query := tx.Where("task_id = ?", task)
	if len(targets) == 1 {
		if targets[0] == 0 {
			return c.ErrDenied
		}
		query = query.Where("target_id = ?", targets[0])
	}
	var rows []d.RestorationCheckpoint
	if err := query.Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 {
		return c.ErrConflict
	}
	*row = rows[0]
	return nil
}

// ResolveRestorationTopic is a task-authorized audited CAS, not a blind retry.
// Unknown may be reconciled to a manually verified concrete topic or explicit
// no-effect failure; a separate retry decision is required after proven failure.
func (s *Store) ResolveRestorationTopic(ctx context.Context, task, entryID uint, in c.Resolution, now time.Time) error {
	if !validResolution(in) || !c.ValidRecoveryEvidence(in.Evidence) || (in.Decision != "retry" && in.Decision != "failed" && in.Decision != "succeeded") {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var entry d.RestorationEntry
		if err := tx.Where("id = ? AND task_id = ?", entryID, task).Take(&entry).Error; err != nil {
			return c.ErrDenied
		}
		var plan d.RestorationCheckpoint
		if err := loadRestoration(tx, task, []uint{entry.TargetID}, &plan); err != nil {
			return err
		}
		if in.ActorID != plan.ActorID {
			return c.ErrDenied
		}
		if err := s.checkRestoration(tx, restorationPlan(plan), now); err != nil {
			return err
		}
		var operation d.TopicOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND version = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND route_version = ? AND retired = ?", entry.OperationID, in.ExpectedVersion, plan.DestinationBotID, entry.DestinationSessionID, plan.TargetID, plan.DestinationRouteVersion, false).Take(&operation).Error; err != nil {
			return c.ErrConflict
		}
		state := "failed"
		if in.Decision == "retry" {
			if entry.State != "failed" || operation.State != "failed" || operation.Action != "create" || in.ThreadID != 0 {
				return c.ErrConflict
			}
			state = "retry_authorized"
			if err := tx.Model(&operation).Update("version", operation.Version+1).Error; err != nil {
				return err
			}
		} else {
			if entry.State != "unknown" || operation.State != "unknown" || (in.Decision == "failed" && in.ThreadID != 0) {
				return c.ErrConflict
			}
			if err := applyTopicResult(tx, &operation, c.TopicResult{State: in.Decision, ThreadID: in.ThreadID}, now); err != nil {
				return err
			}
			if in.Decision == "succeeded" {
				state = "restored"
			}
		}
		if err := tx.Model(&entry).Updates(map[string]any{"state": state, "updated_at": now}).Error; err != nil {
			return err
		}
		return auditResolution(tx, plan.DestinationBotID, entry.ID, "restoration_topic", in, now)
	})
}

// claimRestorationRetry reuses the exact proven-failed operation after an explicit
// audited retry authorization. Bot-wide 429 deadline and in-flight serialization remain.
func claimRestorationRetry(tx *gorm.DB, entry d.RestorationEntry, operation d.TopicOperation, claim string, now time.Time) (c.TopicClaim, error) {
	if operation.State != "failed" || operation.Retired || operation.Action != "create" {
		return c.TopicClaim{}, c.ErrConflict
	}
	blocked, err := topicRetryBlocked(tx, operation.BotID, now)
	if err != nil || blocked {
		return c.TopicClaim{}, err
	}
	operation.State = "creating"
	operation.Claim = claim
	operation.LeaseUntil = now.Add(time.Minute)
	operation.Version++
	operation.UpdatedAt = now
	if err = tx.Save(&operation).Error; err != nil {
		return c.TopicClaim{}, err
	}
	if err = tx.Model(&entry).Updates(map[string]any{"state": "creating", "updated_at": now}).Error; err != nil {
		return c.TopicClaim{}, err
	}
	return c.TopicClaim{Operation: operation, Execute: true}, nil
}

// ReconcileRestorationLegacy resolves only an old unknown create proven to have
// had no effect. It never decrements delivery liabilities, creates mappings or
// allows old-route retries. The evidence reference and operation CAS are audited.
func (s *Store) ReconcileRestorationLegacy(ctx context.Context, task, entryID, operationID uint, in c.Resolution, now time.Time) error {
	if !validResolution(in) || !c.ValidRecoveryEvidence(in.Evidence) || in.Decision != "failed" || in.ThreadID != 0 {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var entry d.RestorationEntry
		if err := tx.Where("id = ? AND task_id = ?", entryID, task).Take(&entry).Error; err != nil {
			return c.ErrDenied
		}
		var plan d.RestorationCheckpoint
		if err := loadRestoration(tx, task, []uint{entry.TargetID}, &plan); err != nil {
			return err
		}
		if in.ActorID != plan.ActorID || entry.Legacy == 0 {
			return c.ErrDenied
		}
		if err := s.checkRestoration(tx, restorationPlan(plan), now); err != nil {
			return err
		}
		if err := s.authorizeSessionHistory(tx, plan, entry.SourceSessionID, now); err != nil {
			return err
		}
		changed := tx.Model(&d.TopicOperation{}).Where("id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND route_version <= ? AND retired = ? AND state IN ? AND version = ? AND action IN ?", operationID, plan.SourceBotID, entry.SourceSessionID, plan.TargetID, plan.SourceRouteVersion, true, []string{"creating", "unknown"}, in.ExpectedVersion, []string{"", "create"}).Updates(map[string]any{"state": "failed", "version": in.ExpectedVersion + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		if err := tx.Model(&entry).Update("legacy", entry.Legacy-1).Error; err != nil {
			return err
		}
		return auditResolution(tx, plan.SourceBotID, operationID, "restoration_legacy", in, now)
	})
}

var _ c.RestorationStore = (*Store)(nil)
