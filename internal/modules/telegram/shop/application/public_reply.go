package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// AuthorizePublicReply validates a public capability against its exact encrypted reply.
// Neither contact ownership nor owner zero can authorize a private message.
func (s *Service) AuthorizePublicReply(ctx context.Context, bot uint, message rt.Message) error {
	if message.PanelCleanup != nil {
		return c.ErrDenied
	}
	ref := message.PublicReply
	if ref == nil || bot == 0 || ref.BotID != bot || ref.TelegramID <= 0 || message.ChatID != ref.TelegramID || ref.Generation == "" || !ref.ExpiresAt.After(s.now()) {
		return c.ErrDenied
	}
	if message.TargetType != rt.TargetShopPrivate || message.OwnerUserID != 0 || message.IdentityStamp != "" || message.InviteDelivery != nil || message.ReminderDelivery != nil || message.Transfer != nil || message.ThreadID != 0 || message.BusinessRef != "" {
		return c.ErrDenied
	}
	if message.Purpose != "panel" && message.Purpose != "public_notice" {
		return c.ErrDenied
	}
	row, err := s.d.Store.GetSession(ctx, bot, ref.TelegramID, ref.TelegramID)
	if err != nil {
		return err
	}
	if row == nil || !row.ExpiresAt.After(s.now()) {
		return c.ErrDenied
	}
	plain, err := crypto.Decrypt(s.key, row.DataCipher)
	if err != nil {
		return err
	}
	var saved state
	if err := json.Unmarshal([]byte(plain), &saved); err != nil {
		return err
	}
	if saved.UserID != 0 || saved.PublicGeneration != ref.Generation {
		return c.ErrDenied
	}
	uid, err := s.d.Identity.Resolve(ctx, c.Actor{BotID: bot, TelegramID: ref.TelegramID}, false)
	if saved.IdentityFailure == "identity_unavailable" {
		if !errors.Is(err, c.ErrDenied) {
			return c.ErrDenied
		}
	} else if saved.IdentityFailure == "account_disabled" {
		if !errors.Is(err, c.ErrAccountDisabled) {
			return c.ErrDenied
		}
	} else if err != nil {
		return err
	} else if uid != 0 {
		return c.ErrDenied
	}
	actual, err := json.Marshal(message)
	if err != nil {
		return err
	}
	for _, expected := range saved.Reply {
		encoded, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		if bytes.Equal(encoded, actual) {
			return nil
		}
	}
	return c.ErrDenied
}
