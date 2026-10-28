package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type legacyGuardStub struct {
	allowed bool
	err     error
	id      uint
}

func (g *legacyGuardStub) LegacyAllowed(_ context.Context, id uint) (bool, error) {
	g.id = id
	return g.allowed, g.err
}
func TestLegacyChannelFenceCoversAllRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/orders", "/payments", "/telegram/config", "/telegram/heartbeat"} {
		for _, tc := range []struct {
			name  string
			guard LegacyChannelGuard
			want  int
		}{
			{"missing", nil, http.StatusForbidden},
			{"pending_or_internal", &legacyGuardStub{}, http.StatusForbidden},
			{"lookup_failure", &legacyGuardStub{err: errors.New("offline")}, http.StatusForbidden},
			{"external_or_unassociated", &legacyGuardStub{allowed: true}, http.StatusNoContent},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				r := gin.New()
				r.Use(func(c *gin.Context) { c.Set(channelClientIDKey, uint(7)) })
				r.Use(LegacyChannelGuardMiddleware(tc.guard))
				r.POST(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
				if w.Code != tc.want {
					t.Fatalf("got %d, want %d", w.Code, tc.want)
				}
				if g, ok := tc.guard.(*legacyGuardStub); ok && g.id != 7 {
					t.Fatal("wrong channel")
				}
			})
		}
	}
}
