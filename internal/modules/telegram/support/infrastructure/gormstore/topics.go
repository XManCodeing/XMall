package gormstore

import (
	"context"
	"errors"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ClaimTopic serializes one creation slot. Replaying a claim never grants Execute
// again; even the same caller must reconcile an uncertain first API request.
func (s *Store) ClaimTopic(ctx context.Context, bot, session, target uint, route, generation uint64, chat int64, claim string, now time.Time, ttl time.Duration) (out c.TopicClaim, err error) {
	if bot == 0 || session == 0 || target == 0 || route == 0 || chat >= 0 || !validLease(claim, ttl) {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		if e := s.checkRoute(tx, bot, target, c.DeliveryBinding{ChatID: chat, RouteVersion: route}, now); e != nil {
			return e
		}
		if blocked, e := topicRetryBlocked(tx, bot, now); e != nil || blocked {
			return e
		}
		lock := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND state IN ?", session, bot, []string{"open", "waiting_verification"}).UpdateColumn("version", gorm.Expr("version + 1"))
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return c.ErrDenied
		}
		var owner d.Session
		if e := tx.Where("id = ? AND bot_id = ?", session, bot).First(&owner).Error; e != nil {
			return e
		}
		if e := s.checkSupportBlock(tx, bot, owner.UserID, now); e != nil {
			return e
		}
		operation, e := nextTopicSlot(tx, bot, session, target, route, chat)
		if errors.Is(e, gorm.ErrRecordNotFound) {
			operation = d.TopicOperation{SlotVersion: operation.SlotVersion, BotID: bot, SessionID: session, TargetID: target, RouteVersion: route, ChatID: chat, State: "creating", Action: "create", Version: 1, Generation: generation, Claim: claim, LeaseUntil: now.Add(ttl), CreatedAt: now, UpdatedAt: now}
			if e = tx.Create(&operation).Error; e != nil {
				return e
			}
			out.Execute = true
		} else if e != nil {
			return e
		} else {
			if operation.ChatID != chat {
				return c.ErrDenied
			}
			if operation.State == "creating" && !now.Before(operation.LeaseUntil) {
				operation.State = "unknown"
				operation.Version++
				operation.UpdatedAt = now
				if e = tx.Save(&operation).Error; e != nil {
					return e
				}
			}
			if operation.State == "failed" && operation.Action == "create" {
				operation.State = "creating"
				operation.Version++
				operation.Generation = generation
				operation.Claim = claim
				operation.LeaseUntil = now.Add(ttl)
				operation.UpdatedAt = now
				if e = tx.Save(&operation).Error; e != nil {
					return e
				}
				out.Execute = true
			}
		}
		out.Operation = operation
		var mapping d.TopicMapping
		e = tx.Where("operation_id = ?", operation.ID).First(&mapping).Error
		if e == nil {
			out.Mapping = &mapping
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		return nil
	})
	return
}

