package broadcastapp

import (
	"context"
	"errors"
	"time"

	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

var ErrRetryDenied = errors.New("only failed deliveries with a definite Telegram rejection can be retried")

// DeliveryItem deliberately excludes encrypted payloads, identity stamps and claims.
type DeliveryItem struct {
	ID             uint      `json:"id"`
	BotID          uint      `json:"bot_id"`
	BroadcastID    uint      `json:"broadcast_id"`
	UserID         uint      `json:"user_id"`
	TelegramUserID int64     `json:"telegram_user_id"`
	ChatID         int64     `json:"chat_id"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	MessageID      int64     `json:"message_id"`
	LastError      string    `json:"last_error"`
	Retryable      bool      `json:"retryable"`
	AvailableAt    time.Time `json:"available_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Optional capabilities avoid widening existing stores and bridge mocks.
type DeliveryReader interface {
	ListBroadcastDeliveries(context.Context, uint, uint, int, int) ([]DeliveryItem, int64, error)
}
type DeliveryRetryStore interface {
	RetryBroadcastDelivery(context.Context, uint, uint, uint) error
}

func (s *Service) deliveryBroadcast(ctx context.Context, id uint) (*broadcastdomain.Broadcast, error) {
	if s == nil || s.store == nil || id == 0 {
		return nil, ErrInvalid
	}
	b, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	if b.DeliveryVersion != 1 || b.BotID == 0 {
		return nil, ErrInvalid
	}
	return b, nil
}

func (s *Service) ListBroadcastDeliveries(ctx context.Context, id uint, page, size int) ([]DeliveryItem, int64, error) {
	b, err := s.deliveryBroadcast(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	reader, ok := s.deliveries.(DeliveryReader)
	if !ok {
		return nil, 0, ErrInvalid
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	// Never accept a caller-controlled bot ID.
	return reader.ListBroadcastDeliveries(ctx, b.BotID, b.ID, page, size)
}

func (s *Service) RetryBroadcastDelivery(ctx context.Context, id, deliveryID uint) error {
	b, err := s.deliveryBroadcast(ctx, id)
	if err != nil {
		return err
	}
	if deliveryID == 0 {
		return ErrInvalid
	}
	store, ok := s.store.(DeliveryRetryStore)
	if !ok {
		return ErrInvalid
	}
	// Atomic requeue + job reopening. No direct send, re-encryption or new stamp:
	// the supervised runtime revalidates the ORIGINAL identity before sending.
	return store.RetryBroadcastDelivery(ctx, b.BotID, b.ID, deliveryID)
}
