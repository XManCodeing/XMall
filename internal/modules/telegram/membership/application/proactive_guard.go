package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// AuthorizeProactiveInviteDelivery validates a background invite independently of panel authorization.
// message must carry exactly one matching persisted link; no other URL/callback or panel mutation is allowed.
func (service *AccessService) AuthorizeProactiveInviteDelivery(ctx context.Context, bot uint, message r.Message) error {
	ref := message.InviteDelivery
	if ref == nil || message.ProactiveDelivery == nil || message.ProactiveDelivery.Stage != "ready" || message.Purpose != "membership_proactive" || message.Operation != "send" || message.ChatID <= 0 || message.OwnerUserID == 0 || message.TargetMessageID != 0 || message.PanelEpoch != "" || message.PanelRevision != 0 {
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
	if len(message.Buttons) != 1 || len(message.Buttons[0]) != 1 {
		return c.ErrUntrustedIdentity
	}
	button := message.Buttons[0][0]
	if !validAccessLink(button.URL) || accessHash(button.URL) != invite.LinkHash || button.Data != "" {
		return c.ErrUntrustedIdentity
	}
	return nil
}
