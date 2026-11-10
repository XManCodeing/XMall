package application

import (
	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	"time"
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

// prepareOrdinary 在事务中确定允许交付的普通项，过滤会员及其他交付方式。
// tx 为已开启事务，order 为调用方叶单，kind 为人工或卡密；失败不得写交付。
func prepareOrdinary(tx ordercontract.Transaction, order *orderdomain.Order, kind string) error {
	if store, ok := tx.Fulfillments().(fc.OrdinaryItemStore); ok {
		ids, err := store.OrdinaryItems(order.ID, kind)
		if err != nil {
			return err
		}
		allowed := map[uint]bool{}
		for _, id := range ids {
			allowed[id] = true
		}
		items := make([]orderdomain.OrderItem, 0, len(ids))
		for _, item := range order.Items {
			if allowed[item.ID] {
				items = append(items, item)
			}
		}
		order.Items = items
		return nil
	}
	if _, found, err := tx.Fulfillments().FindByOrderIDForUpdate(order.ID); err != nil {
		return err
	} else if found {
		return ErrFulfillmentExists
	}
	if kind == constants.FulfillmentTypeAuto {
		for _, item := range order.Items {
			if item.FulfillmentType != kind {
				return ErrFulfillmentNotAuto
			}
		}
	}
	return nil
}

// saveOrdinary 持久化真实普通交付；支持混合叶单合并内容，返回数据库错误。
func saveOrdinary(tx ordercontract.Transaction, fulfillment *fulfillmentdomain.Fulfillment) error {
	if store, ok := tx.Fulfillments().(fc.OrdinaryItemStore); ok {
		return store.SaveOrdinary(fulfillment)
	}
	return tx.Fulfillments().Create(fulfillment)
}

// completeOrdinary 汇总项级完成度；旧测试端口保留既有单一履约兼容路径。
func completeOrdinary(tx ordercontract.Transaction, order *orderdomain.Order, fulfillment *fulfillmentdomain.Fulfillment) (bool, error) {
	if store, ok := tx.Fulfillments().(fc.OrdinaryItemStore); ok {
		return store.CompleteOrdinary(fulfillment)
	}
	status := constants.OrderStatusDelivered
	if fulfillment.Type == constants.FulfillmentTypeAuto {
		status = constants.OrderStatusCompleted
	}
	return true, tx.Orders().UpdateFields(order.ID, map[string]interface{}{"status": status, "updated_at": time.Now()})
}