// ExpireTopicClaims marks a bounded batch of abandoned local intents unknown.
// bot/generation must match the active runtime; expired claims retain their
// original generation and claim so late proven responses remain attributable.
func (s *Store) ExpireTopicClaims(ctx context.Context, bot uint, generation uint64, now time.Time, limit int) error {
	if bot == 0 || generation == 0 || now.IsZero() || limit < 1 || limit > 100 {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := runtimeFence(tx, bot, generation); err != nil {
			return err
		}
		var ids []uint
		if err := tx.Model(&d.TopicOperation{}).Where("bot_id = ? AND state = ? AND lease_until <= ?", bot, "creating", now).
			Order("id ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&d.TopicOperation{}).Where("id IN ? AND bot_id = ? AND state = ? AND lease_until <= ?", ids, bot, "creating", now).
			Updates(map[string]any{"state": "unknown", "version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
}

// validLease rejects unbounded or empty claims before any database work.
func validLease(claim string, ttl time.Duration) bool {
	return strings.TrimSpace(claim) != "" && len(claim) <= 64 && ttl >= time.Second && ttl <= 5*time.Minute
}

// BeginTopicAction persists close/reopen/delete intent before external I/O. A
// pending action immediately disables authorization until its outcome is known.
func (s *Store) BeginTopicAction(ctx context.Context, bot, topic uint, version, generation uint64, action, claim string, now time.Time, ttl time.Duration) (out c.TopicClaim, err error) {
	if !validLease(claim, ttl) || (action != "close" && action != "reopen" && action != "delete") {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		if blocked, e := topicRetryBlocked(tx, bot, now); e != nil || blocked {
			return e
		}
		var mapping d.TopicMapping
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND version = ?", topic, bot, version).First(&mapping).Error; e != nil {
			return c.ErrConflict
		}
		if (action == "close" && mapping.State != "open") || (action == "reopen" && mapping.State != "closed") || (action == "delete" && mapping.State != "open" && mapping.State != "closed") {
			return c.ErrDenied
		}
		if e := s.checkRoute(tx, bot, mapping.TargetID, bindingOf(mapping), now); e != nil {
			return e
		}
		var operation d.TopicOperation
		if e := tx.First(&operation, mapping.OperationID).Error; e != nil {
			return e
		}
		if operation.State == "creating" || operation.State == "unknown" {
			return c.ErrConflict
		}
		operation.Action = action
		operation.State = "creating"
		operation.Version++
		operation.Generation = generation
		operation.Claim = claim
		operation.LeaseUntil = now.Add(ttl)
		operation.UpdatedAt = now
		// Preserve the previous mapping state for a proven-no-effect failure.
		mapping.State = action + "_from_" + mapping.State
		mapping.Version++
		mapping.UpdatedAt = now
		if e := tx.Save(&operation).Error; e != nil {
			return e
		}
		if e := tx.Save(&mapping).Error; e != nil {
			return e
		}
		out = c.TopicClaim{Operation: operation, Mapping: &mapping, Execute: true}
		return nil
	})
	return
}

// FinishTopic accepts only the claimed generation. Late successful responses
// retain their concrete thread ID; an elapsed lease without proof stays unknown.
func (s *Store) FinishTopic(ctx context.Context, bot, id uint, generation uint64, claim string, result c.TopicResult, now time.Time) error {
	if result.State != "succeeded" && result.State != "failed" && result.State != "unknown" {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		var operation d.TopicOperation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND generation = ? AND claim = ? AND state IN ?", id, bot, generation, claim, []string{"creating", "unknown"}).First(&operation).Error; e != nil {
			return c.ErrConflict
		}
		if operation.State == "unknown" && result.State != "succeeded" {
			return c.ErrConflict
		}
		return applyTopicResult(tx, &operation, result, now)
	})
}

// applyTopicResult writes the mapping and operation atomically. Physical topic
// uniqueness includes historical/deleted mappings, preventing reply reassignment.
func applyTopicResult(tx *gorm.DB, operation *d.TopicOperation, result c.TopicResult, now time.Time) error {
	if operation.Retired {
		return c.ErrDenied
	}
	if result.RetryAfterSeconds < 0 || (result.RetryAfterSeconds > 0 && result.State != "failed") {
		return c.ErrDenied
	}
	if result.RetryAfterSeconds > 0 {
		// Saturate pathological platform values rather than overflow into an immediate retry.
		seconds := result.RetryAfterSeconds
		if seconds > int64((1<<63-1)/int64(time.Second)) {
			seconds = int64((1<<63 - 1) / int64(time.Second))
		}
		deadline := now.Add(time.Duration(seconds) * time.Second)
		operation.RetryAfterSeconds = result.RetryAfterSeconds
		operation.RetryNotBefore = &deadline
	}
	var mapping d.TopicMapping
	err := tx.Where("operation_id = ?", operation.ID).First(&mapping).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if mapping.State == "retired" {
		return c.ErrDenied // Disaster isolation is permanent, including late receipts.
	}
	if operation.Action == "create" && result.State == "succeeded" {
		if result.ThreadID <= 0 || mapping.ID != 0 {
			return c.ErrDenied
		}
		mapping = d.TopicMapping{OperationID: operation.ID, BotID: operation.BotID, SessionID: operation.SessionID, TargetID: operation.TargetID, ChatID: operation.ChatID, ThreadID: result.ThreadID, RouteVersion: operation.RouteVersion, State: "open", Version: 1, CreatedAt: now, UpdatedAt: now}
		if err = tx.Create(&mapping).Error; err != nil {
			return err
		}
	} else if operation.Action != "create" {
		if mapping.ID == 0 {
			return c.ErrDenied
		}
		if result.State == "succeeded" {
			switch operation.Action {
			case "close":
				mapping.State = "closed"
			case "reopen":
				mapping.State = "open"
			case "delete":
				mapping.State = "deleted"
			default:
				return c.ErrDenied
			}
		} else if result.State == "failed" {
			_, previous, ok := strings.Cut(mapping.State, "_from_")
			if !ok || (previous != "open" && previous != "closed") {
				return c.ErrDenied
			}
			mapping.State = previous
		}
		// Unknown keeps the action_from_previous marker; it is never send-authorized.
		mapping.Version++
		mapping.UpdatedAt = now
		if err = tx.Save(&mapping).Error; err != nil {
			return err
		}
	}
	operation.State = result.State
	operation.Version++
	operation.UpdatedAt = now
	return tx.Save(operation).Error
}

