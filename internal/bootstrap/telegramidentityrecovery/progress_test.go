package telegramidentityrecovery

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	mappings "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	transport "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/transport/http"
	"github.com/gin-gonic/gin"
)

// readProgress exercises the actual registered HTTP adapter and validates frontend string DTOs.
// task is server-owned; query identity fields deliberately attempt to widen the mapping scope.
func readProgress(t *testing.T, executor *a.IdentityExecutor, task *d.Task) []c.IdentityMapping {
	t.Helper()
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(3)); ctx.Next() })
	transport.RegisterRoutes(router, transport.New(&a.Service{Store: executor.Store, Identity: executor}))
	idJSON, err := json.Marshal(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("GET", "/telegram-bot/recovery/tasks/"+string(idJSON)+"?target_id=999&old_telegram_id=1", nil))
	if result.Code != 200 || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response %s", result.Body.String())
	}
	var body struct {
		Data struct {
			Mappings []c.IdentityMapping `json:"identity_mappings"`
			Task     struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			} `json:"task"`
		}
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Mappings == nil || body.Data.Task.ID == "" || body.Data.Task.Version == "" {
		t.Fatal("missing frontend DTO", result.Body.String())
	}
	for _, row := range body.Data.Mappings {
		if row.ID == "" || row.TargetID != "1" || row.OldTelegramID != "9007199254740993" || row.NewTelegramID != "9007199254740995" {
			t.Fatalf("unsafe mapping DTO %+v", row)
		}
	}
	if strings.Contains(result.Body.String(), "snapshot_hash") {
		t.Fatal("private column exposed")
	}
	return body.Data.Mappings
}

// TestIdentityProgressHTTPIsolationAndTruth assembles real HTTP/executor/DB on one connection.
// It verifies empty preflight, task isolation and actual mapping stages while parent stays recovering.
func TestIdentityProgressHTTPIsolationAndTruth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, executor, input := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestKey = "independent-preview"
	other, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	if rows := readProgress(t, executor, task); len(rows) != 0 {
		t.Fatal("preflight inferred mapping")
	}
	task, err = executor.Confirm(ctx, task.ID, c.Confirmation{ActorID: 3, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil {
		t.Fatal(err)
	}
	rows := readProgress(t, executor, task)
	if len(rows) != 1 || rows[0].State != "awaiting_member" {
		t.Fatal("missing persisted mapping", rows)
	}
	if rows := readProgress(t, executor, other); len(rows) != 0 {
		t.Fatal("another preview leaked committed task", rows)
	}
	observed := m.AccessObservation{TargetID: 1, BotID: 5, ChatID: -10001, RouteVersion: 11, TelegramID: 9007199254740995, Status: "member", IsMember: true, ObservedAt: time.Now().UTC().Add(time.Second), Source: "query"}
	if err := mappings.Advance(db, observed); err != nil {
		t.Fatal(err)
	}
	rows = readProgress(t, executor, task)
	if len(rows) != 1 || rows[0].State != "cleanup_pending" {
		t.Fatal("admission state not authoritative", rows)
	}
	observed.TelegramID = 9007199254740993
	observed.Status = "left"
	observed.IsMember = false
	observed.ObservedAt = observed.ObservedAt.Add(time.Second)
	if err := mappings.Advance(db, observed); err != nil {
		t.Fatal(err)
	}
	rows = readProgress(t, executor, task)
	if len(rows) != 1 || rows[0].State != "completed" {
		t.Fatal("cleanup state not authoritative", rows)
	}
	detail, err := executor.Store.Get(ctx, task.ID)
	if err != nil || detail.Task.State != d.Recovering {
		t.Fatal("GET mutated or inferred parent", err)
	}
}
