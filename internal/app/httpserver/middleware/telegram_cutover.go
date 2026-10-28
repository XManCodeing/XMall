package middleware

import (
	"context"
	"net/http"

	"github.com/XManCodeing/XMall/internal/i18n"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type LegacyChannelGuard interface {
	LegacyAllowed(context.Context, uint) (bool, error)
}

// LegacyChannelGuardMiddleware runs after signature authentication on every
// external channel route, including order/payment routes and old bot settings.
// Unassociated channels are permitted by the guard; missing wiring fails closed.
func LegacyChannelGuardMiddleware(guard LegacyChannelGuard) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := c.Get(channelClientIDKey)
		channelID, valid := id.(uint)
		if ok && valid && channelID != 0 && guard != nil {
			allowed, err := guard.LegacyAllowed(c.Request.Context(), channelID)
			if err == nil && allowed {
				c.Next()
				return
			}
		}
		response.ChannelError(c, http.StatusForbidden, response.CodeForbidden, i18n.T(i18n.ResolveLocale(c), "error.forbidden"), "channel_client_disabled")
		c.Abort()
	}
}
