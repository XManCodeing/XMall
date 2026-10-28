package broadcastcontract

import broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"

type ListFilter struct {
	Page     int
	PageSize int
}

type Store interface {
	Create(broadcast *broadcastdomain.Broadcast) error
	GetByID(id uint) (*broadcastdomain.Broadcast, error)
	List(filter ListFilter) ([]broadcastdomain.Broadcast, int64, error)
	Update(broadcast *broadcastdomain.Broadcast) error
}
