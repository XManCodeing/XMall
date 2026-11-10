package http

import (
	"context"
	"errors"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type topicDeletionHTTP struct {
	c.AdminRecoveryStore
	c.Topics
	denied bool
	state  string
	err    error
	calls  int
}

// TopicOperation returns a lossless physical snapshot owned by bot 2.
func (fixture *topicDeletionHTTP) TopicOperation(context.Context, uint) (c.AdminTopicOperation, error) {
	return c.AdminTopicOperation{ID: 1, BotID: 2, Version: 3, OperationVersion: "3", State: fixture.state, Mapping: &c.AdminTopicMapping{ID: 4, Version: 5, ChatID: -9007199254740993, ThreadID: 9007199254740993, RouteVersion: 6, State: "open"}}, nil
}

// AuthorizeSupportResolution must not substitute for the exact deletion grant.
func (fixture *topicDeletionHTTP) AuthorizeSupportResolution(context.Context, uint, uint) error {
	return c.ErrDenied
}

// AuthorizeSupportTopicDeletion checks the backend actor independently of GET.
func (fixture *topicDeletionHTTP) AuthorizeSupportTopicDeletion(_ context.Context, actor, bot uint) error {
	if fixture.denied || actor != 9 || bot != 2 {
		return c.ErrDenied
	}
	return nil
}

// ConfirmTopicDeleted verifies trusted route/generation and preserves large IDs.
func (fixture *topicDeletionHTTP) ConfirmTopicDeleted(_ context.Context, bot, topic uint, in c.TopicDeletionConfirmation, _ time.Time) error {
	if bot != 2 || topic != 4 || in.OperationID != 1 || in.OperationVersion != 3 || in.ActorID != 9 || in.Generation != 7 || in.ChatID != -9007199254740993 || in.ThreadID != 9007199254740993 {
		return c.ErrDenied
	}
	fixture.calls++
	return fixture.err
}

// TestTopicDeletionHTTP exercises real application authorization/DTO/error paths.
func TestTopicDeletionHTTP(t *testing.T) {
	valid := `{"expected_version":"3","mapping_id":"4","mapping_version":"5","chat_id":"-9007199254740993","thread_id":"9007199254740993","evidence":"ticket:123","confirmed":true}`
	for _, test := range []struct {
		name, body, state string
		actor, denied     bool
		err               error
		status            int
	}{
		{name: "success", body: valid, actor: true, status: 200},
		{name: "not admin", body: valid, status: 403},
		{name: "read only", body: valid, actor: true, denied: true, status: 403},
		{name: "unknown", body: valid, actor: true, state: "unknown", status: 409},
		{name: "creating", body: valid, actor: true, state: "creating", status: 409},
		{name: "stale", body: strings.Replace(valid, `"5"`, `"6"`, 1), actor: true, status: 409},
		{name: "numeric thread", body: strings.Replace(valid, `"thread_id":"9007199254740993"`, `"thread_id":9007199254740993`, 1), actor: true, status: 400},
		{name: "overflow", body: strings.Replace(valid, `"thread_id":"9007199254740993"`, `"thread_id":"9223372036854775808"`, 1), actor: true, status: 400},
		{name: "evidence", body: strings.Replace(valid, "ticket:123", "https://secret", 1), actor: true, status: 400},
		{name: "no consent", body: strings.Replace(valid, "true", "false", 1), actor: true, status: 400},
		{name: "actor injected", body: strings.TrimSuffix(valid, "}") + `,"actor_id":9}`, actor: true, status: 400},
		{name: "extra json", body: valid + `{}`, actor: true, status: 400},
		{name: "CAS", body: valid, actor: true, err: c.ErrConflict, status: 409},
		{name: "storage", body: valid, actor: true, err: errors.New("secret storage"), status: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &topicDeletionHTTP{state: test.state, denied: test.denied, err: test.err}
			if fixture.state == "" {
				fixture.state = "succeeded"
			}
			service := a.NewAdmin(testAdminStore{}, nil, func(context.Context, uint, string, bool) error { return nil }, nil).WithRecovery(fixture, &a.Resolver{Topics: fixture, Authorizer: fixture}, func(context.Context, uint) (uint64, error) { return 7, nil })
			router := gin.New()
			group := router.Group("/admin")
			group.Use(func(ctx *gin.Context) {
				if test.actor {
					ctx.Set("admin_id", uint(9))
				}
				ctx.Set("user_id", uint(9))
				ctx.Next()
			})
			RegisterRoutes(group, New(service))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", "/admin/telegram-bot/support/topic-operations/1/confirm-deleted", strings.NewReader(test.body)))
			if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "secret storage") {
				t.Fatal(response.Code, response.Body.String())
			}
			if test.status == 200 && fixture.calls != 1 {
				t.Fatal("missing write")
			}
			if test.status == 400 || (test.status == 403) || test.state != "" {
				if fixture.calls != 0 {
					t.Fatal("unauthorized write")
				}
			}
		})
	}
}
