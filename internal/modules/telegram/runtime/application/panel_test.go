package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type callbackAPI struct {
	c.API
	mu     sync.Mutex
	calls  []string
	called chan struct{}
}

func (a *callbackAPI) Answer(ctx context.Context, token, id, text string) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("ack requires bounded context")
	}
	a.mu.Lock()
	a.calls = append(a.calls, text)
	a.mu.Unlock()
	select {
	case a.called <- struct{}{}:
	default:
	}
	return nil
}
func TestCallbackSpecificAnswerWinsOnce(t *testing.T) {
	api := &callbackAPI{called: make(chan struct{}, 2)}
	scope := newCallbackScope(context.Background(), api, "token")
	defer scope.close()
	answer := scope.responder("id")
	ctx := c.WithCallbackResponder(context.Background(), answer)
	c.AnswerCallback(ctx, "updated")
	scope.responder("id")("")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || api.calls[0] != "updated" {
		t.Fatal(api.calls)
	}
}
func TestCallbackQueueFallbackDoesNotWaitForHandler(t *testing.T) {
	api := &callbackAPI{called: make(chan struct{}, 2)}
	scope := newCallbackScope(context.Background(), api, "token")
	defer scope.close()
	scope.responder("queued")
	select {
	case <-api.called:
	case <-time.After(3 * time.Second):
		t.Fatal("queued callback left unanswered")
	}
	scope.responder("queued")("too late")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || api.calls[0] != "" {
		t.Fatal(api.calls)
	}
}

func awaitCallback(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("callback lifecycle barrier timed out")
	}
}
func controlledCallbackTimer(s *callbackScope) (chan time.Time, <-chan struct{}, <-chan struct{}) {
	tick := make(chan time.Time, 1)
	started, stopped := make(chan struct{}), make(chan struct{})
	s.newTimer = func() (<-chan time.Time, func()) { close(started); return tick, func() { close(stopped) } }
	return tick, started, stopped
}
func TestCallbackCancelBeforeTimerAndRestart(t *testing.T) {
	api := &callbackAPI{called: make(chan struct{}, 4)}
	old := newCallbackScope(context.Background(), api, "old-token")
	tick, started, stopped := controlledCallbackTimer(old)
	answer := old.responder("same-id")
	awaitCallback(t, started)
	old.close()
	awaitCallback(t, stopped)
	tick <- time.Now()
	answer("obsolete")
	old.responder("late-registration")("obsolete")
	api.mu.Lock()
	n := len(api.calls)
	api.mu.Unlock()
	if n != 0 {
		t.Fatal("cancelled generation answered", n)
	}
	fresh := newCallbackScope(context.Background(), api, "new-token")
	defer fresh.close()
	fresh.responder("same-id")("fresh")
	fresh.responder("same-id")("duplicate")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || api.calls[0] != "fresh" {
		t.Fatal(api.calls)
	}
}
func TestCallbackExplicitAndTimerRaceOnce(t *testing.T) {
	api := &callbackAPI{called: make(chan struct{}, 2)}
	s := newCallbackScope(context.Background(), api, "token")
	defer s.close()
	tick, started, stopped := controlledCallbackTimer(s)
	answer := s.responder("race")
	awaitCallback(t, started)
	gate, done := make(chan struct{}), make(chan struct{})
	go func() { <-gate; answer("explicit"); close(done) }()
	close(gate)
	tick <- time.Now()
	awaitCallback(t, done)
	awaitCallback(t, stopped)
	s.responder("race")("duplicate")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || (api.calls[0] != "" && api.calls[0] != "explicit") {
		t.Fatal(api.calls)
	}
}

type callbackRunStore struct{ lifecycleStore }

func (s *callbackRunStore) List(context.Context) ([]d.Instance, error) {
	return []d.Instance{s.instance}, nil
}
func (*callbackRunStore) Accept(context.Context, uint, uint64, []d.Inbox, int64) error { return nil }

