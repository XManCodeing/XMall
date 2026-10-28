package notificationadapter

import (
	"github.com/homles698698-lab/XMall/internal/constants"
	notificationcontract "github.com/homles698698-lab/XMall/internal/modules/notification/contract"
	procurementcontract "github.com/homles698698-lab/XMall/internal/modules/procurement/contract"
	procurementdomain "github.com/homles698698-lab/XMall/internal/modules/procurement/domain"
	"github.com/homles698698-lab/XMall/internal/shared/jsonmap"
)

type Notifier struct {
	enqueuer notificationcontract.NotificationEnqueuer
}

var _ procurementcontract.FailureNotifier = (*Notifier)(nil)

func New(enqueuer notificationcontract.NotificationEnqueuer) procurementcontract.FailureNotifier {
	if enqueuer == nil {
		return nil
	}
	return &Notifier{enqueuer: enqueuer}
}

func (n *Notifier) NotifyFailure(order *procurementdomain.Order, message string) error {
	return n.enqueuer.Enqueue(notificationcontract.EnqueueInput{
		EventType: constants.NotificationEventExceptionAlert,
		BizType:   constants.NotificationBizTypeProcurement,
		BizID:     order.ID,
		Data: jsonmap.JSON{
			"procurement_order_id": order.ID,
			"local_order_no":       order.LocalOrderNo,
			"error":                message,
		},
	})
}
