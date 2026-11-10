package gormstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"reflect"
	"strings"
	"time"
)

// RouteReaderFactory builds a DB-only adapter bound to tx. Applications may
// install an existing groups adapter; it must never capture a global connection.
type RouteReaderFactory func(tx *gorm.DB) c.RouteReader

// WithRouteReader overrides the conservative same-connection route projection.
// Configure only during construction, before concurrent use.
func (s *Store) WithRouteReader(factory RouteReaderFactory) *Store { s.routes = factory; return s }

// DeliveryCapability exposes production readiness without replacing Store.Ready's message scan.
type DeliveryCapability struct{ Store *Store }

// Ready reports whether the wired delivery store has a current, usable support route.
// bot and target are trusted module identifiers; database failures remain errors.
func (capability DeliveryCapability) Ready(ctx context.Context, bot, target uint) (bool, error) {
	s := capability.Store
	if s == nil || s.db == nil || s.cipher == nil || s.encoder == nil || s.policyRecoveryGuard == nil || bot == 0 || target == 0 {
		return false, nil
	}
	var route struct {
		ChatID       int64
		RouteVersion uint64
	}
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Table("telegram_group_targets AS target").
		Select("target.telegram_chat_id AS chat_id, target.route_version").
		Joins("JOIN telegram_bot_instances AS bot ON bot.id = target.bot_instance_id").
		Where("target.id = ? AND target.bot_instance_id = ? AND bot.last_seen_at > ? AND bot.last_seen_at <= ?", target, bot, now.Add(-45*time.Second), now).Take(&route).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if route.ChatID >= 0 || route.RouteVersion == 0 {
		return false, nil
	}
	err = s.checkRoute(s.db.WithContext(ctx), bot, target, c.DeliveryBinding{ChatID: route.ChatID, RouteVersion: route.RouteVersion}, time.Now().UTC())
	if errors.Is(err, c.ErrDenied) || errors.Is(err, g.ErrCapabilityPending) {
		return false, nil
	}
	return err == nil, err
}

// ingressGeneration snapshots the runtime generation before BEGIN and checks the
// supervised lease when production ingress is wired. It returns no global DB work
// to the transaction; runtimeFence rechecks this exact generation under the lock.
func (s *Store) ingressGeneration(ctx context.Context, bot uint) (uint64, error) {
	var row struct{ Generation uint64 }
	err := s.db.WithContext(ctx).Table("telegram_bot_instances").Select("generation").Where("id = ?", bot).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, c.ErrDenied
	}
	if err != nil {
		return 0, err
	}
	if s.policyRecoveryGuard != nil {
		if err = r.CheckExecutionFence(ctx, bot, row.Generation); err != nil {
			return 0, err
		}
	}
	return row.Generation, nil
}

// ingressFence serializes ingress with recovery using the transaction-bound guard.
// generation was checked before BEGIN; failures leave no session/challenge writes.
func (s *Store) ingressFence(tx *gorm.DB, bot uint, generation uint64) error {
	if s.policyRecoveryGuard != nil {
		if err := s.policyRecoveryGuard(tx, bot); err != nil {
			return err
		}
	}
	if err := runtimeFence(tx, bot, generation); err != nil {
		return err
	}
	if s.policyRecoveryGuard == nil {
		return nil // Legacy isolated stores cannot advertise DeliveryCapability readiness.
	}
	var route struct {
		TargetID     uint
		ChatID       int64
		RouteVersion uint64
	}
	err := tx.Table("telegram_bot_module_configs AS module").
		Select("module.target_id, target.telegram_chat_id AS chat_id, target.route_version").
		Joins("JOIN telegram_group_targets AS target ON target.id = module.target_id AND target.bot_instance_id = module.bot_instance_id").
		Where("module.bot_instance_id = ? AND module.partition = ?", bot, "support").Take(&route).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrDenied
	}
	if err != nil {
		return err
	}
	return s.checkRoute(tx, bot, route.TargetID, c.DeliveryBinding{ChatID: route.ChatID, RouteVersion: route.RouteVersion}, time.Now().UTC())
}

