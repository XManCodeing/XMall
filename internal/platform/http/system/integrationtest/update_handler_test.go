package systemhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/XManCodeing/XMall/internal/platform/http/response"
	systemhttp "github.com/XManCodeing/XMall/internal/platform/http/system"
	"github.com/XManCodeing/XMall/internal/selfupdate"
	"github.com/XManCodeing/XMall/internal/version"
	"github.com/gin-gonic/gin"
)

type neverReleaseChecker struct{ t *testing.T }

func (r neverReleaseChecker) CheckLatestRelease(context.Context) (*version.CheckResult, error) {
	r.t.Fatal("disabled handler called release checker")
	return nil, nil
}

func TestUpdateEndpointsFailClosedWithoutSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, repository := range []string{"", "https://github.com/example/releases"} {
		h := systemhttp.NewAdminHandler(neverReleaseChecker{t}, repository)
		for _, handler := range []gin.HandlerFunc{h.CheckSystemUpdate, h.StartUpdate} {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			handler(c)
			var body struct {
				Code int `json:"status_code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != response.CodeBadRequest {
				t.Fatalf("expected rejection: %s", recorder.Body.String())
			}
		}
	}
}

func TestUpdateCapabilityReportsMissingSource(t *testing.T) {
	h := systemhttp.NewAdminHandler(nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h.GetUpdateCapability(c)
	var body struct {
		Data struct {
			Capability selfupdate.Capability `json:"capability"`
			State      selfupdate.State      `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Capability.CanUpdate || body.Data.Capability.BlockReason != selfupdate.BlockUpdateSource {
		t.Fatalf("unexpected capability: %s", recorder.Body.String())
	}
	if body.Data.State.Status != selfupdate.StatusIdle {
		t.Fatalf("disabled manager must remain idle: %s", recorder.Body.String())
	}
}
