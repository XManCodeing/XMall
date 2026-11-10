package membershiphttp

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminRecoveryStub records the trusted actor and submitted versions without executing external effects.
type adminRecoveryStub struct {
	actor uint
	calls int
	input c.AccessRecoveryInput
}

// ListAccessTasks supplies an empty bounded page for transport assertions.
func (*adminRecoveryStub) ListAccessTasks(context.Context, uint, uint, int) ([]c.AccessTask, error) {
	return []c.AccessTask{}, nil
}

// ListAccessReviews supplies an empty page without invitation material.
func (*adminRecoveryStub) ListAccessReviews(context.Context, uint, uint, int) ([]c.AccessReview, error) {
	return []c.AccessReview{}, nil
}

// RecoverAccess captures only authenticated actor and parsed input for the test.
func (stub *adminRecoveryStub) RecoverAccess(_ context.Context, _, _, actor uint, input c.AccessRecoveryInput) error {
	stub.actor = actor
	stub.calls++
	stub.input = input
	return nil
}

// TestAdminAccessTransport checks authentication, bounded pagination and unforgeable administrator identity.
func TestAdminAccessTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, authenticated := range []bool{false, true} {
		stub := &adminRecoveryStub{}
		router := gin.New()
		group := router.Group("/api/v1/admin")
		if authenticated {
			group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
		}
		RegisterAdminRoutes(group, NewAdminHandler(stub))
		request := httptest.NewRequest("POST", "/api/v1/admin/telegram-bot/membership/access/tasks/3/recover?bot_id=9", strings.NewReader(`{"actor_admin_id":99,"action":"reconcile","expected_version":"7","expected_route_version":"2","reason":"operator_verified"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if authenticated && (stub.calls != 1 || stub.actor != 42 || stub.input.ExpectedVersion != 7) {
			t.Fatalf("actor/version lost: %+v", stub)
		}
		if !authenticated && stub.calls != 0 {
			t.Fatal("anonymous recovery reached service")
		}
		for _, query := range []string{"bot_id=0", "bot_id=9&limit=101", "bot_id=9&cursor=-1"} {
			recorder = httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/admin/telegram-bot/membership/access/tasks?"+query, nil))
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing privacy header")
			}
		}
	}
}
