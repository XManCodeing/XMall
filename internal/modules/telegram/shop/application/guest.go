package application

import (
	"context"
	"errors"
	"log/slog"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// identityFailureKey maps only terminal policy errors; infrastructure failures remain errors.
func identityFailureKey(err error) string {
	switch {
	case errors.Is(err, c.ErrDenied):
		return "identity_unavailable"
	case errors.Is(err, c.ErrAccountDisabled):
		return "account_disabled"
	case errors.Is(err, c.ErrRegistrationDisabled):
		return "registration_disabled"
	case errors.Is(err, c.ErrInviteRequired):
		return "invite_required"
	}
	return ""
}

// guestAction permits public reads only. Provisioning requires a validated current callback.
// It returns handled=true when the action must not enter the private dispatcher.
func (v *view) guestAction(ctx context.Context, a action) (bool, error) {
	switch a.Kind {
	case "home", "catalog", "products", "product", "sku", "delta", "quantity", "more_quantity", "quantity_input", "details", "purchase", "noop", "label", "help", "language", "locale", "bind":
		return false, nil
	case "account", "continue_checkout":
		if !v.explicit {
			return true, c.ErrDenied
		}
		if a.Kind == "continue_checkout" && v.state.LinkProductID != 0 {
			product, err := v.linkProduct(ctx, v.state.Selection.ProductID)
			if err != nil {
				return true, err
			}
			if product == nil || !product.Membership || !product.Available || product.Unsupported {
				return true, c.ErrUnavailable
			}
		}
		if err := v.provision(ctx); err != nil {
			return true, err
		}
		if a.Kind == "continue_checkout" {
			return true, v.purchase(ctx, true)
		}
		return false, nil
	default:
		v.say(text(v.actor.Locale, "guest_account_required"))
		v.homeButton()
		return true, nil
	}
}

// provision upgrades only explicit current-session intent and discards all private state.
// The public selection survives, but prices and idempotency keys never do.
func (v *view) provision(ctx context.Context) error {
	if v.state.IdentityFailure != "" || v.actor.UserID != 0 {
		return c.ErrDenied
	}
	uid, err := v.service.d.Identity.Resolve(ctx, v.actor, true)
	if err != nil {
		slog.Warn("telegram explicit account provisioning failed", "bot_id", v.actor.BotID, "policy", identityFailureKey(err))
		return err
	}
	if uid == 0 {
		return c.ErrDenied
	}
	v.actor.UserID = uid
	if identity, ok := v.service.d.Identity.(c.IdentityGeneration); ok {
		v.actor.Generation, err = identity.Generation(ctx, v.actor)
		if err != nil {
			return err
		}
	}
	ref, err := nonce()
	if err != nil {
		return err
	}
	epoch, err := nonce()
	if err != nil {
		return err
	}
	previous := v.state
	*previous = state{UserID: uid, Generation: v.actor.Generation, Locale: previous.Locale, Selection: previous.Selection, LinkProductID: previous.LinkProductID, Category: previous.Category, ProductPage: previous.ProductPage, LegacyInputThrough: previous.LegacyInputThrough, Ref: ref, PanelEpoch: epoch, Actions: map[string]action{}}
	v.messages = nil
	v.replace = true
	contact, err := v.service.d.Store.Contact(ctx, v.actor.BotID, v.actor.TelegramID)
	if err != nil {
		return err
	}
	if contact == nil {
		return c.ErrDenied
	}
	contact.UserID = uid
	return v.service.d.Store.SaveContact(ctx, contact)
}
