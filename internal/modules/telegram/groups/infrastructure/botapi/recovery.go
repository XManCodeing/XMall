package botapi

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"time"
)

// ProbeRecovery performs only getMe/getChat/getChatMember outside any database transaction.
// in must own the drained destination fence before and after HTTP. It admits no generation.
func (a *Adapter) ProbeRecovery(ctx context.Context, in c.RecoveryProbeInput) (d.Observation, error) {
	empty := d.Observation{}
	store, ok := a.Store.(r.RecoveryFenceStore)
	if !ok || in.TaskID == 0 || in.BotID == 0 || in.TargetID == 0 || in.ChatID >= 0 || in.TelegramID <= 0 {
		return empty, c.ErrInvalid
	}
	fence := r.RecoveryFence{TaskID: in.TaskID, BotID: in.BotID, Version: in.Version, Generation: in.Generation}
	if err := store.RecoveryDrained(ctx, fence); err != nil {
		return empty, err
	}
	instance, err := a.Store.Get(ctx, in.BotID)
	if err != nil {
		return empty, err
	}
	if instance.TelegramID != in.TelegramID || instance.Enabled || instance.Mode != "internal" {
		return empty, c.ErrConflict
	}
	token, err := a.Cipher.Open(instance.TokenCipher)
	if err != nil {
		return empty, c.ErrCapability
	}
	client, err := telegramapi.NewClient(token, a.Options...)
	if err != nil {
		return empty, c.ErrCapability
	}
	identity, err := client.GetMe(ctx)
	if err != nil {
		return empty, c.ErrCapability
	}
	if identity.ID != in.TelegramID {
		return empty, c.ErrConflict
	}
	chat, err := client.GetChat(ctx, in.ChatID)
	if err != nil {
		return empty, c.ErrCapability
	}
	member, err := client.GetChatMember(ctx, in.ChatID, in.TelegramID)
	if err != nil {
		return empty, c.ErrCapability
	}
	if err := store.RecoveryDrained(ctx, fence); err != nil {
		return empty, err
	}
	if chat.ID != in.ChatID || member.User.ID != in.TelegramID {
		return empty, c.ErrCapability
	}
	now := time.Now().UTC()
	observation := d.Observation{BotTelegramID: in.TelegramID, Generation: in.Generation, RouteVersion: in.RouteVersion,
		ChatType: chat.Type, Title: chat.Title, IsForum: chat.IsForum, MemberStatus: member.Status,
		IsMember:        member.Status == "administrator" || member.Status == "creator",
		CanManageTopics: member.Permissions["can_manage_topics"], CanInviteUsers: member.Permissions["can_invite_users"], CanRestrictMembers: member.Permissions["can_restrict_members"],
		State: "ready", Source: "recovery", CheckedAt: &now, ObservedAt: &now, LastSuccessAt: &now}
	// Recovery must not bypass the same verified-self anonymous administrator gate.
	if member.Status == "administrator" && member.IsAnonymous {
		observation.State, observation.ErrorClass = "missing_permissions", "anonymous_admin"
		observation.LastSuccessAt, observation.LastFailureAt = nil, &now
	}
	return observation, nil
}
