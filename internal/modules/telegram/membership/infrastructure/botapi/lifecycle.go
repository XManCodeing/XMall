package botapi

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

var _ c.LifecyclePlatform = (*Access)(nil)

// BanMembershipMember removes the exact historical member after fresh routing and runtime fencing.
// Telegram can delete historical messages; this operation is not a lossless kick.
func (access *Access) BanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	return access.lifecycleEffect(ctx, input, true)
}

// UnbanMembershipMember restores admission only for banned members, never kicking a present member.
func (access *Access) UnbanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	return access.lifecycleEffect(ctx, input, false)
}

// lifecycleEffect bounds a single ban/unban request, preserving unknown outcomes and sanitized retry hints.
// input supplies the fenced route and historical member; ban selects removal versus safe restoration.
func (access *Access) lifecycleEffect(ctx context.Context, input c.AccessJoinAction, ban bool) c.AccessAPIResult {
	ctx, cancel := access.bounded(ctx)
	defer cancel()
	if access == nil || access.Lifecycle == nil || input.TelegramID <= 0 {
		return accessResult(c.AccessFailed, "invalid_request")
	}
	token, _, result := access.prepare(ctx, input.Route)
	if result.Outcome != c.AccessSucceeded {
		return result
	}
	var err error
	operation := "unban_member"
	if ban {
		operation = "ban_member"
		err = access.Lifecycle.BanChatMember(ctx, token, input.Route.ChatID, input.TelegramID)
	} else {
		err = access.Lifecycle.UnbanChatMember(ctx, token, input.Route.ChatID, input.TelegramID)
	}
	result = classifyAccessError(err, false)
	logAccessResult(operation, input.Route, result)
	return result
}
