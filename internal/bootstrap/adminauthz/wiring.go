package adminauthzwiring

import (
	"github.com/XManCodeing/XMall/internal/app/container"
	adminauthztransport "github.com/XManCodeing/XMall/internal/modules/identity/adminauthorization/transport/http"
)

func NewHandler(c *container.Container) *adminauthztransport.AdminHandler {
	return adminauthztransport.NewAdminHandler(
		adminAuthzRolePolicyAdapter{svc: c.AuthzService},
		adminAuthzDirectoryAdapter{admins: c.AdminStore},
		adminAuthzPasswordAdapter{auth: c.AuthService},
		adminAuthzAuthStateAdapter{},
		adminAuthzAuditAdapter{svc: c.AuthzAuditService},
	)
}
