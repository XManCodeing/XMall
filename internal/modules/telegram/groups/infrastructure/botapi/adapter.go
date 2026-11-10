package botapi

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"time"
)

type Cipher interface{ Open(string) (string, error) }
type Adapter struct {
	Store   r.Store
	Cipher  Cipher
	Options []telegramapi.Option
}

// Instance reads bot identity for bot; missing instances are permanent failures,
// while storage failures remain retryable and never expose storage error text.
func (a *Adapter) Instance(ctx context.Context, bot uint) (c.Identity, error) {
	i, err := a.Store.Get(ctx, bot)
	if errors.Is(err, r.ErrNotFound) || (err == nil && i == nil) {
		return c.Identity{}, c.ErrNotFound
	}
	if err != nil {
		return c.Identity{}, c.ErrCapabilityPending
	}
	return c.Identity{TelegramID: i.TelegramID, Generation: i.Generation, Running: i.Enabled && i.Mode == "internal" && i.State == "running" && i.LastSeenAt != nil && time.Since(*i.LastSeenAt) < 45*time.Second}, nil
}
func failure(err error) c.ProbeResult {
	out := c.ProbeResult{ErrorClass: "network"}
	var api *telegramapi.APIError
	if errors.As(err, &api) {
		out.RetryAfter = api.RetryAfter
		switch {
		case api.ErrorCode == 429 || api.HTTPStatus == 429:
			out.ErrorClass = "rate_limited"
		case api.ErrorCode == 401 || api.HTTPStatus == 401:
			out.ErrorClass = "authentication"
		case api.ErrorCode == 403 || api.HTTPStatus == 403:
			out.ErrorClass = "permission_denied"
		case api.ErrorCode == 400:
			out.ErrorClass = "resource_inaccessible"
		default:
			out.ErrorClass = "platform"
		}
	}
	return out
}
func (a *Adapter) Check(ctx context.Context, bot uint, chatID int64) c.ProbeResult {
	i, err := a.Store.Get(ctx, bot)
	if err != nil {
		return c.ProbeResult{ErrorClass: "instance_unavailable"}
	}
	identity, err := a.Instance(ctx, bot)
	if err != nil || !identity.Running {
		return c.ProbeResult{ErrorClass: "instance_stopped"}
	}
	token, err := a.Cipher.Open(i.TokenCipher)
	if err != nil {
		return c.ProbeResult{ErrorClass: "authentication"}
	}
	client, err := telegramapi.NewClient(token, a.Options...)
	if err != nil {
		return c.ProbeResult{ErrorClass: "authentication"}
	}
	chat, err := client.GetChat(ctx, chatID)
	if err != nil {
		return failure(err)
	}
	member, err := client.GetChatMember(ctx, chatID, i.TelegramID)
	if err != nil {
		return failure(err)
	}
	// Credentials can rotate or the running generation can disappear during HTTP.
	current, err := a.Store.Get(ctx, bot)
	if err != nil || current.Version != i.Version || current.Generation != i.Generation || !current.Enabled || current.State != "running" {
		return c.ProbeResult{ErrorClass: "generation_changed"}
	}
	if chat.ID != chatID || member.User.ID != i.TelegramID {
		return c.ProbeResult{ErrorClass: "invalid_response"}
	}
	result := c.ProbeResult{Observation: d.Observation{BotTelegramID: i.TelegramID, Generation: i.Generation, ChatType: chat.Type, Title: chat.Title, IsForum: chat.IsForum, MemberStatus: member.Status, IsMember: member.Status == "administrator" || member.Status == "creator" || member.Status == "member" || (member.Status == "restricted" && member.IsMember), CanManageTopics: member.Permissions["can_manage_topics"], CanInviteUsers: member.Permissions["can_invite_users"], CanRestrictMembers: member.Permissions["can_restrict_members"]}}
	// Only verified self membership is evidence of anonymous administration; API errors are not.
	if member.Status == "administrator" && member.IsAnonymous {
		result.ErrorClass = "anonymous_admin"
	}
	return result
}
