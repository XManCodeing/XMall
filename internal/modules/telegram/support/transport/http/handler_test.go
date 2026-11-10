package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/i18n"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/gin-gonic/gin"
)

// TestResolutionErrorsTranslated ensures the new validation/conflict messages exist in all supported locales.
func TestResolutionErrorsTranslated(t *testing.T) {
	for _, key := range []string{"error.support_policy_invalid", "error.support_admin_invalid", "error.support_resolution_conflict"} {
		values := map[string]bool{}
		for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
			message := i18n.T(locale, key)
			if message == "" || message == key {
				t.Fatal(locale, key)
			}
			values[message] = true
		}
		if len(values) != 3 {
			t.Fatal("missing locale translation", key)
		}
	}
}

type settingsHTTPStore struct {
	c.PolicyStore
	writes int
}

func (s *settingsHTTPStore) Policy(context.Context, uint) (c.Policy, uint64, error) {
	return c.DefaultPolicy(), 0, nil
}
func (s *settingsHTTPStore) SavePolicy(context.Context, uint, uint, uint64, c.Policy, time.Time) (uint64, error) {
	s.writes++
	return 1, nil
}

// TestSettingsHTTP rejects gate/target injection and exposes only safe configuration metadata.
func TestSettingsHTTP(t *testing.T) {
	valid := `{"expected_version":0,"policy":{"max_messages":20,"max_bytes":524288,"max_attempts":5,"retention_seconds":86400,"challenge_ttl_seconds":600,"cooldown_seconds":300}}`
	for _, scenario := range []struct {
		method, body   string
		status, writes int
	}{
		{"GET", "", 200, 0}, {"PUT", valid, 200, 1},
		{"PUT", strings.TrimSuffix(valid, "}") + `,"enabled":true}`, 400, 0},
		{"PUT", strings.TrimSuffix(valid, "}") + `,"target_id":1}`, 400, 0},
		{"PUT", strings.Replace(valid, `"max_attempts":5`, `"max_attempts":0`, 1), 400, 0},
		{"PUT", strings.Replace(valid, `"expected_version":0,`, "", 1), 400, 0},
		{"PUT", valid + `{}`, 400, 0},
	} {
		store := &settingsHTTPStore{}
		service := a.NewAdmin(nil, nil, nil, nil).WithSettings(store, func(context.Context, uint, uint, string) error { return nil })
		router := gin.New()
		group := router.Group("/admin")
		group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(9)); ctx.Next() })
		RegisterRoutes(group, New(service))
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(scenario.method, "/admin/telegram-bot/support/settings/2", strings.NewReader(scenario.body)))
		if result.Code != scenario.status || store.writes != scenario.writes || result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(scenario, result.Body.String())
		}
		if result.Code == 200 && (!strings.Contains(result.Body.String(), `"production_ready":false`) || !strings.Contains(result.Body.String(), `"verification_mode":"click_required"`)) {
			t.Fatal(result.Body.String())
		}
	}
}

type testResolution struct {
	calls  int
	denied bool
	err    error
}

func (s *testResolution) Downstreams(context.Context, c.AdminFilter) ([]c.AdminDownstream, int64, error) {
	return []c.AdminDownstream{}, 0, s.err
}
func (s *testResolution) Downstream(context.Context, uint) (c.AdminDownstream, error) {
	return c.AdminDownstream{ID: 1, BotID: 2}, s.err
}
func (s *testResolution) AuthorizeSupportResolution(_ context.Context, actor, bot uint) error {
	if s.denied || actor != 9 || bot != 2 {
		return c.ErrDenied
	}
	return nil
}
func (s *testResolution) ResolveDownstream(_ context.Context, bot, id uint, generation uint64, in c.Resolution, _ time.Time) error {
	if generation != 7 || in.ActorID != 9 {
		return c.ErrDenied
	}
	s.calls++
	return s.err
}

// TestResolutionHTTP rejects all client-controlled trust fields and invokes the real resolver boundary.
func TestResolutionHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := `{"expected_version":1,"decision":"delivered","evidence":"ticket:123","delivery_id":4}`
	for _, scenario := range []struct {
		name, body    string
		actor, denied bool
		err           error
		status, calls int
	}{
		{name: "valid", body: valid, actor: true, status: 200, calls: 1},
		{name: "group member", body: valid, status: 403},
		{name: "no permission", body: valid, actor: true, denied: true, status: 403},
		{name: "conflict", body: valid, actor: true, err: c.ErrConflict, status: 409},
		{name: "missing", body: valid, actor: true, err: c.ErrAdminNotFound, status: 404},
		{name: "actor injection", body: strings.TrimSuffix(valid, "}") + `,"actor_id":9}`, actor: true, status: 400},
		{name: "generation injection", body: strings.TrimSuffix(valid, "}") + `,"generation":7}`, actor: true, status: 400},
		{name: "receipt injection", body: strings.TrimSuffix(valid, "}") + `,"message_ids":[123]}`, actor: true, status: 400},
		{name: "target injection", body: strings.TrimSuffix(valid, "}") + `,"bot_id":2}`, actor: true, status: 400},
		{name: "trailing body", body: valid + `{}`, actor: true, status: 400},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			resolver := &testResolution{denied: scenario.denied, err: scenario.err}
			service := a.NewAdmin(testAdminStore{}, nil, func(context.Context, uint, string, bool) error { return nil }, nil).WithDownstreams(resolver, &a.DownstreamResolver{Store: resolver, Authorizer: resolver}, func(context.Context, uint) (uint64, error) { return 7, nil })
			router := gin.New()
			group := router.Group("/admin")
			group.Use(func(ctx *gin.Context) {
				if scenario.actor {
					ctx.Set("admin_id", uint(9))
				}
				ctx.Set("user_id", uint(9))
				ctx.Next()
			})
			RegisterRoutes(group, New(service))
			request := httptest.NewRequest("POST", "/admin/telegram-bot/support/downstreams/1/resolve", strings.NewReader(scenario.body))
			request.Header.Set("X-Admin-ID", "9")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != scenario.status || result.Header().Get("Cache-Control") != "no-store" || resolver.calls != scenario.calls {
				t.Fatal(result.Code, result.Body.String(), resolver.calls)
			}
		})
	}
}