// checkRoute validates saved intent, live identity and freshness on tx, not a
// global service. now is supplied by the trusted scheduler clock.
func (s *Store) checkRoute(tx *gorm.DB, bot, target uint, binding c.DeliveryBinding, now time.Time) error {
	if s.routes != nil {
		reader := s.routes(tx)
		if reader == nil {
			return c.ErrDenied
		}
		return reader.CheckSupportRoute(tx.Statement.Context, bot, target, binding, now)
	}
	if bot == 0 || target == 0 || binding.ChatID >= 0 || binding.RouteVersion == 0 {
		return c.ErrDenied
	}
	// Select intent and observation together on the caller's transaction. Expired
	// positive evidence is pending, but disabled/rebound/recovering routes deny.
	view := g.View{}
	err := tx.Table("telegram_group_targets AS target").
		Select("target.*").
		Joins("JOIN telegram_bot_module_configs AS module ON module.target_id = target.id AND module.bot_instance_id = target.bot_instance_id").
		Joins("JOIN telegram_bot_instances AS bot ON bot.id = target.bot_instance_id").
		Where("target.id = ? AND target.bot_instance_id = ? AND target.telegram_chat_id = ? AND target.route_version = ?", target, bot, binding.ChatID, binding.RouteVersion).
		Where("module.partition = ? AND module.enabled = ? AND target.enabled = ? AND target.purpose = ? AND target.recovery_task_id = 0", "support", true, true, "support").
		Where("bot.enabled = ? AND bot.mode = ? AND bot.state = ? AND bot.recovery_task_id = 0 AND bot.generation > 0", true, "internal", "running").Take(&view.Target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrDenied
	}
	if err != nil {
		return err
	}
	var identity g.Identity
	if err = tx.Table("telegram_bot_instances").Select("generation, telegram_id").Where("id = ?", bot).Take(&identity).Error; err != nil {
		return err
	}
	observation := view.Observation
	view.Fresh = observation.ObservedAt != nil && observation.ObservedAt.After(now.Add(-5*time.Minute)) && !observation.ObservedAt.After(now)
	view.Checking = observation.State == "checking"
	if observation.ChatType != "supergroup" || (observation.MemberStatus != "administrator" && observation.MemberStatus != "creator") || !observation.IsMember || !observation.IsForum || !observation.CanManageTopics {
		view.MissingCapabilities = []string{"support"}
	}
	err = g.SupportCapability(view, identity)
	if errors.Is(err, g.ErrCapability) || (err == nil && observation.RouteVersion != binding.RouteVersion) {
		return c.ErrDenied
	}
	return err
}

// trustedTopic requires an open consultation and its exact current route. Old
// mappings remain queryable for auditing but can never authorize fresh delivery.
func (s *Store) trustedTopic(tx *gorm.DB, bot, session uint, binding c.DeliveryBinding, now time.Time) (d.TopicMapping, error) {
	var topic d.TopicMapping
	err := tx.Where("bot_id = ? AND session_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state = ?", bot, session, binding.ChatID, binding.ThreadID, binding.RouteVersion, "open").First(&topic).Error
	if err != nil {
		return topic, c.ErrDenied
	}
	var owner d.Session
	if err = tx.Where("id = ? AND bot_id = ? AND state = ?", session, bot, "open").First(&owner).Error; err != nil {
		return topic, c.ErrDenied
	}
	if e := s.checkSupportBlock(tx, bot, owner.UserID, now); e != nil {
		return topic, e
	}
	return topic, s.checkRoute(tx, bot, topic.TargetID, binding, now)
}

