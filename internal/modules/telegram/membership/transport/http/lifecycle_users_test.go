package membershiphttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/gin-gonic/gin"
)

// lifecycleUsersStub records grouped requests separately from legacy readers.
type lifecycleUsersStub struct {
	attentionStub
	grouped int
	keyword string
}

// LifecycleUsersPage validates bot/page/size and returns retry evidence plus a nullable timestamp.
func (stub *lifecycleUsersStub) LifecycleUsersPage(_ context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleUserTasks, int64, error) {
	stub.grouped++
	if bot != 9 || page != 3 || size != 10 {
		return nil, 0, c.ErrInvalid
	}
	if len(status) == 1 {
		stub.status = status[0]
	}
	at := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	return []c.LifecycleUserTasks{{TelegramID: 9007199254740993, Tasks: []c.LifecycleTask{
		{ID: 21, TelegramID: 9007199254740993, Attempt: 4, LastAttemptAt: &at, UpdatedAt: at},
		{ID: 22, TelegramID: 9007199254740993, UpdatedAt: at},
	}}}, 23, nil
}

// SearchLifecycleUsersPage captures normalized keyword while reusing scoped pagination assertions.
// It returns a projected Telegram username with the original complete task envelope.
func (stub *lifecycleUsersStub) SearchLifecycleUsersPage(ctx context.Context, bot uint, page, size int, status, keyword string) ([]c.LifecycleUserTasks, int64, error) {
	stub.keyword = keyword
	rows, total, err := stub.LifecycleUsersPage(ctx, bot, page, size, status)
	if err == nil {
		rows[0].TelegramUsername = "Seven_Time"
	}
	return rows, total, err
}

// TestLifecycleKeywordTransport checks search normalization, scope and username JSON serialization.
func TestLifecycleKeywordTransport(t *testing.T) {
	for _, value := range []string{" @SEVEN_Time ", "9007199254740993", "@123"} {
		stub := &lifecycleUsersStub{}
		recorder := httptest.NewRecorder()
		lifecycleUsersRouter(stub).ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/telegram-bot/membership/lifecycle/tasks?bot_id=9&page=3&page_size=10&group_by=telegram_id&status=needs_review&keyword="+url.QueryEscape(value), nil))
		normalized, _, err := c.LifecycleKeyword(value)
		var result struct {
			Data struct {
				Items []struct {
					Username string `json:"telegram_username"`
				} `json:"items"`
			} `json:"data"`
		}
		if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if err != nil || stub.keyword != normalized || stub.status != "needs_review" || len(result.Data.Items) != 1 || result.Data.Items[0].Username != "Seven_Time" {
			t.Fatalf("search contract: %s", recorder.Body.String())
		}
	}
}

// lifecycleUsersRouter returns the existing authenticated routes with an optional grouped stub.
func lifecycleUsersRouter(stub *lifecycleUsersStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(42)); ctx.Next() })
	RegisterAdminRoutes(group, NewAdminHandler(stub).WithDisposition(nil, stub))
	return router
}

// TestLifecycleUsersTransport verifies grouped envelope, exact string identities and all retry fields.
func TestLifecycleUsersTransport(t *testing.T) {
	stub := &lifecycleUsersStub{}
	router := lifecycleUsersRouter(stub)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/telegram-bot/membership/lifecycle/tasks?bot_id=9&page=3&page_size=10&group_by=telegram_id&status=completed", nil))
	var result struct {
		Data struct {
			Items []struct {
				TelegramID string           `json:"telegram_id"`
				Tasks      []map[string]any `json:"tasks"`
			} `json:"items"`
			Total     int64 `json:"total"`
			TotalPage int64 `json:"total_page"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if stub.grouped != 1 || stub.lists != 0 || stub.status != "completed" || result.Data.Total != 23 || result.Data.TotalPage != 3 || len(result.Data.Items) != 1 {
		t.Fatalf("wrong grouped response: %s", recorder.Body.String())
	}
	item := result.Data.Items[0]
	if item.TelegramID != "9007199254740993" || len(item.Tasks) != 2 || item.Tasks[0]["attempt"] != float64(4) || item.Tasks[0]["last_attempt_at"] != "2026-11-05T00:00:00Z" || item.Tasks[0]["updated_at"] != "2026-11-05T00:00:00Z" {
		t.Fatalf("lost fields: %+v", item)
	}
	if timestamp, exists := item.Tasks[1]["last_attempt_at"]; !exists || timestamp != nil {
		t.Fatal("absent attempt must be explicit JSON null")
	}
}

// TestLifecycleUsersInvalidTransport rejects invalid modes before any summary, cursor or page read.
func TestLifecycleUsersInvalidTransport(t *testing.T) {
	stub := &lifecycleUsersStub{}
	router := lifecycleUsersRouter(stub)
	queries := []string{
		"bot_id=9&group_by=", "bot_id=9&group_by=chat_id",
		"bot_id=9&group_by=telegram_id&group_by=telegram_id",
		"group_by=telegram_id", "bot_id=0&group_by=telegram_id",
		"bot_id=9&bot_id=10&group_by=telegram_id",
		"bot_id=9&group_by=telegram_id&summary=1",
		"bot_id=9&group_by=telegram_id&cursor=0",
		"bot_id=9&group_by=telegram_id&limit=20",
		"bot_id=9&group_by=telegram_id&status=unknown",
		"bot_id=9&group_by=telegram_id&status=processing&status=completed",
		"bot_id=9&group_by=telegram_id&page=0",
		"bot_id=9&group_by=telegram_id&page=1&page=2",
		"bot_id=9&group_by=telegram_id&page_size=101",
		"bot_id=9&group_by=telegram_id&order_no=ORD",
		"bot_id=9&keyword=alice",
		"bot_id=9&group_by=telegram_id&keyword=alice&keyword=bob",
		"bot_id=9&group_by=telegram_id&keyword=%25",
		"bot_id=9&group_by=telegram_id&keyword=%40",
		"summary=1&keyword=alice",
		"bot_id=9&group_by=%ZZ",
	}
	for _, query := range queries {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/telegram-bot/membership/lifecycle/tasks?"+query, nil))
		var result struct {
			Code int `json:"status_code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if stub.grouped != 0 || stub.lists != 0 || stub.summaries != 0 || result.Code != 400 {
			t.Fatalf("invalid query reached reader: %s %s", query, recorder.Body.String())
		}
	}
}
