package botapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

const testInvite = "https://t.me/+test-invite-secret"

// accessStoreStub returns instance and err for a single runtime lookup.
type accessStoreStub struct {
	r.Store
	instance rd.Instance
	err      error
}

// Get returns a copy of the configured instance for the requested bot.
func (store *accessStoreStub) Get(context.Context, uint) (*rd.Instance, error) {
	instance := store.instance
	return &instance, store.err
}

// accessCipherStub isolates test credential failures and post-decryption races.
type accessCipherStub struct {
	err  error
	open func()
}

// Open validates the ciphertext marker and returns a synthetic credential.
func (cipher accessCipherStub) Open(value string) (string, error) {
	if cipher.open != nil {
		cipher.open()
	}
	if value != "ciphertext" {
		return "", errors.New("unexpected ciphertext")
	}
	return "123:test-token-secret", cipher.err
}

// Seal returns the fake storage marker; access must not encrypt credentials.
func (accessCipherStub) Seal(string) (string, error) { return "ciphertext", nil }

// accessRoutesStub exposes a current route or a preflight resolution error.
type accessRoutesStub struct {
	route c.AccessRoute
	err   error
	calls int
}

// MembershipRoute counts calls and returns the configured route or error.
func (routes *accessRoutesStub) MembershipRoute(context.Context, uint) (c.AccessRoute, error) {
	routes.calls++
	return routes.route, routes.err
}

// accessFenceStub models ownership loss either initially or after preflight.
type accessFenceStub struct {
	calls  int
	failAt int
}

// Check rejects the specified check and all later checks, returning no secrets.
func (fence *accessFenceStub) Check(context.Context, uint, uint64) error {
	fence.calls++
	if fence.failAt > 0 && fence.calls >= fence.failAt {
		return r.ErrStale
	}
	return nil
}

// accessErrorStub models runtime APIError, including converted false responses.
type accessErrorStub struct {
	code      int
	delay     int
	uncertain bool
	kind      string
}

// Error deliberately contains secrets to prove only safe classes escape.
func (accessErrorStub) Error() string { return "123:test-token-secret " + testInvite }

// StatusCode returns the configured platform status code.
func (failure accessErrorStub) StatusCode() int { return failure.code }

// RetrySeconds returns the platform-provided backoff in seconds.
func (failure accessErrorStub) RetrySeconds() int { return failure.delay }

// Uncertain reports whether an external side effect may already have occurred.
func (failure accessErrorStub) Uncertain() bool { return failure.uncertain }

// Kind returns the runtime's explicit classification, never copied to results.
func (failure accessErrorStub) Kind() string { return failure.kind }

// accessAPIStub captures every external action and can wait for cancellation.
type accessAPIStub struct {
	calls    []string
	err      error
	wait     bool
	invite   r.InviteLink
	member   r.ChatMember
	request  r.CreateChatInviteLinkRequest
	token    string
	chatID   int64
	userID   int64
	deadline time.Time
}

// record stores call inputs and returns a configured failure or context expiry.
func (api *accessAPIStub) record(ctx context.Context, action, token string, chatID, userID int64) error {
	api.calls = append(api.calls, action)
	api.token, api.chatID, api.userID = token, chatID, userID
	api.deadline, _ = ctx.Deadline()
	if api.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return api.err
}

// CreateChatInviteLink captures the approval-only request and returns its result.
func (api *accessAPIStub) CreateChatInviteLink(ctx context.Context, token string, request r.CreateChatInviteLinkRequest) (r.InviteLink, error) {
	api.request = request
	return api.invite, api.record(ctx, "create", token, request.ChatID, 0)
}

// RevokeChatInviteLink captures one revoke; link is intentionally not recorded.
func (api *accessAPIStub) RevokeChatInviteLink(ctx context.Context, token string, chatID int64, _ string) (r.InviteLink, error) {
	return api.invite, api.record(ctx, "revoke", token, chatID, 0)
}

