package telegramhttp

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

type guardFake struct {
	allowed bool
	id      uint
}

func (g *guardFake) LegacyAllowed(_ context.Context, id uint) (bool, error) {
	g.id = id
	return g.allowed, nil
}
func TestMigratedConfigAndHeartbeatAreFencedBeforeCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, heartbeat := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/", nil)
		c.Set("channel_client_id", uint(7))
		guard := &guardFake{}
		h := &ChannelBotHandler{Guard: guard}
		// nil settings/tokens deliberately panic if authorization does not run first.
		if heartbeat {
			h.ReportHeartbeat(c)
		} else {
			h.GetBotConfig(c)
		}
		if recorder.Code != 409 || guard.id != 7 {
			t.Fatal(recorder.Code, guard.id)
		}
	}
}
func TestUnrelatedChannelRemainsAllowed(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Set("channel_client_id", uint(8))
	h := &ChannelBotHandler{Guard: &guardFake{allowed: true}}
	if !h.allowLegacy(c) {
		t.Fatal("generic channel blocked")
	}
}
