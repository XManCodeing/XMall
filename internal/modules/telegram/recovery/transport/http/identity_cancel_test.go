package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	support "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/gin-gonic/gin"
)

// identityCancelStore records generic cancellation without accepting a client-supplied task kind.
type identityCancelStore struct {
	identityHTTPStore
	called bool
}

// Cancel records an unconfirmed cancellation and returns a synthetic persisted result.
func (store *identityCancelStore) Cancel(_ context.Context, input c.Cancellation) (*d.Task, error) {
	store.called = true
	if input.ActorID != 7 || input.ExpectedVersion != 9007199254740993 {
		return nil, c.ErrStale
	}
	return &d.Task{ID: input.TaskID, State: d.Cancelled}, nil
}

// TestIdentityCancelHTTPFailsClosed checks absent assembly, permissions, actor binding and string CAS.
func TestIdentityCancelHTTPFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, variant := range []string{"missing", "denied", "foreign", "success", "numeric", "zero"} {
		t.Run(variant, func(t *testing.T) {
			store := &identityCancelStore{identityHTTPStore: identityHTTPStore{task: d.Task{ID: 5, Kind: d.IdentityTransfer, ActorID: 7, VerificationNote: "reviewed"}}}
			service := &a.Service{Store: store}
			calls := 0
			if variant != "missing" {
				service.Identity = &a.IdentityExecutor{Store: store, Backend: identityHTTPBackend{}, Authorize: func(_ context.Context, actor uint, action string) error {
					calls++
					if actor != 7 || action != "identity-cancel" {
						t.Fatal("wrong authorization", actor, action)
					}
					if variant == "denied" {
						return support.ErrDenied
					}
					return nil
				}}
			}
			if variant == "foreign" {
				store.task.ActorID = 8
			}
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			RegisterRoutes(router, New(service))
			body := `{"expected_version":"9007199254740993","actor_id":99}`
			if variant == "numeric" {
				body = `{"expected_version":9007199254740993}`
			}
			if variant == "zero" {
				body = `{"expected_version":"0"}`
			}
			request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/cancel", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if variant == "success" {
				if result.Code != 200 || !store.called || calls != 1 {
					t.Fatal("authorized cancellation failed", result.Code, calls)
				}
			} else if store.called || (variant != "numeric" && result.Code == 200) {
				t.Fatal("unsafe cancellation passed", variant, result.Code)
			}
			if variant == "denied" && result.Code != 403 {
				t.Fatal("permission response", result.Code)
			}
		})
	}
}