type testAdminStore struct{ err error }

func (s testAdminStore) Sessions(context.Context, c.AdminFilter) ([]c.AdminSession, int64, error) {
	return []c.AdminSession{}, 0, s.err
}
func (s testAdminStore) Session(context.Context, uint) (c.AdminSession, error) {
	return c.AdminSession{ID: 1}, s.err
}
func (s testAdminStore) Messages(context.Context, uint, c.AdminFilter) ([]c.AdminMessage, int64, error) {
	return []c.AdminMessage{}, 0, s.err
}
func (s testAdminStore) Payload(context.Context, uint, uint) (c.AdminPayload, error) {
	return c.AdminPayload{Cipher: "secret", RetainUntil: time.Now().Add(-time.Hour)}, s.err
}

type testAdminCipher struct{}

func (testAdminCipher) Seal(value string) (string, error) { return value, nil }
func (testAdminCipher) Open(string) (string, error) {
	return "", errors.New("must not decrypt expired")
}

// TestReadHandlers covers transport validation, denied users, cache headers and safe errors.
func TestReadHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name, path string
		actor      bool
		denied     bool
		err        error
		status     int
		contains   string
	}{
		{name: "empty", path: "/sessions", actor: true, status: 200, contains: `"items":[]`},
		{name: "bad page", path: "/sessions?page=no", actor: true, status: 400},
		{name: "operations", path: "/sessions?unreplied=true&blocked=false&has_topic=true&failure=upstream", actor: true, status: 200, contains: `"items":[]`},
		{name: "bad unreplied", path: "/sessions?unreplied=1", actor: true, status: 400},
		{name: "empty unreplied", path: "/sessions?unreplied=", actor: true, status: 400},
		{name: "duplicate blocked", path: "/sessions?blocked=true&blocked=false", actor: true, status: 400},
		{name: "bad blocked", path: "/sessions?blocked=invalid", actor: true, status: 400},
		{name: "bad topic", path: "/sessions?has_topic=yes", actor: true, status: 400},
		{name: "bad failure", path: "/sessions?failure=payload", actor: true, status: 400},
		{name: "bounded size", path: "/sessions?page_size=101", actor: true, status: 400},
		{name: "bad bot", path: "/sessions?bot_id=-1", actor: true, status: 400},
		{name: "group member", path: "/sessions", status: 403},
		{name: "denied", path: "/sessions/1", actor: true, denied: true, status: 403},
		{name: "bad id", path: "/sessions/0", actor: true, status: 400},
		{name: "missing", path: "/sessions/1", actor: true, err: c.ErrAdminNotFound, status: 404},
		{name: "empty messages", path: "/sessions/1/messages", actor: true, status: 200, contains: `"items":[]`},
		{name: "content denied", path: "/sessions/1/messages/2/content", actor: true, denied: true, status: 403},
		{name: "expired", path: "/sessions/1/messages/2/content", actor: true, status: 200, contains: `"reason":"expired"`},
		{name: "failure", path: "/sessions", actor: true, err: errors.New("secret database details"), status: 500},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service := a.NewAdmin(testAdminStore{err: scenario.err}, testAdminCipher{}, func(_ context.Context, _ uint, path string, exact bool) error {
				if strings.HasSuffix(path, "/content") && !exact {
					t.Fatal("missing exact permission")
				}
				if scenario.denied {
					return c.ErrDenied
				}
				return nil
			}, func(context.Context, uint, uint, uint, string) error { return nil })
			router := gin.New()
			group := router.Group("/admin")
			group.Use(func(ctx *gin.Context) {
				ctx.Set("user_id", uint(7))
				if scenario.actor {
					ctx.Set("admin_id", uint(1))
				}
				ctx.Next()
			})
			RegisterRoutes(group, New(service))
			request := httptest.NewRequest("GET", "/admin/telegram-bot/support"+scenario.path, nil)
			request.Header.Set("X-Admin-ID", "1")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != scenario.status || result.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(result.Code, result.Body.String(), result.Header())
			}
			if !strings.Contains(result.Body.String(), scenario.contains) || strings.Contains(result.Body.String(), "secret") {
				t.Fatal(result.Body.String())
			}
		})
	}
}