// ResolveTopic is an administrator-bound CAS, not a retry button. Confirming no
// effect permits a later claim; confirming success requires the concrete topic.
func (s *Store) ResolveTopic(ctx context.Context, bot, id uint, resolution c.Resolution, now time.Time) error {
	if !validResolution(resolution) || (resolution.Decision != "succeeded" && resolution.Decision != "failed") {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		lock := tx.Model(&d.TopicOperation{}).Where("id = ? AND bot_id = ? AND version = ? AND state IN ?", id, bot, resolution.ExpectedVersion, []string{"unknown", "creating"}).UpdateColumn("version", gorm.Expr("version"))
		if lock.Error != nil {
			return lock.Error
		}
		var operation d.TopicOperation
		if e := tx.Where("id = ? AND bot_id = ? AND version = ?", id, bot, resolution.ExpectedVersion).First(&operation).Error; e != nil {
			return c.ErrConflict
		}
		if operation.State != "unknown" && (operation.State != "creating" || now.Before(operation.LeaseUntil)) {
			return c.ErrConflict
		}
		if e := applyTopicResult(tx, &operation, c.TopicResult{State: resolution.Decision, ThreadID: resolution.ThreadID}, now); e != nil {
			return e
		}
		return auditResolution(tx, bot, id, "topic", resolution, now)
	})
}

// validResolution bounds audit input; actual administrator permissions are
// checked by the caller before entering these repository methods.
func validResolution(in c.Resolution) bool {
	return in.ActorID > 0 && in.ExpectedVersion > 0 && strings.TrimSpace(in.Evidence) != "" && len(in.Evidence) <= 512
}
func auditResolution(tx *gorm.DB, bot, id uint, resource string, in c.Resolution, now time.Time) error {
	return tx.Create(&d.ResolutionAudit{BotID: bot, Resource: resource, ResourceID: id, Version: in.ExpectedVersion, ActorID: in.ActorID, Decision: in.Decision, Evidence: in.Evidence, CreatedAt: now}).Error
}
func bindingOf(topic d.TopicMapping) c.DeliveryBinding {
	return c.DeliveryBinding{ChatID: topic.ChatID, ThreadID: topic.ThreadID, RouteVersion: topic.RouteVersion}
}

