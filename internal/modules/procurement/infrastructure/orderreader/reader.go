package orderreader

import (
	"context"
	"errors"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
)

type contextReader struct {
	procurementcontract.OrderRepository
	ctx    context.Context
	source interface {
		GetByIDContext(context.Context, uint) (*orderdomain.Order, error)
	}
}

func (r *Reader) WithContext(ctx context.Context) (procurementcontract.OrderRepository, error) {
	source, ok := r.source.(interface {
		GetByIDContext(context.Context, uint) (*orderdomain.Order, error)
	})
	if !ok {
		return nil, errors.New("order source does not support cancellation")
	}
	return &contextReader{ctx: ctx, source: source}, nil
}
func (r *contextReader) GetByID(id uint) (*procurementdomain.LocalOrder, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	order, err := r.source.GetByIDContext(r.ctx, id)
	if err != nil || order == nil {
		return nil, err
	}
	mapped := MapOrder(*order)
	return &mapped, nil
}

type Source interface {
	GetByID(id uint) (*orderdomain.Order, error)
	GetByIDs(ids []uint) ([]orderdomain.Order, error)
	UpdateStatus(id uint, status string, updates map[string]interface{}) error
}

type Reader struct {
	source Source
}

var _ procurementcontract.OrderRepository = (*Reader)(nil)

func New(source Source) *Reader {
	if source == nil {
		panic("procurement order reader: source is nil")
	}
	return &Reader{source: source}
}

func (r *Reader) GetByID(id uint) (*procurementdomain.LocalOrder, error) {
	order, err := r.source.GetByID(id)
	if err != nil || order == nil {
		return nil, err
	}
	mapped := MapOrder(*order)
	return &mapped, nil
}

func (r *Reader) GetByIDs(ids []uint) ([]procurementdomain.LocalOrder, error) {
	orders, err := r.source.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	result := make([]procurementdomain.LocalOrder, 0, len(orders))
	for _, order := range orders {
		result = append(result, MapOrder(order))
	}
	return result, nil
}

func (r *Reader) UpdateStatus(id uint, status string, updates map[string]interface{}) error {
	return r.source.UpdateStatus(id, status, updates)
}

// MapOrder 将订单域的持久化实体收窄为采购上下文快照。
func MapOrder(order orderdomain.Order) procurementdomain.LocalOrder {
	result := procurementdomain.LocalOrder{
		ID: order.ID, OrderNo: order.OrderNo, ParentID: order.ParentID,
		UserID: order.UserID, GuestEmail: order.GuestEmail, Status: order.Status,
		Currency: order.Currency, TotalAmount: order.TotalAmount, RefundedAmount: order.RefundedAmount,
		Items:    make([]procurementdomain.LocalOrderItem, 0, len(order.Items)),
		Children: make([]procurementdomain.LocalOrder, 0, len(order.Children)),
	}
	for _, item := range order.Items {
		result.Items = append(result.Items, procurementdomain.LocalOrderItem{
			ProductID: item.ProductID, SKUID: item.SKUID,
			Title: item.TitleJSON, SKUSnapshot: item.SKUSnapshotJSON,
			CostPrice: item.CostPrice, Quantity: item.Quantity, TotalPrice: item.TotalPrice,
			FulfillmentType: item.FulfillmentType, ManualFormSubmissionJSON: item.ManualFormSubmissionJSON,
		})
	}
	for _, child := range order.Children {
		result.Children = append(result.Children, MapOrder(child))
	}
	return result
}
