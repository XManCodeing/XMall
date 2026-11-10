package refund

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/order/contract"
	"github.com/shopspring/decimal"
)

var membershipRequestKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)

// normalizedMembershipRequest validates external inputs and fingerprints immutable intent including actor.
// Amount is exact to cents: unlike the legacy surface, excess fractional precision is rejected, not rounded.
func normalizedMembershipRequest(input c.MembershipRefundInput) (c.MembershipRefundInput, *c.MembershipRefundRequest, error) {
	input.Remark = strings.TrimSpace(input.Remark)
	if !membershipRequestKey.MatchString(input.RequestKey) || input.OrderID == 0 || input.ActorAdminID == 0 || len(input.ItemIDs) == 0 || len(input.ItemIDs) > 100 || len(input.Remark) > 1000 {
		return input, nil, c.ErrRefundRequestInvalid
	}
	input.ItemIDs = append([]uint{}, input.ItemIDs...)
	sort.Slice(input.ItemIDs, func(left, right int) bool { return input.ItemIDs[left] < input.ItemIDs[right] })
	for index, id := range input.ItemIDs {
		if id == 0 || index > 0 && id == input.ItemIDs[index-1] {
			return input, nil, c.ErrRefundRequestInvalid
		}
	}
	switch input.Method {
	case "wallet", "manual":
		if len(input.Amount) > 24 || !regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`).MatchString(strings.TrimSpace(input.Amount)) {
			return input, nil, c.ErrRefundRequestInvalid
		}
		amount, err := decimal.NewFromString(strings.TrimSpace(input.Amount))
		if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(2)) || input.ExistingRefundID != 0 {
			return input, nil, c.ErrRefundRequestInvalid
		}
		input.Amount = amount.StringFixed(2)
	case "existing":
		if input.ExistingRefundID == 0 || strings.TrimSpace(input.Amount) != "" {
			return input, nil, c.ErrRefundRequestInvalid
		}
	default:
		return input, nil, c.ErrRefundRequestInvalid
	}
	// Actor is deliberately included although the HTTP DTO never exposes it.
	encoded, err := json.Marshal(struct {
		Input c.MembershipRefundInput
		Actor uint
	}{input, input.ActorAdminID})
	if err != nil {
		return input, nil, err
	}
	digest := sha256.Sum256(encoded)
	now := time.Now().UTC()
	return input, &c.MembershipRefundRequest{RequestKey: input.RequestKey, Fingerprint: hex.EncodeToString(digest[:]), ActorAdminID: input.ActorAdminID, Method: input.Method, Amount: input.Amount, Remark: input.Remark, ExistingRefundID: input.ExistingRefundID, OrderID: input.OrderID, ItemIDs: input.ItemIDs, FundsState: "pending", RightsState: "pending", CreatedAt: now, UpdatedAt: now}, nil
}

// CommitMembershipRefund reuses existing manual/wallet refund implementations with atomic command hooks.
// Replaying a request reads the durable result instead of charging or crediting money again.
func (service *Service) CommitMembershipRefund(ctx context.Context, input c.MembershipRefundInput) (*c.MembershipRefundRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, request, err := normalizedMembershipRequest(input)
	if err != nil {
		return nil, err
	}
	commands, ok := service.orderStore.(c.MembershipRefundCommands)
	if !ok {
		return nil, c.ErrRefundRequestInvalid
	}
	previous, err := commands.GetMembershipRefund(input.RequestKey)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if previous.Fingerprint != request.Fingerprint {
			return nil, c.ErrRefundRequestConflict
		}
		return previous, nil
	}
	switch input.Method {
	case "existing":
		err = service.orderStore.WithinTransaction(func(tx c.Transaction) error {
			guard, ok := tx.Orders().(c.RefundGuard)
			if !ok {
				return c.ErrRefundRequestInvalid
			}
			if err := guard.LockRefundScope(input.OrderID); err != nil {
				return err
			}
			if err := beginMembershipRefund(tx.Orders(), request); err != nil {
				return err
			}
			return bindMembershipRefund(tx.Orders(), request, input.ExistingRefundID)
		})
	case "manual":
		amount, parseErr := service.ParseRefundAmount(input.Amount)
		if parseErr != nil {
			return nil, parseErr
		}
		_, _, err = service.AdminManualRefund(AdminManualRefundInput{MembershipRequest: request, OrderID: input.OrderID, Amount: amount, Remark: input.Remark})
	case "wallet":
		amount, parseErr := service.ParseRefundAmount(input.Amount)
		if parseErr != nil {
			return nil, parseErr
		}
		_, _, _, err = service.AdminRefundToWallet(AdminRefundToWalletInput{MembershipRequest: request, OrderID: input.OrderID, Amount: amount, Remark: input.Remark})
	}
	if err != nil && !errors.Is(err, c.ErrRefundRequestReplay) {
		return nil, err
	}
	persisted, err := commands.GetMembershipRefund(input.RequestKey)
	if err != nil {
		return nil, err
	}
	if persisted == nil || persisted.Fingerprint != request.Fingerprint || persisted.RefundID == 0 {
		return nil, c.ErrRefundRequestConflict
	}
	slog.InfoContext(ctx, "membership_refund_funds_committed", "order_id", persisted.OrderID, "refund_id", persisted.RefundID)
	return persisted, nil
}

// beginMembershipRefund stages optional combined intent inside the existing refund transaction.
func beginMembershipRefund(store c.Store, request *c.MembershipRefundRequest) error {
	if request == nil {
		return nil
	}
	commands, ok := store.(c.MembershipRefundCommands)
	if !ok {
		return c.ErrRefundRequestInvalid
	}
	return commands.BeginMembershipRefund(request)
}

// bindMembershipRefund binds the real refund ID before the existing refund transaction can commit.
func bindMembershipRefund(store c.Store, request *c.MembershipRefundRequest, refundID uint) error {
	if request == nil {
		return nil
	}
	commands, ok := store.(c.MembershipRefundCommands)
	if !ok {
		return c.ErrRefundRequestInvalid
	}
	return commands.BindMembershipRefund(request.RequestKey, refundID)
}

// ListMembershipRefunds exposes persisted actor-owned requests for browser/restart recovery.
func (service *Service) ListMembershipRefunds(orderID, actor uint) ([]c.MembershipRefundRequest, error) {
	commands, ok := service.orderStore.(c.MembershipRefundCommands)
	if !ok {
		return nil, c.ErrRefundRequestInvalid
	}
	return commands.ListMembershipRefunds(orderID, actor)
}

// ClaimMembershipRefund delegates rights-only ownership; it never starts a financial transaction.
// snapshot, now and manual are validated and atomically compared by the store.
func (service *Service) ClaimMembershipRefund(ctx context.Context, snapshot c.MembershipRefundRequest, now time.Time, manual bool) (*c.MembershipRefundRequest, error) {
	retry, ok := service.orderStore.(c.MembershipRefundRetry)
	if !ok {
		return nil, c.ErrRefundRequestInvalid
	}
	return retry.ClaimMembershipRefund(ctx, snapshot, now, manual)
}

// FinishMembershipRefund records fenced local progress; success does not change refund balances.
func (service *Service) FinishMembershipRefund(ctx context.Context, claim c.MembershipRefundRequest, now time.Time, success bool) error {
	retry, ok := service.orderStore.(c.MembershipRefundRetry)
	if !ok {
		return c.ErrRefundRequestInvalid
	}
	return retry.FinishMembershipRefund(ctx, claim, now, success)
}

// CompleteMembershipRefund delegates durable progress, never invokes another financial transaction.
func (service *Service) CompleteMembershipRefund(key string) error {
	commands, ok := service.orderStore.(c.MembershipRefundCommands)
	if !ok {
		return c.ErrRefundRequestInvalid
	}
	return commands.CompleteMembershipRefund(key)
}
