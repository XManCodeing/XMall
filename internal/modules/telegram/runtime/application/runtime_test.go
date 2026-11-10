package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
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

type runtimeStoreStub struct {
	c.Store
	mu        sync.Mutex
	instances []d.Instance
	listErr   error
	writes    int
}

func (s *runtimeStoreStub) List(context.Context) ([]d.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances, s.listErr
}
func (s *runtimeStoreStub) Get(_ context.Context, id uint) (*d.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.instances {
		if i.ID == id {
			return &i, nil
		}
	}
	return nil, c.ErrNotFound
}
func (s *runtimeStoreStub) Create(_ context.Context, i *d.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i.ID = uint(len(s.instances) + 1)
	s.instances = append(s.instances, *i)
	return nil
}
func (s *runtimeStoreStub) Configure(_ context.Context, id uint, v uint64, f map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	return nil
}

type apiStub struct {
	c.API
	identity c.BotIdentity
	err      error
	webhook  string
	polls    int
}

func (a *apiStub) Identity(context.Context, string) (c.BotIdentity, error) { return a.identity, a.err }
func (a *apiStub) Webhook(context.Context, string) (string, error)         { return a.webhook, a.err }

func TestDisabledAndDatabaseFailureKeepRuntimeAlive(t *testing.T) {
	for _, err := range []error{nil, errors.New("database unavailable")} {
		s := &runtimeStoreStub{listErr: err}
		runtime := NewRuntime(NewManager(s, &apiStub{}, "test-key"), nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- runtime.Start(ctx) }()
		select {
		case err := <-result:
			t.Fatalf("runtime returned while disabled/degraded: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		cancel()
		stopCtx, done := context.WithTimeout(context.Background(), time.Second)
		if err := runtime.Stop(stopCtx); err != nil {
			t.Fatal(err)
		}
		done()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
}
func TestStopBeforeStartDoesNotLeak(t *testing.T) {
	r := NewRuntime(NewManager(&runtimeStoreStub{}, &apiStub{}, "test-key"), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(r.Stop(ctx), context.DeadlineExceeded) {
		t.Fatal("unstarted service must not falsely report drained")
	}
	result := make(chan error, 1)
	go func() { result <- r.Start(context.Background()) }()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("late start ignored stop")
	}
}
func TestManagerNeverStoresPlainTokenOrEnablesNewInstance(t *testing.T) {
	s := &runtimeStoreStub{}
	api := &apiStub{identity: c.BotIdentity{ID: 42, Username: "shop"}}
	manager := NewManager(s, api, "test-key")
	instance, err := manager.Create(context.Background(), "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Enabled || instance.TokenCipher == "secret-token" || instance.TokenCipher == "" {
		t.Fatal("unsafe instance defaults")
	}
	plain, err := manager.Token(instance)
	if err != nil || plain != "secret-token" {
		t.Fatal("credential did not round trip")
	}
	view := View(*instance)
	if view.TokenCipher != "" || !view.TokenSet {
		t.Fatal("view exposes credential")
	}
}
func TestManagerRejectsWebhookIdentitySwapAndVersionConflict(t *testing.T) {
	s := &runtimeStoreStub{}
	api := &apiStub{identity: c.BotIdentity{ID: 42}}
	m := NewManager(s, api, "test-key")
	i, err := m.Create(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err = m.Configure(context.Background(), i.ID, 99, &enabled, nil); !errors.Is(err, c.ErrConflict) {
		t.Fatal("missing conflict")
	}
	api.webhook = "https://old.example/webhook"
	if err = m.Configure(context.Background(), i.ID, i.Version, &enabled, nil); err == nil {
		t.Fatal("implicitly took over webhook")
	}
	api.identity.ID = 43
	token := "replacement"
	if err = m.Configure(context.Background(), i.ID, i.Version, nil, &token); err == nil {
		t.Fatal("silently reused instance for different bot")
	}
	if s.writes != 0 {
		t.Fatal("invalid configuration was persisted")
	}
}
