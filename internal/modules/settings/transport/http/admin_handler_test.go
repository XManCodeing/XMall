package settingshttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/XManCodeing/XMall/internal/constants"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

type unsupportedMenuStub struct{ TelegramBotAdminService }

func (unsupportedMenuStub) PatchTelegramBotConfigSection(string, int, json.RawMessage) (settingsmessaging.TelegramBotConfigSetting, error) {
	return settingsmessaging.TelegramBotConfigSetting{}, settingsapp.ErrTelegramBotMenuUnsupported
}

func TestTelegramMenuUnsupportedReturnsExplicitBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "section", Value: "menu"}}
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/admin/settings/telegram-bot/menu", strings.NewReader(`{"expected_version":0,"data":{"items":[]}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	NewTelegramBotHandler(unsupportedMenuStub{}).PatchTelegramBotConfigSection(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "support only builtin") {
		t.Fatalf("expected actionable 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

type adminSettingsStub struct {
	updateCalls int
}

func (s *adminSettingsStub) GetByKey(string) (jsonmap.JSON, error) {
	return nil, nil
}

func (s *adminSettingsStub) UpdateWithEffects(string, map[string]interface{}) (settingsapp.UpdateResult, error) {
	s.updateCalls++
	return settingsapp.UpdateResult{}, nil
}

func (s *adminSettingsStub) InvalidateCallbackRoutesCache() {}

func TestAdminHandlerRejectsGoogleAuthOnGenericUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &adminSettingsStub{}
	handler := NewAdminHandler(stub)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/v1/admin/settings",
		strings.NewReader(`{"key":"`+constants.SettingKeyGoogleAuthConfig+`","value":{"enabled":true,"client_id":""}}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")

	handler.Update(context)

	if stub.updateCalls != 0 {
		t.Fatalf("generic update persisted protected Google auth config")
	}
	var body struct {
		StatusCode int    `json:"status_code"`
		Msg        string `json:"msg"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	if recorder.Code != http.StatusOK || body.StatusCode != 400 {
		t.Fatalf("http=%d status_code=%d body=%s", recorder.Code, body.StatusCode, recorder.Body.String())
	}
	if !strings.Contains(body.Msg, "/admin/settings/google-auth") {
		t.Fatalf("unexpected rejection message: %q", body.Msg)
	}
}

// 有专用接口的配置一律不能走通用接口，否则会跳过校验与运行时生效。
func TestAdminHandlerRejectsDedicatedKeysOnGenericUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for key, endpoint := range dedicatedSettingEndpoints {
		stub := &adminSettingsStub{}
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", strings.NewReader(`{"key":"`+key+`","value":{}}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		NewAdminHandler(stub).Update(ctx)
		if stub.updateCalls != 0 || !strings.Contains(recorder.Body.String(), endpoint) {
			t.Fatalf("%s: generic update must be rejected with hint %s, body=%s", key, endpoint, recorder.Body.String())
		}
	}
}
