package cutoverhttp

import (
	cutover "github.com/XManCodeing/XMall/internal/modules/telegram/cutover/application"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type Handler struct{ Service *cutover.Service }

func (h *Handler) Import(c *gin.Context) {
	var r cutover.ImportRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	result, err := h.Service.Import(c.Request.Context(), r)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", cutover.ErrBlocked)
		return
	}
	response.Success(c, result)
}
func (h *Handler) Preflight(c *gin.Context) {
	var r cutover.Request
	if err := c.ShouldBindJSON(&r); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	p, err := h.Service.Preflight(c.Request.Context(), r)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", cutover.ErrBlocked)
		return
	}
	response.Success(c, p)
}
func (h *Handler) Apply(c *gin.Context) {
	var r cutover.Request
	if err := c.ShouldBindJSON(&r); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	if err := h.Service.Apply(c.Request.Context(), r); err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", cutover.ErrBlocked)
		return
	}
	// Reapplying a completed cutover does not disable an already running bot.
	response.Success(c, gin.H{"mode": "internal"})
}
