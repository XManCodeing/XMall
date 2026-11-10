package botapi_test

import (
	"context"
	"encoding/json"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/botapi"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestRecoveryProbeNeverStartsGeneration verifies the production HTTP adapter under a zero-generation fence.
// The synthetic server queries the same MaxOpenConns=1 database to detect HTTP inside transactions.
func TestRecoveryProbeNeverStartsGeneration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&r.Instance{}, &r.GenerationExit{}); err != nil {
		t.Fatal(err)
	}
	instance := r.Instance{ID: 2, TelegramID: 123, Mode: "internal", Version: 4, State: "stopped", TokenCipher: "cipher"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	store := rs.New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fence := rc.RecoveryFence{BotID: 2, TaskID: 77, Version: 4}
	if err := store.BlockRecovery(ctx, fence); err != nil {
		t.Fatal(err)
	}
	var anonymous atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		row, err := store.Get(request.Context(), 2)
		if err != nil || row.Enabled || row.Generation != 0 || row.RecoveryTaskID != 77 {
			t.Error("fence escaped", err)
		}
		payload := `{"ok":true,"result":{"id":123,"is_bot":true}}`
		switch request.URL.Path {
		case "/bot123:synthetic/getMe":
		case "/bot123:synthetic/getChat":
			payload = `{"ok":true,"result":{"id":-1001,"type":"supergroup","is_forum":true}}`
		case "/bot123:synthetic/getChatMember":
			if anonymous.Load() {
				payload = `{"ok":true,"result":{"user":{"id":123},"status":"administrator","is_anonymous":true,"can_manage_topics":true,"can_invite_users":true,"can_restrict_members":true}}`
			} else {
				payload = `{"ok":true,"result":{"user":{"id":123},"status":"administrator","can_manage_topics":true,"can_invite_users":true,"can_restrict_members":true}}`
			}
		default:
			t.Error("unexpected side effect", request.URL.Path)
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	adapter := &botapi.Adapter{Store: store, Cipher: cipher{}, Options: []telegramapi.Option{telegramapi.WithBaseURL(server.URL)}}
	observation, err := adapter.ProbeRecovery(ctx, gc.RecoveryProbeInput{TaskID: 77, TargetID: 1, BotID: 2, Version: 4, TelegramID: 123, ChatID: -1001, RouteVersion: 5})
	if err != nil || !observation.CanInviteUsers || observation.Generation != 0 {
		t.Fatal(observation, err)
	}
	anonymous.Store(true)
	observation, err = adapter.ProbeRecovery(ctx, gc.RecoveryProbeInput{TaskID: 77, TargetID: 1, BotID: 2, Version: 4, TelegramID: 123, ChatID: -1001, RouteVersion: 5})
	if err != nil || observation.ErrorClass != "anonymous_admin" || observation.State != "missing_permissions" || observation.LastSuccessAt != nil {
		t.Fatal("recovery accepted anonymous administrator", observation, err)
	}
}

type cipher struct{}

func (cipher) Open(string) (string, error) { return "123:synthetic", nil }
func TestReadOnlyProbeAndGenerationFence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "probe.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	if err = db.AutoMigrate(&r.Instance{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	i := r.Instance{TelegramID: 123, Mode: "internal", Enabled: true, State: "running", LastSeenAt: &now, Generation: 1, TokenCipher: "cipher"}
	if err = db.Create(&i).Error; err != nil {
		t.Fatal(err)
	}
	mode := "ok"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		var count int64
		if err := db.Model(&r.Instance{}).Count(&count).Error; err != nil {
			t.Error(err)
		}
		if mode == "rate" {
			w.WriteHeader(429)
			w.Write([]byte(`{"ok":false,"error_code":429,"parameters":{"retry_after":40}}`))
			return
		}
		if mode == "chat_not_found" || (mode == "member_not_found" && req.URL.Path == "/bot123:synthetic/getChatMember") {
			if _, err := w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)); err != nil {
				t.Error(err)
			}
			return
		}
		if req.URL.Path == "/bot123:synthetic/getChat" {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": -1001, "type": "supergroup", "is_forum": true}})
			return
		}
		if req.URL.Path != "/bot123:synthetic/getChatMember" {
			t.Error("unexpected side effect", req.URL.Path)
		}
		if mode == "generation" {
			db.Model(&r.Instance{}).Where("id = ?", i.ID).Update("generation", 2)
		}
		memberID, status := 123, "administrator"
		if mode == "wrong_identity" {
			memberID = 999
		}
		if mode == "not_admin" {
			status = "member"
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"user": map[string]int{"id": memberID}, "status": status, "can_manage_topics": true,
			"is_anonymous": mode == "anonymous" || mode == "wrong_identity" || mode == "not_admin" || mode == "generation",
		}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	adapter := &botapi.Adapter{Store: rs.New(db), Cipher: cipher{}, Options: []telegramapi.Option{telegramapi.WithBaseURL(server.URL)}}
	result := adapter.Check(context.Background(), i.ID, -1001)
	if result.ErrorClass != "" || !result.Observation.CanManageTopics || calls != 2 {
		t.Fatal(result, calls)
	}
	for _, test := range []struct{ mode, class string }{
		{"anonymous", "anonymous_admin"}, {"ok", ""}, {"not_admin", ""},
		{"wrong_identity", "invalid_response"}, {"chat_not_found", "resource_inaccessible"}, {"member_not_found", "resource_inaccessible"},
	} {
		mode = test.mode
		result = adapter.Check(context.Background(), i.ID, -1001)
		if result.ErrorClass != test.class {
			t.Fatalf("%s: %+v", mode, result)
		}
		if mode == "anonymous" && (result.Observation.MemberStatus != "administrator" || result.Observation.BotTelegramID != 123) {
			t.Fatal("anonymous self evidence lost", result)
		}
	}
	mode = "rate"
	result = adapter.Check(context.Background(), i.ID, -1001)
	if result.ErrorClass != "rate_limited" || result.RetryAfter != 40 {
		t.Fatal(result)
	}
	mode = "generation"
	result = adapter.Check(context.Background(), i.ID, -1001)
	if result.ErrorClass != "generation_changed" {
		t.Fatal(result)
	}
}
