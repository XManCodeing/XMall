package membershiphttp

import (
	"context"
	"net/url"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// attentionRequest validates the optional mode before list parsing. Summary accepts exactly
// summary=1 and no filters: silently ignoring bot/pagination would misrepresent a global count.
// It returns handled=true for either a summary response or invalid mode, leaving lists unchanged.
func attentionRequest(ctx *gin.Context, read func(context.Context) (c.AttentionSummary, error)) bool {
	query, err := url.ParseQuery(ctx.Request.URL.RawQuery)
	if err != nil {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	values, exists := query["summary"]
	if !exists {
		return false
	}
	if len(values) != 1 || values[0] != "1" || len(query) != 1 {
		adminAccessError(ctx, c.ErrInvalid)
		return true
	}
	if read == nil {
		adminAccessError(ctx, c.ErrCapability)
		return true
	}
	summary, err := read(ctx.Request.Context())
	if err != nil {
		adminAccessError(ctx, err)
		return true
	}
	response.Success(ctx, summary)
	return true
}

// accessSummary binds an optional capability without broadening the existing admin interface.
// reviews selects the review queue; missing capability is rejected only for summary requests.
func (handler *AdminHandler) accessSummary(ctx *gin.Context, reviews bool) bool {
	reader, ok := handler.access.(c.AccessAttentionReader)
	if !ok {
		return attentionRequest(ctx, nil)
	}
	if reviews {
		return attentionRequest(ctx, reader.ReviewAttention)
	}
	return attentionRequest(ctx, reader.AccessAttention)
}

// lifecycleSummary reuses the lifecycle GET authorization and requires an optional read capability.
func (handler *AdminHandler) lifecycleSummary(ctx *gin.Context) bool {
	reader, ok := handler.lifecycle.(c.LifecycleAttentionReader)
	if !ok {
		return attentionRequest(ctx, nil)
	}
	return attentionRequest(ctx, reader.LifecycleAttention)
}
