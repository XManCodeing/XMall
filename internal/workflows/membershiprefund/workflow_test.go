package membershiprefund

import (
	"context"
	"errors"
	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"testing"
	"time"
)

type fundsFixture struct {
	request       order.MembershipRefundRequest
	commits       int
	completeErr   error
	completeCalls int
}

func (f *fundsFixture) CommitMembershipRefund(context.Context, order.MembershipRefundInput) (*order.MembershipRefundRequest, error) {
	f.commits++
	return &f.request, nil
}
func (f *fundsFixture) CompleteMembershipRefund(string) error {
	f.completeCalls++
	if f.completeErr != nil {
		return f.completeErr
	}
	f.request.RightsState = "queued"
	return nil
}
func (f *fundsFixture) ListMembershipRefunds(uint, uint) ([]order.MembershipRefundRequest, error) {
	return []order.MembershipRefundRequest{f.request}, nil
}

// ClaimMembershipRefund models durable claim metadata for workflow unit tests.
func (f *fundsFixture) ClaimMembershipRefund(_ context.Context, snapshot order.MembershipRefundRequest, now time.Time, manual bool) (*order.MembershipRefundRequest, error) {
	if snapshot.RequestKey == "" {
		return nil, order.ErrRefundRequestInvalid
	}
	if snapshot.RetryLeaseUntil != nil && snapshot.RetryLeaseUntil.After(now) || !manual && (snapshot.RetryAttempts >= order.MembershipRefundRetryLimit || snapshot.RightsState != "pending" || snapshot.RetryAfter != nil && snapshot.RetryAfter.After(now)) {
		return nil, nil
	}
	snapshot.RetryVersion++
	snapshot.RetryAttempts++
	if manual {
		snapshot.RetryAttempts = 1
	}
	lease := now.Add(order.MembershipRefundRetryLease)
	next := lease.Add(order.MembershipRefundRetryDelay(snapshot.RetryAttempts))
	snapshot.RetryLeaseUntil, snapshot.RetryAfter = &lease, &next
	f.request = snapshot
	return &snapshot, nil
}

// FinishMembershipRefund models a failed bookkeeping write retaining the claimed lease.
func (f *fundsFixture) FinishMembershipRefund(_ context.Context, claim order.MembershipRefundRequest, now time.Time, success bool) error {
	f.completeCalls++
	if f.completeErr != nil {
		return f.completeErr
	}
	claim.RetryLeaseUntil = nil
	if success {
		claim.RightsState, claim.RetryAfter = "queued", nil
	} else {
		next := now.Add(order.MembershipRefundRetryDelay(claim.RetryAttempts))
		claim.RetryAfter = &next
	}
	f.request = claim
	return nil
}

type rightsFixture struct {
	fail     bool
	disposed bool
	calls    int
}

func (r *rightsFixture) RefundCandidates(context.Context, uint) ([]membership.RefundCandidate, error) {
	return []membership.RefundCandidate{{RefundSelection: membership.RefundSelection{RefundID: 5, OrderItemID: 8}, Disposed: r.disposed}}, nil
}
func (r *rightsFixture) DisposeRefund(context.Context, membership.RefundSelection) error {
	r.calls++
	if r.fail {
		return errors.New("local cleanup unavailable")
	}
	r.disposed = true
	return nil
}

// TestWorkflowResumesCommittedMoney proves failed local disposal reports funds completed and is resumable.
func TestWorkflowResumesCommittedMoney(t *testing.T) {
	funds := &fundsFixture{request: order.MembershipRefundRequest{RequestKey: "durable-key", ActorAdminID: 7, OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}}
	rights := &rightsFixture{fail: true}
	workflow := Workflow{Funds: funds, Rights: rights, Authorize: func(context.Context, uint, string) error { return nil }}
	first, err := workflow.Execute(context.Background(), order.MembershipRefundInput{})
	if err != nil || first.FundsState != "completed" || first.RightsState != "pending" {
		t.Fatal(first, err)
	}
	rights.fail = false
	second, err := workflow.Execute(context.Background(), order.MembershipRefundInput{})
	if err != nil || second.RightsState != "queued" || second.RefundID != 5 {
		t.Fatal(second, err)
	}
	if rights.calls != 2 {
		t.Fatal(rights.calls)
	}
}

// TestWorkflowResumesAfterProgressWriteFailure covers the crash window after successful local disposal.
func TestWorkflowResumesAfterProgressWriteFailure(t *testing.T) {
	funds := &fundsFixture{request: order.MembershipRefundRequest{RequestKey: "durable-key", ActorAdminID: 7, OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}, completeErr: errors.New("synthetic progress failure")}
	rights := &rightsFixture{}
	workflow := Workflow{Funds: funds, Rights: rights, Authorize: func(context.Context, uint, string) error { return nil }}
	first, err := workflow.Execute(context.Background(), order.MembershipRefundInput{})
	if err != nil || first.RightsState != "pending" || !rights.disposed {
		t.Fatal(first, err)
	}
	funds.completeErr = nil
	workflow.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	second, err := workflow.Execute(context.Background(), order.MembershipRefundInput{})
	if err != nil || second.RightsState != "queued" || rights.calls != 1 || funds.completeCalls != 2 {
		t.Fatal(second, err, rights.calls, funds.completeCalls)
	}
	if _, err := workflow.Execute(context.Background(), order.MembershipRefundInput{}); err != nil || rights.calls != 1 || funds.completeCalls != 2 {
		t.Fatal(err, rights.calls, funds.completeCalls)
	}
}

// TestWorkflowPreviewDeniedBeforeRead prevents the read preview from bypassing either independent permission.
func TestWorkflowPreviewDeniedBeforeRead(t *testing.T) {
	workflow := Workflow{Funds: &fundsFixture{}, Rights: &rightsFixture{}, Authorize: func(context.Context, uint, string) error { return order.ErrRefundRequestDenied }}
	if _, err := workflow.Preview(context.Background(), 7, 1, "wallet", []uint{8}); !errors.Is(err, order.ErrRefundRequestDenied) {
		t.Fatal(err)
	}
}

// TestWorkflowDeniedBeforeMoney confirms replays cannot bypass independent authorization.
func TestWorkflowDeniedBeforeMoney(t *testing.T) {
	funds := &fundsFixture{}
	workflow := Workflow{Funds: funds, Rights: &rightsFixture{}, Authorize: func(context.Context, uint, string) error { return order.ErrRefundRequestDenied }}
	if _, err := workflow.Execute(context.Background(), order.MembershipRefundInput{}); !errors.Is(err, order.ErrRefundRequestDenied) || funds.commits != 0 {
		t.Fatal(err, funds.commits)
	}
}
