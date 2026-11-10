package http

import (
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// ConfirmDeletedTopic accepts strict snapshot strings and verified-fact consent.
// Actor is taken solely from admin middleware; errors remain finite and no-store.
func (h *Handler) ConfirmDeletedTopic(ctx *gin.Context) {
	admin, ok := actor(ctx)
	if !ok {
		return
	}
	id, ok := param(ctx, "id")
	if !ok {
		return
	}
	var input c.AdminTopicDeletionInput
	if !strictResolution(ctx, &input) {
		return
	}
	if err := h.service.ConfirmDeletedTopic(ctx.Request.Context(), admin, id, input); err != nil {
		fail(ctx, err)
		return
	}
	response.Success(ctx, gin.H{"id": id, "confirmed": true})
}
