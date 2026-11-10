package membershiphttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/gin-gonic/gin"
)

// attentionStub implements optional read ports and records calls without accessing a database.
type attentionStub struct {
	adminRecoveryStub
	summaries int
	lists     int
	status    string
	orderNo   string
	keyword   string
}

// AccessAttention returns a synthetic global count and records invocation.
func (stub *attentionStub) AccessAttention(context.Context) (c.AttentionSummary, error) {
	stub.summaries++
	return c.AttentionSummary{AttentionCount: 123}, nil
}

// ReviewAttention delegates to the synthetic summary counter.
func (stub *attentionStub) ReviewAttention(ctx context.Context) (c.AttentionSummary, error) {
	return stub.AccessAttention(ctx)
}

// LifecycleAttention delegates to the synthetic summary counter.
func (stub *attentionStub) LifecycleAttention(ctx context.Context) (c.AttentionSummary, error) {
	return stub.AccessAttention(ctx)
}

// ListAccessTasks records bounded list calls and returns no operations.
func (stub *attentionStub) ListAccessTasks(context.Context, uint, uint, int) ([]c.AccessTask, error) {
	stub.lists++
	return []c.AccessTask{}, nil
}

// ListAccessReviews records bounded list calls and returns no reviews.
func (stub *attentionStub) ListAccessReviews(context.Context, uint, uint, int) ([]c.AccessReview, error) {
	stub.lists++
	return []c.AccessReview{}, nil
}

// Tasks records lifecycle list calls without executing workers.
func (stub *attentionStub) Tasks(context.Context, uint, uint, int) ([]c.LifecycleTask, error) {
	stub.lists++
	return []c.LifecycleTask{}, nil
}

// Recover satisfies the existing lifecycle admin port without side effects.
func (*attentionStub) Recover(context.Context, c.LifecycleRecovery) error { return nil }

// AccessTasksPage records numbered transport requests without emulating a cursor walk.
func (stub *attentionStub) AccessTasksPage(_ context.Context, bot uint, page, size int, orderNo ...string) ([]c.AccessTask, int64, error) {
	stub.orderNo, _ = c.AccessOrderFilter(orderNo...)
	stub.lists++
	if bot != 9 || page != 3 || size != 10 {
		return nil, 0, c.ErrInvalid
	}
	return []c.AccessTask{}, 23, nil
}

// AccessReviewsPage reuses the bounded stub assertion for review routing.
func (stub *attentionStub) AccessReviewsPage(ctx context.Context, bot uint, page, size int) ([]c.AccessReview, int64, error) {
	_, total, err := stub.AccessTasksPage(ctx, bot, page, size)
	return []c.AccessReview{}, total, err
}

// SearchAccessReviewsPage captures a normalized keyword and returns a projected username for transport tests.
func (stub *attentionStub) SearchAccessReviewsPage(ctx context.Context, bot uint, page, size int, keyword string, status ...string) ([]c.AccessReview, int64, error) {
	stub.status, _ = c.AccessReviewFilter(status...)
	stub.keyword = keyword
	_, total, err := stub.AccessTasksPage(ctx, bot, page, size)
	return []c.AccessReview{{TelegramID: 9007199254740993, TelegramUsername: "Seven_Time"}}, total, err
}

// TestReviewSearchTransport verifies keyword scope, serialization and rejection before repository calls.
func TestReviewSearchTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &attentionStub{}
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
	RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
	base := "/admin/telegram-bot/membership/access/reviews?bot_id=9&page=3&page_size=10"
	for _, keyword := range []string{" @SEVEN_Time ", "9007199254740993", "@123"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"&keyword="+url.QueryEscape(keyword), nil))
		want, _, _ := c.LifecycleKeyword(keyword)
		if recorder.Code != 200 || stub.keyword != want || !strings.Contains(recorder.Body.String(), `"telegram_username":"Seven_Time"`) {
			t.Fatalf("search lost: %s", recorder.Body.String())
		}
	}
	for _, status := range []string{"pending", "reviewed", "all"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"&status="+status+"&keyword=seven", nil))
		if recorder.Code != 200 || stub.status != status || stub.keyword != "@seven" {
			t.Fatalf("review status lost: %s", recorder.Body.String())
		}
	}
	for _, suffix := range []string{"&status=unknown", "&status=pending&status=reviewed", "&status=", "&status=pending&cursor=1", "&keyword=%25", "&keyword=@", "&keyword=one&keyword=two", "&keyword=one&cursor=1", "&keyword=one&limit=10", "&keyword=one&summary=1"} {
		before := stub.lists
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", base+suffix, nil))
		if stub.lists != before {
			t.Fatalf("invalid search reached reader: %s", suffix)
		}
	}
}