// ApproveChatJoinRequest records exactly one approval of the supplied identity.
func (api *accessAPIStub) ApproveChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	return api.record(ctx, "approve", token, chatID, userID)
}

// DeclineChatJoinRequest records exactly one decline of the supplied identity.
func (api *accessAPIStub) DeclineChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	return api.record(ctx, "decline", token, chatID, userID)
}

// GetChatMember records a read-only observation without membership mutations.
func (api *accessAPIStub) GetChatMember(ctx context.Context, token string, chatID, userID int64) (r.ChatMember, error) {
	return api.member, api.record(ctx, "get", token, chatID, userID)
}

// accessFixture returns a runnable adapter, owned context and its fake API.
func accessFixture() (*Access, context.Context, *accessAPIStub, c.AccessRoute) {
	route := c.AccessRoute{TargetID: 2, BotID: 1, ChatID: -100, RouteVersion: 3, Generation: 7}
	api := &accessAPIStub{
		invite: r.InviteLink{InviteLink: testInvite, Creator: r.UserReference{ID: 123}, CreatesJoinRequest: true, ExpireDate: time.Now().Add(time.Hour).Unix()},
		member: r.ChatMember{User: r.UserReference{ID: 456}, Status: "member"},
	}
	adapter := &Access{
		Store:  &accessStoreStub{instance: rd.Instance{ID: 1, TelegramID: 123, Enabled: true, Mode: "internal", State: "running", Generation: 7, TokenCipher: "ciphertext"}},
		Cipher: accessCipherStub{}, API: api, Query: api, Routes: &accessRoutesStub{route: route},
	}
	return adapter, r.WithExecutionFence(context.Background(), &accessFenceStub{}), api, route
}

// runAccessAction invokes an action with valid input and returns only its outcome.
func runAccessAction(adapter *Access, ctx context.Context, api *accessAPIStub, route c.AccessRoute, action string) c.AccessAPIResult {
	input := c.AccessJoinAction{Route: route, TelegramID: 456}
	switch action {
	case "create":
		return adapter.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, Name: "access-1", ExpiresAt: time.Unix(api.invite.ExpireDate, 0)}).AccessAPIResult
	case "approve":
		return adapter.ApproveMembershipJoin(ctx, input)
	case "decline":
		return adapter.DeclineMembershipJoin(ctx, input)
	case "revoke":
		api.invite.IsRevoked = true
		return adapter.RevokeMembershipInvite(ctx, route, testInvite)
	default:
		return adapter.GetMembershipMember(ctx, input).AccessAPIResult
	}
}

// TestAccessActions verifies exact target, credentials, deadlines and no retries.
func TestAccessActions(t *testing.T) {
	for _, action := range []string{"create", "approve", "decline", "revoke", "get"} {
		t.Run(action, func(t *testing.T) {
			adapter, ctx, api, route := accessFixture()
			result := runAccessAction(adapter, ctx, api, route, action)
			if result.Outcome != c.AccessSucceeded || result.ErrorClass != "" || len(api.calls) != 1 || api.calls[0] != action {
				t.Fatalf("result=%+v calls=%v", result, api.calls)
			}
			if api.token != "123:test-token-secret" || api.chatID != route.ChatID || api.deadline.IsZero() {
				t.Fatal("credential/target/deadline mismatch")
			}
			if action == "create" && (api.request.ExpireDate != api.invite.ExpireDate || api.request.Name != "access-1") {
				t.Fatal("invite request lost expiry/name")
			}
			if action != "create" && action != "revoke" && api.userID != 456 {
				t.Fatal("wrong applicant identity")
			}
		})
	}
}

