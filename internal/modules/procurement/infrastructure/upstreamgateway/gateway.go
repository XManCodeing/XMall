package upstreamgateway

import (
	"context"
	"errors"
	"fmt"

	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	siteconnectiondomain "github.com/XManCodeing/XMall/internal/modules/siteconnection/domain"
	"github.com/XManCodeing/XMall/internal/upstream"
)

type Provider interface {
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	GetAdapter(connection *siteconnectiondomain.Connection) (upstream.Adapter, error)
}

type Gateway struct {
	provider Provider
}

var _ procurementcontract.ConnectionProvider = (*Gateway)(nil)

func New(provider Provider) *Gateway {
	if provider == nil {
		panic("procurement upstream gateway: provider is nil")
	}
	return &Gateway{provider: provider}
}

func (g *Gateway) Open(connectionID uint) (procurementcontract.UpstreamConnection, error) {
	connection, err := g.provider.GetByID(connectionID)
	if err != nil || connection == nil {
		return nil, err
	}
	adapter, err := g.provider.GetAdapter(connection)
	if err != nil {
		return nil, err
	}
	return &session{connection: connection, adapter: adapter}, nil
}

// SnapshotProvider stores opaque encrypted connection data, never plaintext credentials.
type SnapshotProvider interface {
	CaptureProcurementSnapshot(uint) (string, error)
	OpenProcurementSnapshot(uint, string) (*siteconnectiondomain.Connection, upstream.Adapter, error)
}

func (g *Gateway) Capture(id uint) (string, error) {
	if provider, ok := g.provider.(SnapshotProvider); ok {
		return provider.CaptureProcurementSnapshot(id)
	}
	connection, err := g.provider.GetByID(id)
	if err != nil {
		return "", err
	}
	if connection != nil && connection.Protocol == "generic-http" {
		return "", errors.New("generic connection snapshot provider required")
	}
	return "", nil
}

func (g *Gateway) OpenSnapshot(id uint, snapshot string) (procurementcontract.UpstreamConnection, error) {
	provider, ok := g.provider.(SnapshotProvider)
	if !ok {
		return nil, errors.New("connection snapshot provider unavailable")
	}
	connection, adapter, err := provider.OpenProcurementSnapshot(id, snapshot)
	if err != nil {
		return nil, err
	}
	if connection == nil || adapter == nil {
		return nil, errors.New("invalid frozen connection")
	}
	return &session{connection: connection, adapter: adapter}, nil
}

type session struct {
	connection *siteconnectiondomain.Connection
	adapter    upstream.Adapter
}

var _ procurementcontract.UpstreamConnection = (*session)(nil)

func (s *session) CallbackURL() string    { return s.connection.CallbackURL }
func (s *session) RetryMax() int          { return s.connection.RetryMax }
func (s *session) RetryIntervals() string { return s.connection.RetryIntervals }

func (s *session) CreateOrder(ctx context.Context, request procurementcontract.CreateOrderRequest) (*procurementcontract.CreateOrderResult, error) {
	result, err := s.adapter.CreateOrder(ctx, upstream.CreateUpstreamOrderReq{
		ProductExternalID: request.ProductExternalID, SKUExternalID: request.SKUExternalID,
		SKUID: request.SKUID, Quantity: request.Quantity, ManualFormData: request.ManualFormData,
		DownstreamOrderNo: request.DownstreamOrderNo, TraceID: request.TraceID, CallbackURL: request.CallbackURL,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("empty upstream create response")
	}
	return &procurementcontract.CreateOrderResult{
		ExternalID: result.ExternalID, Fulfillment: fromUpstreamFulfillment(result.Fulfillment),
		OK: result.OK, OrderID: result.OrderID, OrderNo: result.OrderNo,
		Status: result.Status, Amount: result.Amount, Currency: result.Currency,
		ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage,
	}, nil
}

func (s *session) GetOrder(ctx context.Context, orderID uint) (*procurementcontract.UpstreamOrder, error) {
	detail, err := s.adapter.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return fromUpstreamOrder(detail), nil
}

func fromUpstreamOrder(detail *upstream.UpstreamOrderDetail) *procurementcontract.UpstreamOrder {
	if detail == nil {
		return nil
	}
	return &procurementcontract.UpstreamOrder{
		ExternalID: detail.ExternalID,
		OrderID:    detail.OrderID, OrderNo: detail.OrderNo, Status: detail.Status,
		Amount: detail.Amount, RefundedAmount: detail.RefundedAmount,
		Currency: detail.Currency, RefundRecords: detail.RefundRecords,
		Fulfillment: fromUpstreamFulfillment(detail.Fulfillment),
	}
}

func (s *session) GetOrderExternal(ctx context.Context, id string) (*procurementcontract.UpstreamOrder, error) {
	adapter, ok := s.adapter.(upstream.ExternalOrderAdapter)
	if !ok {
		return nil, errors.New("external order query unsupported")
	}
	detail, err := adapter.GetOrderExternal(ctx, id)
	return fromUpstreamOrder(detail), err
}

func (s *session) CancelOrderExternal(ctx context.Context, id string) error {
	adapter, ok := s.adapter.(upstream.ExternalOrderAdapter)
	if !ok {
		return errors.New("external order cancellation unsupported")
	}
	returnErr := adapter.CancelOrderExternal(ctx, id)
	if errors.Is(returnErr, upstream.ErrUpstreamCancelNotAllowed) {
		return fmt.Errorf("%w: %v", procurementcontract.ErrUpstreamCancelRefused, returnErr)
	}
	return returnErr
}

func (s *session) LookupOrder(ctx context.Context, no string) (*procurementcontract.UpstreamOrder, error) {
	adapter, ok := s.adapter.(upstream.OrderLookupAdapter)
	if !ok {
		return nil, errors.New("order lookup unsupported; manual verification required")
	}
	detail, err := adapter.LookupOrder(ctx, no)
	return fromUpstreamOrder(detail), err
}

func (s *session) CancelOrder(ctx context.Context, orderID uint) error {
	err := s.adapter.CancelOrder(ctx, orderID)
	if errors.Is(err, upstream.ErrUpstreamCancelNotAllowed) {
		return fmt.Errorf("%w: %v", procurementcontract.ErrUpstreamCancelRefused, err)
	}
	return err
}

func fromUpstreamFulfillment(value *upstream.UpstreamFulfillment) *procurementcontract.Fulfillment {
	if value == nil {
		return nil
	}
	return &procurementcontract.Fulfillment{
		Type: value.Type, Status: value.Status, Payload: value.Payload,
		DeliveryData: value.DeliveryData, DeliveredAt: value.DeliveredAt,
	}
}
