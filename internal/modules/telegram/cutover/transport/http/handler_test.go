package cutoverhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cutover "github.com/XManCodeing/XMall/internal/modules/telegram/cutover/application"
	"github.com/gin-gonic/gin"
)

type completedStore struct{ instance cutover.Instance }

func (s completedStore) Target(context.Context, uint) (cutover.Instance, error) {
	return s.instance, nil
}
func (s completedStore) ByChannel(context.Context, uint) (*cutover.Instance, error) {
	return &s.instance, nil
}
func (s completedStore) Switch(context.Context, cutover.Instance, string, bool) error {
	panic("completed cutover must not mutate")
}
func TestRoutesAreCallableAndCompletedApplyDoesNotReportDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s := &cutover.Service{Store: completedStore{cutover.Instance{BotID: 4, ChannelID: 9, Version: 3, Mode: "internal", Enabled: true}}}
	RegisterRoutes(r.Group("/api/v1/admin"), &Handler{Service: s})
	for _, action := range []string{"preflight", "apply"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/telegram/cutover/"+action, strings.NewReader(`{"channel_id":9,"bot_id":4,"version":3}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var body struct {
			Code int                    `json:"code"`
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || body.Data["mode"] != "internal" {
			t.Fatal(w.Code, w.Body.String())
		}
		if enabled, present := body.Data["enabled"]; present && enabled != true {
			t.Fatal("completed apply falsely reports disabled")
		}
	}
}
