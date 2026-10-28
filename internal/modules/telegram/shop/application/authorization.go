package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// AuthorizeStamp prevents queued sensitive replies from being revived by an
// unlink/relink of the same website account. Legacy unstamped messages fail closed.
func (s *Service) AuthorizeStamp(ctx context.Context, bot uint, tg int64, expected string) error {
	identity, ok := s.d.Identity.(c.IdentityGeneration)
	if !ok {
		return c.ErrDenied
	}
	actor := c.Actor{BotID: bot, TelegramID: tg}
	uid, err := s.d.Identity.Resolve(ctx, actor, false)
	if err != nil {
		return err
	}
	actor.UserID = uid
	current, err := identity.Generation(ctx, actor)
	if err != nil {
		return err
	}
	if expected == "" || current != expected {
		return c.ErrDenied
	}
	return nil
}