// ControlForUpdate restores one immutable menu after an interrupted inbox attempt.
func (s *Store) ControlForUpdate(ctx context.Context, bot uint, update int64) (*d.OperationControl, error) {
	var row d.OperationControl
	err := s.db.WithContext(ctx).Where("bot_id = ? AND update_id = ?", bot, update).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

// SaveControl binds exact callbacks to one actor, topic, session revision and generation.
// The encrypted envelope and deduplication record commit together, without HTTP.
func (s *Store) SaveControl(ctx context.Context, bot uint, update r.Update, ref string, callbacks []string, message r.Message, now time.Time) (out d.OperationControl, err error) {
	if !operationActor(bot, update) || update.ID <= 0 || len(ref) != 24 || len(callbacks) == 0 || len(callbacks) > 3 || s.cipher == nil || message.Purpose != c.OperationControlPurpose || message.TargetType != r.TargetSupportGroup || message.Operation != "send" || message.BusinessRef != "support:control:"+ref || message.ChatID != update.ChatID || message.ThreadID != update.ThreadID || len(message.Buttons) != len(callbacks) {
		return out, c.ErrDenied
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return out, err
	}
	encrypted, err := s.cipher.Seal(string(raw))
	if err != nil {
		return out, err
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var first d.OperationReference
		for index, callback := range callbacks {
			if len(message.Buttons[index]) != 1 || message.Buttons[index][0].Data != callback || !strings.HasPrefix(callback, c.OperationCallbackPrefix) {
				return c.ErrDenied
			}
			var entry d.OperationReference
			if e := tx.Where("bot_id = ? AND ref = ?", bot, strings.TrimPrefix(callback, c.OperationCallbackPrefix)).First(&entry).Error; e != nil {
				return c.ErrDenied
			}
			if entry.ActorID != update.UserID || entry.ChatID != update.ChatID || entry.ThreadID != update.ThreadID || entry.RouteVersion != message.RouteVersion || entry.ConsumedAt != nil || !now.Before(entry.ExpiresAt) {
				return c.ErrDenied
			}
			if index == 0 {
				first = entry
			} else if entry.SessionID != first.SessionID || entry.TopicID != first.TopicID || entry.SessionVersion != first.SessionVersion || entry.Generation != first.Generation {
				return c.ErrConflict
			}
		}
		out = d.OperationControl{BotID: bot, UpdateID: update.ID, Ref: ref, ActorID: update.UserID, ChatID: update.ChatID, ThreadID: update.ThreadID, TopicID: first.TopicID, SessionID: first.SessionID, SessionVersion: first.SessionVersion, RouteVersion: first.RouteVersion, Generation: first.Generation, PayloadCipher: encrypted, ExpiresAt: first.ExpiresAt, CreatedAt: now}
		if e := s.checkControlBinding(tx, out, now); e != nil {
			return e
		}
		return tx.Create(&out).Error
	})
	return out, err
}

// CheckControl denies altered envelopes, expired references and stale routes/sessions.
func (s *Store) CheckControl(ctx context.Context, bot uint, message r.Message, now time.Time) (out d.OperationControl, err error) {
	if message.Purpose != c.OperationControlPurpose || message.TargetType != r.TargetSupportGroup || s.cipher == nil || !strings.HasPrefix(message.BusinessRef, "support:control:") {
		return out, c.ErrDenied
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := tx.Where("bot_id = ? AND ref = ?", bot, strings.TrimPrefix(message.BusinessRef, "support:control:")).First(&out).Error; e != nil {
			return c.ErrDenied
		}
		plain, e := s.cipher.Open(out.PayloadCipher)
		var expected r.Message
		if e != nil || json.Unmarshal([]byte(plain), &expected) != nil || !reflect.DeepEqual(expected, message) {
			return c.ErrDenied
		}
		return s.checkControlBinding(tx, out, now)
	})
	return out, err
}

// checkControlBinding is DB-only and uses the caller's single transaction connection.
func (s *Store) checkControlBinding(tx *gorm.DB, row d.OperationControl, now time.Time) error {
	if row.DailyKey != nil {
		return s.checkVerificationControl(tx, row, now)
	}
	if !now.Before(row.ExpiresAt) {
		return c.ErrDenied
	}
	if err := runtimeFence(tx, row.BotID, row.Generation); err != nil {
		return err
	}
	var topic d.TopicMapping
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state = ?", row.TopicID, row.BotID, row.SessionID, row.ChatID, row.ThreadID, row.RouteVersion, "open").First(&topic).Error; err != nil {
		return c.ErrDenied
	}
	var count int64
	if err := tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND version = ?", row.SessionID, row.BotID, row.SessionVersion).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return c.ErrConflict
	}
	return s.operationRoute(tx, topic, now)
}

// transaction reads and writes use the context-bound same connection exclusively.
func (s *Store) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn)
}

// TopicIdentity loads the authoritative nickname and user ID for a server-owned session.
func (s *Store) TopicIdentity(ctx context.Context, bot, session uint) (out d.Session, err error) {
	err = s.db.WithContext(ctx).Where("id = ? AND bot_id = ?", session, bot).First(&out).Error
	return
}