// ObserveTopic applies a native service event to an exact existing mapping.
// The update watermark is persisted even for suppressed observations: replaying
// an event received during a local operation must not undo its eventual result.
// Platform dates are second-resolution, so the entire local-operation second is
// a conservative barrier. Pending/unknown/delete operations are never resolved.
func (s *Store) ObserveTopic(ctx context.Context, event c.TopicEvent, now time.Time) error {
	update := event.Update
	service := update.TopicService
	if event.TargetID == 0 || event.RouteVersion == 0 || now.IsZero() || update.ID <= 0 ||
		update.Type != "message" || update.MessageKind != "service" || update.Private || update.ChatType != "supergroup" ||
		update.ChatID >= 0 || update.ThreadID <= 0 || !update.IsTopicMessage || update.MessageID <= 0 ||
		service == nil || (service.State != "closed" && service.State != "open") || service.Date <= 0 || service.Date > now.Unix() {
		return c.ErrDenied
	}
	if err := r.CheckExecutionFence(ctx, event.BotID, event.Generation); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		// The execution fence renews Redis and reads through the runtime root pool.
		// Keep it before BEGIN; revalidate persisted generation on this transaction
		// instead, so SQLite's only connection cannot wait for itself.
		if err := runtimeFence(tx, event.BotID, event.Generation); err != nil {
			return err
		}
		binding := c.DeliveryBinding{ChatID: update.ChatID, ThreadID: update.ThreadID, RouteVersion: event.RouteVersion}
		if err := s.checkRoute(tx, event.BotID, event.TargetID, binding, now); err != nil {
			return err
		}
		var mapping d.TopicMapping
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND chat_id = ? AND thread_id = ? AND target_id = ? AND route_version = ?",
			event.BotID, update.ChatID, update.ThreadID, event.TargetID, event.RouteVersion).First(&mapping).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // unknown create: never infer the owner from an arbitrary thread
		}
		if err != nil || update.ID <= mapping.LastEventUpdateID {
			return err
		}
		var operation d.TopicOperation
		if err = tx.Where("id = ? AND bot_id = ? AND chat_id = ? AND target_id = ? AND route_version = ?", mapping.OperationID,
			event.BotID, update.ChatID, event.TargetID, event.RouteVersion).First(&operation).Error; err != nil {
			return err
		}
		changes := map[string]any{"last_event_update_id": update.ID}
		// Do not compare mapping.UpdatedAt: it includes receipt times of earlier
		// service events, which may legitimately arrive out of chronological order.
		if (mapping.State == "open" || mapping.State == "closed") &&
			(operation.State == "succeeded" || operation.State == "failed") && service.Date > operation.UpdatedAt.Unix() {
			changes["state"] = service.State
			changes["version"] = gorm.Expr("version + 1")
			changes["updated_at"] = now
		}
		return tx.Model(&d.TopicMapping{}).Where("id = ? AND version = ? AND last_event_update_id < ?", mapping.ID, mapping.Version, update.ID).Updates(changes).Error
	})
}

var _ c.TopicEvents = (*Store)(nil)
var _ c.Topics = (*Store)(nil)

// topicRetryBlocked fences persisted bot-wide deadlines and serializes in-flight calls.
// tx is already fenced; now is the trusted worker clock. True forbids another call.
func topicRetryBlocked(tx *gorm.DB, bot uint, now time.Time) (bool, error) {
	var count int64
	err := tx.Model(&d.TopicOperation{}).Where("bot_id = ? AND (retry_not_before > ? OR (state = ? AND lease_until > ?))", bot, now, "creating", now).Count(&count).Error
	return count > 0, err
}

// deletedTopicSlot returns true only for a proven deletion with retained ownership.
func deletedTopicSlot(tx *gorm.DB, operation d.TopicOperation) (bool, error) {
	if operation.Action != "delete" || operation.State != "succeeded" {
		return false, nil
	}
	var mapping d.TopicMapping
	err := tx.Where("operation_id = ? AND bot_id = ? AND session_id = ? AND chat_id = ? AND route_version = ? AND state = ?", operation.ID, operation.BotID, operation.SessionID, operation.ChatID, operation.RouteVersion, "deleted").Take(&mapping).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, c.ErrConflict
	}
	return err == nil, err
}

