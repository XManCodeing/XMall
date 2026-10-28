package categoryhttp

import (
	categorydomain "github.com/homles698698-lab/XMall/internal/modules/catalog/category/domain"
	categorypresenter "github.com/homles698698-lab/XMall/internal/modules/catalog/category/transport/presenter"
	"github.com/homles698698-lab/XMall/internal/platform/http/ginutil"
	"github.com/homles698698-lab/XMall/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

type PublicQueries interface {
	ListActive() ([]categorydomain.Category, error)
}

type PublicHandler struct {
	queries PublicQueries
}

func NewPublicHandler(queries PublicQueries) *PublicHandler {
	if queries == nil {
		panic("category public handler: queries are nil")
	}
	return &PublicHandler{queries: queries}
}

func (handler *PublicHandler) List(c *gin.Context) {
	categories, err := handler.queries.ListActive()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.category_fetch_failed", err)
		return
	}
	response.Success(c, categorypresenter.List(categories))
}