// VerificationSessions scans today's intercepted consultations, including recently verified ones.
// The bounded cursor allows runtime reconciliation to retry without relying on inbox completion.
func (s *Store) VerificationSessions(ctx context.Context, bot, after uint, limit int, now time.Time) (out []d.Session, err error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrDenied
	}
	day := now.In(time.FixedZone("CST", 8*3600))
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).UTC()
	err = s.db.WithContext(ctx).Where("bot_id = ? AND id > ? AND state IN ?", bot, after, []string{"open", "waiting_verification"}).
		Where("EXISTS (SELECT 1 FROM telegram_support_challenges c WHERE c.session_id = telegram_support_sessions.id AND c.bot_id = ? AND c.created_at >= ? AND c.created_at < ?)", bot, start, start.Add(24*time.Hour)).
		Order("id ASC").Limit(limit).Find(&out).Error
	return
}

// EnsureVerificationControl atomically creates one immutable card per bot/session/Beijing day.
// It does not impersonate a Telegram actor and never includes customer message content.
func (s *Store) EnsureVerificationControl(ctx context.Context, bot, session uint, generation uint64, now time.Time) (out *d.OperationControl, err error) {
	if s.cipher == nil || bot == 0 || session == 0 || generation == 0 {
		return nil, c.ErrDenied
	}
	localDay := now.In(time.FixedZone("CST", 8*3600))
	day := localDay.Format("2006-01-02")
	dayStart := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), 0, 0, 0, 0, localDay.Location()).UTC()
	key := fmt.Sprintf("%d:%d:%s", bot, session, day)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if e := runtimeFence(tx, bot, generation); e != nil {
			return e
		}
		// Serialize the daily unique slot on its parent session, not on a guessed actor.
		if err := dblock.Row(tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND state IN ?", session, bot, []string{"open", "waiting_verification"}), "version", c.ErrDenied); err != nil {
			return err
		}
		var challenges int64
		if e := tx.Model(&d.Challenge{}).Where("bot_id = ? AND session_id = ? AND created_at >= ? AND created_at < ?", bot, session, dayStart, dayStart.Add(24*time.Hour)).Count(&challenges).Error; e != nil {
			return e
		}
		if challenges == 0 {
			return c.ErrDenied
		}
		var existing d.OperationControl
		if e := tx.Where("daily_key = ?", key).First(&existing).Error; e == nil {
			out = &existing
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var owner d.Session
		if e := tx.Where("id = ? AND bot_id = ?", session, bot).First(&owner).Error; e != nil {
			return e
		}
		if e := s.checkSupportBlock(tx, bot, owner.UserID, now); e != nil {
			return e
		}
		var topic d.TopicMapping
		if e := tx.Where("bot_id = ? AND session_id = ? AND state = ?", bot, session, "open").Order("id DESC").First(&topic).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return c.ErrDenied
			}
			return e
		}
		if e := s.operationRoute(tx, topic, now); e != nil {
			return e
		}
		row, e := s.createVerificationControl(tx, owner, topic, generation, key, now)
		if e == nil {
			out = &row
		}
		return e
	})
	return
}

