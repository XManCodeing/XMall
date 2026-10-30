package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"sync"
	"testing"
	"time"
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
	r := NewRuntime(NewManager(nil, api, "test-key"), nil, nil)
	answer := r.callbackResponder(context.Background(), "token", "id")
	ctx := c.WithCallbackResponder(context.Background(), answer)
	c.AnswerCallback(ctx, "updated")
	r.callbackResponder(ctx, "token", "id")("")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || api.calls[0] != "updated" {
		t.Fatal(api.calls)
	}
}
func TestCallbackQueueFallbackDoesNotWaitForHandler(t *testing.T) {
	api := &callbackAPI{called: make(chan struct{}, 2)}
	r := NewRuntime(NewManager(nil, api, "test-key"), nil, nil)
	r.callbackResponder(context.Background(), "token", "queued")
	select {
	case <-api.called:
	case <-time.After(3 * time.Second):
		t.Fatal("queued callback left unanswered")
	}
	answer := r.callbackResponder(context.Background(), "token", "queued")
	answer("too late")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.calls) != 1 || api.calls[0] != "" {
		t.Fatal(api.calls)
	}
}
