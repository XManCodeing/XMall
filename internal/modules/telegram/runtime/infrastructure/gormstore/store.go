package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db             *gorm.DB
	openCredential func(string) (string, error)
}

// SetCredentialOpener is wired once before the runtime is exposed.
func (s *Store) SetCredentialOpener(open func(string) (string, error)) { s.openCredential = open }

func (s *Store) guardCredential(row *d.Instance) error {
	if s.openCredential != nil {
		token, err := s.openCredential(row.TokenCipher)
		prefix, suffix, ok := strings.Cut(strings.TrimSpace(token), ":")
		id, parseErr := strconv.ParseInt(prefix, 10, 64)
		if err != nil || !ok || suffix == "" || parseErr != nil || id <= 0 || id != row.TelegramID {
			return c.ErrConflict
		}
	}
	return nil
}

var _ c.Store = (*Store)(nil)

func New(db *gorm.DB) *Store { return &Store{db: db} }

// Persist only a bounded, non-sensitive diagnostic. Upstream errors can contain
// Bot API URLs, credentials, message text or database parameters.
func readyAt(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func safeError(value string) string {
	switch value {
	case "", "credential unavailable", "webhook check failed or takeover required",
		"telegram polling failed", "update processing failed", "recipient authorization failed",
		"message payload unavailable", "recipient binding changed", "delivery outcome unknown",
		"telegram rejected message":
		return value
	default:
		return "telegram operation failed"
	}
}
func result(tx *gorm.DB, missing error) error {
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return missing
	}
	return nil
}
func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrNotFound
	}
	return err
}
func (s *Store) List(ctx context.Context) ([]d.Instance, error) {
	var rows []d.Instance
	err := s.db.WithContext(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}
func (s *Store) Get(ctx context.Context, id uint) (*d.Instance, error) {
	var row d.Instance
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &row, nil
}
func (s *Store) Create(ctx context.Context, row *d.Instance) error {
	row.LastError = safeError(row.LastError)
	if row.Mode != "" && row.Mode != "internal" {
		return c.ErrConflict
	}
	if err := s.guardCredential(row); err != nil {
		return err
	}
	// The unique TelegramID index arbitrates simultaneous creates on both
	// SQLite and PostgreSQL; do not perform a racy read-before-insert check.
	tx := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "telegram_id"}}, DoNothing: true,
	}).Create(row)
	return result(tx, c.ErrDuplicate)
}
func (s *Store) Configure(ctx context.Context, id uint, version uint64, values map[string]interface{}) error {
	changes := make(map[string]interface{}, len(values)+1)
	for k, v := range values {
		switch k {
		case "enabled":
			if _, ok := v.(bool); !ok {
				return c.ErrConflict
			}
			changes[k] = v
		case "username", "token_cipher":
			if _, ok := v.(string); !ok {
				return c.ErrConflict
			}
			changes[k] = v
		default:
			return fmt.Errorf("unsupported telegram configuration field %q", k)
		}
	}
	changes["version"] = gorm.Expr("version + 1")
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, id); err != nil {
			return err
		}
		var row d.Instance
		if err := tx.First(&row, id).Error; err != nil {
			return notFound(err)
		}
		if row.Version != version {
			return c.ErrConflict
		}
		enabled, hasEnabled := values["enabled"].(bool)
		_, credentialChange := values["token_cipher"]
		if credentialChange {
			row.TokenCipher = values["token_cipher"].(string)
			if err := s.guardCredential(&row); err != nil {
				return err
			}
		}
		if (hasEnabled && enabled) || (row.Enabled && credentialChange && !(hasEnabled && !enabled)) {
			if row.Mode != "internal" {
				return c.ErrConflict
			}
			if err := s.guardCredential(&row); err != nil {
				return err
			}
		}
		return result(tx.Model(&d.Instance{}).Where("id = ? AND version = ?", id, version).Updates(changes), c.ErrConflict)
	})
}

func (s *Store) SaveProfile(ctx context.Context, id uint, version uint64, values map[string]interface{}) error {
	changes := make(map[string]interface{}, len(values)+1)
	for k, v := range values {
		switch k {
		case "profile_name", "profile_description", "profile_cover_url", "profile_sync_error":
			if _, ok := v.(string); !ok {
				return c.ErrConflict
			}
			changes[k] = v
		default:
			return fmt.Errorf("unsupported telegram profile field %q", k)
		}
	}
	changes["profile_version"] = gorm.Expr("CASE WHEN profile_version = 0 THEN 2 ELSE profile_version + 1 END")
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, id); err != nil {
			return err
		}
		var row d.Instance
		if err := tx.First(&row, id).Error; err != nil {
			return notFound(err)
		}
		current := row.ProfileVersion
		if current == 0 {
			current = 1
		}
		if current != version {
			return c.ErrConflict
		}
		return result(tx.Model(&d.Instance{}).Where("id = ? AND (profile_version = ? OR (profile_version = 0 AND ? = 1))", id, version, version).Updates(changes), c.ErrConflict)
	})
}

