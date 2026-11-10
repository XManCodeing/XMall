package broadcaststore

import (
	"context"
	"errors"

	broadcastcontract "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/contract"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db               *gorm.DB
	botRecoveryGuard func(*gorm.DB, uint) error
}

// WithBotRecoveryGuard installs the runtime-owned transaction fence in production.
// guard must use tx exclusively; it must not perform network calls or global reads.
func (r *Store) WithBotRecoveryGuard(guard func(*gorm.DB, uint) error) *Store {
	r.botRecoveryGuard = guard
	return r
}

// guardRecovery serializes a bot's ordinary writes with disaster confirmation.
// tx is the caller's active transaction; bot is the persisted instance identifier.
func (r *Store) guardRecovery(tx *gorm.DB, bot uint) error {
	if r.botRecoveryGuard == nil {
		return nil
	}
	return r.botRecoveryGuard(tx, bot)
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (r *Store) Create(ctx context.Context, broadcast *broadcastdomain.Broadcast) error {
	if broadcast == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.guardRecovery(tx, broadcast.BotID); err != nil {
			return err
		}
		return tx.Create(broadcast).Error
	})
}

func (r *Store) GetByID(ctx context.Context, id uint) (*broadcastdomain.Broadcast, error) {
	if id == 0 {
		return nil, nil
	}
	var broadcast broadcastdomain.Broadcast
	if err := r.db.WithContext(ctx).Where("deleted_at IS NULL").First(&broadcast, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &broadcast, nil
}

func (r *Store) List(ctx context.Context, filter broadcastcontract.ListFilter) ([]broadcastdomain.Broadcast, int64, error) {
	query := r.db.WithContext(ctx).Model(&broadcastdomain.Broadcast{}).Where("deleted_at IS NULL")

	if filter.ActiveOnly {
		query = query.Where("status IN ?", []string{"pending", "running"})
	}
	if filter.Keyset {
		query = query.Where("id > ?", filter.AfterID)
	}
	var total int64
	if !filter.Keyset {
		if err := query.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	if filter.PageSize > 0 {
		page := filter.Page
		if page < 1 {
			page = 1
		}
		query = query.Offset((page - 1) * filter.PageSize).Limit(filter.PageSize)
	}

	var items []broadcastdomain.Broadcast
	order := "created_at DESC"
	if filter.Keyset {
		order = "id ASC"
	}
	if err := query.Order(order).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *Store) Update(ctx context.Context, broadcast *broadcastdomain.Broadcast) error {
	if broadcast == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Legacy jobs have no Bot owner; permit their existing quarantine path only.
		if broadcast.BotID != 0 {
			if err := r.guardRecovery(tx, broadcast.BotID); err != nil {
				return err
			}
		}
		var previous broadcastdomain.Broadcast
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, broadcast.ID).Error; err != nil {
			return err
		}
		// A broadcast's originating Bot is immutable; never move historical work.
		if previous.BotID != broadcast.BotID {
			return errors.New("broadcast: originating bot cannot change")
		}
		// A retry may reopen the job after Progress read its terminal snapshot.
		// Recheck on the same transaction before saving, so active-only recovery
		// never loses a retried recipient behind a stale terminal status.
		if broadcast.DeliveryVersion == 1 && (broadcast.Status == "failed" || broadcast.Status == "needs_review" || broadcast.Status == "completed") {
			var pending int64
			if err := tx.Table("telegram_bot_deliveries").Where("bot_id = ? AND broadcast_id = ? AND status IN ?", broadcast.BotID, broadcast.ID, []string{"pending", "retry", "processing"}).Count(&pending).Error; err != nil {
				return err
			}
			if pending > 0 {
				broadcast.Status, broadcast.CompletedAt, broadcast.LastError = "running", nil, ""
			}
		}
		return tx.Save(broadcast).Error
	})
}
