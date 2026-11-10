package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"github.com/gin-gonic/gin"
)

// enablePreviewProbe returns read-only evidence for the requested bot/chat.
// Empty failure means success; title is deliberately independent of submitted names.
type enablePreviewProbe struct {
	title   string
	failure string
}

// Instance returns a running fixture identity for the requested bot.
func (enablePreviewProbe) Instance(context.Context, uint) (c.Identity, error) {
	return c.Identity{Running: true}, nil
}

// Check returns the configured Telegram title, capabilities and error class.
func (probe enablePreviewProbe) Check(context.Context, uint, int64) c.ProbeResult {
	return c.ProbeResult{ErrorClass: probe.failure, Observation: d.Observation{ChatType: "supergroup", Title: probe.title, IsMember: true, MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true}}
}

// enableTitleStore records writes for HTTP contract tests without persistence dependencies.
type enableTitleStore struct {
	c.Store
	target d.Target
	writes int
}

// Save captures target and assigns its ID for the detail response.
func (store *enableTitleStore) Save(_ context.Context, target *d.Target, _, _ uint64) error {
	store.writes++
	target.ID = 1
	store.target = *target
	return nil
}

// SaveChecked captures enabled saves through the same test store.
func (store *enableTitleStore) SaveChecked(ctx context.Context, target *d.Target, version, route, _ uint64) error {
	return store.Save(ctx, target, version, route)
}

// Get returns the last saved target for the response.
func (store *enableTitleStore) Get(context.Context, uint) (*d.Target, error) {
	return &store.target, nil
}

// History returns empty histories for the isolated HTTP contract.
func (*enableTitleStore) History(context.Context, uint) ([]d.Route, []d.Operation, error) {
	return nil, nil, nil
}

// TestEnablePreviewNeverWrites uses a nil store: accidental persistence would panic.
// The dedicated route accepts absent names and ignores forged names without registering.
func TestEnablePreviewNeverWrites(t *testing.T) {
	for _, name := range []string{"", `"name":"Forged",`} {
		response := groupTitleRequest(t, nil, enablePreviewProbe{title: "Telegram群名"}, "/telegram-bot/groups/enable-preview", `{`+name+`"purpose":"membership","bot_instance_id":1,"telegram_chat_id":"-100123","enabled":true}`)
		var payload struct {
			Data c.View `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != 200 || !payload.Data.Enabled || payload.Data.Name != "Telegram群名" {
			t.Fatalf("preview failed: %s, %v", response.Body.String(), err)
		}
	}
}

// TestCreateGroupTitleHTTP verifies optional/forged names and disabled/enabled
// create responses all use Telegram evidence, without changing transport error classes.
func TestCreateGroupTitleHTTP(t *testing.T) {
	for _, name := range []string{"", `"name":"Forged",`} {
		for _, enabled := range []string{"false", "true"} {
			store := &enableTitleStore{}
			response := groupTitleRequest(t, store, enablePreviewProbe{title: "Telegram群名"}, "/telegram-bot/groups", `{`+name+`"purpose":"membership","bot_instance_id":1,"telegram_chat_id":"-100123","enabled":`+enabled+`}`)
			var payload struct {
				Data c.Detail `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != 200 || payload.Data.Name != "Telegram群名" || store.writes != 1 || payload.Data.Enabled != (enabled == "true") {
				t.Fatalf("create failed: %s, %v", response.Body.String(), err)
			}
		}
	}
}

// TestCreateGroupTitleErrorsHTTP checks failed/empty/oversized titles reject both
// write and preview routes with existing public errors, never persisting fake names.
func TestCreateGroupTitleErrorsHTTP(t *testing.T) {
	for _, test := range []struct {
		probe enablePreviewProbe
		code  int
		class string
	}{
		{enablePreviewProbe{title: "Ignored", failure: "network"}, 422, "chat_id_unresolved"},
		{enablePreviewProbe{}, 422, "chat_id_unresolved"},
		{enablePreviewProbe{title: strings.Repeat("群", 121)}, 400, "invalid"},
	} {
		for _, path := range []string{"/telegram-bot/groups", "/telegram-bot/groups/enable-preview"} {
			store := &enableTitleStore{}
			response := groupTitleRequest(t, store, test.probe, path, `{"name":"Forged","purpose":"membership","bot_instance_id":1,"telegram_chat_id":"-100123","enabled":true}`)
			var payload struct {
				Class string `json:"error_class"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != test.code || payload.Class != test.class || store.writes != 0 {
				t.Fatalf("error contract mismatch: %s, %v; writes=%d", response.Body.String(), err, store.writes)
			}
		}
	}
}

// groupTitleRequest registers authenticated routes and returns a POST response for
// body; store and probe are explicit to verify read-only behavior and failure mapping.
func groupTitleRequest(t *testing.T, store c.Store, probe c.Probe, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	RegisterRoutes(router, New(a.New(store, probe)))
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
