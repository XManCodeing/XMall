package contract

import fulfillmentdomain "github.com/homles698698-lab/XMall/internal/modules/fulfillment/domain"

// Store 是交付记录持久化端口。
type Store interface {
	Create(fulfillment *fulfillmentdomain.Fulfillment) error
	GetByOrderID(orderID uint) (*fulfillmentdomain.Fulfillment, error)
	FindByOrderIDForUpdate(orderID uint) (*fulfillmentdomain.Fulfillment, bool, error)
}
