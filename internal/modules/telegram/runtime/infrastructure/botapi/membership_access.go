package botapi

import (
	"context"
	"errors"
	"log/slog"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

var _ c.MembershipAccessAPI = Adapter{}
var _ c.MembershipQueryAPI = Adapter{}

// CreateChatInviteLink sends one approval-link request using token and returns
// the existing InviteLink projection. The caller supplies all execution fencing.
func (adapter Adapter) CreateChatInviteLink(ctx context.Context, token string, request c.CreateChatInviteLinkRequest) (c.InviteLink, error) {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return c.InviteLink{}, err
	}
	result, err := client.CreateChatInviteLink(ctx, api.CreateChatInviteLinkRequest{ChatID: request.ChatID, Name: request.Name, ExpireDate: request.ExpireDate})
	if err != nil {
		return c.InviteLink{}, membershipOperationError("create_invite", err, false)
	}
	return *inviteRef(&result), nil
}

// RevokeChatInviteLink uses token to revoke inviteLink in chatID and returns
// its structured state. It does not assume that another bot owns the link.
func (adapter Adapter) RevokeChatInviteLink(ctx context.Context, token string, chatID int64, inviteLink string) (c.InviteLink, error) {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return c.InviteLink{}, err
	}
	result, err := client.RevokeChatInviteLink(ctx, chatID, inviteLink)
	if err != nil {
		return c.InviteLink{}, membershipOperationError("revoke_invite", err, false)
	}
	return *inviteRef(&result), nil
}

// ApproveChatJoinRequest approves userID in chatID using token, returning a safe
// error or nil. Approval does not write or imply an observed member record.
func (adapter Adapter) ApproveChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return err
	}
	return membershipOperationError("approve_join", client.ApproveChatJoinRequest(ctx, chatID, userID), false)
}

// DeclineChatJoinRequest rejects userID in chatID using token, returning nil on
// confirmed rejection. Unknown outcomes remain unknown without blind retries.
func (adapter Adapter) DeclineChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return err
	}
	return membershipOperationError("decline_join", client.DeclineChatJoinRequest(ctx, chatID, userID), false)
}

// GetChatMember reuses the existing client query for userID in chatID and returns
// the full status projection, preserving restricted.is_member and permissions.
func (adapter Adapter) GetChatMember(ctx context.Context, token string, chatID, userID int64) (c.ChatMember, error) {
	if chatID >= 0 || userID <= 0 {
		return c.ChatMember{}, &sendError{code: 400, kind: "invalid_content"}
	}
	client, err := adapter.membershipClient(token)
	if err != nil {
		return c.ChatMember{}, err
	}
	result, err := client.GetChatMember(ctx, chatID, userID)
	if err == nil && (result.User.ID != userID || !validMemberStatus(result.Status)) {
		err = errors.New("telegram: invalid member result")
	}
	if err != nil {
		return c.ChatMember{}, membershipOperationError("get_member", err, true)
	}
	return memberRef(result), nil
}

// membershipClient constructs the existing client from token and adapter options;
// failures are safe local rejections, never raw option errors containing secrets.
func (adapter Adapter) membershipClient(token string) (*api.Client, error) {
	client, err := adapter.client(token)
	if err != nil {
		return nil, &sendError{code: 400, kind: "invalid_content"}
	}
	return client, nil
}

// validMemberStatus accepts Telegram's documented status for a member query and
// returns false for missing or unsupported values rather than inventing absence.
func validMemberStatus(status string) bool {
	switch status {
	case "creator", "administrator", "member", "restricted", "left", "kicked":
		return true
	default:
		return false
	}
}

// membershipOperationError reuses shared error classification for err. Read-only
// query failures can retry; side-effect failures remain uncertain except explicit
// platform refusal. Logs contain only the fixed operation and safe classifications.
func membershipOperationError(operation string, err error, readOnly bool) error {
	result := classifyOperationError(err)
	if result == nil {
		return nil
	}
	var upstream *api.APIError
	if errors.As(err, &upstream) && upstream.Kind() == "refused" {
		result.unknown = false
	}
	if readOnly && result.unknown {
		result.unknown, result.kind = false, "retryable"
	}
	slog.Warn("telegram_membership_operation_failed", "operation", operation, "kind", result.Kind(), "status", result.code, "uncertain", result.unknown)
	return result
}
