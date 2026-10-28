package fulfillmentwiring

import (
	"github.com/homles698698-lab/XMall/internal/app/container"
	fulfillmenttransport "github.com/homles698698-lab/XMall/internal/modules/fulfillment/transport/http"
)

func NewAdminHandler(c *container.Container) *fulfillmenttransport.AdminHandler {
	return fulfillmenttransport.NewAdminHandler(
		fulfillmentManualCreatorAdapter{svc: c.FulfillmentService},
		fulfillmentAdminOrderAdapter{orders: c.OrderService},
	)
}
