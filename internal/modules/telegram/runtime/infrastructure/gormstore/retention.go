package gormstore

import (
	"context"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
)

var _ c.PayloadRetentionStore = (*Store)(nil)

func utcRetention(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// The parent write both checks generation and prevents it changing until commit.
func fenceGeneration(tx *gorm.DB, botID uint, generation uint64) error {
	if err := lockBot(tx, botID); err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&d.Instance{}).Where("id = ? AND generation = ?", botID, generation).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return c.ErrStale
	}
	return nil
}

func (s *Store) HoldInboxPayload(ctx context.Context, botID uint, generation uint64, id uint, claim string, retainUntil *time.Time) error {
	return s.holdPayload(ctx, &d.Inbox{}, botID, generation, id, claim, retainUntil)
}
func (s *Store) HoldDeliveryPayload(ctx context.Context, botID uint, generation uint64, id uint, claim string, retainUntil *time.Time) error {
	return s.holdPayload(ctx, &d.Delivery{}, botID, generation, id, claim, retainUntil)
}
func (s *Store) ReleaseInboxPayload(ctx context.Context, botID uint, generation uint64, id uint) error {
	return s.releasePayload(ctx, &d.Inbox{}, botID, generation, id)
}
func (s *Store) ReleaseDeliveryPayload(ctx context.Context, botID uint, generation uint64, id uint) error {
	return s.releasePayload(ctx, &d.Delivery{}, botID, generation, id)
}

func (s *Store) holdPayload(ctx context.Context, model interface{}, botID uint, generation uint64, id uint, claim string, retainUntil *time.Time) error {
	if claim == "" {
		return c.ErrStale
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fenceGeneration(tx, botID, generation); err != nil {
			return err
		}
		changes := map[string]interface{}{"payload_held": true}
		if retainUntil != nil {
			until := retainUntil.UTC()
			// Independent dependencies cannot accidentally shorten an existing TTL.
			changes["payload_retain_until"] = gorm.Expr("CASE WHEN payload_retain_until IS NULL OR payload_retain_until < ? THEN ? ELSE payload_retain_until END", until, until)
		}
		return result(tx.Model(model).Where("id = ? AND bot_id = ? AND status = ? AND claim = ? AND claimed_until > ? AND payload_cipher <> ?", id, botID, "processing", claim, time.Now().UTC(), "").Updates(changes), c.ErrStale)
	})
}
func (s *Store) releasePayload(ctx context.Context, model interface{}, botID uint, generation uint64, id uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fenceGeneration(tx, botID, generation); err != nil {
			return err
		}
		// The trusted owner releases only after its durable reference is no longer
		// needed. This does not make an unknown delivery eligible for cleanup.
		return result(tx.Model(model).Where("id = ? AND bot_id = ? AND payload_held = ?", id, botID, true).Update("payload_held", false), c.ErrStale)
	})
}
