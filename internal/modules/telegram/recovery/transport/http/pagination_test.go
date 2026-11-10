package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"github.com/gin-gonic/gin"
)

// pageStore records whether the handler selected numbered or legacy cursor queries.
type pageStore struct {
	c.Store
	query  c.PageQuery
	filter c.ListFilter
	taskID uint
	calls  int
	legacy bool
	err    error
}

// ListPage records filters/query and returns counted metadata or the configured error.
func (store *pageStore) ListPage(_ context.Context, filter c.ListFilter, query c.PageQuery) (c.TaskPage, error) {
	store.query, store.filter = query, filter
	store.calls++
	return c.TaskPage{PageInfo: &c.PageInfo{Page: query.Page, PageSize: query.PageSize, Total: 45, TotalPage: 5}}, store.err
}

// ListStepsPage records the parent ID and exact numbered query, returning counted metadata.
func (store *pageStore) ListStepsPage(ctx context.Context, id uint, query c.PageQuery) (c.StepPage, error) {
	store.taskID = id
	page, err := store.ListPage(ctx, c.ListFilter{}, query)
	return c.StepPage{PageInfo: page.PageInfo}, err
}

// List records the unchanged legacy task cursor and returns its continuation.
func (store *pageStore) List(_ context.Context, filter c.ListFilter) (c.TaskPage, error) {
	store.filter, store.legacy = filter, true
	store.calls++
	return c.TaskPage{NextAfterID: 42}, store.err
}

// ListSteps records the unchanged legacy checkpoint cursor and parent ID.
func (store *pageStore) ListSteps(ctx context.Context, id, after uint, limit int) (c.StepPage, error) {
	store.taskID = id
	page, err := store.List(ctx, c.ListFilter{AfterID: after, Limit: limit})
	return c.StepPage{NextAfterID: page.NextAfterID}, err
}

// pageRouter installs the production routes with optional authenticated identity for boundary tests.
func pageRouter(store *pageStore, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if authenticated {
		router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	}
	RegisterRoutes(router, New(&a.Service{Store: store}))
	return router
}

// TestNumberedPageHandlers checks exact page selection, optional defaults and cursor compatibility.
func TestNumberedPageHandlers(t *testing.T) {
	for _, path := range []string{"/telegram-bot/recovery/tasks", "/telegram-bot/recovery/tasks/7/steps"} {
		store := &pageStore{}
		router := pageRouter(store, true)
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?page=3&page_size=10", nil))
		if result.Code != 200 || store.query.Page != 3 || store.query.PageSize != 10 || !strings.Contains(result.Body.String(), `"total":45`) {
			t.Fatalf("numbered %s: %s %+v", path, result.Body, store)
		}
		result = httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?page_size=50", nil))
		if result.Code != 200 || store.query.Page != 1 || store.query.PageSize != 50 {
			t.Fatalf("defaults: %s %+v", result.Body, store)
		}
		result = httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?after_id=9007199254740993&limit=20", nil))
		if result.Code != 200 || !store.legacy || store.filter.AfterID != 9007199254740993 || strings.Contains(result.Body.String(), `"total"`) {
			t.Fatalf("legacy: %s %+v", result.Body, store)
		}
		if strings.HasSuffix(path, "/steps") && store.taskID != 7 {
			t.Fatal("lost parent scope")
		}
	}
}

// TestNumberedPageValidation rejects ambiguous/invalid requests and keeps authentication mandatory.
func TestNumberedPageValidation(t *testing.T) {
	for _, path := range []string{"/telegram-bot/recovery/tasks", "/telegram-bot/recovery/tasks/7/steps"} {
		for _, query := range []string{"page=0", "page=-1", "page=", "page=1.5", "page=999999999999999999999", "page_size=101", "page_size=0", "page=2&limit=20", "page=2&after_id=0", "page=2&page=3", "page=9223372036854775807&page_size=100"} {
			store := &pageStore{}
			result := httptest.NewRecorder()
			pageRouter(store, true).ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?"+query, nil))
			if result.Code != 400 || store.calls != 0 {
				t.Fatalf("accepted %s: %s", query, result.Body)
			}
		}
		store := &pageStore{}
		result := httptest.NewRecorder()
		pageRouter(store, false).ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?page=1", nil))
		if store.calls != 0 || !strings.Contains(result.Body.String(), "401") || result.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("authentication bypass: %s", result.Body)
		}
		store.err = c.ErrNotFound
		result = httptest.NewRecorder()
		pageRouter(store, true).ServeHTTP(result, httptest.NewRequest(http.MethodGet, path+"?page=1", nil))
		if result.Code != 404 {
			t.Fatalf("lost error: %s", result.Body)
		}
	}
}
