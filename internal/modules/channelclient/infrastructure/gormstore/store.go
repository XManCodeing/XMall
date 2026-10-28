package channelclientstore

import (
	"errors"
	"time"

	channelclientdomain "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"

	"github.com/XManCodeing/XMall/internal/crypto"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db   *gorm.DB
	open func(string) (string, error)
}

func (store *Store) SetCredentialSecret(secret string) {
	key := crypto.DeriveKey(secret)
	store.open = func(s string) (string, error) { return crypto.Decrypt(key, s) }
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (store *Store) Create(client *channelclientdomain.Client) error {
	return store.db.Transaction(func(tx *gorm.DB) error {
		if err := runtime.OwnershipLock(tx); err != nil {
			return err
		}
		client.Revision = 1
		if err := tx.Create(client).Error; err != nil {
			return err
		}
		if client.ChannelType == "telegram_bot" {
			return runtime.GuardLegacyWrite(tx, store.open, client.ID, client.BotToken, false)
		}
		return nil
	})
}

// RepairTelegramHistory is an explicit administrator attestation of the complete
// historical Bot ID set, not inference from a replacement token. The channel must
// already be disabled, and the unresolved marker is retained as an audit record.
func (store *Store) RepairTelegramHistory(id uint, revision uint64, identities []int64) error {
	if len(identities) == 0 || len(identities) > 100 {
		return errors.New("complete historical bot IDs required")
	}
	return store.db.Transaction(func(tx *gorm.DB) error {
		if err := runtime.OwnershipLock(tx); err != nil {
			return err
		}
		var client channelclientdomain.Client
		if err := tx.First(&client, id).Error; err != nil {
			return err
		}
		if client.Status != 0 || client.Revision != revision || client.ChannelType != "telegram_bot" {
			return errors.New("disable channel and reload before history repair")
		}
		var marker d.BotOwnership
		if err := tx.Where("channel_id = ? AND state = ?", id, "unresolved").First(&marker).Error; err != nil {
			return err
		}
		for _, botID := range identities {
			if botID <= 0 {
				return errors.New("invalid historical bot ID")
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&d.BotOwnership{ChannelID: id, TelegramID: botID, State: "legacy"}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&marker).Updates(map[string]interface{}{"state": "attested", "snapshot_cipher": "", "attested_at": time.Now().UTC(), "repair_revision": revision}).Error; err != nil {
			return err
		}
		return tx.Model(&client).Update("revision", gorm.Expr("revision + 1")).Error
	})
}

func (store *Store) FindByID(id uint) (*channelclientdomain.Client, error) {
	return store.first(store.db.Where("id = ?", id))
}

func (store *Store) FindByChannelKey(key string) (*channelclientdomain.Client, error) {
	return store.first(store.authorized(store.db.Where("channel_key = ?", key)))
}

func (store *Store) FindActiveByChannelType(channelType string) (*channelclientdomain.Client, error) {
	return store.first(store.authorized(store.db.Where("channel_type = ? AND status = 1", channelType)))
}

func (store *Store) FindAll() ([]channelclientdomain.Client, error) {
	var clients []channelclientdomain.Client
	if err := store.db.Where("deleted_at IS NULL").Order("created_at DESC").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

func (store *Store) Update(client *channelclientdomain.Client) error {
	return store.db.Transaction(func(tx *gorm.DB) error {
		if err := runtime.OwnershipLock(tx); err != nil {
			return err
		}
		var old channelclientdomain.Client
		if err := tx.First(&old, client.ID).Error; err != nil {
			return err
		}
		if old.DeletedAt != nil || old.Revision != client.Revision || old.ChannelType != client.ChannelType {
			return errors.New("channel configuration conflict")
		}
		if old.ChannelType == "telegram_bot" {
			safeDisable := client.Status == 0 && client.BotToken == old.BotToken && client.CallbackURL == old.CallbackURL
			if err := runtime.GuardLegacyWrite(tx, store.open, client.ID, client.BotToken, safeDisable); err != nil {
				return err
			}
		}
		client.Revision++
		// Never Save: its missing-row fallback can resurrect a deleted/stale row.
		return tx.Model(&old).Select("name", "description", "channel_secret", "bot_token", "callback_url", "status", "revision").Updates(client).Error
	})
}

func (store *Store) UpdateLastUsed(id uint, usedAt time.Time) error {
	return store.db.Model(&channelclientdomain.Client{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("last_used_at", usedAt).Error
}

func (store *Store) Delete(id uint, deletedAt time.Time) error {
	return store.db.Transaction(func(tx *gorm.DB) error {
		if err := runtime.OwnershipLock(tx); err != nil {
			return err
		}
		if err := runtime.OwnershipBackfill(tx, store.open); err != nil {
			return err
		}
		return tx.Model(&channelclientdomain.Client{}).Where("id = ? AND deleted_at IS NULL", id).Updates(map[string]interface{}{"deleted_at": deletedAt, "status": 0, "revision": gorm.Expr("revision + 1")}).Error
	})
}

// Administrative reads remain available for repair; credential-resolution reads
// must not revive unknown, pending or taken-over channels after an upgrade.
func (store *Store) authorized(tx *gorm.DB) *gorm.DB {
	return tx.Where(`(channel_type <> 'telegram_bot' OR (
	 NOT EXISTS (SELECT 1 FROM telegram_bot_ownership h WHERE h.channel_id = channel_clients.id AND h.state IN ('unresolved','pending','internal'))
	 AND NOT EXISTS (SELECT 1 FROM telegram_bot_ownership h JOIN telegram_bot_instances b ON b.telegram_id = h.telegram_id WHERE h.channel_id = channel_clients.id AND (b.channel_client_id IS NULL OR b.channel_client_id <> channel_clients.id OR b.mode <> 'external'))
	))`)
}

func (store *Store) first(query *gorm.DB) (*channelclientdomain.Client, error) {
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := runtime.OwnershipLock(tx); err != nil {
			return err
		}
		return runtime.OwnershipBackfill(tx, store.open)
	}); err != nil {
		return nil, err
	}
	var client channelclientdomain.Client
	if err := query.Where("deleted_at IS NULL").First(&client).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &client, nil
}
