package fulfillmentwiring

import (
	"github.com/XManCodeing/XMall/internal/app/container"
	fulfillmenttransport "github.com/XManCodeing/XMall/internal/modules/fulfillment/transport/http"
)

func NewAdminHandler(c *container.Container) *fulfillmenttransport.AdminHandler {
	return fulfillmenttransport.NewAdminHandler(
		fulfillmentManualCreatorAdapter{svc: c.FulfillmentService},
		fulfillmentAdminOrderAdapter{orders: c.OrderService},
	)
}
