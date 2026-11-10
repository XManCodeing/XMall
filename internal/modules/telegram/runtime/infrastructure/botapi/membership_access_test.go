package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

// membershipFailureTransport supplies a raw transport error to test redaction.
type membershipFailureTransport struct{}

// RoundTrip consumes the supplied request only to emulate a failed transport;
// it returns no response and a deliberately sensitive error for adapter tests.
func (membershipFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, errors.New("123:secret raw_private_error https://t.me/+secret")
}

// TestMembershipTransportRedaction checks that transport errors are unknown and
// neither errors nor structured logs expose the token, link or raw error.
func TestMembershipTransportRedaction(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	adapter := Adapter{Options: []api.Option{api.WithHTTPClient(&http.Client{Transport: membershipFailureTransport{}})}}
	for name, action := range membershipActions(adapter) {
		err := action(context.Background())
		var classified c.APIError
		if !errors.As(err, &classified) || !classified.Uncertain() {
			t.Fatalf("%s transport failure must be unknown", name)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "raw_private_error") {
			t.Fatal("sensitive error escaped")
		}
	}
	if logs.Len() == 0 || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "raw_private_error") {
		t.Fatal("missing or sensitive operation logs")
	}
}

// membershipActions builds side-effect calls for adapter using test identifiers;
// the returned functions propagate errors without exposing returned secret links.
func membershipActions(adapter Adapter) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"create": func(ctx context.Context) error {
			_, err := adapter.CreateChatInviteLink(ctx, "123:secret", c.CreateChatInviteLinkRequest{ChatID: -42})
			return err
		},
		"revoke": func(ctx context.Context) error {
			_, err := adapter.RevokeChatInviteLink(ctx, "123:secret", -42, "https://t.me/+secret")
			return err
		},
		"approve": func(ctx context.Context) error {
			return adapter.ApproveChatJoinRequest(ctx, "123:secret", -42, 9)
		},
		"ban": func(ctx context.Context) error {
			return adapter.BanChatMember(ctx, "123:secret", -42, 9)
		},
		"unban": func(ctx context.Context) error {
			return adapter.UnbanChatMember(ctx, "123:secret", -42, 9)
		},
		"decline": func(ctx context.Context) error {
			return adapter.DeclineChatJoinRequest(ctx, "123:secret", -42, 9)
		},
	}
}

// TestMembershipErrorClassification exercises actual HTTP refusals, throttling,
// server failures and invalid results; t reports outcome or secret leakage errors.
func TestMembershipErrorClassification(t *testing.T) {
	cases := []struct {
		name, body, kind string
		status, delay    int
		uncertain        bool
	}{
		{"forbidden", `{"ok":false,"error_code":403,"description":"123:secret raw_private_error"}`, "forbidden", 403, 0, false},
		{"bad_request", `{"ok":false,"error_code":400,"description":"123:secret raw_private_error"}`, "unknown", 400, 0, false},
		{"throttled", `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`, "retryable", 429, 17, false},
		{"server_error", `{"ok":false,"error_code":500}`, "unknown", 500, 0, true},
		{"malformed", `123:secret raw_private_error`, "unknown", 200, 0, true},
		{"null", `{"ok":true,"result":null}`, "unknown", 200, 0, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				writer.WriteHeader(item.status)
				fmt.Fprint(writer, item.body)
			}))
			defer server.Close()
			adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
			for name, action := range membershipActions(adapter) {
				err := action(context.Background())
				var classified *sendError
				if !errors.As(err, &classified) || classified.Uncertain() != item.uncertain || classified.Kind() != item.kind || classified.RetrySeconds() != item.delay {
					t.Fatalf("%s: wrong error classification: %#v", name, err)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "raw_private_error") || errors.Unwrap(err) != nil {
					t.Fatal("raw error or token escaped")
				}
			}
			if calls != len(membershipActions(adapter)) {
				t.Fatalf("unexpected retry: %d requests", calls)
			}
		})
	}
}

// TestMembershipTimeout keeps each request in flight until its deadline; t checks
// that writes remain unknown while the read-only member query can safely retry.
func TestMembershipTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Error(err)
			return
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL), api.WithTimeout(20 * time.Millisecond)}}
	for name, action := range membershipActions(adapter) {
		err := action(context.Background())
		var classified c.APIError
		if !errors.As(err, &classified) || !classified.Uncertain() {
			t.Fatalf("%s timeout must remain unknown: %v", name, err)
		}
	}
	_, err := adapter.GetChatMember(context.Background(), "123:secret", -42, 9)
	var classified *sendError
	if !errors.As(err, &classified) || classified.Uncertain() || classified.Kind() != "retryable" {
		t.Fatalf("read-only timeout must allow retry: %v", err)
	}
}