// LifecycleTasksPage reuses the bounded stub assertion for lifecycle routing.
func (stub *attentionStub) LifecycleTasksPage(ctx context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleTask, int64, error) {
	if len(status) == 1 {
		stub.status = status[0]
	}
	_, total, err := stub.AccessTasksPage(ctx, bot, page, size)
	return []c.LifecycleTask{}, total, err
}

// TestLifecycleStatusTransport checks filtered pagination and rejects ambiguous/unscoped filters.
func TestLifecycleStatusTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &attentionStub{}
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
	RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
	base := "/admin/telegram-bot/membership/lifecycle/tasks?bot_id=9&page=3&page_size=10"
	for _, suffix := range []string{"&status=unknown", "&status=completed&status=processing", "&status=completed&cursor=1", "&status=completed&summary=1"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", base+suffix, nil))
		if stub.lists != 0 || stub.summaries != 0 {
			t.Fatalf("invalid filter reached reader: %s", suffix)
		}
	}
	for _, status := range []string{"", "processing", "needs_review", "completed"} {
		before := stub.lists
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"&status="+status, nil))
		if stub.lists != before+1 || stub.status != status || recorder.Code != 200 {
			t.Fatalf("filter not forwarded: %s %s", status, recorder.Body.String())
		}
	}
}

// TestNumberedPageTransport preserves summary/cursor contracts and rejects mixed pagination.
func TestNumberedPageTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"access/tasks", "access/reviews", "lifecycle/tasks"} {
		stub := &attentionStub{}
		router := gin.New()
		group := router.Group("/admin")
		group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
		RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
		base := "/admin/telegram-bot/membership/" + path
		for _, query := range []string{"bot_id=9&page=0", "bot_id=9&page=3&page_size=101", "bot_id=9&page=3&page_size=10&cursor=0", "bot_id=9&page=3&limit=10", "page=3", "bot_id=9&page=3&page=4", "summary=1&page=3", "bot_id=9&page=999999999999999999999999"} {
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", base+"?"+query, nil))
			if stub.lists != 0 || stub.summaries != 0 {
				t.Fatalf("invalid query reached reader: %s", query)
			}
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"?bot_id=9&page=3&page_size=10", nil))
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if stub.lists != 1 || body.Data["total"] != float64(23) || body.Data["page"] != float64(3) || body.Data["page_size"] != float64(10) || body.Data["total_page"] != float64(3) || body.Data["next_cursor"] != nil {
			t.Fatalf("invalid page response: %s", recorder.Body.String())
		}
	}
}

// TestAttentionTransport validates exact numeric DTOs, strict summary parameters and unchanged lists.
func TestAttentionTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"access/tasks", "access/reviews", "lifecycle/tasks"} {
		t.Run(path, func(t *testing.T) {
			stub := &attentionStub{}
			router := gin.New()
			group := router.Group("/admin")
			group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
			RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
			base := "/admin/telegram-bot/membership/" + path
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"?summary=1", nil))
			var result struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if stub.summaries != 1 || len(result.Data) != 1 || result.Data["attention_count"] != float64(123) || recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("invalid summary: %s", recorder.Body.String())
			}
			for _, query := range []string{"summary=0", "summary=true", "summary=", "summary=01", "summary=1&summary=1", "summary=1&bot_id=9", "summary=1&cursor=-1", "summary=1&limit=20", "summary=1&other=x", "summary=1&bad=%zz", "summary=1;bot_id=9", "", "bot_id=0", "bot_id=9&limit=101"} {
				recorder = httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest("GET", base+"?"+query, nil))
				if stub.summaries != 1 || stub.lists != 0 {
					t.Fatalf("invalid query reached service: %s", query)
				}
			}
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", base+"?bot_id=9&limit=20&cursor=0", nil))
			if stub.lists != 1 {
				t.Fatal("existing list mode changed")
			}
		})
	}
}

// TestAttentionUsesExistingAuthorization proves that neither missing identity nor denied
// route middleware can reach the optional global reader; no new summary route is registered.
func TestAttentionUsesExistingAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, deny := range []bool{false, true} {
		stub := &attentionStub{}
		router := gin.New()
		group := router.Group("/admin")
		if deny {
			group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.AbortWithStatus(403) })
		}
		RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
		if len(router.Routes()) != 7 {
			t.Fatal("summary must not add routes or permissions")
		}
		for _, path := range []string{"access/tasks", "access/reviews", "lifecycle/tasks"} {
			for _, query := range []string{"summary=1", "bot_id=9&page=3&page_size=10"} {
				router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin/telegram-bot/membership/"+path+"?"+query, nil))
			}
		}
		if stub.summaries != 0 || stub.lists != 0 {
			t.Fatal("unauthorized summary reached service")
		}
	}
}
