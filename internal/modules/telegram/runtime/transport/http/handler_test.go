package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/i18n"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/gin-gonic/gin"
)

type storeStub struct {
	c.Store
	instance  d.Instance
	createErr error
}

func (s *storeStub) List(context.Context) ([]d.Instance, error)     { return []d.Instance{s.instance}, nil }
func (s *storeStub) Get(context.Context, uint) (*d.Instance, error) { i := s.instance; return &i, nil }
func (s *storeStub) Create(context.Context, *d.Instance) error      { return s.createErr }

type identityAPI struct{ c.API }

func (identityAPI) Identity(context.Context, string) (c.BotIdentity, error) {
	return c.BotIdentity{ID: 123}, nil
}

func TestDuplicateCreationIsHTTP409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &storeStub{createErr: c.ErrDuplicate}
	router := gin.New()
	RegisterRoutes(router.Group("/admin"), New(app.NewManager(store, identityAPI{}, "test")))
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		t.Run(locale, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/telegram-bot/instances", bytes.NewBufferString(`{"token":"123:test-token"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept-Language", locale)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			var body struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusConflict || body.Message != i18n.T(locale, "error.telegram_bot_duplicate") {
				t.Fatalf("duplicate response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestInstancesNeverReturnStoredCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &storeStub{instance: d.Instance{ID: 1, TokenCipher: "private-ciphertext", Version: 2}}
	router := gin.New()
	RegisterRoutes(router.Group("/admin"), New(app.NewManager(store, nil, "test")))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/telegram-bot/instances", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-ciphertext") || strings.Contains(response.Body.String(), "token_cipher") {
		t.Fatalf("unsafe response: %s", response.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
}
func TestConfigurationConflictIsHTTP409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &storeStub{instance: d.Instance{ID: 1, Version: 2}}
	router := gin.New()
	RegisterRoutes(router.Group("/admin"), New(app.NewManager(store, nil, "test")))
	req := httptest.NewRequest(http.MethodPatch, "/admin/telegram-bot/instances/1", bytes.NewBufferString(`{"expected_version":1,"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 409 {
		t.Fatalf("expected conflict: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message != i18n.T("zh-CN", "error.telegram_bot_conflict") {
		t.Fatalf("unexpected conflict message: %s", body.Message)
	}
}
