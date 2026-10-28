package broadcaststore

import (
	"context"
	"errors"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	b "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RetryBroadcastDelivery updates the existing ledger row and job atomically.
// Exact error allowlisting excludes identity failures and ambiguous sends. Never
// replace PayloadCipher (including its original stamp), key or attempt history.
func (s *Store) RetryBroadcastDelivery(ctx context.Context, bot, broadcast, delivery uint) error {
	if bot == 0 || broadcast == 0 || delivery == 0 {
		return app.ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job b.Broadcast
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND bot_id = ? AND deleted_at IS NULL", broadcast, bot).First(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if job.DeliveryVersion != 1 {
			return app.ErrInvalid
		}
		var row d.Delivery
		err = tx.Where("id = ? AND bot_id = ? AND broadcast_id = ?", delivery, bot, broadcast).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !d.CanRetryBroadcastDelivery(row) {
			return app.ErrRetryDenied
		}
		now := time.Now().UTC()
		result := tx.Model(&d.Delivery{}).
			Where("id = ? AND bot_id = ? AND broadcast_id = ? AND status = ? AND last_error = ? AND message_id = 0 AND claim = '' AND claimed_until IS NULL AND payload_cipher <> ''", delivery, bot, broadcast, "failed", "telegram rejected message").
			Updates(map[string]interface{}{"status": "retry", "available_at": now, "last_error": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return app.ErrRetryDenied
		}
		return tx.Model(&b.Broadcast{}).Where("id = ?", broadcast).Updates(map[string]interface{}{
			"status": "running", "completed_at": nil, "last_error": "",
			"failed_count": gorm.Expr("CASE WHEN failed_count > 0 THEN failed_count - 1 ELSE 0 END"),
		}).Error
	})
}
