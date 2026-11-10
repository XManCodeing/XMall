package membershiprefund

import (
	"context"
	"errors"
	"log/slog"
	"time"

	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
)

// now returns the injectable UTC scheduling clock, defaulting to production wall time.
func (workflow *Workflow) now() time.Time {
	if workflow.Now != nil {
		return workflow.Now().UTC()
	}
	return time.Now().UTC()
}

// attempt claims persistent ownership before disposal and records even failed progress.
// manual bypasses the due time and exhausted budget, after Execute has checked authorization and identity.
func (workflow *Workflow) attempt(ctx context.Context, request *order.MembershipRefundRequest, manual bool) error {
	retry, ok := workflow.Funds.(order.MembershipRefundRetry)
	if !ok {
		return order.ErrRefundRequestInvalid
	}
	claim, err := retry.ClaimMembershipRefund(ctx, *request, workflow.now(), manual)
	if err != nil || claim == nil {
		return err
	}
	*request = *claim
	if claim.RefundID == 0 || claim.ActorAdminID == 0 || claim.RequestKey == "" || len(claim.ItemIDs) == 0 || claim.FundsState != "completed" {
		err = order.ErrRefundRequestInvalid
	} else {
		// Local disposal must finish within its ownership window; fencing still rejects late acknowledgements.
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, order.MembershipRefundRetryLease-5*time.Second)
		err = workflow.dispose(attemptCtx, claim)
		cancelAttempt()
	}
	// Cancellation must not erase the persisted attempt. A short detached write completes its bookkeeping.
	progressCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	progressErr := retry.FinishMembershipRefund(progressCtx, *claim, workflow.now(), err == nil)
	if progressErr == nil {
		request.RetryLeaseUntil = nil
		if err == nil {
			request.RightsState, request.RetryAfter = "queued", nil
		} else if claim.RetryAttempts >= order.MembershipRefundRetryLimit {
			request.RightsState, request.RetryAfter = "pending", nil
		} else {
			next := workflow.now().Add(order.MembershipRefundRetryDelay(claim.RetryAttempts))
			request.RetryAfter = &next
		}
	}
	return errors.Join(err, progressErr)
}

// PendingReader reads a bounded page of committed termination intent, never creates a refund.
// after is an ID cursor; returned requests must have durable positive refund IDs.
type PendingReader func(context.Context, uint, int) ([]order.MembershipRefundRequest, error)

// Recover retries local rights disposal from already-authorized durable intent.
// It never calls CommitMembershipRefund or checks mutable administrator permissions:
// removing a role cannot undo an already-committed refund/termination decision.
// next advances past failed records so one broken request cannot starve later ones.
func (workflow *Workflow) Recover(ctx context.Context, read PendingReader, after uint, limit int) (next uint, scanned int, err error) {
	if read == nil || workflow.Funds == nil || workflow.Rights == nil || limit < 1 || limit > 100 {
		return after, 0, order.ErrRefundRequestInvalid
	}
	requests, err := read(ctx, after, limit)
	if err != nil {
		return after, 0, err
	}
	next = after
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return next, scanned, err
		}
		next, scanned = request.ID, scanned+1
		if failure := workflow.attempt(ctx, &request, false); failure != nil {
			slog.WarnContext(ctx, "membership_refund_rights_recovery_pending", "request_id", request.ID, "refund_id", request.RefundID, "rights_state", request.RightsState, "retry_attempts", request.RetryAttempts)
			err = errors.Join(err, failure)
		} else if request.RightsState == "queued" {
			slog.InfoContext(ctx, "membership_refund_rights_recovered", "request_id", request.ID, "refund_id", request.RefundID, "rights_state", request.RightsState, "retry_attempts", request.RetryAttempts)
		}
	}
	return next, scanned, err
}
