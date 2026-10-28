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

// legacyChannel deliberately projects only the fields required for ownership;
// channel secrets and other credential material must never be loaded here.
type legacyChannel struct {
	ID       uint
	BotToken string
}

// OwnershipLock is shared by both legacy-channel and runtime writers. A durable
// singleton write lock covers absent identities on every supported SQL dialect.
func OwnershipLock(tx *gorm.DB) error {
	return result(tx.Model(&d.BotOwnership{}).Where("id = ? AND state = ?", 1, "lock").UpdateColumn("lock_version", gorm.Expr("lock_version + 1")), c.ErrConflict)
}

func tokenIdentity(open func(string) (string, error), cipher string) (int64, error) {
	if open == nil || cipher == "" {
		return 0, c.ErrConflict
	}
	token, err := open(cipher)
	prefix, suffix, ok := strings.Cut(strings.TrimSpace(token), ":")
	id, parseErr := strconv.ParseInt(prefix, 10, 64)
	if err != nil || !ok || suffix == "" || parseErr != nil || id <= 0 {
		return 0, c.ErrConflict
	}
	return id, nil
}

// OwnershipBackfill uses only the immutable upgrade snapshot. Replacing today's
// credential cannot resolve an unknown historical identity. Unknown rows persist
// across failed requests because migration installed them before writes resume.
func OwnershipBackfill(tx *gorm.DB, open func(string) (string, error)) error {
	var pending []d.BotOwnership
	if err := tx.Where("state = ?", "unresolved").Find(&pending).Error; err != nil {
		return err
	}
	for _, row := range pending {
		id, err := tokenIdentity(open, row.SnapshotCipher)
		if err != nil {
			continue
		}
		history := d.BotOwnership{ChannelID: row.ChannelID, TelegramID: id, State: "legacy"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&history).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{"state": "resolved", "snapshot_cipher": ""}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) legacyOwners(tx *gorm.DB, telegramID int64) ([]uint, error) {
	if err := OwnershipLock(tx); err != nil {
		return nil, err
	}
	if err := OwnershipBackfill(tx, s.openCredential); err != nil {
		return nil, err
	}
	var unknown int64
	if err := tx.Model(&d.BotOwnership{}).Where("state = ?", "unresolved").Count(&unknown).Error; err != nil {
		return nil, err
	}
	if unknown != 0 {
		return nil, c.ErrConflict
	}
	var owners []uint
	if err := tx.Model(&d.BotOwnership{}).Where("telegram_id = ? AND channel_id > 0", telegramID).Order("channel_id").Pluck("channel_id", &owners).Error; err != nil {
		return nil, err
	}
	return owners, nil
}

// GuardLegacyWrite runs under OwnershipLock before any channel mutation. History
// is append-only, and pending/completed takeover permanently fences its channel.
func GuardLegacyWrite(tx *gorm.DB, open func(string) (string, error), channel uint, cipher string, safeDisable bool) error {
	if err := OwnershipBackfill(tx, open); err != nil {
		return err
	}
	var records []d.BotOwnership
	if err := tx.Where("channel_id = ?", channel).Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		if record.State == "unresolved" {
			if safeDisable {
				return nil
			}
			return c.ErrConflict
		}
		if record.State == "pending" || record.State == "internal" {
			if safeDisable {
				return nil
			}
			return c.ErrConflict
		}
		if record.TelegramID > 0 && !safeDisable {
			var conflicts int64
			if err := tx.Model(&d.Instance{}).Where("telegram_id = ? AND (channel_client_id IS NULL OR channel_client_id <> ? OR mode <> ?)", record.TelegramID, channel, "external").Count(&conflicts).Error; err != nil {
				return err
			}
			if conflicts != 0 {
				return c.ErrConflict
			}
		}
	}
	var unknown int64
	if err := tx.Model(&d.BotOwnership{}).Where("state = ?", "unresolved").Count(&unknown).Error; err != nil {
		return err
	}
	if unknown != 0 && !safeDisable {
		return c.ErrConflict
	}
	if cipher == "" {
		if len(records) == 0 {
			return c.ErrConflict
		}
		return nil
	}
	id, err := tokenIdentity(open, cipher)
	if err != nil {
		return err
	}
	var instances int64
	if err := tx.Model(&d.Instance{}).Where("telegram_id = ? AND (channel_client_id IS NULL OR channel_client_id <> ? OR mode <> ?)", id, channel, "external").Count(&instances).Error; err != nil {
		return err
	}
	if instances != 0 {
		return c.ErrConflict
	}
	var conflicts int64
	if err := tx.Model(&d.BotOwnership{}).Where("telegram_id = ? AND channel_id <> ?", id, channel).Count(&conflicts).Error; err != nil {
		return err
	}
	if conflicts != 0 {
		return c.ErrConflict
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&d.BotOwnership{ChannelID: channel, TelegramID: id, State: "legacy"}).Error
}