// TestAccessPreflightStopsEveryAction ensures all failures precede external I/O.
func TestAccessPreflightStopsEveryAction(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Access, context.Context) context.Context
	}{
		{"no_fence", func(_ *Access, _ context.Context) context.Context { return context.Background() }},
		{"lost_lease", func(_ *Access, ctx context.Context) context.Context {
			return r.WithExecutionFence(ctx, &accessFenceStub{failAt: 1})
		}},
		{"lost_during_preflight", func(_ *Access, ctx context.Context) context.Context {
			return r.WithExecutionFence(ctx, &accessFenceStub{failAt: 2})
		}},
		{"old_route", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Routes.(*accessRoutesStub).route.RouteVersion++
			return ctx
		}},
		{"old_bot", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Routes.(*accessRoutesStub).route.BotID++
			return ctx
		}},
		{"old_chat", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Routes.(*accessRoutesStub).route.ChatID--
			return ctx
		}},
		{"disabled", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Store.(*accessStoreStub).instance.Enabled = false
			return ctx
		}},
		{"external", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Store.(*accessStoreStub).instance.Mode = "external"
			return ctx
		}},
		{"stopped", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Store.(*accessStoreStub).instance.State = "stopped"
			return ctx
		}},
		{"old_generation", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Store.(*accessStoreStub).instance.Generation++
			return ctx
		}},
		{"database_error", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Store.(*accessStoreStub).err = errors.New("secret")
			return ctx
		}},
		{"route_error", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Routes.(*accessRoutesStub).err = errors.New("secret")
			return ctx
		}},
		{"no_routes", func(adapter *Access, ctx context.Context) context.Context { adapter.Routes = nil; return ctx }},
		{"cipher_error", func(adapter *Access, ctx context.Context) context.Context {
			adapter.Cipher = accessCipherStub{err: errors.New("secret")}
			return ctx
		}},
		{"canceled", func(_ *Access, ctx context.Context) context.Context {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx
		}},
	}
	for _, testCase := range cases {
		for _, action := range []string{"create", "approve", "decline", "revoke", "get"} {
			t.Run(testCase.name+"/"+action, func(t *testing.T) {
				adapter, ctx, api, route := accessFixture()
				ctx = testCase.change(adapter, ctx)
				result := runAccessAction(adapter, ctx, api, route, action)
				if result.Outcome != c.AccessRetryable || len(api.calls) != 0 || result.ErrorClass == "" {
					t.Fatalf("preflight result=%+v calls=%v", result, api.calls)
				}
				if testCase.name == "lost_lease" && adapter.Routes.(*accessRoutesStub).calls != 0 {
					t.Fatal("route resolver entered after ownership loss")
				}
			})
		}
	}
}

// TestAccessErrorClassification verifies uncertainty priority and safe failures.
func TestAccessErrorClassification(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		want  string
		delay time.Duration
	}{
		{"429", accessErrorStub{code: 429, delay: 37}, c.AccessRetryable, 37 * time.Second},
		{"429_fallback", accessErrorStub{code: 429}, c.AccessRetryable, 5 * time.Second},
		{"uncertain_429", accessErrorStub{code: 429, delay: 37, uncertain: true}, c.AccessUnknown, 0},
		{"uncertain_400", accessErrorStub{code: 400, uncertain: true}, c.AccessUnknown, 0},
		{"401", accessErrorStub{code: 401}, c.AccessFailed, 0},
		{"403", accessErrorStub{code: 403}, c.AccessFailed, 0},
		{"false_response", accessErrorStub{kind: "refused"}, c.AccessFailed, 0},
		{"retryable", accessErrorStub{kind: "retryable"}, c.AccessRetryable, 0},
		{"uncertain_retryable", accessErrorStub{kind: "retryable", uncertain: true}, c.AccessUnknown, 0},
		{"unclassified_500", accessErrorStub{code: 500}, c.AccessUnknown, 0},
	}
	for _, testCase := range cases {
		for _, action := range []string{"create", "approve", "decline", "revoke", "get"} {
			t.Run(testCase.name+"/"+action, func(t *testing.T) {
				adapter, ctx, api, route := accessFixture()
				api.err = testCase.err
				result := runAccessAction(adapter, ctx, api, route, action)
				want := testCase.want
				if action == "get" && testCase.name == "unclassified_500" {
					want = c.AccessRetryable
				}
				if result.Outcome != want || result.RetryAfter != testCase.delay || len(api.calls) != 1 {
					t.Fatalf("result=%+v calls=%v", result, api.calls)
				}
				if strings.Contains(result.ErrorClass, "secret") || strings.Contains(result.ErrorClass, "https") {
					t.Fatal("raw error leaked")
				}
			})
		}
	}
}

