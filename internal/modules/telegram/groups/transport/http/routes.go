package http

import "github.com/gin-gonic/gin"

func RegisterRoutes(admin gin.IRoutes, h *Handler) {
	admin.GET("/telegram-bot/groups", h.List)
	admin.POST("/telegram-bot/groups", h.Save)
	admin.POST("/telegram-bot/groups/enable-preview", h.PreviewEnable)
	admin.POST("/telegram-bot/groups/:id/enable-preview", h.PreviewEnable)
	admin.GET("/telegram-bot/groups/:id", h.Get)
	admin.PATCH("/telegram-bot/groups/:id", h.Save)
	admin.POST("/telegram-bot/groups/:id/check", h.Check)
	admin.POST("/telegram-bot/groups/:id/replacement-preview", h.ReplacementPreview)
	admin.GET("/telegram-bot/instances/:id/modules", h.Modules)
	admin.PATCH("/telegram-bot/instances/:id/modules", h.SaveModule)
}