func (s *Store) guardInternal(tx *gorm.DB, row *d.Instance) error {
	if s.openCredential != nil {
		token, err := s.openCredential(row.TokenCipher)
		prefix, _, ok := strings.Cut(strings.TrimSpace(token), ":")
		id, parseErr := strconv.ParseInt(prefix, 10, 64)
		if err != nil || !ok || parseErr != nil || id != row.TelegramID {
			return c.ErrConflict
		}
	}
	owners, err := s.legacyOwners(tx, row.TelegramID)
	if err != nil {
		return err
	}
	if row.ChannelClientID != nil && !row.CutoverCompleted {
		return c.ErrConflict
	}
	for _, owner := range owners {
		if !row.CutoverCompleted || row.ChannelClientID == nil || *row.ChannelClientID != owner {
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
	if row.CutoverCompleted || row.ChannelClientID != nil || (row.Mode != "" && row.Mode != "internal") {
		return c.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.guardInternal(tx, row); err != nil {
			return err
		}
		return tx.Create(row).Error
	})
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
		if err := OwnershipLock(tx); err != nil {
			return err
		}
		if err := lockBot(tx, id); err != nil {
			return err
		}
		if err := OwnershipLock(tx); err != nil {
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
		if credentialChange && s.openCredential != nil {
			cipher, ok := values["token_cipher"].(string)
			plain, err := s.openCredential(cipher)
			prefix, _, valid := strings.Cut(strings.TrimSpace(plain), ":")
			identity, parseErr := strconv.ParseInt(prefix, 10, 64)
			if !ok || err != nil || !valid || parseErr != nil || identity != row.TelegramID {
				return c.ErrConflict
			}
			row.TokenCipher = cipher
		}
		if credentialChange && row.Mode == "cutover_pending" {
			return c.ErrConflict
		}
		if (hasEnabled && enabled) || (row.Enabled && credentialChange && !(hasEnabled && !enabled)) {
			if row.Mode != "internal" {
				return c.ErrConflict
			}
			if err := s.guardInternal(tx, &row); err != nil {
				return err
			}
		}
		return result(tx.Model(&d.Instance{}).Where("id = ? AND version = ?", id, version).Updates(changes), c.ErrConflict)
	})
}

