package botapi

import (
	"context"
	"errors"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

// SupportMember never persists an actor's permission. Each operator action and
// deferred customer delivery must call it against the current configured group.
func (a *Adapter) SupportMember(ctx context.Context, bot uint, chat, actor int64) (bool, error) {
	if actor <= 0 || chat >= 0 {
		return false, c.ErrInvalid
	}
	i, err := a.Store.Get(ctx, bot)
	if err != nil {
		if errors.Is(err, r.ErrNotFound) {
			return false, c.ErrNotFound
		}
		return false, c.ErrCapabilityPending
	}
	if i == nil || !i.Enabled || i.Mode != "internal" || i.State != "running" {
		return false, c.ErrCapability
	}
	identity, err := a.Instance(ctx, bot)
	if err != nil {
		return false, err
	}
	if !identity.Running || identity.Generation != i.Generation {
		return false, c.ErrCapability
	}
	token, err := a.Cipher.Open(i.TokenCipher)
	if err != nil {
		return false, c.ErrCapability
	}
	client, err := telegramapi.NewClient(token, a.Options...)
	if err != nil {
		return false, c.ErrCapability
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	member, err := client.GetChatMember(lookupCtx, chat, actor)
	if err != nil {
		classified := failure(err)
		switch classified.ErrorClass {
		case "authentication", "permission_denied", "resource_inaccessible":
			return false, c.ErrCapability
		default:
			return false, &c.CapabilityPending{RetryAfterSeconds: classified.RetryAfter}
		}
	}
	current, err := a.Store.Get(ctx, bot)
	if err != nil {
		if errors.Is(err, r.ErrNotFound) {
			return false, c.ErrNotFound
		}
		return false, c.ErrCapabilityPending
	}
	if current == nil || !current.Enabled || current.Mode != "internal" || current.State != "running" || current.Generation != i.Generation || current.Version != i.Version {
		return false, c.ErrConflict
	}
	if member.User.ID != actor || member.User.IsBot {
		return false, c.ErrCapability
	}
	switch member.Status {
	case "creator", "administrator", "member":
		return true, nil
	case "restricted":
		return member.IsMember, nil
	default:
		return false, nil
	}
}

var _ c.SupportMemberProbe = (*Adapter)(nil)