func (s *Store) BeginGeneration(ctx context.Context, id uint, version uint64) (*d.Instance, error) {
	var row d.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, id); err != nil {
			return err
		}
		var current d.Instance
		if err := tx.First(&current, id).Error; err != nil {
			return notFound(err)
		}
		if err := s.guardCredential(&current); err != nil {
			return err
		}
		if err := result(tx.Model(&d.Instance{}).Where("id = ? AND version = ? AND enabled = ? AND mode = ?", id, version, true, "internal").Updates(map[string]interface{}{"generation": gorm.Expr("generation + 1"), "state": "starting", "last_error": ""}), c.ErrStale); err != nil {
			return err
		}
		return tx.First(&row, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) Heartbeat(ctx context.Context, id uint, generation uint64, state, lastError string, applied uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if state == "running" {
			if err := lockBot(tx, id); err != nil {
				return err
			}
			var row d.Instance
			if err := tx.First(&row, id).Error; err != nil {
				return notFound(err)
			}
			if err := s.guardCredential(&row); err != nil {
				return err
			}
		}
		return result(tx.Model(&d.Instance{}).Where("id = ? AND generation = ?", id, generation).Updates(map[string]interface{}{"state": state, "last_error": safeError(lastError), "last_seen_at": time.Now().UTC(), "applied_version": applied}), c.ErrStale)
	})
}
func (s *Store) Accept(ctx context.Context, id uint, generation uint64, rows []d.Inbox, offset int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// This write fences the generation and locks the instance until commit, so a
		// concurrent restart cannot race between the check and the inbox inserts.
		if err := result(tx.Model(&d.Instance{}).Where("id = ? AND generation = ?", id, generation).Updates(map[string]interface{}{"offset": gorm.Expr("CASE WHEN "+tx.Statement.Quote("offset")+" < ? THEN ? ELSE "+tx.Statement.Quote("offset")+" END", offset, offset), "last_received_at": time.Now().UTC()}), c.ErrStale); err != nil {
			return err
		}
		for _, input := range rows {
			row := d.Inbox{BotID: id, UpdateID: input.UpdateID, PayloadCipher: input.PayloadCipher, Status: "pending", AvailableAt: input.AvailableAt.UTC()}
			if row.AvailableAt.IsZero() {
				row.AvailableAt = time.Now().UTC()
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "update_id"}}, DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Taking the parent write lock first also serializes contenders on SQLite (which
// has no SELECT FOR UPDATE), and never uses an additional connection in a tx.
func lockBot(tx *gorm.DB, id uint) error {
	return tx.Model(&d.Instance{}).Where("id = ?", id).UpdateColumn("generation", gorm.Expr("generation")).Error
}
func (s *Store) ClaimInbox(ctx context.Context, bot uint, claim string, until time.Time) (*d.Inbox, error) {
	if claim == "" || !until.After(time.Now()) {
		return nil, c.ErrConflict
	}
	until = until.UTC()
	var row d.Inbox
	found := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, bot); err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND status IN ?", bot, []string{"pending", "retry", "processing"}).Order("update_id ASC").Order("id ASC").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if row.Status == "processing" {
			if row.ClaimedUntil != nil && row.ClaimedUntil.After(now) {
				return nil
			}
		} else if row.AvailableAt.After(now) {
			return nil
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{"status": "processing", "claim": claim, "claimed_until": until, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
			return err
		}
		row.Status = "processing"
		row.Claim = claim
		row.ClaimedUntil = &until
		row.Attempts++
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &row, nil
}
func (s *Store) FinishInbox(ctx context.Context, id uint, claim, status, lastError string, available time.Time) error {
	if claim == "" {
		return c.ErrStale
	}
	return result(s.db.WithContext(ctx).Model(&d.Inbox{}).Where("id = ? AND status = ? AND claim = ?", id, "processing", claim).Updates(map[string]interface{}{"status": status, "claim": "", "claimed_until": nil, "last_error": safeError(lastError), "available_at": readyAt(available)}), c.ErrStale)
}
func (s *Store) Contact(ctx context.Context, bot uint, user int64) (*d.Contact, error) {
	var row d.Contact
	err := s.db.WithContext(ctx).Where("bot_id = ? AND telegram_user_id = ?", bot, user).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) SaveContact(ctx context.Context, row *d.Contact) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "telegram_user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"chat_id", "user_id", "reachable", "locale", "updated_at"})}).Create(row).Error
}
func (s *Store) GetSession(ctx context.Context, bot uint, user, chat int64) (*d.Session, error) {
	var row d.Session
	err := s.db.WithContext(ctx).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ?", bot, user, chat).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) SaveSession(ctx context.Context, row *d.Session, version uint64) error {
	row.ExpiresAt = readyAt(row.ExpiresAt)
	if version == 0 {
		fresh := *row
		fresh.ID = 0
		fresh.Version = 1
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockBot(tx, row.BotID); err != nil {
				return err
			}
			var existing d.Session
			err := tx.Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ?", row.BotID, row.TelegramUserID, row.ChatID).First(&existing).Error
			if err == nil {
				return c.ErrConflict
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return tx.Create(&fresh).Error
		})
		if err != nil {
			return err
		}
		*row = fresh
		return nil
	}
	tx := s.db.WithContext(ctx).Model(&d.Session{}).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND version = ?", row.BotID, row.TelegramUserID, row.ChatID, version).Updates(map[string]interface{}{"version": gorm.Expr("version + 1"), "state": row.State, "data_cipher": row.DataCipher, "expires_at": row.ExpiresAt, "panel_epoch": row.PanelEpoch, "panel_revision": row.PanelRevision})
	if err := result(tx, c.ErrConflict); err != nil {
		return err
	}
	row.Version = version + 1
	return nil
}
func (s *Store) Enqueue(ctx context.Context, row *d.Delivery) error {
	row.AvailableAt = row.AvailableAt.UTC()
	if row.AvailableAt.IsZero() {
		row.AvailableAt = time.Now().UTC()
	}
	row.LastError = safeError(row.LastError)
	if row.Status == "" {
		row.Status = "pending"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, row.BotID); err != nil {
			return err
		}
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "key"}}, DoNothing: true}).Create(row)
		if created.Error != nil || created.RowsAffected == 0 {
			return created.Error
		}
		if row.Purpose != "panel" {
			return nil
		}
		// Only untouched render requests are mergeable. Notices, cleanup and
		// started/unknown creations are never replaced by a newer rendering.
		return tx.Model(&d.Delivery{}).Where("bot_id = ? AND chat_id = ? AND telegram_user_id = ? AND purpose = ? AND panel_epoch = ? AND panel_revision < ? AND status = ? AND attempts = 0", row.BotID, row.ChatID, row.TelegramUserID, "panel", row.PanelEpoch, row.PanelRevision, "pending").Updates(map[string]interface{}{"status": "superseded", "payload_cipher": ""}).Error
	})
}
func (s *Store) ClaimDelivery(ctx context.Context, bot uint, claim string, until time.Time) (*d.Delivery, error) {
	if claim == "" || !until.After(time.Now()) {
		return nil, c.ErrConflict
	}
	until = until.UTC()
	var row d.Delivery
	found := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBot(tx, bot); err != nil {
			return err
		}
		now := time.Now().UTC()
		// Editing/deleting the same target is idempotent; recover it only within
		// the bounded attempt budget. A send remains unknown after a crash.
		if err := tx.Model(&d.Delivery{}).Where("bot_id = ? AND status = ? AND operation IN ? AND attempts < ? AND target_message_id > 0 AND (claimed_until IS NULL OR claimed_until <= ?)", bot, "processing", []string{"edit", "delete", "clear_buttons", "markup"}, 3, now).Where("purpose <> ? OR attempts < ?", "cleanup", 2).Updates(map[string]interface{}{"status": "retry", "claim": "", "claimed_until": nil, "available_at": now}).Error; err != nil {
			return err
		}
		// A crash after send has an unknown outcome. Never automatically resend it.
		if err := tx.Model(&d.Delivery{}).Where("bot_id = ? AND status = ? AND (claimed_until IS NULL OR claimed_until <= ?)", bot, "processing", now).Updates(map[string]interface{}{"status": "unknown", "claim": "", "claimed_until": nil, "last_error": "delivery outcome unknown"}).Error; err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND status IN ? AND available_at <= ?", bot, []string{"pending", "retry"}, now).Order("priority DESC").Order("id ASC").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{"status": "processing", "claim": claim, "claimed_until": until, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
			return err
		}
		row.Status = "processing"
		row.Claim = claim
		row.ClaimedUntil = &until
		row.Attempts++
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &row, nil
}
func (s *Store) FinishDelivery(ctx context.Context, id uint, claim, status string, message int64, lastError string, available time.Time) error {
	if claim == "" {
		return c.ErrStale
	}
	return result(s.db.WithContext(ctx).Model(&d.Delivery{}).Where("id = ? AND status = ? AND claim = ?", id, "processing", claim).Updates(map[string]interface{}{"status": status, "message_id": message, "claim": "", "claimed_until": nil, "last_error": safeError(lastError), "available_at": readyAt(available)}), c.ErrStale)
}
func (s *Store) ListDeliveries(ctx context.Context, bot, broadcast uint, page, size int) ([]d.Delivery, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	q := s.db.WithContext(ctx).Model(&d.Delivery{}).Where("bot_id = ?", bot)
	if broadcast != 0 {
		q = q.Where("broadcast_id = ?", broadcast)
	}
	var total int64
	var rows []d.Delivery
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}
