package http

import (
	"strconv"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"github.com/gin-gonic/gin"
)

// numberedPagination opts in when either page parameter is present, preserving legacy cursor requests.
// Mixed, repeated, empty, nonpositive and overflowing inputs are rejected before storage access.
func numberedPagination(ctx *gin.Context) (c.PageQuery, bool, bool) {
	values := ctx.Request.URL.Query()
	_, hasPage := values["page"]
	_, hasSize := values["page_size"]
	if !hasPage && !hasSize {
		return c.PageQuery{}, false, true
	}
	input := c.PageQuery{Page: 1, PageSize: 20}
	for _, key := range []string{"after_id", "limit"} {
		if _, exists := values[key]; exists {
			fail(ctx, c.ErrInvalid)
			return input, true, false
		}
	}
	for key, target := range map[string]*int{"page": &input.Page, "page_size": &input.PageSize} {
		if raw, exists := values[key]; exists {
			if len(raw) != 1 {
				fail(ctx, c.ErrInvalid)
				return input, true, false
			}
			value, err := strconv.Atoi(raw[0])
			if err != nil || value < 1 {
				fail(ctx, c.ErrInvalid)
				return input, true, false
			}
			*target = value
		}
	}
	if _, err := input.Normalize(); err != nil {
		fail(ctx, err)
		return input, true, false
	}
	return input, true, true
}
