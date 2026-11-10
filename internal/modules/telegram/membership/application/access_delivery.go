package application

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// PrepareInviteDelivery returns current authorization for an existing active invite.
// It never grants rights, creates an invitation or marks website retrieval delivered.
func (service *AccessService) PrepareInviteDelivery(ctx context.Context, identity c.AccessIdentity, id uint) (*rt.InviteDeliveryRef, error) {
	invite, err := service.Store.Invite(ctx, id)
	if err != nil {
		return nil, err
	}
	if invite == nil || invite.AccountID != identity.AccountID || invite.TelegramID != identity.TelegramID || invite.BotID != identity.BotID || invite.State != "active" || !d.IsInviteUsableAt(*invite, service.accessNow()) {
		return nil, c.ErrUntrustedIdentity
	}
	if err := service.authorizeAccess(ctx, identity, invite.TargetID); err != nil {
		return nil, err
	}
	route, err := service.Routes.MembershipRoute(ctx, invite.TargetID)
	if err != nil {
		return nil, err
	}
	if !sameAccessRoute(route, invite.BotID, invite.ChatID, invite.RouteVersion) {
		return nil, c.ErrConflict
	}
	identity.Route = &route
	eligible, err := service.Store.Eligibility(ctx, identity, invite.TargetID, service.accessNow())
	if err != nil {
		return nil, err
	}
	if !eligible.Active || invite.BindingVersion == 0 || invite.BindingVersion != eligible.BindingVersion {
		return nil, c.ErrUntrustedIdentity
	}
	return &rt.InviteDeliveryRef{InviteID: id, TargetID: invite.TargetID, BindingVersion: eligible.BindingVersion, EntitlementVersion: eligible.Version, RouteVersion: route.RouteVersion, Generation: route.Generation}, nil
}

// AuthorizeInviteDelivery validates the exact persisted link and current authorization just before HTTP.
// Mismatches fail closed and errors never include link, cipher or platform text.
func (service *AccessService) AuthorizeInviteDelivery(ctx context.Context, bot uint, message rt.Message) error {
	ref := message.InviteDelivery
	if ref == nil || message.Purpose != "panel" || message.ChatID <= 0 || message.OwnerUserID == 0 {
		return c.ErrUntrustedIdentity
	}
	current, err := service.PrepareInviteDelivery(ctx, c.AccessIdentity{AccountID: message.OwnerUserID, TelegramID: message.ChatID, BotID: bot}, ref.InviteID)
	if err != nil {
		return err
	}
	if *current != *ref {
		return c.ErrConflict
	}
	invite, err := service.Store.Invite(ctx, ref.InviteID)
	if err != nil {
		return err
	}
	if invite.Retired || invite.State != "active" {
		return c.ErrUntrustedIdentity
	}
	matched := 0
	for _, row := range message.Buttons {
		for _, button := range row {
			if validAccessLink(button.URL) {
				if accessHash(button.URL) != invite.LinkHash {
					return c.ErrUntrustedIdentity
				}
				matched++
			}
		}
	}
	if matched != 1 {
		return c.ErrUntrustedIdentity
	}
	return nil
}
