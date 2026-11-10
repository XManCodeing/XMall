package application

import (
	"context"
	"errors"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// attentionRepositoryStub embeds unused worker ports and injects read failures for summary tests.
type attentionRepositoryStub struct {
	c.AccessRepository
	failure error
}

// CountAccessAttention returns the injected error without modifying any durable state.
func (stub attentionRepositoryStub) CountAccessAttention(context.Context) (int64, error) {
	return 0, stub.failure
}

// CountReviewAttention returns the injected error without inspecting secret columns.
func (stub attentionRepositoryStub) CountReviewAttention(context.Context) (int64, error) {
	return 0, stub.failure
}

// AccessDeliveryCandidates supplies one valid candidate to exercise delivery error propagation.
func (attentionRepositoryStub) AccessDeliveryCandidates(context.Context, uint, int) ([]c.AccessTask, error) {
	row := c.AccessTask{InviteID: 1}
	row.ID, row.BotID = 1, 9
	return []c.AccessTask{row}, nil
}

// attentionDeliveryStub injects a read failure instead of a potentially misleading zero count.
type attentionDeliveryStub struct{ failure error }

// InviteDelivery returns the configured safe error and no receipt.
func (stub attentionDeliveryStub) InviteDelivery(context.Context, uint, uint) (rt.InviteDeliveryStatus, error) {
	return rt.InviteDeliveryStatus{}, stub.failure
}

// TestAttentionReadFailures checks missing optional ports and both repository/runtime failures.
func TestAttentionReadFailures(t *testing.T) {
	ctx := context.Background()
	access := &AccessService{}
	if _, err := access.AccessAttention(ctx); !errors.Is(err, c.ErrCapability) {
		t.Fatalf("missing access port: %v", err)
	}
	if _, err := access.ReviewAttention(ctx); !errors.Is(err, c.ErrCapability) {
		t.Fatalf("missing review port: %v", err)
	}
	if _, err := (&Lifecycle{}).LifecycleAttention(ctx); !errors.Is(err, c.ErrCapability) {
		t.Fatalf("missing lifecycle port: %v", err)
	}
	failure := errors.New("synthetic read failure")
	access.Store = attentionRepositoryStub{failure: failure}
	if _, err := access.AccessAttention(ctx); !errors.Is(err, failure) {
		t.Fatalf("access read error lost: %v", err)
	}
	if _, err := access.ReviewAttention(ctx); !errors.Is(err, failure) {
		t.Fatalf("review read error lost: %v", err)
	}
	access.Store = attentionRepositoryStub{}
	access.Deliveries = attentionDeliveryStub{failure: failure}
	if _, err := access.AccessAttention(ctx); !errors.Is(err, failure) {
		t.Fatalf("delivery read error lost: %v", err)
	}
}
