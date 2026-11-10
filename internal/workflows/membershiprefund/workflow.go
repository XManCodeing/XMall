package membershiprefund

import (
	"context"
	"log/slog"
	"time"

	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// Workflow coordinates durable money/intent with local entitlement disposal only through contracts.
type Workflow struct {
	Funds     order.MembershipRefundFunds
	Rights    membership.RefundAdmin
	Authorize func(context.Context, uint, string) error
	Now       func() time.Time
}

// Execute rechecks both independent permissions before reading or mutating an idempotent request.
// Pending local rights work is returned as resumable success: funds were committed and must not be repeated.
func (workflow *Workflow) Execute(ctx context.Context, input order.MembershipRefundInput) (*order.MembershipRefundRequest, error) {
	if workflow.Authorize == nil || workflow.Funds == nil || workflow.Rights == nil {
		return nil, order.ErrRefundRequestDenied
	}
	if err := workflow.Authorize(ctx, input.ActorAdminID, input.Method); err != nil {
		return nil, err
	}
	request, err := workflow.Funds.CommitMembershipRefund(ctx, input)
	if err != nil {
		return nil, err
	}
	if request.RightsState == "queued" {
		return request, nil
	}
	if err := workflow.attempt(ctx, request, true); err != nil {
		// Log identifiers only. The durable request is retained even if disposal or progress recording fails.
		slog.WarnContext(ctx, "membership_refund_rights_pending", "order_id", request.OrderID, "refund_id", request.RefundID)
		return request, nil
	}
	return request, nil
}

// Preview verifies both permissions before accessing paid purchase facts; no funds are committed.
// actor and method come from the authenticated HTTP request, items are explicitly selected purchase IDs.
func (workflow *Workflow) Preview(ctx context.Context, actor, orderID uint, method string, items []uint) (*membership.RefundPreview, error) {
	if workflow.Authorize == nil {
		return nil, order.ErrRefundRequestDenied
	}
	if err := workflow.Authorize(ctx, actor, method); err != nil {
		return nil, err
	}
	reader, ok := workflow.Rights.(membership.RefundPreviewReader)
	if !ok {
		return nil, membership.ErrConflict
	}
	return reader.PreviewRefund(ctx, orderID, items)
}

// Requests returns only actor-owned persisted attempts after both independent permission checks.
func (workflow *Workflow) Requests(ctx context.Context, actor, orderID uint) ([]order.MembershipRefundRequest, error) {
	if workflow.Authorize == nil || workflow.Funds == nil {
		return nil, order.ErrRefundRequestDenied
	}
	if err := workflow.Authorize(ctx, actor, "existing"); err != nil {
		if walletErr := workflow.Authorize(ctx, actor, "wallet"); walletErr != nil {
			return nil, order.ErrRefundRequestDenied
		}
	}
	return workflow.Funds.ListMembershipRefunds(orderID, actor)
}

// dispose resolves fresh verified evidence for each explicitly selected item, independent of money proportion.
// Already-disposed items are skipped, allowing recovery after a crash between disposition and progress writes.
func (workflow *Workflow) dispose(ctx context.Context, request *order.MembershipRefundRequest) error {
	for _, itemID := range request.ItemIDs {
		candidates, err := workflow.Rights.RefundCandidates(ctx, request.OrderID)
		if err != nil {
			return err
		}
		found := false
		for _, candidate := range candidates {
			if candidate.RefundID != request.RefundID || candidate.OrderItemID != itemID {
				continue
			}
			found = true
			if candidate.Disposed {
				break
			}
			selection := candidate.RefundSelection
			selection.ActorAdminID = request.ActorAdminID
			if err := workflow.Rights.DisposeRefund(ctx, selection); err != nil {
				return err
			}
			break
		}
		if !found {
			return membership.ErrConflict
		}
	}
	return nil
}
