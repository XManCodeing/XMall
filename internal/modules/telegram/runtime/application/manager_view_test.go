package application

import (
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

func TestViewSeparatesDesiredAndObservedState(t *testing.T) {
	recent := time.Now().Add(-time.Second)
	expired := time.Now().Add(-45 * time.Second)
	for _, tt := range []struct {
		name    string
		enabled bool
		mode    string
		state   string
		seen    *time.Time
		want    string
		fresh   bool
	}{
		{"disabled worker still running", false, "internal", "running", &recent, "running", true},
		{"disabled worker still stopping", false, "internal", "stopping", &recent, "stopping", true},
		{"disabled expired worker is not confirmed stopped", false, "internal", "running", &expired, "stale", false},
		{"disabled missing heartbeat is not confirmed stopped", false, "internal", "stopping", nil, "stale", false},
		{"enabled expired worker", true, "internal", "running", &expired, "stale", false},
		{"enabled missing heartbeat", true, "internal", "starting", nil, "stale", false},
		{"confirmed stop survives expired heartbeat", false, "internal", "stopped", &expired, "stopped", false},
		{"new stopped instance needs no heartbeat", false, "internal", "stopped", nil, "stopped", false},
		{"enable intent does not erase confirmed stop", true, "internal", "stopped", &expired, "stopped", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance := d.Instance{Enabled: tt.enabled, Mode: tt.mode, State: tt.state, LastSeenAt: tt.seen, TokenCipher: "test-cipher"}
			view := View(instance)
			if view.State != tt.want || view.Fresh != tt.fresh || view.Enabled != tt.enabled {
				t.Fatalf("got state=%s fresh=%v enabled=%v", view.State, view.Fresh, view.Enabled)
			}
			if view.TokenCipher != "" || !view.TokenSet || instance.TokenCipher != "test-cipher" || instance.State != tt.state {
				t.Fatal("view leaked token or mutated source instance")
			}
		})
	}
}
