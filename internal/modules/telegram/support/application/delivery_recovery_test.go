package application

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

type deliveryRecoveryFixture struct {
	calls int
}

// RecoverDelivery records trusted application arguments and returns metadata;
// no storage or transport runs in this boundary-only test double.
func (fixture *deliveryRecoveryFixture) RecoverDelivery(_ context.Context, bot, id uint, generation uint64, actor uint, direction, action string, in c.AdminDeliveryRecoveryInput, _ time.Time) (c.AdminDeliveryRecovery, error) {
	fixture.calls++
	if bot != 2 || id != 3 || generation != 7 || actor != 9 || direction != "downstream" || action != "retry" {
		return c.AdminDeliveryRecovery{}, c.ErrDenied
	}
	return c.AdminDeliveryRecovery{ID: id, Version: in.ExpectedVersion + 1, RetryQueued: true}, nil
}

// TestDeliveryRecoveryRechecksExactAuthority verifies permissions lost while
// generation loads cannot reach storage. Exact POST path is checked twice; no
// client-controlled generation, bot, destination or receipt is accepted.
func TestDeliveryRecoveryRechecksExactAuthority(t *testing.T) {
	fixture := &deliveryRecoveryFixture{}
	service := NewAdmin(&adminTestStore{}, nil, func(context.Context, uint, string, bool) error { return nil }, nil)
	service.WithDownstreams(&adminResolutionFixture{}, nil, func(context.Context, uint) (uint64, error) { return 7, nil })
	checks := 0
	service.WithDeliveryRecovery(fixture, func(_ context.Context, actor uint, path string) error {
		checks++
		if actor != 9 || path != c.AdminDownstreamPath+"/retry" || checks == 2 {
			return c.ErrDenied
		}
		return nil
	})
	input := c.AdminDeliveryRecoveryInput{ExpectedVersion: 4, Evidence: "ticket:retry"}
	if _, err := service.RecoverDelivery(context.Background(), 9, 3, "downstream", "retry", input); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	if fixture.calls != 0 || checks != 2 {
		t.Fatal(fixture.calls, checks)
	}
	service.WithDeliveryRecovery(fixture, func(context.Context, uint, string) error { return nil })
	out, err := service.RecoverDelivery(context.Background(), 9, 3, "downstream", "retry", input)
	if err != nil || !out.RetryQueued || fixture.calls != 1 {
		t.Fatal(out, err, fixture.calls)
	}
}