// createVerificationControl persists the card and all four action references in one transaction.
func (s *Store) createVerificationControl(tx *gorm.DB, owner d.Session, topic d.TopicMapping, generation uint64, key string, now time.Time) (row d.OperationControl, err error) {
	ref, err := operationRef()
	if err != nil {
		return row, err
	}
	row = d.OperationControl{BotID: owner.BotID, Ref: ref, DailyKey: &key, SessionID: owner.ID, SessionVersion: owner.Version, TopicID: topic.ID, ChatID: topic.ChatID, ThreadID: topic.ThreadID, RouteVersion: topic.RouteVersion, Generation: generation, ExpiresAt: now.AddDate(10, 0, 0), CreatedAt: now}
	// The existing bot/update uniqueness remains intact; automatic cards reserve negative IDs.
	var first d.OperationReference
	buttons := []r.Button{}
	for index, days := range []int{1, 7, 30, 0} {
		actionRef, e := operationRef()
		if e != nil {
			return row, e
		}
		entry := d.OperationReference{Ref: actionRef, BotID: owner.BotID, SessionID: owner.ID, TopicID: topic.ID, ChatID: topic.ChatID, ThreadID: topic.ThreadID, RouteVersion: topic.RouteVersion, Generation: generation, SessionVersion: owner.Version, Action: c.OperationBlock, ControlRef: ref, BlockDays: days, ExpiresAt: row.ExpiresAt, CreatedAt: now}
		if e = tx.Create(&entry).Error; e != nil {
			return row, e
		}
		if index == 0 {
			first = entry
		}
		buttons = append(buttons, r.Button{Text: []string{"🈲1天", "🈲1周", "🈲1月", "🈲永久"}[index], Data: c.OperationCallbackPrefix + actionRef})
	}
	row.UpdateID = -int64(first.ID)
	source := strings.TrimSpace(owner.DisplayName)
	if strings.TrimSpace(owner.Username) != "" {
		source = "@" + strings.TrimPrefix(strings.TrimSpace(owner.Username), "@")
	}
	if source == "" {
		source = fmt.Sprintf("%d", owner.UserID)
	}
	message := r.Message{TargetType: r.TargetSupportGroup, Purpose: c.OperationControlPurpose, Operation: "send", TargetKind: "text", ChatID: row.ChatID, ThreadID: row.ThreadID, RouteVersion: row.RouteVersion, BusinessRef: "support:control:" + ref, Text: fmt.Sprintf("对方ID：%d\n消息来自：%s", owner.UserID, source), Buttons: [][]r.Button{buttons}}
	raw, err := json.Marshal(message)
	if err != nil {
		return row, err
	}
	row.PayloadCipher, err = s.cipher.Seal(string(raw))
	if err != nil {
		return row, err
	}
	err = tx.Create(&row).Error
	return
}

// operationRef returns a cryptographically random opaque callback capability.
func operationRef() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// checkVerificationControl authorizes only identity controls; ordinary trustedTopic stays open-only.
// Current generation is checked by application; historic cards retain their original receipt.
func (s *Store) checkVerificationControl(tx *gorm.DB, row d.OperationControl, now time.Time) error {
	if row.ActorID != 0 || row.DailyKey == nil || row.ConsumedAt != nil || !now.Before(row.ExpiresAt) {
		return c.ErrDenied
	}
	var topic d.TopicMapping
	if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND chat_id = ? AND thread_id = ? AND route_version = ? AND state = ?", row.TopicID, row.BotID, row.SessionID, row.ChatID, row.ThreadID, row.RouteVersion, "open").First(&topic).Error; err != nil {
		return c.ErrDenied
	}
	var owner d.Session
	if err := tx.Where("id = ? AND bot_id = ? AND state IN ?", row.SessionID, row.BotID, []string{"open", "waiting_verification"}).First(&owner).Error; err != nil {
		return c.ErrDenied
	}
	if err := s.checkSupportBlock(tx, row.BotID, owner.UserID, now); err != nil {
		return err
	}
	return s.operationRoute(tx, topic, now)
}

// ApplyVerificationOperation binds a live authorized real actor to the exact sent card.
// Consuming the entire card makes repeat clicks (including other durations) non-extending.
func (s *Store) ApplyVerificationOperation(ctx context.Context, bot uint, update r.Update, ref string, generation uint64, now time.Time) error {
	if !operationActor(bot, update) || update.CallbackID == "" || update.MessageID <= 0 {
		return c.ErrDenied
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := runtimeFence(tx, bot, generation); err != nil {
			return err
		}
		var entry d.OperationReference
		if err := tx.Where("bot_id = ? AND ref = ? AND actor_id = 0 AND control_ref <> ''", bot, ref).First(&entry).Error; err != nil {
			return c.ErrDenied
		}
		var row d.OperationControl
		if err := tx.Where("bot_id = ? AND ref = ?", bot, entry.ControlRef).First(&row).Error; err != nil {
			return c.ErrDenied
		}
		if row.DailyKey == nil || row.ChatID != update.ChatID || row.ThreadID != update.ThreadID || !now.Before(row.ExpiresAt) || entry.Action != c.OperationBlock {
			return c.ErrDenied
		}
		if err := s.verificationReceipt(tx, row, update.MessageID); err != nil {
			return err
		}
		var topic d.TopicMapping
		if err := tx.Where("id = ? AND bot_id = ? AND session_id = ? AND state = ?", row.TopicID, bot, row.SessionID, "open").First(&topic).Error; err != nil {
			return c.ErrDenied
		}
		if topic.ChatID != row.ChatID || topic.ThreadID != row.ThreadID || topic.RouteVersion != row.RouteVersion {
			return c.ErrDenied
		}
		if err := s.operationRoute(tx, topic, now); err != nil {
			return err
		}
		if row.ConsumedAt != nil {
			return nil
		}
		changed := tx.Model(&row).Where("consumed_at IS NULL").UpdateColumn("consumed_at", now)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return nil
		}
		if err := dblock.Row(tx.Model(&d.Session{}).Where("id = ? AND bot_id = ? AND state IN ?", row.SessionID, bot, []string{"open", "waiting_verification"}), "version", c.ErrDenied); err != nil {
			return err
		}
		var owner d.Session
		if err := tx.Where("id = ? AND bot_id = ? AND state IN ?", row.SessionID, bot, []string{"open", "waiting_verification"}).First(&owner).Error; err != nil {
			return c.ErrDenied
		}
		entry.ActorID, entry.Generation = update.UserID, generation
		if err := applyVerificationBlock(tx, entry, owner, now); err != nil {
			return err
		}
		if err := tx.Model(&entry).UpdateColumn("consumed_at", now).Error; err != nil {
			return err
		}
		return tx.Create(&d.OperationAudit{ReferenceID: entry.ID, BotID: bot, SessionID: owner.ID, TopicID: topic.ID, ActorID: update.UserID, ChatID: row.ChatID, ThreadID: row.ThreadID, RouteVersion: row.RouteVersion, Generation: generation, SessionVersion: owner.Version, Action: fmt.Sprintf("block_%dd", entry.BlockDays), CreatedAt: now}).Error
	})
}

