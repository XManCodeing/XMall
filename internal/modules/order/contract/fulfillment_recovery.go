package contract

import (
	"context"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"time"
)

// FulfillmentRecoveryStore scans leaf orders only; callers re-enter the existing
// idempotent fulfillment/procurement use case. It never allocates inventory.
type FulfillmentRecoveryStore interface {
	ListPaidWithoutFulfillment(ctx context.Context, afterID uint, before time.Time, limit int) ([]uint, error)
	GetByIDContext(ctx context.Context, id uint) (*orderdomain.Order, error)
}
