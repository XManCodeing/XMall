package application

import (
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
)

func appendFulfilledEvent(tx ordercontract.Transaction, order *orderdomain.Order, fulfillment *fulfillmentdomain.Fulfillment) error {
	var parentID uint
	if order.ParentID != nil {
		parentID = *order.ParentID
	}
	return delivery.AppendOrder(tx.Orders(), delivery.EventOrderFulfilled, order.ID, parentID, 0, fulfillment.ID)
}

// ReadDelivered returns the existing fulfillment only. Re-delivery must use this
// entry point instead of CreateAuto so it can never allocate another card.
func (s *Service) ReadDelivered(orderID, userID uint) (*fulfillmentdomain.Fulfillment, error) {
	if userID == 0 {
		return nil, ErrOrderNotFound
	}
	order, err := s.orderStore.GetByID(orderID)
	if err != nil {
		return nil, err
	}
	if order == nil || order.UserID != userID {
		return nil, ErrOrderNotFound
	}
	return order.Fulfillment, nil
}
