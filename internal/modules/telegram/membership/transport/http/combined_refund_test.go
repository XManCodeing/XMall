package membershiphttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// combinedPreviewStub records read identity without invoking money or entitlement services.
type combinedPreviewStub struct {
	err            error
	actor, orderID uint
}

// Requests captures trusted parameters and returns the configured read failure.
func (stub *combinedPreviewStub) Requests(_ context.Context, actor, orderID uint) ([]order.MembershipRefundRequest, error) {
	stub.actor, stub.orderID = actor, orderID
	return []order.MembershipRefundRequest{}, stub.err
}

// Execute must never be reached by a preview GET.
func (*combinedPreviewStub) Execute(context.Context, order.MembershipRefundInput) (*order.MembershipRefundRequest, error) {
	panic("preview executed a refund")
}

// TestCombinedRefundRequiresOfflineConfirmation rejects unchecked manual refund before workflow execution.
func TestCombinedRefundRequiresOfflineConfirmation(t *testing.T) {
	router := gin.New()
	group := router.Group("/api/v1/admin", func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
	RegisterCombinedRefundRoutes(group, &combinedPreviewStub{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/admin/telegram-bot/membership/orders/7/refund-and-terminate", strings.NewReader(`{"method":"manual","amount":"10","item_ids":[8],"request_key":"test-request-key-123"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.StatusCode != response.CodeBadRequest {
		t.Fatal(body)
	}
}

// TestCombinedRefundPreviewErrors separates backend unavailability from actual authorization rejection.
func TestCombinedRefundPreviewErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fixture := range []struct {
		name string
		err  error
		code int
	}{
		{"success", nil, 0},
		{"permission", order.ErrRefundRequestDenied, response.CodeForbidden},
		{"database", errors.New("synthetic missing table"), response.CodeInternal},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			stub := &combinedPreviewStub{err: fixture.err}
			router := gin.New()
			group := router.Group("/api/v1/admin", func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
			RegisterCombinedRefundRoutes(group, stub)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/admin/telegram-bot/membership/orders/7/refund-and-terminate", nil))
			var body response.Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.StatusCode != fixture.code || stub.actor != 42 || stub.orderID != 7 || recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected preview: body=%+v stub=%+v", body, stub)
			}
		})
	}
}
