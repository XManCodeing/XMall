package http

import "github.com/gin-gonic/gin"

func RegisterRoutes(admin gin.IRoutes, h *Handler) {
	admin.GET("/telegram-bot/instances", h.List)
	admin.POST("/telegram-bot/instances", h.Create)
	admin.PATCH("/telegram-bot/instances/:id", h.Configure)
	admin.POST("/telegram-bot/instances/:id/test", h.Test)
	admin.GET("/telegram-bot/instances/:id/status", h.Status)
	admin.GET("/telegram-bot/instances/:id/deliveries", h.Deliveries)
	admin.GET("/telegram-bot/instances/:id/profile", h.GetProfile)
	admin.PUT("/telegram-bot/instances/:id/profile", h.UpdateProfile)
}
