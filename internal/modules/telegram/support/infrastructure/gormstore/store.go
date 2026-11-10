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
	"strconv"
	"strings"
	"time"
)

type Store struct {
	db                  *gorm.DB
	cipher              c.Cipher
	routes              RouteReaderFactory
	encoder             ReadyEncoder
	recovery            DeliveryRecoveryFactory
	restoration         RestorationGuardFactory
	drains              RecoveryDrainFactory
	policyRecoveryGuard func(*gorm.DB, uint) error
}

// Models exposes the downstream recovery ledger to explicit migration owners.
// Existing support models remain registered by the versioned migration registry.
func Models() []any { return []any{&d.RestorationDownstreamLiability{}} }

// registerDownstreamPage snapshots at most limit frozen source rows for one
// new entry. A ledger insert and its counter increment share the checkpoint CAS.
// Old aggregate-only entries are never selected or backfilled.
func registerDownstreamPage(tx *gorm.DB, plan d.RestorationCheckpoint, limit int, now time.Time) error {
	var entry d.RestorationEntry
	err := tx.Where("task_id = ? AND target_id = ? AND downstream_tracking_version = 1 AND downstream_scanned = ?", plan.TaskID, plan.TargetID, false).Order("id ASC").Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var rows []d.Downstream
	if err = tx.Where("bot_id = ? AND session_id = ? AND id > ? AND id <= ?", plan.SourceBotID, entry.SourceSessionID, entry.DownstreamCursor, entry.DownstreamUpperBound).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	var counted uint64
	for _, row := range rows {
		entry.DownstreamCursor = row.ID
		if row.State != "pending" && row.State != "unknown" && row.State != "failed" {
			continue
		}
		liability := d.RestorationDownstreamLiability{TaskID: plan.TaskID, TargetID: plan.TargetID, EntryID: entry.ID, SourceBotID: plan.SourceBotID, SourceSessionID: entry.SourceSessionID, DownstreamID: row.ID, SourceVersion: row.Version, SourceState: row.State, TopicID: row.TopicID, RouteVersion: row.RouteVersion, AlbumID: row.AlbumID, State: "pending", Version: 1, CreatedAt: now, UpdatedAt: now}
		// A uniqueness violation is a conflicting source snapshot, not permission
		// to silently increment the aggregate a second time.
		if err = tx.Create(&liability).Error; err != nil {
			return err
		}
		counted++
	}
	entry.DownstreamScanned = len(rows) < limit || entry.DownstreamCursor == entry.DownstreamUpperBound
	return tx.Model(&entry).Updates(map[string]any{"downstream_cursor": entry.DownstreamCursor, "downstream_scanned": entry.DownstreamScanned, "legacy": gorm.Expr("legacy + ?", counted), "updated_at": now}).Error
}

// advanceDownstreamLiabilities registers one bounded source page and consumes
// one bounded receipt page under the existing guard and checkpoint writer lock.
// The independent cursor revisits invalid receipts only after reaching the end.
func (s *Store) advanceDownstreamLiabilities(tx *gorm.DB, plan *d.RestorationCheckpoint, limit int, now time.Time) error {
	if err := registerDownstreamPage(tx, *plan, limit, now); err != nil {
		return err
	}
	var rows []d.RestorationDownstreamLiability
	if err := tx.Where("task_id = ? AND target_id = ? AND state = ? AND id > ?", plan.TaskID, plan.TargetID, "pending", plan.DownstreamReceiptCursor).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.authorizeSessionHistory(tx, *plan, row.SourceSessionID, now); err != nil {
			return err
		}
		err := tx.Transaction(func(itemTx *gorm.DB) error { return s.consumeDownstreamLiability(itemTx, *plan, row, now) })
		if err != nil && !errors.Is(err, c.ErrDenied) {
			return err
		}
	}
	plan.DownstreamReceiptCursor = 0
	if len(rows) == limit {
		plan.DownstreamReceiptCursor = rows[len(rows)-1].ID
	}
	return nil
}