// TestMembershipFalseAndLocalFailures ensures explicit false and client setup
// failures never become success or ambiguous side effects; t receives failures.
func TestMembershipFalseAndLocalFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"ok":true,"result":false}`)
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	for _, action := range []func(context.Context) error{membershipActions(adapter)["approve"], membershipActions(adapter)["decline"]} {
		err := action(context.Background())
		var classified *sendError
		if !errors.As(err, &classified) || classified.Uncertain() || classified.Kind() != "refused" {
			t.Fatalf("expected explicit refusal: %v", err)
		}
	}
	adapter.Options = []api.Option{func(*api.Client) error { return errors.New("123:secret raw_private_error") }}
	for name, action := range membershipActions(adapter) {
		err := action(context.Background())
		var classified *sendError
		if !errors.As(err, &classified) || classified.Uncertain() || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: unsafe local failure", name)
		}
	}
}

// TestMembershipProjection verifies the adapter preserves invitation provenance
// and all status variants, with restricted membership independent of status.
func TestMembershipProjection(t *testing.T) {
	status, isMember := "restricted", true
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "getChatMember"):
			fmt.Fprintf(writer, `{"ok":true,"result":{"user":{"id":9},"status":%q,"is_member":%t,"until_date":2000000000,"can_send_messages":false}}`, status, isMember)
		case strings.HasSuffix(request.URL.Path, "revokeChatInviteLink"):
			fmt.Fprint(writer, `{"ok":true,"result":{"invite_link":"https://t.me/+test","creator":{"id":8,"is_bot":true},"creates_join_request":true,"is_revoked":true,"expire_date":2000000000}}`)
		default:
			fmt.Fprint(writer, `{"ok":true,"result":{"invite_link":"https://t.me/+test","creator":{"id":8,"is_bot":true},"creates_join_request":true,"expire_date":2000000000,"pending_join_request_count":3}}`)
		}
	}))
	defer server.Close()
	adapter := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	invite, err := adapter.CreateChatInviteLink(context.Background(), "123:secret", c.CreateChatInviteLinkRequest{ChatID: -42})
	if err != nil || invite.Creator.ID != 8 || !invite.Creator.IsBot || !invite.CreatesJoinRequest || invite.ExpireDate != 2000000000 || invite.PendingJoinRequestCount != 3 {
		t.Fatalf("lost invitation provenance: %v", err)
	}
	revoked, err := adapter.RevokeChatInviteLink(context.Background(), "123:secret", -42, invite.InviteLink)
	if err != nil || !revoked.IsRevoked || revoked.Creator.ID != 8 {
		t.Fatalf("lost revoked metadata: %v", err)
	}
	for _, current := range []string{"creator", "administrator", "member", "restricted", "left", "kicked"} {
		status = current
		for _, presence := range []bool{true, false} {
			isMember = presence
			member, err := adapter.GetChatMember(context.Background(), "123:secret", -42, 9)
			allowed, present := member.Permissions["can_send_messages"]
			if err != nil || member.Status != status || member.IsMember != presence || member.User.ID != 9 || !present || allowed {
				t.Fatalf("lost member metadata: %v", err)
			}
		}
	}
}

// TestMembershipUpdateProvenance checks the existing update mapping, including
// actor versus subject and expired/revoked invite fields, without changing it.
func TestMembershipUpdateProvenance(t *testing.T) {
	var raw api.Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"chat_member":{"chat":{"id":-42,"type":"supergroup"},"from":{"id":7},"date":100,"old_chat_member":{"user":{"id":9},"status":"left"},"new_chat_member":{"user":{"id":9},"status":"restricted","is_member":true},"invite_link":{"invite_link":"https://t.me/+test","creator":{"id":8},"creates_join_request":true,"is_revoked":true,"expire_date":50},"via_join_request":true}}`), &raw); err != nil {
		t.Fatal(err)
	}
	update := mapUpdate(raw)
	if update.Member == nil || update.UserID != 7 || update.Member.Operator.ID != 7 || update.Member.Subject.ID != 9 || !update.Member.New.IsMember || !update.Member.ViaJoinRequest {
		t.Fatal("member subject or actor lost")
	}
	invite := update.Member.InviteLink
	if invite == nil || invite.Creator.ID != 8 || invite.ExpireDate != 50 || !invite.IsRevoked {
		t.Fatal("invitation provenance lost")
	}
	if err := json.Unmarshal([]byte(`{"update_id":2,"chat_join_request":{"chat":{"id":-42,"type":"supergroup"},"from":{"id":9},"date":100,"user_chat_id":9007199254740,"invite_link":{"invite_link":"https://t.me/+test","creator":{"id":8},"creates_join_request":true,"expire_date":50,"is_revoked":true}}}`), &raw); err != nil {
		t.Fatal(err)
	}
	// Reset the prior union alternative because json.Unmarshal preserves absent fields.
	raw.ChatMember = nil
	update = mapUpdate(raw)
	if update.JoinRequest == nil || update.JoinRequest.User.ID != 9 || update.JoinRequest.UserChatID != 9007199254740 || update.JoinRequest.InviteLink.Creator.ID != 8 || update.ChatID != -42 {
		t.Fatal("join request identity or provenance lost")
	}
}