// ImportLegacy is the only entry point that may associate a legacy channel.
// It also quarantines pre-existing ordinary instances instead of allowing them
// to escape cutover by virtue of having been created before this protection.
func (s *Store) ImportLegacy(ctx context.Context, input *d.Instance, sourceCipher string) (*d.Instance, error) {
	var row d.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owners, err := s.legacyOwners(tx, input.TelegramID)
		if err != nil {
			return err
		}
		if input.ChannelClientID == nil || len(owners) != 1 || owners[0] != *input.ChannelClientID {
			return c.ErrConflict
		}
		var source legacyChannel
		if err := tx.Table("channel_clients").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "bot_token").Where("id = ? AND deleted_at IS NULL", owners[0]).First(&source).Error; err != nil {
			return c.ErrConflict
		}
		if source.BotToken != sourceCipher {
			return c.ErrConflict
		}
		sourceID, sourceErr := tokenIdentity(s.openCredential, source.BotToken)
		inputID, inputErr := tokenIdentity(s.openCredential, input.TokenCipher)
		if sourceErr != nil || inputErr != nil || sourceID != input.TelegramID || inputID != input.TelegramID {
			return c.ErrConflict
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("telegram_id = ?", input.TelegramID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = *input
			row.ID, row.Enabled, row.Mode, row.CutoverCompleted = 0, false, "external", false
			return tx.Create(&row).Error
		}
		if err != nil {
			return err
		}
		if row.ChannelClientID != nil && *row.ChannelClientID != owners[0] {
			return c.ErrConflict
		}
		if row.CutoverCompleted {
			if row.ChannelClientID == nil {
				return c.ErrConflict
			}
			return nil
		}
		if !row.Enabled && row.ChannelClientID != nil && (row.Mode == "external" || row.Mode == "cutover_pending") {
			stored, oldErr := s.openCredential(row.TokenCipher)
			incoming, newErr := s.openCredential(input.TokenCipher)
			if oldErr == nil && newErr == nil && stored == incoming {
				return nil
			}
		}
		// Import may rotate a credential only while still external. Pending
		// operations must not silently acquire a different source snapshot.
		if row.Mode == "cutover_pending" {
			return c.ErrConflict
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{"channel_client_id": owners[0], "mode": "external", "enabled": false, "token_cipher": input.TokenCipher, "username": input.Username, "version": gorm.Expr("version + 1"), "generation": gorm.Expr("generation + 1")}).Error; err != nil {
			return err
		}
		return tx.First(&row, row.ID).Error
	})
	return &row, err
}

// SwitchCutover is intentionally separate from ordinary configuration. The
// completion marker can only be earned by the explicit pending -> internal CAS.
func (s *Store) SwitchCutover(ctx context.Context, id uint, version uint64, mode string) error {
	from := "external"
	completed := false
	if mode == "internal" {
		from, completed = "cutover_pending", true
	} else if mode != "cutover_pending" {
		return c.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := OwnershipLock(tx); err != nil {
			return err
		}
		var row d.Instance
		if err := tx.First(&row, id).Error; err != nil {
			return notFound(err)
		}
		owners, err := s.legacyOwners(tx, row.TelegramID)
		if err != nil {
			return err
		}
		if row.ChannelClientID == nil || len(owners) != 1 || owners[0] != *row.ChannelClientID {
			return c.ErrConflict
		}
		var source legacyChannel
		if err := tx.Table("channel_clients").Select("id", "bot_token").Where("id = ? AND deleted_at IS NULL", *row.ChannelClientID).First(&source).Error; err != nil {
			return c.ErrConflict
		}
		sourceID, err := tokenIdentity(s.openCredential, source.BotToken)
		if err != nil || sourceID != row.TelegramID {
			return c.ErrConflict
		}
		if err := result(tx.Model(&d.Instance{}).Where("id = ? AND version = ? AND mode = ? AND enabled = ? AND channel_client_id IS NOT NULL", id, version, from, false).Updates(map[string]interface{}{"mode": mode, "cutover_completed": completed, "version": gorm.Expr("version + 1")}), c.ErrConflict); err != nil {
			return err
		}
		state := "pending"
		if completed {
			state = "internal"
		}
		return tx.Model(&d.BotOwnership{}).Where("channel_id = ? AND telegram_id > 0", *row.ChannelClientID).Update("state", state).Error
	})
}
func (s *Store) BeginGeneration(ctx context.Context, id uint, version uint64) (*d.Instance, error) {
	var row d.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := OwnershipLock(tx); err != nil {
			return err
		}
		var current d.Instance
		if err := tx.First(&current, id).Error; err != nil {
			return notFound(err)
		}
		if err := s.guardInternal(tx, &current); err != nil {
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
			if err := OwnershipLock(tx); err != nil {
				return err
			}
			var row d.Instance
			if err := tx.First(&row, id).Error; err != nil {
				return notFound(err)
			}
			if err := s.guardInternal(tx, &row); err != nil {
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
	tx := s.db.WithContext(ctx).Model(&d.Session{}).Where("bot_id = ? AND telegram_user_id = ? AND chat_id = ? AND version = ?", row.BotID, row.TelegramUserID, row.ChatID, version).Updates(map[string]interface{}{"version": gorm.Expr("version + 1"), "state": row.State, "data_cipher": row.DataCipher, "expires_at": row.ExpiresAt})
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
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "key"}}, DoNothing: true}).Create(row).Error
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
