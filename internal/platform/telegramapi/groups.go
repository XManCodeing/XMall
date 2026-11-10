package telegramapi

import "context"

// Capability detection is read-only. This does not send a message, create a
// topic/invite, enumerate members, or change any group permission.
func (c *Client) GetChat(ctx context.Context, chatID int64) (Chat, error) {
	var result Chat
	err := c.callJSON(ctx, "getChat", struct {
		ChatID int64 `json:"chat_id"`
	}{chatID}, &result)
	return result, err
}
func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (ChatMember, error) {
	var result ChatMember
	err := c.callJSON(ctx, "getChatMember", struct {
		ChatID int64 `json:"chat_id"`
		UserID int64 `json:"user_id"`
	}{chatID, userID}, &result)
	return result, err
}
