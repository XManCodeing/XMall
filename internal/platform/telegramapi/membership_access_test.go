package telegramapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestMembershipAccessTransport checks JSON parameters and structured responses
// for the supplied test client; failures are reported through t.
func TestMembershipAccessTransport(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Error("expected JSON POST")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["chat_id"] != float64(-10042) {
			t.Error("wrong physical chat")
		}
		method := strings.TrimPrefix(request.URL.Path, "/bot123:dummy_secret/")
		switch method {
		case "createChatInviteLink":
			if body["creates_join_request"] != true || body["name"] != "会员" || body["expire_date"] != float64(2000000000) {
				t.Error("wrong invitation parameters")
			}
			if _, exists := body["member_limit"]; exists {
				t.Error("approval link must omit member_limit")
			}
			fmt.Fprint(writer, `{"ok":true,"result":{"invite_link":"https://t.me/+test","creator":{"id":9,"is_bot":true},"creates_join_request":true,"expire_date":2000000000,"name":"会员","pending_join_request_count":2}}`)
		case "revokeChatInviteLink":
			if body["invite_link"] != "https://t.me/+test" {
				t.Error("wrong invitation")
			}
			fmt.Fprint(writer, `{"ok":true,"result":{"invite_link":"https://t.me/+test","creator":{"id":9},"creates_join_request":true,"is_revoked":true}}`)
		case "approveChatJoinRequest", "declineChatJoinRequest", "getChatMember":
			if body["user_id"] != float64(9007199254740) || len(body) != 2 {
				t.Error("wrong user parameters")
			}
			if method == "getChatMember" {
				fmt.Fprint(writer, `{"ok":true,"result":{"status":"restricted","user":{"id":9007199254740},"is_member":true,"can_send_messages":false}}`)
			} else {
				fmt.Fprint(writer, `{"ok":true,"result":true}`)
			}
		default:
			t.Error("unexpected method")
		}
	})
	ctx := context.Background()
	invite, err := client.CreateChatInviteLink(ctx, CreateChatInviteLinkRequest{ChatID: -10042, Name: "会员", ExpireDate: 2000000000})
	if err != nil || invite.Creator.ID != 9 || invite.ExpireDate != 2000000000 || invite.PendingJoinRequestCount != 2 {
		t.Fatalf("invite metadata missing: %v", err)
	}
	revoked, err := client.RevokeChatInviteLink(ctx, -10042, invite.InviteLink)
	if err != nil || !revoked.IsRevoked {
		t.Fatalf("revocation failed: %v", err)
	}
	if err := client.ApproveChatJoinRequest(ctx, -10042, 9007199254740); err != nil {
		t.Fatal(err)
	}
	if err := client.DeclineChatJoinRequest(ctx, -10042, 9007199254740); err != nil {
		t.Fatal(err)
	}
	member, err := client.GetChatMember(ctx, -10042, 9007199254740)
	if err != nil || member.Status != "restricted" || !member.IsMember || member.Permissions["can_send_messages"] {
		t.Fatalf("member metadata missing: %v", err)
	}
}

// TestGetChatMemberAnonymousFlag checks true, false and absent flags through HTTP decoding.
// Parameter t receives failures; the test returns no value.
func TestGetChatMemberAnonymousFlag(t *testing.T) {
	for _, field := range []string{`,"is_anonymous":true`, `,"is_anonymous":false`, ""} {
		t.Run(field, func(t *testing.T) {
			client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
				if _, err := fmt.Fprintf(writer, `{"ok":true,"result":{"status":"administrator","user":{"id":9},"can_invite_users":true%s}}`, field); err != nil {
					t.Error(err)
				}
			})
			member, err := client.GetChatMember(context.Background(), -42, 9)
			if err != nil || member.IsAnonymous != (field == `,"is_anonymous":true`) || !member.Permissions["can_invite_users"] {
				t.Fatalf("anonymous flag decoding: %+v %v", member, err)
			}
		})
	}
}

// TestMembershipFalseResult verifies false is an explicit platform refusal for
// both approval actions; t receives any unexpected classification.
func TestMembershipFalseResult(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"ok":true,"result":false}`)
	})
	for _, action := range []func(context.Context, int64, int64) error{client.ApproveChatJoinRequest, client.DeclineChatJoinRequest} {
		err := action(context.Background(), -42, 9)
		var platform *APIError
		if !errors.As(err, &platform) || platform.Kind() != "refused" {
			t.Fatalf("expected explicit refusal, got %v", err)
		}
	}
}

// TestMembershipValidation ensures invalid inputs fail before HTTP and cannot
// include supplied values in errors; the test reports all failures through t.
func TestMembershipValidation(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		t.Error("invalid input reached HTTP")
	})
	for _, request := range []CreateChatInviteLinkRequest{
		{ChatID: 0}, {ChatID: 42}, {ChatID: -42, Name: strings.Repeat("界", 33)},
		{ChatID: -42, Name: string([]byte{0xff})}, {ChatID: -42, ExpireDate: -1},
	} {
		_, err := client.CreateChatInviteLink(context.Background(), request)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("expected validation error: %v", err)
		}
	}
	if _, err := client.RevokeChatInviteLink(context.Background(), -42, " "); err == nil {
		t.Error("empty link accepted")
	}
	if err := client.ApproveChatJoinRequest(context.Background(), -42, 0); err == nil {
		t.Error("invalid user accepted")
	}
}