// TestAccessInviteResponses rejects forged, unsafe or mismatched invite results.
func TestAccessInviteResponses(t *testing.T) {
	cases := []struct {
		name   string
		change func(*r.InviteLink)
	}{
		{"creator", func(invite *r.InviteLink) { invite.Creator.ID++ }},
		{"not_join_request", func(invite *r.InviteLink) { invite.CreatesJoinRequest = false }},
		{"revoked", func(invite *r.InviteLink) { invite.IsRevoked = true }},
		{"primary", func(invite *r.InviteLink) { invite.IsPrimary = true }},
		{"member_limit", func(invite *r.InviteLink) { invite.MemberLimit = 1 }},
		{"expiry", func(invite *r.InviteLink) { invite.ExpireDate++ }},
		{"fake_url", func(invite *r.InviteLink) { invite.InviteLink = "https://example.org/help" }},
		{"empty", func(invite *r.InviteLink) { *invite = r.InviteLink{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, ctx, api, route := accessFixture()
			expires := time.Unix(api.invite.ExpireDate, 0)
			testCase.change(&api.invite)
			result := adapter.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, ExpiresAt: expires})
			if result.Outcome != c.AccessUnknown || result.Link != "" || result.ErrorClass != "invalid_response" || len(api.calls) != 1 {
				t.Fatalf("unsafe invite accepted: %+v", result)
			}
		})
	}
}

// TestAccessRevokeResponses requires matching ownership, link and revoked state.
func TestAccessRevokeResponses(t *testing.T) {
	for _, field := range []string{"creator", "link", "revoked"} {
		adapter, ctx, api, route := accessFixture()
		api.invite.IsRevoked = true
		switch field {
		case "creator":
			api.invite.Creator.ID++
		case "link":
			api.invite.InviteLink = "https://t.me/+different-secret"
		case "revoked":
			api.invite.IsRevoked = false
		}
		result := adapter.RevokeMembershipInvite(ctx, route, testInvite)
		if result.Outcome != c.AccessUnknown || result.ErrorClass != "invalid_response" || len(api.calls) != 1 {
			t.Fatalf("revoke %s accepted: %+v", field, result)
		}
	}
}

// TestAccessSuccessfulInviteAndWrongMember checks the only exposed secret path
// and ensures a query cannot report another Telegram user's membership.
func TestAccessSuccessfulInviteAndWrongMember(t *testing.T) {
	adapter, ctx, api, route := accessFixture()
	invite := adapter.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, ExpiresAt: time.Unix(api.invite.ExpireDate, 0)})
	if invite.Outcome != c.AccessSucceeded || invite.Link != testInvite {
		t.Fatal("confirmed invite was not returned")
	}
	api.member.User.ID++
	member := adapter.GetMembershipMember(ctx, c.AccessJoinAction{Route: route, TelegramID: 456})
	if member.Outcome != c.AccessRetryable || member.Status != "" || member.IsMember {
		t.Fatal("another member's observation accepted")
	}
}

