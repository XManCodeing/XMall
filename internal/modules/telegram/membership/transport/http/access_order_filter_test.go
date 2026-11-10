package membershiphttp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAccessOrderFilterTransport proves order_no is forwarded or explicitly rejected,
// never silently discarded by cursor, summary, review or lifecycle handlers.
func TestAccessOrderFilterTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &attentionStub{}
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
	RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
	base := "/admin/telegram-bot/membership/"
	invalid := []string{
		"access/tasks?bot_id=9&order_no=first&order_no=second",
		"access/tasks?bot_id=9&order_no=ORD&cursor=0",
		"access/tasks?bot_id=9&order_no=ORD&limit=10",
		"access/tasks?summary=1&order_no=ORD",
		"access/tasks?bot_id=9&order_no=space%20value",
		"access/tasks?bot_id=9&order_no=%00",
		"access/tasks?bot_id=9&order_no=%ZZ",
		"access/tasks?bot_id=9&order_no=" + strings.Repeat("a", 65),
		"access/tasks?order_no=ORD&page=1",
		"access/reviews?bot_id=9&order_no=ORD&page=1",
		"lifecycle/tasks?bot_id=9&order_no=ORD&page=1",
	}
	for _, query := range invalid {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+query, nil))
		if stub.lists != 0 || stub.summaries != 0 || !strings.Contains(recorder.Body.String(), `"status_code":400`) {
			t.Fatalf("invalid request not rejected: %s %s", query, recorder.Body.String())
		}
	}
	for _, orderNo := range []string{"USER-20261104", ""} {
		recorder := httptest.NewRecorder()
		before := stub.lists
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"access/tasks?bot_id=9&page=3&page_size=10&order_no="+orderNo, nil))
		if stub.lists != before+1 || stub.orderNo != orderNo || recorder.Code != 200 {
			t.Fatalf("filter not forwarded: %s", recorder.Body.String())
		}
	}
}
