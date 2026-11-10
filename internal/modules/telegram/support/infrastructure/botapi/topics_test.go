package botapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type topicInstances struct{ r.Store }

func (topicInstances) Get(context.Context, uint) (*rd.Instance, error) {
	return &rd.Instance{ID: 1, Generation: 7, Enabled: true, Mode: "internal", State: "running", TokenCipher: "encrypted"}, nil
}

type topicCipher struct{}

func (topicCipher) Open(string) (string, error)   { return "123:test-secret", nil }
func (topicCipher) Seal(s string) (string, error) { return s, nil }

type topicFence struct{ err error }

func (f topicFence) Check(context.Context, uint, uint64) error { return f.err }

type topicError struct {
	code      int
	uncertain bool
}

func (e topicError) Error() string {
	return "https://api.telegram.org/bot123:test-secret/createForumTopic"
}
func (e topicError) StatusCode() int   { return e.code }
func (e topicError) RetrySeconds() int { return 37 }
func (e topicError) Uncertain() bool   { return e.uncertain }

type topicAPI struct {
	r.ForumTopicAPI
	calls int
	err   error
	id    int64
}

func (a *topicAPI) CreateForumTopic(context.Context, string, r.ForumTopicRequest) (r.ForumTopicResult, error) {
	a.calls++
	return r.ForumTopicResult{ThreadID: a.id}, a.err
}
func TestTopicAdapterDistinguishesRejectionFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		id         int64
		err        error
	}{
		{"ok", "succeeded", 9, nil}, {"empty", "unknown", 0, nil},
		{"rejected", "failed", 0, topicError{400, false}}, {"rate", "failed", 0, topicError{429, false}},
		{"server", "unknown", 0, topicError{500, false}}, {"uncertain", "unknown", 0, topicError{400, true}}, {"network", "unknown", 0, errors.New("network")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &topicAPI{err: tc.err, id: tc.id}
			adapter := Topics{Store: topicInstances{}, Cipher: topicCipher{}, API: api}
			route := g.SupportRoute{BotID: 1, ChatID: -100, Generation: 7, RouteVersion: 1}
			result, err := adapter.ExecuteTopic(r.WithExecutionFence(context.Background(), topicFence{}), route, "create", 0, "Support #3")
			if result.State != tc.want || api.calls != 1 {
				t.Fatalf("state=%s calls=%d", result.State, api.calls)
			}
			if (tc.name == "rate" && result.RetryAfterSeconds != 37) || (tc.name != "rate" && result.RetryAfterSeconds != 0) {
				t.Fatal("retry deadline lost or fabricated", result)
			}
			if err != nil && strings.Contains(err.Error(), "test-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}
func TestTopicAdapterCannotExecuteWithoutLiveFence(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), r.WithExecutionFence(context.Background(), topicFence{errors.New("lost")})} {
		api := &topicAPI{id: 9}
		adapter := Topics{Store: topicInstances{}, Cipher: topicCipher{}, API: api}
		result, err := adapter.ExecuteTopic(ctx, g.SupportRoute{BotID: 1, ChatID: -100, Generation: 7, RouteVersion: 1}, "create", 0, "Support #3")
		if err == nil || result.State != "failed" || api.calls != 0 {
			t.Fatal("unleased side effect")
		}
	}
}