// TestAccessMemberStatus preserves restricted flags and never changes kicked state.
func TestAccessMemberStatus(t *testing.T) {
	for _, status := range []string{"member", "creator", "administrator", "restricted", "left", "kicked", "invalid"} {
		for _, flag := range []bool{false, true} {
			adapter, ctx, api, route := accessFixture()
			api.member.Status, api.member.IsMember = status, flag
			result := adapter.GetMembershipMember(ctx, c.AccessJoinAction{Route: route, TelegramID: 456})
			if status == "invalid" {
				if result.Outcome != c.AccessRetryable || result.Status != "" {
					t.Fatal("invalid member accepted")
				}
				continue
			}
			want := status == "member" || status == "creator" || status == "administrator" || status == "restricted" && flag
			if result.Outcome != c.AccessSucceeded || result.Status != status || result.IsMember != want || len(api.calls) != 1 {
				t.Fatalf("status=%s flag=%t result=%+v", status, flag, result)
			}
		}
	}
}

// TestAccessTimeout bounds all calls and distinguishes reads from side effects.
func TestAccessTimeout(t *testing.T) {
	for _, action := range []string{"create", "approve", "decline", "revoke", "get"} {
		t.Run(action, func(t *testing.T) {
			adapter, ctx, api, route := accessFixture()
			adapter.Timeout, api.wait = 5*time.Millisecond, true
			started := time.Now()
			result := runAccessAction(adapter, ctx, api, route, action)
			want := c.AccessUnknown
			if action == "get" {
				want = c.AccessRetryable
			}
			if result.Outcome != want || len(api.calls) != 1 || time.Since(started) > time.Second {
				t.Fatalf("timeout result=%+v", result)
			}
		})
	}
	adapter, ctx, _, _ := accessFixture()
	adapter.Timeout = time.Hour
	bounded, cancel := adapter.bounded(ctx)
	defer cancel()
	deadline, ok := bounded.Deadline()
	if !ok || time.Until(deadline) > accessMaximumTimeout {
		t.Fatal("timeout maximum not enforced")
	}
}

// TestAccessLogRedaction proves upstream errors and invitation URLs stay secret.
func TestAccessLogRedaction(t *testing.T) {
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	adapter, ctx, api, route := accessFixture()
	for _, action := range []string{"create", "approve", "decline", "revoke", "get"} {
		api.err = accessErrorStub{code: 429, uncertain: true}
		runAccessAction(adapter, ctx, api, route, action)
	}
	if buffer.Len() == 0 || strings.Contains(buffer.String(), "secret") || strings.Contains(buffer.String(), "https://") {
		t.Fatal("missing audit or leaked secret")
	}
}

// TestAccessInputValidation rejects malformed links, user IDs and expired invites.
func TestAccessInputValidation(t *testing.T) {
	for _, link := range []string{"", "https://t.me/support", "https://t.me/+abc", "http://t.me/+abcdefgh", "https://t.me.evil/+abcdefgh", "https://t.me/+abcdefgh?x=1", "https://t.me/+abcdefgh?", "https://t.me/+abcdefgh#x", "https://user@t.me/+abcdefgh", "https://t.me/+abc%64efgh", "https://t.me/+abcdefgh/"} {
		adapter, ctx, api, route := accessFixture()
		if result := adapter.RevokeMembershipInvite(ctx, route, link); result.Outcome != c.AccessFailed || len(api.calls) != 0 {
			t.Fatal("invalid revoke sent")
		}
	}
	for _, link := range []string{testInvite, "https://t.me/joinchat/abcdefgh_-"} {
		if !validAccessLink(link) {
			t.Fatal("legitimate invitation rejected")
		}
	}
	adapter, ctx, api, route := accessFixture()
	result := adapter.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: route, ExpiresAt: time.Now().Add(-time.Second)})
	if result.Outcome != c.AccessFailed || len(api.calls) != 0 {
		t.Fatal("expired invitation sent")
	}
	for _, input := range []c.AccessJoinAction{{Route: route}, {Route: route, TelegramID: -1}} {
		if adapter.ApproveMembershipJoin(ctx, input).Outcome != c.AccessFailed || adapter.DeclineMembershipJoin(ctx, input).Outcome != c.AccessFailed || adapter.GetMembershipMember(ctx, input).Outcome != c.AccessFailed || len(api.calls) != 0 {
			t.Fatal("invalid user sent")
		}
	}
}
