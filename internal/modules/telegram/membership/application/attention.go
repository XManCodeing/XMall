package application

import (
	"context"
	"log/slog"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// AccessAttention aggregates current operation failures and latest invitation delivery evidence.
// ctx propagates cancellation through every page; runtime owns delivery normalization and secrets.
func (service *AccessService) AccessAttention(ctx context.Context) (summary c.AttentionSummary, err error) {
	defer func() { logAttentionError(ctx, "access", err) }()
	store, ok := service.Store.(c.AccessAttentionRepository)
	if !ok {
		return summary, c.ErrCapability
	}
	summary.AttentionCount, err = store.CountAccessAttention(ctx)
	if err != nil || service.Deliveries == nil {
		return summary, err
	}
	var after uint
	for {
		rows, readErr := store.AccessDeliveryCandidates(ctx, after, 100)
		if readErr != nil {
			return c.AttentionSummary{}, readErr
		}
		for _, row := range rows {
			status, readErr := service.Deliveries.InviteDelivery(ctx, row.BotID, row.InviteID)
			if readErr != nil {
				return c.AttentionSummary{}, readErr
			}
			if status.State == "failed" || status.State == "unknown" {
				summary.AttentionCount++
			}
		}
		if len(rows) < 100 {
			return summary, nil
		}
		last := rows[len(rows)-1].ID
		if last <= after {
			return c.AttentionSummary{}, c.ErrConflict
		}
		after = last
	}
}

// ReviewAttention returns unresolved, not-yet-manually-reviewed applications across all bots.
func (service *AccessService) ReviewAttention(ctx context.Context) (summary c.AttentionSummary, err error) {
	defer func() { logAttentionError(ctx, "reviews", err) }()
	store, ok := service.Store.(c.AccessAttentionRepository)
	if !ok {
		return summary, c.ErrCapability
	}
	summary.AttentionCount, err = store.CountReviewAttention(ctx)
	return summary, err
}

// LifecycleAttention delegates to the optional durable read port; absent support fails closed.
func (service *Lifecycle) LifecycleAttention(ctx context.Context) (summary c.AttentionSummary, err error) {
	defer func() { logAttentionError(ctx, "lifecycle", err) }()
	store, ok := service.Store.(c.LifecycleAttentionReader)
	if !ok {
		return summary, c.ErrCapability
	}
	return store.LifecycleAttention(ctx)
}

// logAttentionError records the failing queue only; database diagnostics and secrets stay private.
func logAttentionError(ctx context.Context, queue string, err error) {
	if err != nil {
		slog.ErrorContext(ctx, "membership attention summary failed", "queue", queue)
	}
}

var _ c.AccessAttentionReader = (*AccessService)(nil)
var _ c.LifecycleAttentionReader = (*Lifecycle)(nil)