// nextTopicSlot reads the latest serialized slot. ErrRecordNotFound authorizes
// insertion at the returned revision only when no slot or a proven deletion exists.
func nextTopicSlot(tx *gorm.DB, bot, session, target uint, route uint64, chat int64) (d.TopicOperation, error) {
	var operation d.TopicOperation
	err := tx.Where("bot_id = ? AND session_id = ? AND target_id = ? AND route_version = ?", bot, session, target, route).Order("slot_version DESC").First(&operation).Error
	if err != nil {
		return operation, err
	}
	if operation.Retired {
		return operation, c.ErrDenied
	}
	if operation.ChatID != chat {
		return operation, c.ErrDenied
	}
	replacement, err := deletedTopicSlot(tx, operation)
	if err != nil || !replacement {
		return operation, err
	}
	if operation.SlotVersion >= uint64(1<<63-1) {
		return operation, c.ErrConflict
	}
	return d.TopicOperation{SlotVersion: operation.SlotVersion + 1}, gorm.ErrRecordNotFound
}

// ConfirmTopicDeleted records an administrator's explicit verified deletion.
// bot/topic identify server-owned data; in binds physical IDs, route, generation,
// mapping CAS and audit evidence. Unknown/in-flight calls cannot be adjudicated
// here. Returns an error on stale/ambiguous inputs, without external side effects.
func (s *Store) ConfirmTopicDeleted(ctx context.Context, bot, topic uint, in c.TopicDeletionConfirmation, now time.Time) error {
	if bot == 0 || topic == 0 || in.Generation == 0 || in.ActorID == 0 || in.ExpectedVersion == 0 ||
		in.ExpectedVersion >= uint64(1<<63-1) || in.ChatID >= 0 || in.ThreadID <= 0 || in.RouteVersion == 0 ||
		in.Evidence == "" || len(in.Evidence) > 512 || now.IsZero() {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := runtimeFence(tx, bot, in.Generation); err != nil {
			return err
		}
		var mapping d.TopicMapping
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND version = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state IN ?", topic, bot, in.ExpectedVersion, in.ChatID, in.ThreadID, in.RouteVersion, []string{"open", "closed"}).Take(&mapping).Error; err != nil {
			return c.ErrConflict
		}
		if err := s.operationRoute(tx, mapping, now); err != nil {
			return err
		}
		var operation d.TopicOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND session_id = ? AND chat_id = ? AND route_version = ? AND state IN ?", mapping.OperationID, bot, mapping.SessionID, mapping.ChatID, mapping.RouteVersion, []string{"succeeded", "failed"}).Take(&operation).Error; err != nil {
			return c.ErrConflict
		}
		if (in.OperationID != 0 && operation.ID != in.OperationID) || (in.OperationVersion != 0 && operation.Version != in.OperationVersion) {
			return c.ErrConflict
		}
		if operation.Version >= uint64(1<<63-1) {
			return c.ErrConflict
		}
		changed := tx.Model(&mapping).Where("version = ?", in.ExpectedVersion).Updates(map[string]any{"state": "deleted", "version": in.ExpectedVersion + 1, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return c.ErrConflict
		}
		if err := tx.Model(&operation).Updates(map[string]any{"action": "delete", "state": "succeeded", "version": operation.Version + 1, "generation": in.Generation, "updated_at": now}).Error; err != nil {
			return err
		}
		return auditResolution(tx, bot, topic, "topic_deleted", c.Resolution{ActorID: in.ActorID, ExpectedVersion: in.ExpectedVersion, Decision: "deleted", Evidence: in.Evidence}, now)
	})
}

// topicMappingSnapshot attaches only exact operation-owned metadata to a detail.
// Missing mapping is normal for unproven creation; database errors fail the read.
func (s *AdminStore) topicMappingSnapshot(ctx context.Context, row *c.AdminTopicOperation) error {
	row.OperationVersion = strconv.FormatUint(row.Version, 10)
	var mapping c.AdminTopicMapping
	err := s.db.WithContext(ctx).Model(&d.TopicMapping{}).Select("id, version, chat_id, thread_id, route_version, state").Where("operation_id = ? AND bot_id = ? AND session_id = ? AND target_id = ? AND route_version = ?", row.ID, row.BotID, row.SessionID, row.TargetID, row.RouteVersion).Take(&mapping).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err == nil {
		row.Mapping = &mapping
	}
	return err
}