// recordBlockedInput persists a body-free tombstone for update using the locked
// session and trusted policy/clock. It returns database errors; replay is a no-op.
func recordBlockedInput(tx *gorm.DB, session d.Session, update r.Update, policy c.Policy, now time.Time) error {
	var prior d.BufferedMessage
	err := tx.Where("bot_id = ? AND update_id = ?", session.BotID, update.ID).First(&prior).Error
	if err == nil {
		if prior.SessionID != session.ID || prior.UserID != update.UserID {
			return c.ErrDenied
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var recent int64
	if err = tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND user_id = ? AND reason = ? AND created_at > ?", session.BotID, update.UserID, "blocked", now.Add(-policy.Cooldown)).Count(&recent).Error; err != nil {
		return err
	}
	// Callback answers use Telegram's bounded callback channel, not a fresh notice.
	row := d.BufferedMessage{BotID: session.BotID, UpdateID: update.ID, SessionID: session.ID,
		UserID: update.UserID, ChatID: update.ChatID, Locale: update.Locale, State: "rejected", Reason: "blocked",
		FeedbackPending: recent == 0 && update.CallbackID == "", CreatedAt: now, UpdatedAt: now, RetainUntil: now.Add(policy.Retention)}
	return tx.Create(&row).Error
}

// CheckNotice checks notice against the current block on the same connection.
// Only persisted fixed restriction feedback survives a block; errors fail closed.
func (s *Store) CheckNotice(ctx context.Context, notice c.Notice, now time.Time) error {
	if notice.BotID == 0 || notice.UserID <= 0 || notice.ChatID != notice.UserID {
		return c.ErrDenied
	}
	if notice.Kind == "feedback" && notice.Reason == "blocked" {
		return nil
	}
	return s.checkSupportBlock(s.db.WithContext(ctx), notice.BotID, notice.UserID, now)
}

// cancelBlockedChallenges permanently invalidates queued verification for the
// supplied bot/session. Unblocking must not revive these challenge references.
func cancelBlockedChallenges(tx *gorm.DB, bot, session uint, now time.Time) error {
	return tx.Model(&d.Challenge{}).Where("bot_id = ? AND session_id = ? AND state = ?", bot, session, "pending").Updates(map[string]any{
		"state": "cancelled", "notice_pending": false, "version": gorm.Expr("version + 1"), "updated_at": now,
	}).Error
}

func New(db *gorm.DB, cipher ...c.Cipher) *Store {
	s := &Store{db: db}
	if len(cipher) > 0 {
		s.cipher = cipher[0]
	}
	return s
}

// The first write obtains SQLite's writer lock before any read; row update also
// serializes PostgreSQL/MySQL contenders without a process-local mutex.
func lockSession(tx *gorm.DB, bot uint, user, chat int64, now time.Time) (d.Session, error) {
	s := d.Session{BotID: bot, UserID: user, ChatID: chat, State: "waiting_verification", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "user_id"}}, DoNothing: true}).Create(&s).Error; err != nil {
		return s, err
	}
	if err := tx.Model(&d.Session{}).Where("bot_id = ? AND user_id = ?", bot, user).UpdateColumn("version", gorm.Expr("version + 1")).Error; err != nil {
		return s, err
	}
	s = d.Session{}
	err := tx.Where("bot_id = ? AND user_id = ?", bot, user).First(&s).Error
	if err == nil && s.ChatID != chat {
		err = c.ErrDenied
	}
	return s, err
}
func expireMessages(tx *gorm.DB, session uint, now time.Time) error {
	return tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND legacy_task_id = 0 AND state IN ? AND retain_until <= ?", session, []string{"buffered", "ready", "claimed"}, now).Updates(map[string]any{"state": "expired", "reason": "expired", "payload_cipher": "", "feedback_pending": true, "updated_at": now}).Error
}

