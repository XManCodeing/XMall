package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/gin-gonic/gin"
)

// identityHTTPStore supplies a persisted task so HTTP cannot inject identity or actor values.
type identityHTTPStore struct {
	c.Store
	task d.Task
}

// Get returns the immutable reviewed task; id is checked to detect route parsing regressions.
func (store identityHTTPStore) Get(_ context.Context, id uint) (*c.Detail, error) {
	if id != 5 {
		return nil, c.ErrNotFound
	}
	return &c.Detail{Task: store.task, Snapshot: d.Snapshot{IdentityTargets: []d.IdentityTarget{{TargetID: "1"}}}}, nil
}

// Confirm echoes only the trusted confirmation fields supplied by the adapter.
func (store identityHTTPStore) Confirm(_ context.Context, input c.Confirmation) (*d.Task, error) {
	if input.ActorID != 7 || input.ExpectedVersion != 9007199254740993 {
		return nil, c.ErrStale
	}
	return &store.task, nil
}

// identityHTTPBackend is deliberately unreachable: this test replays a confirmed task.
type identityHTTPBackend struct{ a.IdentityBackend }

// TestIdentityHTTPActorAndCAS checks auth, decimal-string versions, actor spoofing and per-action checks.
func TestIdentityHTTPActorAndCAS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, authenticated := range []bool{false, true} {
		calls := 0
		ref := strings.Repeat("1", 64)
		confirmed := d.Task{ID: 5, Kind: d.IdentityTransfer, ActorID: 7, PreflightRef: ref, VerificationNote: "reviewed"}
		confirmed.ConfirmedAt = &confirmed.CreatedAt
		store := identityHTTPStore{task: confirmed}
		executor := &a.IdentityExecutor{Store: store, Backend: identityHTTPBackend{}, Authorize: func(_ context.Context, actor uint, action string) error {
			calls++
			if actor != 7 || action != "identity-confirm" {
				return errors.New("unexpected authority")
			}
			return nil
		}}
		router := gin.New()
		if authenticated {
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
		}
		RegisterRoutes(router, New(&a.Service{Store: store, Identity: executor}))
		body := `{"preflight_ref":"` + ref + `","expected_version":"9007199254740993","actor_id":99,"old_telegram_id":"123"}`
		request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/identity-confirm", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		if authenticated && (result.Code != 200 || calls != 1) {
			t.Fatalf("response=%s calls=%d", result.Body.String(), calls)
		}
		if !authenticated && calls != 0 {
			t.Fatal("unauthenticated executor call")
		}
		if result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing cache protection")
		}
	}
}
