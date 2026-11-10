package http

import (
	"context"
	"net/http/httptest"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/gin-gonic/gin"
)

// progressBackend records the exact server-loaded task and can simulate database failure.
type progressBackend struct {
	a.IdentityBackend
	calls int
	err   error
}

// Mappings returns stored test evidence only for the task obtained through the server store.
func (backend *progressBackend) Mappings(_ context.Context, detail *c.Detail) ([]c.IdentityMapping, error) {
	backend.calls++
	if detail.Task.ID != 5 {
		return nil, c.ErrStale
	}
	if backend.err != nil {
		return nil, backend.err
	}
	return []c.IdentityMapping{}, nil
}

// TestIdentityProgressHTTPAuthenticationAndFailure prevents anonymous reads and false success.
// Missing assembly remains handled by the existing generic detail route; production is assembled.
func TestIdentityProgressHTTPAuthenticationAndFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, authenticated := range []bool{false, true} {
		backend := &progressBackend{err: c.ErrStale}
		store := identityHTTPStore{task: d.Task{ID: 5, Kind: d.IdentityTransfer}}
		handler := New(&a.Service{Store: store, Identity: &a.IdentityExecutor{Store: store, Backend: backend}})
		router := gin.New()
		if authenticated {
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
		}
		RegisterRoutes(router, handler)
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest("GET", "/telegram-bot/recovery/tasks/5", nil))
		if !authenticated && backend.calls != 0 {
			t.Fatal("anonymous mapping read")
		}
		if authenticated && (backend.calls != 1 || result.Code != 409) {
			t.Fatalf("read failure fabricated success: %s", result.Body.String())
		}
		if result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing cache protection")
		}
	}
}
