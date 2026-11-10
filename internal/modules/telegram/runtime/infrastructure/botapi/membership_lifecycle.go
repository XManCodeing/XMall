package botapi

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

var _ c.MembershipLifecycleAPI = Adapter{}

// BanChatMember sends one removal for userID in chatID using token, returning a sanitized outcome.
// Network ambiguity is never retried here; Telegram's historical-message deletion risk remains.
func (adapter Adapter) BanChatMember(ctx context.Context, token string, chatID, userID int64) error {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return err
	}
	return membershipOperationError("ban_member", client.BanChatMember(ctx, chatID, userID), false)
}

// UnbanChatMember sends only-if-banned admission restoration and returns a sanitized error.
// The platform client hardcodes the safety flag, so callers cannot remove current members.
func (adapter Adapter) UnbanChatMember(ctx context.Context, token string, chatID, userID int64) error {
	client, err := adapter.membershipClient(token)
	if err != nil {
		return err
	}
	return membershipOperationError("unban_member", client.UnbanChatMember(ctx, chatID, userID), false)
}
