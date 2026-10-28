// Package execution resolves frozen connections and recovers ambiguous submissions.
package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	"github.com/XManCodeing/XMall/internal/modules/procurement/domain"
)

func Open(provider contract.ConnectionProvider, order *domain.Order) (contract.UpstreamConnection, error) {
	if order.ConnectionSnapshot != "" {
		snapshots, ok := provider.(contract.SnapshotConnectionProvider)
		if !ok {
			return nil, errors.New("frozen connection unavailable")
		}
		return snapshots.OpenSnapshot(order.ConnectionID, order.ConnectionSnapshot)
	}
	return provider.Open(order.ConnectionID)
}

func Get(ctx context.Context, connection contract.UpstreamConnection, order *domain.Order) (*contract.UpstreamOrder, error) {
	if order.UpstreamOrderExternalID != "" {
		if external, ok := connection.(contract.ExternalOrderConnection); ok {
			return external.GetOrderExternal(ctx, order.UpstreamOrderExternalID)
		}
		return nil, errors.New("external order query unsupported")
	}
	if order.UpstreamOrderID == 0 {
		return nil, errors.New("upstream order identity missing")
	}
	return connection.GetOrder(ctx, order.UpstreamOrderID)
}

func Cancel(ctx context.Context, connection contract.UpstreamConnection, order *domain.Order) error {
	if order.UpstreamOrderExternalID != "" {
		if external, ok := connection.(contract.ExternalOrderConnection); ok {
			return external.CancelOrderExternal(ctx, order.UpstreamOrderExternalID)
		}
		return errors.New("external order cancellation unsupported")
	}
	if order.UpstreamOrderID == 0 {
		return errors.New("upstream order identity missing")
	}
	return connection.CancelOrder(ctx, order.UpstreamOrderID)
}

// Recover never submits again. A negative/unsupported lookup is not proof that
// the original request did not debit the upstream wallet.
func Recover(ctx context.Context, repo contract.Repository, connection contract.UpstreamConnection, order *domain.Order) (*contract.UpstreamOrder, error) {
	var detail *contract.UpstreamOrder
	var err error
	if order.UpstreamOrderExternalID != "" || order.UpstreamOrderID != 0 {
		detail, err = Get(ctx, connection, order)
	} else if lookup, ok := connection.(contract.OrderLookupConnection); ok {
		detail, err = lookup.LookupOrder(ctx, order.LocalOrderNo)
	} else {
		err = errors.New("lookup unsupported; manual verification required")
	}
	if err != nil || detail == nil || (detail.ExternalID == "" && detail.OrderID == 0) {
		message := "submission outcome unknown; manual verification required"
		if err != nil {
			message += ": " + err.Error()
		}
		_, updateErr := repo.UpdateStatus(order.ID, order.Status, "unknown", map[string]interface{}{"error_message": message, "updated_at": time.Now()})
		return nil, updateErr
	}
	if order.CancellationPending {
		return detail, nil
	}
	externalID := detail.ExternalID
	changed, err := repo.UpdateStatus(order.ID, order.Status, "accepted", map[string]interface{}{
		"upstream_order_id": detail.OrderID, "upstream_order_external_id": externalID,
		"upstream_order_no": detail.OrderNo, "upstream_amount": detail.Amount,
		"upstream_currency": detail.Currency, "error_message": "", "retry_count": 0, "updated_at": time.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("persist recovered order: %w", err)
	}
	if !changed {
		return nil, nil
	}
	return detail, nil
}
