package upstreamreader

import (
	"context"
	"fmt"

	reconciliationcontract "github.com/homles698698-lab/XMall/internal/modules/reconciliation/contract"
	siteconnectiondomain "github.com/homles698698-lab/XMall/internal/modules/siteconnection/domain"
	"github.com/homles698698-lab/XMall/internal/upstream"
)

type ConnectionProvider interface {
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	GetAdapter(connection *siteconnectiondomain.Connection) (upstream.Adapter, error)
}

type Reader struct {
	connections ConnectionProvider
}

var _ reconciliationcontract.UpstreamOrderProvider = (*Reader)(nil)

type session struct {
	adapter upstream.Adapter
}

var _ reconciliationcontract.UpstreamOrderReader = (*session)(nil)

func New(connections ConnectionProvider) *Reader {
	if connections == nil {
		panic("reconciliation upstream reader: connections is nil")
	}
	return &Reader{connections: connections}
}

func (r *Reader) Open(connectionID uint) (reconciliationcontract.UpstreamOrderReader, error) {
	connection, err := r.connections.GetByID(connectionID)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	if connection == nil {
		return nil, fmt.Errorf("connection unavailable")
	}
	adapter, err := r.connections.GetAdapter(connection)
	if err != nil {
		return nil, fmt.Errorf("get adapter: %w", err)
	}
	if adapter == nil {
		return nil, fmt.Errorf("adapter unavailable")
	}
	return &session{adapter: adapter}, nil
}

func (r *Reader) OpenSnapshot(id uint, snapshot string) (reconciliationcontract.UpstreamOrderReader, error) {
	provider, ok := r.connections.(interface {
		OpenProcurementSnapshot(uint, string) (*siteconnectiondomain.Connection, upstream.Adapter, error)
	})
	if !ok {
		return nil, fmt.Errorf("frozen connection unavailable")
	}
	_, adapter, err := provider.OpenProcurementSnapshot(id, snapshot)
	if err != nil {
		return nil, err
	}
	if adapter == nil {
		return nil, fmt.Errorf("frozen adapter unavailable")
	}
	return &session{adapter: adapter}, nil
}

func (s *session) GetExternal(ctx context.Context, id string) (*reconciliationcontract.UpstreamOrder, error) {
	adapter, ok := s.adapter.(upstream.ExternalOrderAdapter)
	if !ok {
		return nil, fmt.Errorf("external order query unsupported")
	}
	detail, err := adapter.GetOrderExternal(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("empty upstream order")
	}
	return &reconciliationcontract.UpstreamOrder{Status: detail.Status, Amount: detail.Amount, Currency: detail.Currency}, nil
}

func (s *session) Get(ctx context.Context, upstreamOrderID uint) (*reconciliationcontract.UpstreamOrder, error) {
	detail, err := s.adapter.GetOrder(ctx, upstreamOrderID)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("empty upstream order")
	}
	return &reconciliationcontract.UpstreamOrder{Status: detail.Status, Amount: detail.Amount, Currency: detail.Currency}, nil
}
