package telegramapi

import "context"

// BanChatMember removes userID from chatID, returning only confirmed success or a safe error.
// Telegram always deletes the user's messages in supergroups/channels regardless of
// revoke_messages; callers must retain that risk and must not promise lossless removal.
func (c *Client) BanChatMember(ctx context.Context, chatID, userID int64) error {
	return c.answerJoinRequest(ctx, "banChatMember", chatID, userID)
}

// UnbanChatMember restores admission for userID without removing a current member.
// only_if_banned is unconditional, not an optional caller-controlled flag.
func (c *Client) UnbanChatMember(ctx context.Context, chatID, userID int64) error {
	if chatID >= 0 || userID <= 0 {
		return invalid("invalid_content")
	}
	var result bool
	if err := c.callJSON(ctx, "unbanChatMember", struct {
		ChatID       int64 `json:"chat_id"`
		UserID       int64 `json:"user_id"`
		OnlyIfBanned bool  `json:"only_if_banned"`
	}{chatID, userID, true}, &result); err != nil {
		return err
	}
	if !result {
		return &APIError{HTTPStatus: 200, kind: "refused"}
	}
	return nil
}
