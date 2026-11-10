package http

import (
	"encoding/json"
	"io"
	"net/http"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// deliveryRecovery returns a handler for one fixed direction/action. It accepts
// only CAS/evidence, rejects receipt injection and trailing JSON, and returns a
// metadata result. JWT/RBAC middleware supplies the actor, never client fields.
func (h *Handler) deliveryRecovery(direction, action string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		admin, ok := actor(ctx)
		if !ok {
			return
		}
		id, ok := param(ctx, "id")
		if !ok {
			return
		}
		var input c.AdminDeliveryRecoveryInput
		decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			fail(ctx, c.ErrAdminInvalid)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			fail(ctx, c.ErrAdminInvalid)
			return
		}
		out, err := h.service.RecoverDelivery(ctx.Request.Context(), admin, id, direction, action, input)
		if err != nil {
			fail(ctx, err)
			return
		}
		response.Success(ctx, out)
	}
}