var _ c.TopicDeletions = (*Store)(nil)

// stoppedDisasterSource requires persisted recovery ownership and actual runtime
// generation exit proofs, never inferring drain from stopped/disabled alone.
func (s *Store) stoppedDisasterSource(tx *gorm.DB, bot uint, generation uint64) error {
	if s.drains == nil {
		return c.ErrDenied
	}
	port := s.drains(tx)
	if port == nil {
		return c.ErrDenied
	}
	var row struct {
		ID, RecoveryTaskID  uint
		Version, Generation uint64
	}
	if err := tx.Table("telegram_bot_instances").Where("id = ? AND generation = ? AND mode = ? AND state = ?", bot, generation, "internal", "stopped").Take(&row).Error; err != nil {
		return c.ErrConflict
	}
	if row.RecoveryTaskID == 0 {
		return c.ErrDenied
	}
	return port.RecoveryDrained(tx.Statement.Context, r.RecoveryFence{BotID: bot, TaskID: row.RecoveryTaskID, Version: row.Version, Generation: generation})
}

// BeginDisasterContinuation snapshots all source support families, without sending
// or copying destination data. A source permits one immutable plan only; identical
// retries return its durable progress. Caller must authorize and drain the runtime.
func (s *Store) BeginDisasterContinuation(ctx context.Context, in c.DisasterContinuationInput, now time.Time) (out c.DisasterProgress, err error) {
	if !validDisasterKey(in.Key) || in.SourceBotID == 0 || in.DestinationBotID == 0 || in.ActorID == 0 || in.SourceGeneration == 0 || !c.ValidRecoveryEvidence(in.Evidence) || now.IsZero() {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if err := s.stoppedDisasterSource(tx, in.SourceBotID, in.SourceGeneration); err != nil {
			return err
		}
		var row d.DisasterCheckpoint
		err := tx.Where(clause.Or(clause.Eq{Column: clause.Column{Name: "key"}, Value: in.Key}, clause.Eq{Column: clause.Column{Name: "source_bot_id"}, Value: in.SourceBotID})).Take(&row).Error
		if err == nil {
			if row.Key != in.Key || row.SourceBotID != in.SourceBotID || row.DestinationBotID != in.DestinationBotID || row.SourceGeneration != in.SourceGeneration || row.ActorID != in.ActorID || row.Evidence != in.Evidence {
				return c.ErrConflict
			}
			out = disasterProgress(row)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var destination int64
		if err = tx.Table("telegram_bot_instances").Where("id = ? AND mode = ?", in.DestinationBotID, "internal").Count(&destination).Error; err != nil {
			return err
		}
		if destination != 1 {
			return c.ErrDenied
		}
		row = d.DisasterCheckpoint{Key: in.Key, SourceBotID: in.SourceBotID, DestinationBotID: in.DestinationBotID, SourceGeneration: in.SourceGeneration, ActorID: in.ActorID, Evidence: in.Evidence, Revision: 1, Phase: "topics", CreatedAt: now, UpdatedAt: now}
		if err = captureDisasterBounds(tx, &row); err != nil {
			return err
		}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		out = disasterProgress(row)
		return nil
	})
	if err != nil {
		out = c.DisasterProgress{}
	} else {
		slog.InfoContext(ctx, "support_disaster_checkpoint_persisted", "revision", out.Revision, "phase", out.Phase, "local_complete", out.LocalComplete)
	}
	return
}

