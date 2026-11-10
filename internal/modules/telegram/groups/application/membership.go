package application

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
)

// MembershipRoute resolves a single enabled logical membership target and current capability observation.
// targetID comes from a persisted entitlement, not a client-supplied physical chat. No network call is made here.
// A route is only an authorization snapshot; the caller must also check its execution fence before external I/O.
func (service *Service) MembershipRoute(ctx context.Context, targetID uint) (c.SupportRoute, error) {
	if targetID == 0 || service.Store == nil || service.Probe == nil {
		return c.SupportRoute{}, c.ErrInvalid
	}
	target, err := service.Store.Get(ctx, targetID)
	if err != nil {
		return c.SupportRoute{}, err
	}
	if target == nil || target.RecoveryTaskID != 0 || target.ID != targetID || !target.Enabled || target.Purpose != "membership" || target.BotInstanceID == 0 || target.TelegramChatID >= 0 || target.RouteVersion == 0 {
		return c.SupportRoute{}, c.ErrCapability
	}
	identity, err := service.Probe.Instance(ctx, target.BotInstanceID)
	if err != nil {
		return c.SupportRoute{}, err
	}
	if !identity.Running || identity.Generation == 0 || identity.TelegramID <= 0 {
		return c.SupportRoute{}, c.ErrCapability
	}
	view := service.View(*target)
	if !view.Fresh || target.Observation.State != "ready" || len(view.MissingCapabilities) != 0 || target.Observation.Generation != identity.Generation || target.Observation.BotTelegramID != identity.TelegramID {
		return c.SupportRoute{}, c.ErrCapability
	}
	// New-sales configuration intentionally does not gate access for existing paid entitlements.
	return c.SupportRoute{TargetID: target.ID, BotID: target.BotInstanceID, ChatID: target.TelegramChatID, RouteVersion: target.RouteVersion, Generation: identity.Generation}, nil
}
