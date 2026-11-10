package http

import (
	"context"
	"net/http/httptest"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/gin-gonic/gin"
)

// sessionFilterStore records the validated HTTP query without changing other ports.
type sessionFilterStore struct {
	testAdminStore
	filter c.AdminFilter
}

// Sessions receives the authenticated filter and returns an empty metadata page.
func (store *sessionFilterStore) Sessions(_ context.Context, filter c.AdminFilter) ([]c.AdminSession, int64, error) {
	store.filter = filter
	return []c.AdminSession{}, 0, nil
}

// TestSessionOperationHTTPFilter verifies false is preserved rather than dropped.
func TestSessionOperationHTTPFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &sessionFilterStore{}
	service := a.NewAdmin(store, nil, func(context.Context, uint, string, bool) error { return nil }, nil)
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(1)); ctx.Next() })
	RegisterRoutes(group, New(service))
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("GET", "/admin/telegram-bot/support/sessions?page=2&page_size=10&bot_id=3&state=open&unreplied=true&blocked=false&has_topic=true&failure=downstream", nil))
	query := store.filter
	if result.Code != 200 || query.Page != 2 || query.PageSize != 10 || query.BotID != 3 || query.State != "open" || query.Failure != "downstream" || query.Unreplied == nil || !*query.Unreplied || query.Blocked == nil || *query.Blocked || query.HasTopic == nil || !*query.HasTopic {
		t.Fatal(result.Code, result.Body.String(), query)
	}
}
