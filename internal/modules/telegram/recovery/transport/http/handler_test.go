package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/gin-gonic/gin"
)

// cancelStore records exact authenticated actor/version passed through the boundary.
type cancelStore struct {
	c.Store
	input c.Cancellation
}

// Get returns the non-identity fixture for routing through the unchanged generic cancellation.
func (store *cancelStore) Get(_ context.Context, id uint) (*c.Detail, error) {
	return &c.Detail{Task: d.Task{ID: id, Kind: d.GroupReplace}}, nil
}

// Cancel returns a synthetic cancelled task without allowing any actual execution.
func (store *cancelStore) Cancel(_ context.Context, input c.Cancellation) (*d.Task, error) {
	store.input = input
	return &d.Task{ID: input.TaskID, Version: input.ExpectedVersion + 1, State: d.Cancelled}, nil
}

// TestCancelUsesAuthenticatedActorAndStringVersion verifies precision and rejects body actor spoofing.
func TestCancelUsesAuthenticatedActorAndStringVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &cancelStore{}
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	RegisterRoutes(router, New(&a.Service{Store: store}))
	request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/cancel", strings.NewReader(`{"expected_version":"9007199254740993","actor_id":99}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != 200 || store.input.ActorID != 7 || store.input.TaskID != 5 || store.input.ExpectedVersion != 9007199254740993 {
		t.Fatalf("input=%+v body=%s", store.input, result.Body.String())
	}
	if result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing privacy header")
	}
}

// TestRecoveryRoutesDoNotExposeExecution checks that preview work cannot accidentally enable migrations.
func TestRecoveryRoutesDoNotExposeExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router, New(nil))
	for _, path := range []string{"/telegram-bot/recovery/tasks/1/confirm", "/telegram-bot/recovery/tasks/1/execute", "/telegram-bot/recovery/tasks/1/complete"} {
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(http.MethodPost, path, nil))
		if result.Code != 404 {
			t.Fatalf("unexpected execution endpoint: %s", path)
		}
	}
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/telegram-bot/recovery/tasks", nil))
	var body struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || body.StatusCode != 401 {
		t.Fatalf("missing authentication: %s %v", result.Body.String(), err)
	}
}
