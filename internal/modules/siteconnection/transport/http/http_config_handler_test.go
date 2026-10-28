package siteconnectionhttp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	app "github.com/homles698698-lab/XMall/internal/modules/siteconnection/application"
)

func TestHTTPConfigCapabilityMessagesAreLocalized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ locale, text string }{{"zh-CN", "缺少商品列表"}, {"zh-TW", "缺少商品列表"}, {"en-US", "Missing products"}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.Header.Set("X-Lang", tc.locale)
		finishHTTPConfig(c, &app.HTTPConfigResult{ValidationErrors: []string{"httpIntegration.missingProducts"}}, nil)
		if !strings.Contains(w.Body.String(), tc.text) || strings.Contains(w.Body.String(), "httpIntegration.missingProducts") {
			t.Fatalf("unlocalized response: %s", w.Body.String())
		}
	}
}

func TestBindHTTPConfigRejectsOversizeAndTrailingJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"action":"test"}` + strings.Repeat(" ", app.HTTPMaxBytes),
		`{"action":"test"} {"action":"create"}`,
		`{"response":"` + strings.Repeat("x", app.HTTPMaxBytes) + `"}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		var input app.HTTPDebugInput
		if bindHTTPConfig(c, &input) {
			t.Fatal("accepted oversized/trailing request")
		}
		if !strings.Contains(w.Body.String(), `"status_code":400`) {
			t.Fatalf("response %s", w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader("{\"action\":\"test\"}\n"))
	var input app.HTTPDebugInput
	if !bindHTTPConfig(c, &input) || input.Action != "test" {
		t.Fatal("valid JSON rejected")
	}
}
