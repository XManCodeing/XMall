package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"log/slog"
	"time"
)

// reconcileLocal runs leased local-only work on its own joined lane, unaffected by network batches.
// Cancellation stops this lane before runtime releases its generation lease.
func (r *Runtime) reconcileLocal(ctx context.Context, instance *d.Instance, cancel context.CancelFunc) {
	r.reconcileLane(ctx, instance, cancel, r.ReconcileLocal, "telegram_local_reconcile_failed")
}

// reconcileLane isolates each callback round on a joined lane. action performs
// reconciliation; category is a fixed safe log label, never an error payload.
func (r *Runtime) reconcileLane(ctx context.Context, instance *d.Instance, cancel context.CancelFunc, action func(context.Context, uint) error, category string) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		if err := r.checkRecovery(ctx, instance); err != nil {
			cancel()
			return
		}
		if err := recoverCall(func() error { return action(ctx, instance.ID) }); err != nil && ctx.Err() == nil {
			// Never log panic/error values: local work can contain credentials.
			slog.WarnContext(ctx, category, "bot_id", instance.ID, "panic", err == errProcessingPanic, "backoff", backoff)
			if !wait(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = 2 * time.Second
		if !wait(ctx, backoff) {
			return
		}
	}
}

// authorizeProactive requires an exact background envelope and all ordinary private identity guards.
// It does not touch panel/session state; membership checks are additional, never identity bypasses.
func (r *Runtime) authorizeProactive(ctx context.Context, bot uint, item *d.Delivery, message c.Message) error {
	guard, ok := r.handler.(c.ProactiveDeliveryAuthorizer)
	if !ok || message.ProactiveDelivery == nil || message.TargetType != c.TargetShopPrivate || item.Purpose != "membership_proactive" || message.Purpose != item.Purpose || message.Operation != "send" || item.Operation != "send" || item.Key != message.BusinessRef || item.UserID != message.OwnerUserID || item.UserID == 0 || message.IdentityStamp == "" || item.ChatID != message.ChatID || item.TargetMessageID != 0 {
		return errTarget
	}
	if err := guard.AuthorizeProactiveDelivery(ctx, bot, message); err != nil {
		return err
	}
	if err := r.handler.AuthorizeDelivery(ctx, bot, item.TelegramUserID, item.UserID); err != nil {
		return err
	}
	return r.authorizeStamp(ctx, bot, item.TelegramUserID, message.IdentityStamp)
}
