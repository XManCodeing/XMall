package botapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/botapi"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

type memberInstanceStore struct {
	rc.Store
	generation atomic.Uint64
	now        time.Time
}

func (s *memberInstanceStore) Get(context.Context, uint) (*r.Instance, error) {
	return &r.Instance{ID: 1, TelegramID: 123, Mode: "internal", Enabled: true, State: "running", LastSeenAt: &s.now, Generation: s.generation.Load(), Version: 1, TokenCipher: "cipher"}, nil
}
func TestSupportMemberUsesFreshActorLookupAndRejectsStaleIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, status         string
		member, bot, allowed bool
		id                   int64
		rotate               bool
	}{
		{"member", "member", true, false, true, 77, false},
		{"admin", "administrator", true, false, true, 77, false},
		{"creator", "creator", true, false, true, 77, false},
		{"restricted member", "restricted", true, false, true, 77, false},
		{"restricted outsider", "restricted", false, false, false, 77, false},
		{"left", "left", false, false, false, 77, false},
		{"kicked", "kicked", false, false, false, 77, false},
		{"bot", "member", true, true, false, 77, false},
		{"wrong actor", "member", true, false, false, 78, false},
		{"rotated", "member", true, false, false, 77, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memberInstanceStore{now: time.Now()}
			store.generation.Store(7)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if req.URL.Path != "/bot123:synthetic/getChatMember" {
					t.Error("unexpected endpoint", req.URL.Path)
				}
				var input struct {
					ChatID int64 `json:"chat_id"`
					UserID int64 `json:"user_id"`
				}
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input.ChatID != -100 || input.UserID != 77 {
					t.Error("actor/chat mismatch", input)
				}
				if tc.rotate {
					store.generation.Add(1)
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"user": map[string]any{"id": tc.id, "is_bot": tc.bot}, "status": tc.status, "is_member": tc.member}})
			}))
			defer server.Close()
			adapter := &botapi.Adapter{Store: store, Cipher: cipher{}, Options: []telegramapi.Option{telegramapi.WithBaseURL(server.URL)}}
			for n := 0; n < 2; n++ {
				allowed, err := adapter.SupportMember(context.Background(), 1, -100, 77)
				if (allowed && err == nil) != tc.allowed {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("membership permission was cached")
			}
		})
	}
}

// TestSupportMemberClassifiesLookupFailures uses actual HTTP responses and a
// timed-out request to verify only explicit denial becomes ErrCapability.
func TestSupportMemberClassifiesLookupFailures(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		code    int
		pending bool
	}{
		{"rate_limited", 429, true}, {"timeout", 0, true},
		{"server", 503, true}, {"forbidden", 403, false},
		{"invalid_route", 400, false}, {"authentication", 401, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := &memberInstanceStore{now: time.Now()}
			store.generation.Store(7)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if scenario.code == 0 {
					select {
					case <-req.Context().Done():
					case <-release:
					}
					return
				}
				w.WriteHeader(scenario.code)
				if err := json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": scenario.code, "description": "synthetic error", "parameters": map[string]any{"retry_after": 73}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			defer close(release)
			adapter := &botapi.Adapter{Store: store, Cipher: cipher{}, Options: []telegramapi.Option{telegramapi.WithBaseURL(server.URL)}}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			allowed, err := adapter.SupportMember(ctx, 1, -100, 77)
			if allowed || errors.Is(err, c.ErrCapabilityPending) != scenario.pending || errors.Is(err, c.ErrCapability) == scenario.pending {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
			if scenario.code == 429 {
				var retry interface{ RetrySeconds() int }
				if !errors.As(err, &retry) || retry.RetrySeconds() != 73 {
					t.Fatal("retry_after lost", err)
				}
			}
		})
	}
}

// failingIdentityStore injects a failure specifically at the second identity read.
type failingIdentityStore struct {
	memberInstanceStore
	calls int
	err   error
}

// Get returns the initial running instance, then the configured identity failure.
func (s *failingIdentityStore) Get(ctx context.Context, bot uint) (*r.Instance, error) {
	s.calls++
	if s.calls == 2 {
		return nil, s.err
	}
	return s.memberInstanceStore.Get(ctx, bot)
}

// TestSupportMemberPreservesInstanceFailure verifies storage failure is retryable
// while a genuinely missing instance remains a permanent route failure.
func TestSupportMemberPreservesInstanceFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		store := &failingIdentityStore{memberInstanceStore: memberInstanceStore{now: time.Now()}, err: context.DeadlineExceeded}
		store.generation.Store(7)
		expected := c.ErrCapabilityPending
		if missing {
			store.err, expected = rc.ErrNotFound, c.ErrNotFound
		}
		adapter := &botapi.Adapter{Store: store, Cipher: cipher{}}
		allowed, err := adapter.SupportMember(context.Background(), 1, -100, 77)
		if allowed || !errors.Is(err, expected) || errors.Is(err, c.ErrCapability) {
			t.Fatal(allowed, err)
		}
	}
}
