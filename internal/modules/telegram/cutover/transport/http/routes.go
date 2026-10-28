package cutoverhttp

import "github.com/gin-gonic/gin"

func RegisterRoutes(admin gin.IRoutes, h *Handler) {
	admin.POST("/telegram/cutover/import", h.Import)
	admin.POST("/telegram/cutover/preflight", h.Preflight)
	admin.POST("/telegram/cutover/apply", h.Apply)
}