// validDisasterKey validates an opaque ASCII identifier; no customer data belongs here.
func validDisasterKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for _, char := range key {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

// captureDisasterBounds fixes indexed family high-water marks on the caller's tx.
// New rows are outside this finite plan; the source must remain drained throughout.
func captureDisasterBounds(tx *gorm.DB, row *d.DisasterCheckpoint) error {
	families := []struct {
		model any
		bound *uint
	}{
		{&d.TopicMapping{}, &row.TopicBound}, {&d.TopicOperation{}, &row.OperationBound},
		{&d.Session{}, &row.SessionBound}, {&d.BufferedMessage{}, &row.MessageBound},
		{&d.Downstream{}, &row.ReplyBound}, {&d.Challenge{}, &row.ChallengeBound},
	}
	for _, family := range families {
		if err := tx.Model(family.model).Where("bot_id = ?", row.SourceBotID).Select("COALESCE(MAX(id), 0)").Scan(family.bound).Error; err != nil {
			return err
		}
	}
	return nil
}

// disasterFamily returns the next finite phase and its immutable scan boundary.
func disasterFamily(row d.DisasterCheckpoint) (any, uint, string) {
	switch row.Phase {
	case "topics":
		return &d.TopicMapping{}, row.TopicBound, "operations"
	case "operations":
		return &d.TopicOperation{}, row.OperationBound, "messages"
	case "messages":
		return &d.BufferedMessage{}, row.MessageBound, "replies"
	case "replies":
		return &d.Downstream{}, row.ReplyBound, "challenges"
	case "challenges":
		return &d.Challenge{}, row.ChallengeBound, "sessions"
	case "sessions":
		return &d.Session{}, row.SessionBound, "local_complete"
	}
	return nil, 0, ""
}

// disasterProgress exposes local isolation counters, never delivery completion.
func disasterProgress(row d.DisasterCheckpoint) c.DisasterProgress {
	_, bound, _ := disasterFamily(row)
	return c.DisasterProgress{Key: row.Key, Revision: row.Revision, Phase: row.Phase, Cursor: row.Cursor, UpperBound: bound,
		LocalComplete: row.Phase == "local_complete", RetiredTopics: row.RetiredTopics, ClosedSessions: row.ClosedSessions,
		CancelledMessages: row.CancelledMessages, RetainedMessages: row.RetainedMessages, RetainedReplies: row.RetainedReplies,
		UncertainTopics: row.UncertainTopics, ContinuationPolicy: "fresh_consultation_only"}
}

// DisasterContinuationProgress reads durable progress for a caller-authorized key.
// Missing keys return the storage error; progress contains no platform receipts.
func (s *Store) DisasterContinuationProgress(ctx context.Context, key string) (c.DisasterProgress, error) {
	if !validDisasterKey(key) {
		return c.DisasterProgress{}, c.ErrDenied
	}
	var row d.DisasterCheckpoint
	if err := s.db.WithContext(ctx).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Take(&row).Error; err != nil {
		return c.DisasterProgress{}, err
	}
	return disasterProgress(row), nil
}

// AdvanceDisasterContinuation processes at most limit (1..100) rows in one family.
// CAS, effects and progress commit atomically; errors return no completion claim.
// No network or global DB access occurs inside this transaction.
func (s *Store) AdvanceDisasterContinuation(ctx context.Context, key string, revision uint64, limit int, now time.Time) (out c.DisasterProgress, err error) {
	if !validDisasterKey(key) || revision == 0 || revision >= uint64(1<<63-1) || limit < 1 || limit > 100 || now.IsZero() {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var row d.DisasterCheckpoint
		if err := tx.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Take(&row).Error; err != nil {
			return err
		}
		if err := s.stoppedDisasterSource(tx, row.SourceBotID, row.SourceGeneration); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("revision = ?", revision).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Take(&row).Error; err != nil {
			return c.ErrConflict
		}
		if row.Phase == "local_complete" {
			out = disasterProgress(row)
			return nil
		}
		model, bound, next := disasterFamily(row)
		if model == nil {
			return c.ErrConflict
		}
		var ids []uint
		if err := tx.Model(model).Where("bot_id = ? AND id > ? AND id <= ?", row.SourceBotID, row.Cursor, bound).Order("id ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := isolateDisasterPage(tx, &row, model, ids, now); err != nil {
				return err
			}
			row.Cursor = ids[len(ids)-1]
		}
		if len(ids) < limit || row.Cursor == bound {
			row.Phase, row.Cursor = next, 0
		}
		row.Revision++
		row.UpdatedAt = now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = disasterProgress(row)
		return nil
	})
	if err != nil {
		out = c.DisasterProgress{}
	} else {
		slog.InfoContext(ctx, "support_disaster_checkpoint_persisted", "revision", out.Revision, "phase", out.Phase, "local_complete", out.LocalComplete)
	}
	return
}

