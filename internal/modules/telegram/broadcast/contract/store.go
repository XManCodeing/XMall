package broadcastcontract

import (
	"context"

	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

type ListFilter struct {
	Page     int
	PageSize int
}

type Store interface {
	Create(ctx context.Context, broadcast *broadcastdomain.Broadcast) error
	GetByID(ctx context.Context, id uint) (*broadcastdomain.Broadcast, error)
	List(ctx context.Context, filter ListFilter) ([]broadcastdomain.Broadcast, int64, error)
	Update(ctx context.Context, broadcast *broadcastdomain.Broadcast) error
}