type callbackRunAPI struct {
	*controlledAnswerAPI
	polls int
}

func (*callbackRunAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (a *callbackRunAPI) Poll(ctx context.Context, _ string, _ int64) ([]c.Update, error) {
	a.polls++
	if a.polls == 1 {
		return []c.Update{{ID: 1, CallbackID: "queued"}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRuntimeStopJoinsCallbackHTTPBeforeLeaseRelease(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { close(entered); <-req.Context().Done() }))
	defer server.Close()
	answerAPI := &controlledAnswerAPI{url: server.URL, client: server.Client(), cancelled: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(answerAPI.release) }) }
	defer unblock()
	api := &callbackRunAPI{controlledAnswerAPI: answerAPI}
	store := &callbackRunStore{lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}}
	manager := NewManager(store, api, "test-key")
	var err error
	store.instance.TokenCipher, err = manager.Seal("token")
	if err != nil {
		t.Fatal(err)
	}
	lease := &lifecycleLease{}
	runtime := NewRuntime(manager, lifecycleLeaser{lease}, lifecycleHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() {
		defer close(started)
		if err := runtime.Start(ctx); err != nil {
			t.Error(err)
		}
	}()
	defer func() { cancel(); unblock(); awaitCallback(t, started) }()
	awaitCallback(t, entered)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	stopped := make(chan error, 1)
	go func() { stopped <- runtime.Stop(stopCtx) }()
	awaitCallback(t, answerAPI.cancelled)
	if lease.released.Load() {
		t.Fatal("lease released before Answer returned")
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before Answer: %v", err)
	default:
	}
	unblock()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-stopCtx.Done():
		t.Fatal("Stop did not join callback")
	}
	awaitCallback(t, started)
	if !lease.released.Load() || answerAPI.calls.Load() != 1 {
		t.Fatal("callback not drained before lease release")
	}
}

type controlledAnswerAPI struct {
	c.API
	url       string
	client    *http.Client
	cancelled chan struct{}
	release   chan struct{}
	calls     atomic.Int32
}

func (a *controlledAnswerAPI) Answer(ctx context.Context, token, id, text string) error {
	a.calls.Add(1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url+"/"+token, nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(req)
	if response != nil {
		response.Body.Close()
	}
	<-ctx.Done()
	close(a.cancelled)
	<-a.release // cancellation observed, but Answer has not returned yet
	return err
}
func TestCallbackCancelDuringHTTPJoinsAndIsolatesBots(t *testing.T) {
	entered, serverExited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		close(entered)
		<-req.Context().Done()
		close(serverExited)
	}))
	defer server.Close()
	api := &controlledAnswerAPI{url: server.URL, client: server.Client(), cancelled: make(chan struct{}), release: make(chan struct{})}
	first := newCallbackScope(context.Background(), api, "old-token")
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(api.release) }) }
	defer func() { first.cancel(); unblock(); first.close() }()
	otherAPI := &callbackAPI{called: make(chan struct{}, 2)}
	other := newCallbackScope(context.Background(), otherAPI, "other-token")
	defer other.close()
	answer := first.responder("same-id")
	answerDone := make(chan struct{})
	go func() { answer("one"); close(answerDone) }()
	awaitCallback(t, entered)
	drained := make(chan struct{})
	go func() { first.close(); close(drained) }()
	awaitCallback(t, api.cancelled)
	awaitCallback(t, serverExited)
	select {
	case <-drained:
		t.Fatal("scope drained before Answer exited")
	default:
	}
	// Another bot must answer and drain while the first is still blocked.
	otherDone := make(chan struct{})
	go func() { other.responder("same-id")("other"); other.close(); close(otherDone) }()
	awaitCallback(t, otherDone)
	unblock()
	awaitCallback(t, drained)
	awaitCallback(t, answerDone)
	answer("late")
	first.responder("late")("late")
	if api.calls.Load() != 1 {
		t.Fatal("HTTP after scope drained", api.calls.Load())
	}
}
