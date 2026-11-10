package container

import (
	"context"
	bridge "github.com/XManCodeing/XMall/internal/bootstrap/telegramidentityrecovery"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
)

// initIdentityRecovery wires database-only migration work; existing runtime owns admission/cleanup.
// No sales gate, Telegram credentials or route executor is changed by this assembly.
func (container *Container) initIdentityRecovery() {
	container.TelegramRecovery.Identity = &a.IdentityExecutor{Store: container.TelegramRecovery.Store, Backend: bridge.New(gormdb.DB), Authorize: container.authorizeIdentityRecovery}
}

// authorizeIdentityRecovery requires an exact action grant for a real backend administrator.
// action is an internal fixed route selector; arbitrary URLs cannot be authorized through it.
func (container *Container) authorizeIdentityRecovery(ctx context.Context, actor uint, action string) error {
	var path string
	switch action {
	case "identity-preflight":
		path = "/admin/telegram-bot/recovery/identity-preflight"
	case "identity-cancel":
		path = "/admin/telegram-bot/recovery/tasks/:id/cancel"
	case "identity-confirm", "identity-resume":
		path = "/admin/telegram-bot/recovery/tasks/:id/" + action
	default:
		return c.ErrInvalid
	}
	return container.authorizeSupportPermission(ctx, actor, path, "POST", true)
}
