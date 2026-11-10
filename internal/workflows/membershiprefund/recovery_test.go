package membershiprefund

import (
	"context"
	"errors"
	"testing"
	"time"

	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
)

// TestRecoveryNeverRepeatsMoney covers disposal failure and lost progress acknowledgement.
func TestRecoveryNeverRepeatsMoney(t *testing.T) {
	funds := &fundsFixture{request: order.MembershipRefundRequest{ID: 3, ActorAdminID: 7, RequestKey: "refund-3", OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}}
	rights := &rightsFixture{fail: true}
	workflow := Workflow{Funds: funds, Rights: rights}
	read := func(context.Context, uint, int) ([]order.MembershipRefundRequest, error) {
		return []order.MembershipRefundRequest{funds.request}, nil
	}
	next, count, err := workflow.Recover(context.Background(), read, 0, 10)
	if err == nil || next != 3 || count != 1 || funds.commits != 0 {
		t.Fatalf("first=%d/%d %v", next, count, err)
	}
	rights.fail = false
	workflow.Now = func() time.Time { return time.Now().Add(time.Hour) }
	funds.completeErr = errors.New("lost progress")
	if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err == nil {
		t.Fatal("progress failure lost")
	}
	funds.completeErr = nil
	workflow.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, _, err := workflow.Recover(context.Background(), read, 0, 10); err != nil {
		t.Fatal(err)
	}
	if funds.commits != 0 || rights.calls != 2 || funds.request.RightsState != "queued" {
		t.Fatalf("money=%d rights=%d state=%s", funds.commits, rights.calls, funds.request.RightsState)
	}
}

// TestRecoveryAdvancesPastInvalidRequest prevents a malformed old row starving a later one.
func TestRecoveryAdvancesPastInvalidRequest(t *testing.T) {
	funds := &fundsFixture{}
	rights := &rightsFixture{}
	workflow := Workflow{Funds: funds, Rights: rights}
	read := func(context.Context, uint, int) ([]order.MembershipRefundRequest, error) {
		return []order.MembershipRefundRequest{{ID: 1}, {ID: 2, ActorAdminID: 7, RequestKey: "second", OrderID: 2, RefundID: 5, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}}, nil
	}
	next, count, err := workflow.Recover(context.Background(), read, 0, 10)
	if !errors.Is(err, order.ErrRefundRequestInvalid) || next != 2 || count != 2 || rights.calls != 1 || funds.commits != 0 {
		t.Fatalf("next=%d count=%d rights=%d err=%v", next, count, rights.calls, err)
	}
}