// consultationChallenge loads or renews the challenge for the locked session.
// update/ref/policy/now are trusted ingress inputs; returns the challenge or DB error.
func consultationChallenge(tx *gorm.DB, session *d.Session, update r.Update, ref string, policy c.Policy, now time.Time) (d.Challenge, error) {
	var challenge d.Challenge
	if session.ChallengeID != 0 {
		if err := tx.First(&challenge, session.ChallengeID).Error; err != nil {
			return challenge, err
		}
	}
	if session.State == "open" && (challenge.State != "passed" || challenge.ConsumedAt == nil || c.VerificationDay(*challenge.ConsumedAt) != c.VerificationDay(now)) {
		session.State = "waiting_verification"
	}
	if challenge.ID != 0 && challenge.State == "pending" && !now.Before(challenge.ExpiresAt) {
		if err := failVerificationRound(tx, session.ID, "expired", now); err != nil {
			return challenge, err
		}
		challenge.State, challenge.NoticePending = "expired", true
		challenge.Version++
		if err := tx.Save(&challenge).Error; err != nil {
			return challenge, err
		}
	}
	if session.State != "open" && !now.Before(session.CooldownUntil) && (challenge.ID == 0 || challenge.State != "pending") {
		challenge = d.Challenge{NoticePending: true, Locale: update.Locale, Ref: ref, SessionID: session.ID, BotID: session.BotID, UserID: update.UserID, ChatID: update.ChatID, State: "pending", Version: 1, MaxAttempts: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
		if err := c.NewArithmetic(&challenge); err != nil {
			return challenge, err
		}
		if err := tx.Create(&challenge).Error; err != nil {
			return challenge, err
		}
		session.ChallengeID = challenge.ID
	}
	return challenge, nil
}

// finishVerification applies the checked challenge to its locked session/outcome.
// policy/now control expiry and cooldown; all mutations use tx and return DB errors.
func finishVerification(tx *gorm.DB, session *d.Session, challenge *d.Challenge, outcome *c.Outcome, correct bool, policy c.Policy, now time.Time) error {
	challenge.Attempts++
	if !now.Before(challenge.ExpiresAt) {
		challenge.State = "expired"
		challenge.NoticePending = true
		outcome.Reason = "expired"
	} else if !correct || challenge.Attempts > challenge.MaxAttempts {
		challenge.State = "locked"
		outcome.Reason = "wrong"
	} else {
		challenge.State = "passed"
		challenge.ConsumedAt = &now
		outcome.Reason = "passed"
		session.State = "open"
		session.Attempts = 0
	}
	challenge.Version++
	challenge.UpdatedAt = now
	if challenge.State != "passed" {
		session.State = "waiting_verification"
		session.CooldownUntil = now.Add(policy.Cooldown)
		if err := failVerificationRound(tx, session.ID, outcome.Reason, now); err != nil {
			return err
		}
	}
	challenge.NoticePending = challenge.State == "expired"
	if err := expireMessages(tx, session.ID, now); err != nil {
		return err
	}
	if challenge.State == "passed" {
		if err := tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND legacy_task_id = 0 AND state = ? AND retain_until > ?", session.ID, "buffered", now).Updates(map[string]any{"state": "ready", "released_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	if err := tx.Save(challenge).Error; err != nil {
		return err
	}
	outcome.Session = *session
	return tx.Save(session).Error
}

// Accept persists update under the bot/customer lock with policy limits.
// payload is already encrypted; returns the durable outcome or transaction error.
func (s *Store) Accept(ctx context.Context, bot uint, u r.Update, payload string, size int64, ref string, p c.Policy, now time.Time) (out c.Outcome, err error) {
	if bot == 0 || u.UserID <= 0 || u.ChatID != u.UserID || u.ID <= 0 || size < 0 || ref == "" || len(ref) > 32 || strings.Contains(ref, ":") {
		return out, c.ErrDenied
	}
	p = c.VerificationPolicy(p)
	generation, err := s.ingressGeneration(ctx, bot)
	if err != nil {
		return out, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ingressFence(tx, bot, generation); err != nil {
			return err
		}
		session, e := lockSession(tx, bot, u.UserID, u.ChatID, now)
		if e != nil {
			return e
		}
		if session.State == "closed" && (!u.Private || u.UserID <= 0 || u.ChatID != u.UserID || u.MessageID <= 0 || u.CallbackID != "") {
			return c.ErrDenied
		}
		if e = s.checkSupportBlock(tx, bot, u.UserID, now); e != nil {
			if errors.Is(e, c.ErrDenied) {
				out.Reason = "blocked"
				return recordBlockedInput(tx, session, u, p, now)
			}
			return e
		}
		session.Locale = u.Locale
		session.DisplayName = string([]rune(strings.TrimSpace(u.FirstName + " " + u.LastName)))
		if name := []rune(session.DisplayName); len(name) > 256 {
			session.DisplayName = string(name[:256])
		}
		session.Username = u.Username
		if name := []rune(session.Username); len(name) > 64 {
			session.Username = string(name[:64])
		}
		out.Session = session
		if e = expireMessages(tx, session.ID, now); e != nil {
			return e
		}
		var existing d.BufferedMessage
		e = tx.Where("bot_id = ? AND update_id = ?", bot, u.ID).First(&existing).Error
		if e == nil {
			if existing.SessionID != session.ID {
				return c.ErrDenied
			}
			out.Reason = existing.Reason
			if existing.State == "expired" {
				out.Reason = "expired"
			}
			if session.ChallengeID != 0 {
				var ch d.Challenge
				if e = tx.First(&ch, session.ChallengeID).Error; e != nil {
					return e
				}
				out.Challenge = &ch
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		// Only a fresh private consultation reopens the permanent customer slot.
		// Old callbacks/messages remain tombstones and cannot reopen it by replay.
		if session.State == "closed" {
			session.State = "waiting_verification"
			session.ChallengeID = 0
		}
		if c.ContentRejection(u) != "" {
			return persistConsultation(tx, &session, d.Challenge{}, u, "", 0, p, now, &out)
		}
		ch, e := consultationChallenge(tx, &session, u, ref, p, now)
		if e != nil {
			return e
		}
		return persistConsultation(tx, &session, ch, u, payload, size, p, now, &out)
	})
	return
}
func (s *Store) Challenge(ctx context.Context, ref string) (*d.Challenge, error) {
	var ch d.Challenge
	err := s.db.WithContext(ctx).Where("ref = ?", ref).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, c.ErrDenied
	}
	return &ch, err
}

// Verify checks update/ref against the bot/customer challenge using policy/now.
// It returns a durable outcome or transaction error without reopening old input.
func (s *Store) Verify(ctx context.Context, bot uint, u r.Update, ref string, p c.Policy, now time.Time) (out c.Outcome, err error) {
	ref, answer := c.ParseVerificationAnswer(ref)
	// Charge the trusted caller, never the owner of a guessed/forwarded reference.
	if bot == 0 || u.UserID <= 0 || u.ChatID != u.UserID {
		return out, c.ErrDenied
	}
	generation, err := s.ingressGeneration(ctx, bot)
	if err != nil {
		return out, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) (ret error) {
		if err := s.ingressFence(tx, bot, generation); err != nil {
			return err
		}
		session, e := lockSession(tx, bot, u.UserID, u.ChatID, now)
		if e != nil {
			return e
		}
		if session.State == "closed" {
			return c.ErrDenied
		}
		if e = s.checkSupportBlock(tx, bot, u.UserID, now); e != nil {
			if errors.Is(e, c.ErrDenied) {
				out.Reason = "blocked"
				return recordBlockedInput(tx, session, u, p, now)
			}
			return e
		}
		out.Session = session
		var prior d.BufferedMessage
		check := tx.Where("bot_id = ? AND update_id = ?", bot, u.ID).First(&prior).Error
		if check == nil {
			if prior.State != "callback" || prior.UserID != u.UserID {
				return c.ErrDenied
			}
			out.Reason = prior.Reason
			return nil
		}
		if !errors.Is(check, gorm.ErrRecordNotFound) {
			return check
		}
		// Successful callback tombstones retain an encrypted deletion proof, never plaintext refs.
		// ClaimInbox currently blocks later updates behind its oldest unfinished row.
		// Lease recovery and direct contract calls still cannot assume completion order.
		defer func() {
			if ret == nil && u.ID > 0 {
				ret = s.recordVerificationCallback(tx, bot, session.ID, u, out.Reason, now)
			}
		}()
		if now.Before(session.CooldownUntil) {
			out.Reason = "cooldown"
			return nil
		}
		if session.AttemptWindow.IsZero() || !now.Before(session.AttemptWindow.Add(p.Cooldown)) {
			session.Attempts = 0
			session.AttemptWindow = now
		}
		var ch d.Challenge
		e = tx.Where("ref = ? AND bot_id = ? AND user_id = ? AND chat_id = ?", ref, bot, u.UserID, u.ChatID).First(&ch).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		invalid := e != nil || session.ChallengeID != ch.ID || answer < 0 || ch.Question == "" || len(strings.Split(ch.Choices, ",")) != 5
		if invalid {
			session.Attempts++
			out.Reason = "invalid"
			if session.Attempts >= p.MaxAttempts {
				session.CooldownUntil = now.Add(p.Cooldown)
				out.Reason = "cooldown"
			}
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}
		out.Challenge = &ch
		if ch.State == "passed" {
			out.Reason = "expired"
			if ch.ConsumedAt != nil && c.VerificationDay(*ch.ConsumedAt) == c.VerificationDay(now) {
				out.Reason = "passed"
			}
			return nil
		}
		if ch.State != "pending" {
			out.Reason = "expired"
			return nil
		}
		return finishVerification(tx, &session, &ch, &out, answer == ch.AnswerIndex, p, now)
	})
	return
}

// recordVerificationCallback stores a deduplicated result for bot/session/update.
// Successful callbacks retain encrypted proof for deletion; persistence errors roll back verification.
func (s *Store) recordVerificationCallback(tx *gorm.DB, bot, session uint, update r.Update, reason string, now time.Time) error {
	payload := ""
	if reason == "passed" && update.CallbackID != "" && update.MessageID > 0 {
		if s.cipher == nil {
			return c.ErrDenied
		}
		// Persist only deletion evidence, not the callback's copied text/media content.
		proof := r.Update{ID: update.ID, Private: update.Private, UserID: update.UserID, ChatID: update.ChatID, MessageID: update.MessageID, CallbackID: update.CallbackID, CallbackData: update.CallbackData}
		raw, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		payload, err = s.cipher.Seal(string(raw))
		if err != nil {
			return err
		}
	}
	return tx.Create(&d.BufferedMessage{BotID: bot, UpdateID: update.ID, SessionID: session, UserID: update.UserID, ChatID: update.ChatID, State: "callback", Reason: reason, PayloadCipher: payload, CreatedAt: now, UpdatedAt: now}).Error
}

// VerificationCleanup resolves bot/update to a verified callback's exact Telegram message.
// It returns only scoped persisted evidence, including after an unknown send receipt.
func (s *Store) VerificationCleanup(ctx context.Context, bot uint, updateID int64) (r.Update, error) {
	var source r.Update
	if bot == 0 || updateID <= 0 || s.cipher == nil {
		return source, c.ErrDenied
	}
	var row d.BufferedMessage
	if err := s.db.WithContext(ctx).Where("bot_id = ? AND update_id = ? AND state = ? AND reason = ?", bot, updateID, "callback", "passed").First(&row).Error; err != nil {
		return source, err
	}
	raw, err := s.cipher.Open(row.PayloadCipher)
	if err != nil || json.Unmarshal([]byte(raw), &source) != nil {
		return r.Update{}, c.ErrDenied
	}
	ref, _ := c.ParseVerificationAnswer(strings.TrimPrefix(source.CallbackData, "support:"))
	var challenge d.Challenge
	if err = s.db.WithContext(ctx).Where("bot_id = ? AND session_id = ? AND ref = ? AND state = ?", bot, row.SessionID, ref, "passed").First(&challenge).Error; err != nil {
		return r.Update{}, err
	}
	if source.ID != updateID || source.UserID != row.UserID || source.ChatID != row.ChatID || source.ChatID != source.UserID || source.UserID != challenge.UserID || source.ChatID != challenge.ChatID || !source.Private || source.CallbackID == "" || source.MessageID <= 0 || !strings.HasPrefix(source.CallbackData, "support:") {
		return r.Update{}, c.ErrDenied
	}
	return source, nil
}

func (s *Store) Ready(ctx context.Context, bot, session uint, limit int, now time.Time) ([]d.BufferedMessage, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var rows []d.BufferedMessage
	err := s.db.WithContext(ctx).Where("bot_id = ? AND session_id = ? AND legacy_task_id = 0 AND state = ? AND retain_until > ?", bot, session, "ready", now).Order("sequence ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// challengeNotice projects a stored challenge into its durable notification.
func challengeNotice(ch d.Challenge) c.Notice {
	reason := "challenge"
	if ch.State == "expired" {
		reason = "expired"
	}
	return c.Notice{BotID: ch.BotID, ID: ch.ID, Kind: "challenge", Ref: ch.Ref, Reason: reason, Locale: ch.Locale, Key: "support:" + reason + ":" + ch.Ref, UserID: ch.UserID, ChatID: ch.ChatID}
}

// messageNotice returns feedback metadata for the supplied buffered message.
func messageNotice(m d.BufferedMessage) c.Notice {
	return c.Notice{BotID: m.BotID, ID: m.ID, Kind: "feedback", Ref: fmt.Sprintf("message:%d", m.ID), Reason: m.Reason, Locale: m.Locale, Key: fmt.Sprintf("support:feedback:%d:%s", m.ID, m.Reason), UserID: m.UserID, ChatID: m.ChatID}
}

// Notices lists bounded pending notices for bot at the trusted clock now.
func (s *Store) Notices(ctx context.Context, bot uint, limit int, now time.Time) ([]c.Notice, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var messages []d.BufferedMessage
	if err := s.db.WithContext(ctx).Where("bot_id = ? AND feedback_pending = ?", bot, true).Order("updated_at ASC, id ASC").Limit(limit).Find(&messages).Error; err != nil {
		return nil, err
	}
	out := make([]c.Notice, 0, limit*2)
	for _, m := range messages {
		out = append(out, messageNotice(m))
	}
	var challenges []d.Challenge
	if err := s.db.WithContext(ctx).Where("bot_id = ? AND notice_pending = ? AND (state = ? OR (state = ? AND expires_at > ?))", bot, true, "expired", "pending", now).Order("updated_at ASC, id ASC").Limit(limit).Find(&challenges).Error; err != nil {
		return nil, err
	}
	for _, ch := range challenges {
		out = append(out, challengeNotice(ch))
	}
	return out, nil
}

// Notice resolves a bot-scoped durable reference or returns a denial/error.
func (s *Store) Notice(ctx context.Context, bot uint, ref string) (c.Notice, error) {
	if strings.HasPrefix(ref, "message:") {
		id, err := strconv.ParseUint(strings.TrimPrefix(ref, "message:"), 10, 64)
		if err != nil {
			return c.Notice{}, c.ErrDenied
		}
		var m d.BufferedMessage
		if err = s.db.WithContext(ctx).Where("id = ? AND bot_id = ?", id, bot).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.Notice{}, c.ErrDenied
			}
			return c.Notice{}, err
		}
		if !c.FeedbackReason(m.Reason) {
			return c.Notice{}, c.ErrDenied
		}
		return messageNotice(m), nil
	}
	ch, err := s.Challenge(ctx, ref)
	if err != nil {
		return c.Notice{}, err
	}
	if ch.BotID != bot {
		return c.Notice{}, c.ErrDenied
	}
	return challengeNotice(*ch), nil
}

// DeferNotice moves one failed notice behind older pending work without losing it.
// notice identifies the persisted row; now is the trusted retry observation time.
func (s *Store) DeferNotice(ctx context.Context, notice c.Notice, now time.Time) error {
	if notice.Kind == "challenge" {
		return s.db.WithContext(ctx).Model(&d.Challenge{}).Where("id = ? AND bot_id = ? AND notice_pending = ?", notice.ID, notice.BotID, true).Update("updated_at", now).Error
	}
	return s.db.WithContext(ctx).Model(&d.BufferedMessage{}).Where("id = ? AND bot_id = ? AND feedback_pending = ?", notice.ID, notice.BotID, true).Update("updated_at", now).Error
}

// QuarantineNotice consumes only a matching invalid notice and retains its cause.
// Feedback stores an explicit error marker; challenge state remains available for audit.
func (s *Store) QuarantineNotice(ctx context.Context, notice c.Notice) error {
	if notice.Kind == "feedback" {
		return s.db.WithContext(ctx).Model(&d.BufferedMessage{}).Where("id = ? AND bot_id = ? AND reason = ?", notice.ID, notice.BotID, notice.Reason).Updates(map[string]any{"feedback_pending": false, "error_class": "notice_invalid"}).Error
	}
	return s.NoticeEnqueued(ctx, notice)
}

// NoticeEnqueued acknowledges only the supplied bot-scoped notice and reason.
func (s *Store) NoticeEnqueued(ctx context.Context, n c.Notice) error {
	if n.Kind == "challenge" {
		return s.db.WithContext(ctx).Model(&d.Challenge{}).Where("id = ? AND bot_id = ? AND state = ?", n.ID, n.BotID, map[bool]string{true: "expired", false: "pending"}[n.Reason == "expired"]).Update("notice_pending", false).Error
	}
	return s.db.WithContext(ctx).Model(&d.BufferedMessage{}).Where("id = ? AND bot_id = ? AND reason = ?", n.ID, n.BotID, n.Reason).Update("feedback_pending", false).Error
}

// persistConsultation applies queue limits and saves encrypted input under the session lock.
// The challenge snapshot controls verification; topic existence never changes message state.
func persistConsultation(tx *gorm.DB, session *d.Session, challenge d.Challenge, update r.Update, payload string, size int64, policy c.Policy, now time.Time, outcome *c.Outcome) error {
	reason, state := "", "buffered"
	if now.Before(session.CooldownUntil) {
		reason = "cooldown"
	}
	var count int64
	var bytes struct{ Total int64 }
	active := []string{"buffered", "ready", "claimed", "sending", "unknown"}
	if err := tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND legacy_task_id = 0 AND state IN ?", session.ID, active).Count(&count).Error; err != nil {
		return err
	}
	if err := tx.Model(&d.BufferedMessage{}).Select("COALESCE(SUM(bytes), 0) AS total").Where("session_id = ? AND legacy_task_id = 0 AND state IN ?", session.ID, active).Scan(&bytes).Error; err != nil {
		return err
	}
	if count >= int64(policy.MaxMessages) || size < 0 || size > policy.MaxBytes || bytes.Total > policy.MaxBytes-size {
		reason = "limit"
	}
	if reason == "" {
		reason = c.ContentRejection(update)
	}
	if reason != "" {
		state, payload = "rejected", ""
	} else if session.State == "open" {
		state = "ready"
	}
	session.Sequence++
	row := d.BufferedMessage{UserID: update.UserID, ChatID: update.ChatID, Locale: update.Locale, FeedbackPending: reason != "", BotID: session.BotID, UpdateID: update.ID, SessionID: session.ID, Sequence: session.Sequence, PayloadCipher: payload, Bytes: size, State: state, Reason: reason, RetainUntil: now.Add(policy.Retention), CreatedAt: now, UpdatedAt: now}
	if state == "ready" {
		row.ReleasedAt = &now
	}
	if state == "buffered" && row.RetainUntil.Before(challenge.ExpiresAt) {
		row.RetainUntil = challenge.ExpiresAt
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	if err := collectAlbum(tx, &row, update, now); err != nil {
		return err
	}
	session.UpdatedAt = now
	if err := tx.Save(session).Error; err != nil {
		return err
	}
	outcome.Session, outcome.Reason = *session, reason
	if challenge.ID != 0 {
		outcome.Challenge = &challenge
	}
	return nil
}

// failVerificationRound terminalizes only unreleased input in the locked session.
// Payload remains admin-readable until retention cleanup; future passes cannot revive it.
func failVerificationRound(tx *gorm.DB, session uint, reason string, now time.Time) error {
	if err := expireMessages(tx, session, now); err != nil {
		return err
	}
	return tx.Model(&d.BufferedMessage{}).Where("session_id = ? AND legacy_task_id = 0 AND state = ?", session, "buffered").Updates(map[string]any{"state": "rejected", "reason": reason, "feedback_pending": false, "updated_at": now}).Error
}

// expireVerificationRounds serializes expiry with Accept/Verify using each customer lock.
// A pass committed first wins; a timeout committed first permanently rejects the round.
func expireVerificationRounds(tx *gorm.DB, bot uint, now time.Time) error {
	var challenges []d.Challenge
	if err := tx.Where("bot_id = ? AND state = ? AND expires_at <= ?", bot, "pending", now).Order("id ASC").Limit(100).Find(&challenges).Error; err != nil {
		return err
	}
	for _, candidate := range challenges {
		session, err := lockSession(tx, bot, candidate.UserID, candidate.ChatID, now)
		if err != nil {
			return err
		}
		var current d.Challenge
		if err = tx.First(&current, candidate.ID).Error; err != nil {
			return err
		}
		if current.State != "pending" || now.Before(current.ExpiresAt) {
			continue
		}
		if session.ChallengeID == current.ID {
			if err = failVerificationRound(tx, session.ID, "expired", now); err != nil {
				return err
			}
		}
		if err = tx.Model(&current).Updates(map[string]any{"state": "expired", "notice_pending": true, "updated_at": now, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Expire(ctx context.Context, bot uint, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := expireVerificationRounds(tx, bot, now); err != nil {
			return err
		}
		var ids []uint
		if err := expirableSupportMessages(tx, bot, now).Order("id ASC").Limit(100).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			ids, err := expandSupportCleanupPage(tx.Model(&d.BufferedMessage{}), ids)
			if err != nil {
				return err
			}
			if err := tx.Model(&d.BufferedMessage{}).Where("id IN ? AND state IN ?", ids, []string{"buffered", "ready", "claimed"}).Updates(map[string]any{"state": "expired", "reason": "expired", "payload_cipher": "", "feedback_pending": true, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		// Telegram cannot delete these old private messages; expire encrypted callback proof
		// with the existing terminal retention rather than retain it indefinitely.
		callbacks := tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND state = ? AND updated_at <= ? AND payload_cipher <> ?", bot, "callback", now.Add(-supportTerminalAge), "")
		if err := clearCipherPage(callbacks, map[string]any{"payload_cipher": ""}); err != nil {
			return err
		}
		return clearTerminalSupportPayloads(tx, bot, now)
	})
}
