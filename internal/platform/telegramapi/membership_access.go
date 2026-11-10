package telegramapi

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// CreateChatInviteLinkRequest accepts only approval-based invitations. ExpireDate
// is a Unix timestamp (zero omits it); Name is at most 32 Unicode characters.
// There is intentionally no member_limit or switch to direct membership.
type CreateChatInviteLinkRequest struct {
	ChatID     int64  `json:"chat_id"`
	Name       string `json:"name,omitempty"`
	ExpireDate int64  `json:"expire_date,omitempty"`
}

// CreateChatInviteLink creates one additional bot-owned approval link for request.
// It returns structured metadata or a safe error, without retrying ambiguous calls.
func (c *Client) CreateChatInviteLink(ctx context.Context, request CreateChatInviteLinkRequest) (ChatInviteLink, error) {
	if request.ChatID >= 0 || request.ExpireDate < 0 || !utf8.ValidString(request.Name) || utf8.RuneCountInString(request.Name) > 32 {
		return ChatInviteLink{}, invalid("invalid_content")
	}
	var result ChatInviteLink
	err := c.callJSON(ctx, "createChatInviteLink", struct {
		CreateChatInviteLinkRequest
		CreatesJoinRequest bool `json:"creates_join_request"`
	}{request, true}, &result)
	if err != nil {
		return ChatInviteLink{}, err
	}
	if strings.TrimSpace(result.InviteLink) == "" || result.Creator.ID <= 0 || !result.CreatesJoinRequest || result.IsPrimary || result.IsRevoked || result.MemberLimit != 0 {
		return ChatInviteLink{}, errors.New("telegram: invalid invitation result")
	}
	return result, nil
}

// RevokeChatInviteLink revokes inviteLink in chatID and returns the revoked
// metadata. Telegram permits managing only this bot's links; never retry here.
func (c *Client) RevokeChatInviteLink(ctx context.Context, chatID int64, inviteLink string) (ChatInviteLink, error) {
	if chatID >= 0 || strings.TrimSpace(inviteLink) == "" || !utf8.ValidString(inviteLink) {
		return ChatInviteLink{}, invalid("invalid_content")
	}
	var result ChatInviteLink
	err := c.callJSON(ctx, "revokeChatInviteLink", struct {
		ChatID     int64  `json:"chat_id"`
		InviteLink string `json:"invite_link"`
	}{chatID, inviteLink}, &result)
	if err != nil {
		return ChatInviteLink{}, err
	}
	if result.InviteLink != inviteLink || result.Creator.ID <= 0 || !result.IsRevoked {
		return ChatInviteLink{}, errors.New("telegram: invalid revocation result")
	}
	return result, nil
}

// ApproveChatJoinRequest approves userID's request in chatID, requiring admin
// can_invite_users. Nil confirms approval, not a separate membership observation.
func (c *Client) ApproveChatJoinRequest(ctx context.Context, chatID, userID int64) error {
	return c.answerJoinRequest(ctx, "approveChatJoinRequest", chatID, userID)
}

// DeclineChatJoinRequest declines userID's request in chatID, requiring admin
// can_invite_users. Nil confirms rejection; transport failure leaves it unknown.
func (c *Client) DeclineChatJoinRequest(ctx context.Context, chatID, userID int64) error {
	return c.answerJoinRequest(ctx, "declineChatJoinRequest", chatID, userID)
}

// answerJoinRequest sends one trusted method for chatID/userID and returns only
// safe errors. A decoded false is a definite refusal, not an ambiguous timeout.
func (c *Client) answerJoinRequest(ctx context.Context, method string, chatID, userID int64) error {
	if chatID >= 0 || userID <= 0 {
		return invalid("invalid_content")
	}
	var result bool
	if err := c.callJSON(ctx, method, struct {
		ChatID int64 `json:"chat_id"`
		UserID int64 `json:"user_id"`
	}{chatID, userID}, &result); err != nil {
		return err
	}
	if !result {
		return &APIError{HTTPStatus: 200, kind: "refused"}
	}
	return nil
}
