package systemhttp_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	systemhttp "github.com/XManCodeing/XMall/internal/platform/http/system"
	"github.com/XManCodeing/XMall/internal/selfupdate"
	"github.com/XManCodeing/XMall/internal/version"
	"github.com/gin-gonic/gin"
)

func TestRestartDeniedBeforeAcknowledgement(t *testing.T) {
	previous := version.BuildType
	version.BuildType = "source"
	defer func() { version.BuildType = previous }()
	router := gin.New()
	h := systemhttp.NewAdminHandler(nil)
	router.POST("/restart", h.RestartService)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/restart", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if data, ok := body["data"].(map[string]any); ok && data["restarting"] == true {
		t.Fatal("denied restart acknowledged success")
	}
	if selfupdate.RestartRequested() {
		t.Fatal("denied restart requested shutdown")
	}
}
