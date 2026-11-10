package application

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
)

// AuthorizeSupportMember checks the present physical route and live membership.
// A member observation from an earlier update is never a reusable permission.
func (s *Service) AuthorizeSupportMember(ctx context.Context, bot uint, chat, actor int64, version uint64) error {
	if actor <= 0 || chat >= 0 || version == 0 {
		return c.ErrCapability
	}
	route, err := s.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return err
	}
	if route.ChatID != chat || route.RouteVersion != version {
		return c.ErrConflict
	}
	probe, ok := s.Probe.(c.SupportMemberProbe)
	if !ok {
		return c.ErrCapability
	}
	member, err := probe.SupportMember(ctx, bot, chat, actor)
	if err != nil {
		return err
	}
	if !member {
		return c.ErrCapability
	}
	current, err := s.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return err
	}
	if current != route {
		return c.ErrConflict
	}
	return nil
}

// ResolveSupportRoute is the narrow cross-module boundary for consultation
// routing. A saved enabled switch or a stale green capability check is not
// sufficient: both the current runtime generation and configured target match.
func (s *Service) ResolveSupportRoute(ctx context.Context, bot uint) (c.SupportRoute, error) {
	if bot == 0 {
		return c.SupportRoute{}, c.ErrInvalid
	}
	identity, err := s.Probe.Instance(ctx, bot)
	if err != nil {
		return c.SupportRoute{}, err
	}
	if !identity.Running || identity.Generation == 0 {
		return c.SupportRoute{}, c.ErrCapability
	}
	modules, err := s.Store.Modules(ctx, bot)
	if err != nil {
		return c.SupportRoute{}, err
	}
	var targetID uint
	var effective bool
	for _, module := range modules {
		// Configured intent is used only to diagnose why an effective route is
		// unavailable. It must never authorize delivery on its own.
		if module.BotInstanceID == bot && module.Partition == "support" && (module.ConfiguredEnabled || module.Enabled) {
			if targetID != 0 || module.TargetID == 0 {
				return c.SupportRoute{}, c.ErrConflict
			}
			targetID = module.TargetID
			effective = module.Enabled && module.EffectiveEnabled
		}
	}
	if targetID == 0 {
		return c.SupportRoute{}, c.ErrCapability
	}
	target, err := s.Store.Get(ctx, targetID)
	if err != nil {
		return c.SupportRoute{}, err
	}
	if target == nil || target.RecoveryTaskID != 0 || target.BotInstanceID != bot || target.Purpose != "support" || !target.Enabled || target.TelegramChatID >= 0 || target.RouteVersion == 0 {
		return c.SupportRoute{}, c.ErrCapability
	}
	view := s.View(*target)
	if err := c.SupportCapability(view, identity); err != nil {
		return c.SupportRoute{}, err
	}
	if !effective {
		return c.SupportRoute{}, c.ErrCapability
	}
	return c.SupportRoute{TargetID: target.ID, BotID: bot, ChatID: target.TelegramChatID, RouteVersion: target.RouteVersion, Generation: identity.Generation}, nil
}