// isolateDisasterPage updates only bounded local metadata. Original unknown
// outcomes, physical IDs and dispatch evidence remain in the old scope for audit.
func isolateDisasterPage(tx *gorm.DB, row *d.DisasterCheckpoint, model any, ids []uint, now time.Time) error {
	query := tx.Model(model).Where("bot_id = ? AND id IN ?", row.SourceBotID, ids)
	changes := map[string]any{"updated_at": now}
	switch row.Phase {
	case "topics":
		changes["state"], changes["version"] = "retired", gorm.Expr("version + 1")
		row.RetiredTopics += uint64(len(ids))
	case "operations":
		var count int64
		if err := query.Session(&gorm.Session{}).Where("state IN ?", []string{"unknown", "creating"}).Count(&count).Error; err != nil {
			return err
		}
		row.UncertainTopics += uint64(count)
		changes["retired"], changes["version"] = true, gorm.Expr("version + 1")
	case "messages":
		return isolateDisasterMessages(tx, row, ids, now)
	case "replies":
		var count int64
		if err := query.Where("state IN ?", []string{"pending", "unknown", "failed"}).Count(&count).Error; err != nil {
			return err
		}
		row.RetainedReplies += uint64(count)
		return nil
	case "challenges":
		changes["notice_pending"] = false
		if err := query.Session(&gorm.Session{}).Updates(changes).Error; err != nil {
			return err
		}
		query = query.Where("state = ?", "pending")
		changes["state"], changes["version"] = "cancelled", gorm.Expr("version + 1")
	case "sessions":
		changes["state"], changes["challenge_id"], changes["version"] = "closed", 0, gorm.Expr("version + 1")
		row.ClosedSessions += uint64(len(ids))
	default:
		return c.ErrDenied
	}
	return query.Updates(changes).Error
}

// isolateDisasterMessages cancels never-prepared sources only. Prepared, sending
// and unknown work retains its exact payload/state and is never replayed here.
func isolateDisasterMessages(tx *gorm.DB, row *d.DisasterCheckpoint, ids []uint, now time.Time) error {
	query := tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND id IN ?", row.SourceBotID, ids)
	if err := query.Session(&gorm.Session{}).Updates(map[string]any{"feedback_pending": false}).Error; err != nil {
		return err
	}
	safe := query.Session(&gorm.Session{}).Where("state IN ? AND delivery_id = 0", []string{"buffered", "ready", "claimed"}).Where("NOT EXISTS (SELECT 1 FROM telegram_support_ready_dispatches p WHERE p.message_id = telegram_support_messages.id)")
	// A legacy runtime row also prevents assuming no external attempt. Avoid
	// database-specific concatenation by checking the exact business key per row.
	var candidates []d.BufferedMessage
	if err := safe.Session(&gorm.Session{}).Find(&candidates).Error; err != nil {
		return err
	}
	cancelIDs := make([]uint, 0, len(candidates))
	for _, message := range candidates {
		var attempts int64
		if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ?", row.SourceBotID).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: c.ReadyBusinessRef(message.SessionID, message.ID)}).Count(&attempts).Error; err != nil {
			return err
		}
		if attempts == 0 {
			cancelIDs = append(cancelIDs, message.ID)
		}
	}
	changed := safe.Where("id IN ?", cancelIDs).Updates(map[string]any{"state": "cancelled", "reason": "disaster_retired", "claim": "", "lease_until": nil, "updated_at": now})
	if changed.Error != nil {
		return changed.Error
	}
	row.CancelledMessages += uint64(changed.RowsAffected)
	var count int64
	if err := query.Where("state IN ?", []string{"buffered", "ready", "claimed", "sending", "unknown", "failed"}).Count(&count).Error; err != nil {
		return err
	}
	row.RetainedMessages += uint64(count)
	return nil
}

var _ c.DisasterContinuation = (*Store)(nil)