// verificationReceipt rejects copied/forwarded cards even inside the original topic.
func (s *Store) verificationReceipt(tx *gorm.DB, row d.OperationControl, message int64) error {
	var proof deliveryProof
	if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ? AND status = ?", row.BotID, "sent").Where("\"key\" = ?", "support:control:"+row.Ref).First(&proof).Error; err != nil {
		return c.ErrDenied
	}
	if proof.MessageID != message || len(proof.MessageIDs) != 1 || proof.MessageIDs[0] != message || proof.ChatID != row.ChatID || proof.TargetType != string(r.TargetSupportGroup) || proof.Purpose != c.OperationControlPurpose || proof.Operation != "send" || proof.TelegramUserID != 0 || (proof.PayloadCipher != "" && proof.PayloadCipher != row.PayloadCipher) {
		return c.ErrDenied
	}
	return nil
}

// applyVerificationBlock uses the existing support block and cancels buffered content permanently.
func applyVerificationBlock(tx *gorm.DB, entry d.OperationReference, owner d.Session, now time.Time) error {
	if entry.BlockDays != 0 && entry.BlockDays != 1 && entry.BlockDays != 7 && entry.BlockDays != 30 {
		return c.ErrDenied
	}
	var expires *time.Time
	if entry.BlockDays > 0 {
		until := now.Add(time.Duration(entry.BlockDays) * 24 * time.Hour)
		expires = &until
	}
	if err := cancelBlockedChallenges(tx, owner.BotID, owner.ID, now); err != nil {
		return err
	}
	if err := tx.Model(&d.BufferedMessage{}).Where("bot_id = ? AND session_id = ? AND state IN ?", owner.BotID, owner.ID, []string{"buffered", "ready", "claimed"}).Updates(map[string]any{"state": "cancelled", "reason": "blocked", "payload_cipher": "", "claim": "", "lease_until": nil, "feedback_pending": false, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&owner).UpdateColumn("version", gorm.Expr("version + 1")).Error; err != nil {
		return err
	}
	var block d.SupportBlock
	err := tx.Where("bot_id = ? AND user_id = ?", owner.BotID, owner.UserID).First(&block).Error
	if err == nil && block.Enabled && (block.ExpiresAt == nil || now.Before(*block.ExpiresAt)) {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&d.SupportBlock{BotID: owner.BotID, UserID: owner.UserID, Enabled: true, ExpiresAt: expires, Version: 1, ActorID: entry.ActorID, Evidence: "verification_card", CreatedAt: now, UpdatedAt: now}).Error
	}
	return tx.Model(&block).Updates(map[string]any{"enabled": true, "expires_at": expires, "version": gorm.Expr("version + 1"), "actor_id": entry.ActorID, "evidence": "verification_card", "updated_at": now}).Error
}
