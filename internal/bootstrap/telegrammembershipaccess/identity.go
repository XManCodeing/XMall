package telegrammembershipaccess

import (
	"context"
	assembly "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershiprecovery"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	routes "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"strconv"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	externalidentity "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	users "github.com/XManCodeing/XMall/internal/modules/identity/user/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// Identity resolves a current authenticated account without creating a user or requiring a Bot contact.
// Stores are only called outside membership database transactions.
type Identity struct {
	Bindings externalidentity.Store
	Users    users.Store
}

// ForAccount returns the verified numeric Telegram binding for account; absent/disabled bindings fail closed.
// bot optionally scopes Bot entry points; zero is permitted for the independent website entry.
func (identity Identity) ForAccount(ctx context.Context, account, bot uint) (membership.AccessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return membership.AccessIdentity{}, err
	}
	if account == 0 || identity.Bindings == nil || identity.Users == nil {
		return membership.AccessIdentity{}, membership.ErrUntrustedIdentity
	}
	user, err := identity.Users.GetByID(account)
	if err != nil {
		return membership.AccessIdentity{}, err
	}
	if user == nil || user.ID != account || user.DeletedAt != nil || strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
		return membership.AccessIdentity{}, membership.ErrUntrustedIdentity
	}
	binding, err := identity.Bindings.GetByUserProvider(account, constants.UserOAuthProviderTelegram)
	if err != nil {
		return membership.AccessIdentity{}, err
	}
	if binding == nil || binding.UserID != account || binding.ID == 0 {
		return membership.AccessIdentity{}, membership.ErrUntrustedIdentity
	}
	telegramID, err := strconv.ParseInt(binding.ProviderUserID, 10, 64)
	if err != nil || telegramID <= 0 || strconv.FormatInt(telegramID, 10) != binding.ProviderUserID {
		return membership.AccessIdentity{}, membership.ErrUntrustedIdentity
	}
	if err := ctx.Err(); err != nil {
		return membership.AccessIdentity{}, err
	}
	return membership.AccessIdentity{AccountID: account, TelegramID: telegramID, BotID: bot}, nil
}

// AuthorizeMembership performs an early identity gate, not the sensitive authorization snapshot.
// The membership repository rechecks users/current binding and entitlement on one transaction;
// execution independently checks the current physical group's kicked state outside transactions.
func (identity Identity) AuthorizeMembership(ctx context.Context, actor membership.AccessIdentity, target uint) error {
	if actor.AccountID == 0 || actor.TelegramID <= 0 {
		return membership.ErrUntrustedIdentity
	}
	current, err := identity.ForAccount(ctx, actor.AccountID, actor.BotID)
	if err != nil {
		return err
	}
	if current.TelegramID != actor.TelegramID {
		return membership.ErrUntrustedIdentity
	}
	return nil
}

// RecoveryAccess is optional and serves only server-resolved same-Bot recovery routes.
// Public input contains account/target or a runtime update, never parent/route/admin IDs.
type RecoveryAccess interface {
	Available(context.Context, membership.AccessIdentity, uint) error
	Entry(context.Context, membership.AccessIdentity, uint) (*membership.AccessEntry, error)
	Receive(context.Context, uint, uint, runtime.Update) error
}

// Recovery resolves each target's actual parent before delegating task operations.
// Worker rechecks scopes, capabilities, sponsor RBAC and persisted inbox evidence.
type Recovery struct {
	Groups    groups.Store
	Parents   recovery.Store
	Worker    *routes.GroupRecovery
	BotWorker *routes.BotRecovery
}

// request derives routing from persisted target/parent; zero actor means system continuation.
func (bridge *Recovery) request(ctx context.Context, target uint) (routes.Request, error) {
	out := routes.Request{}
	if bridge == nil || bridge.Groups == nil || bridge.Parents == nil || bridge.Worker == nil || target == 0 {
		return out, membership.ErrCapability
	}
	group, err := bridge.Groups.Get(ctx, target)
	if err != nil {
		return out, err
	}
	if group == nil || group.RecoveryTaskID == 0 || group.Purpose != "membership" || !group.Enabled {
		return out, membership.ErrConflict
	}
	detail, err := bridge.Parents.Get(ctx, group.RecoveryTaskID)
	if err != nil {
		return out, err
	}
	snapshot := detail.Snapshot
	if detail.Task.ID != group.RecoveryTaskID || detail.Task.Kind != "group_replace" || detail.Task.ConfirmedAt == nil || detail.Task.ConfirmedActorID == 0 {
		return out, membership.ErrConflict
	}
	if snapshot.Old.TargetID != target || snapshot.New.TargetID != target || snapshot.Old.BotID == 0 || snapshot.Old.BotID != snapshot.New.BotID || snapshot.New.BotID != group.BotInstanceID || snapshot.Old.ChatID >= 0 || snapshot.New.ChatID >= 0 || snapshot.Old.ChatID == snapshot.New.ChatID || snapshot.New.ChatID != group.TelegramChatID || snapshot.ExpectedOld.Route+1 != group.RouteVersion || snapshot.ExpectedNew.Generation == 0 {
		return out, membership.ErrConflict
	}
	out = routes.Request{TaskID: detail.Task.ID, TargetID: target, BotID: group.BotInstanceID, OldChatID: snapshot.Old.ChatID, NewChatID: snapshot.New.ChatID, OldRouteVersion: snapshot.ExpectedOld.Route, NewRouteVersion: group.RouteVersion, Generation: snapshot.ExpectedNew.Generation, Purpose: "membership"}
	return out, nil
}

// Available checks identity/ledger and recovery guards without issuing invitations.
func (bridge *Recovery) Available(ctx context.Context, identity membership.AccessIdentity, target uint) error {
	if input, handled, err := bridge.botRequest(ctx, target); handled || err != nil {
		if err != nil {
			return err
		}
		return bridge.BotWorker.BotAvailable(ctx, input, identity)
	}
	input, err := bridge.request(ctx, target)
	if err != nil {
		return err
	}
	return bridge.Worker.Available(ctx, input, identity)
}

// Entry is website-only; original AccessService owns identity, ledger and decryption checks.
func (bridge *Recovery) Entry(ctx context.Context, identity membership.AccessIdentity, target uint) (*membership.AccessEntry, error) {
	if input, handled, err := bridge.botRequest(ctx, target); handled || err != nil {
		if err != nil {
			return nil, err
		}
		return bridge.BotWorker.BotEntry(ctx, input, identity)
	}
	input, err := bridge.request(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := bridge.Worker.Available(ctx, input, identity); err != nil {
		return nil, err
	}
	return bridge.Worker.Entry(ctx, input, identity)
}

// Receive requires original processing inbox payload and live runtime lease, with no reply.
func (bridge *Recovery) Receive(ctx context.Context, bot, target uint, update runtime.Update) error {
	input, err := bridge.request(ctx, target)
	if err != nil {
		return err
	}
	if input.BotID != bot || input.NewChatID != update.ChatID {
		return membership.ErrConflict
	}
	if update.JoinRequest != nil && time.Unix(update.JoinRequest.Date, 0).After(time.Now().Add(time.Minute)) {
		return membership.ErrInvalid
	}
	if update.Member != nil && time.Unix(update.Member.Date, 0).After(time.Now().Add(time.Minute)) {
		return membership.ErrInvalid
	}
	return bridge.Worker.ReceiveUpdate(ctx, input, update)
}

// botRequest resolves only immutable parent endpoint mappings; a stopped runtime
// has no Receive path. An authenticated website cannot submit a route or join event.
func (bridge *Recovery) botRequest(ctx context.Context, target uint) (membership.BotRouteRecoveryRequest, bool, error) {
	out := membership.BotRouteRecoveryRequest{}
	if bridge == nil || bridge.Groups == nil || bridge.Parents == nil {
		return out, false, membership.ErrCapability
	}
	group, err := bridge.Groups.Get(ctx, target)
	if err != nil {
		return out, false, err
	}
	if group == nil || group.RecoveryTaskID == 0 {
		return out, false, nil
	}
	detail, err := bridge.Parents.Get(ctx, group.RecoveryTaskID)
	if err != nil {
		return out, false, err
	}
	if detail.Task.Kind != "bot_replace" {
		return out, false, nil
	}
	if bridge.BotWorker == nil || group.Purpose != "membership" || !group.Enabled {
		return out, true, membership.ErrCapability
	}
	snapshot := detail.Snapshot
	route, err := assembly.ResolveBotRoute(snapshot, target)
	if err != nil {
		return out, true, err
	}
	if route.Purpose != "membership" || snapshot.New.BotID != group.BotInstanceID || route.DestinationChat() != group.TelegramChatID || route.ChatID == route.DestinationChat() {
		return out, true, membership.ErrConflict
	}
	out = membership.BotRouteRecoveryRequest{TaskID: group.RecoveryTaskID, TargetID: target, OldBotID: snapshot.Old.BotID, NewBotID: snapshot.New.BotID, OldChatID: route.ChatID, NewChatID: route.DestinationChat(), OldRouteVersion: route.RouteVersion, NewRouteVersion: route.RouteVersion + 1, OldVersion: snapshot.ExpectedOld.Config, NewVersion: snapshot.ExpectedNew.Config, OldGeneration: snapshot.ExpectedOld.Generation, NewGeneration: snapshot.ExpectedNew.Generation}
	if _, err := assembly.BotInventory(snapshot, out); err != nil {
		return out, true, err
	}
	if out.NewRouteVersion != group.RouteVersion {
		return out, true, membership.ErrConflict
	}
	return out, true, nil
}

// recoveryAvailable reads actual recovery availability without mutating or revealing an invite.
func (website *Website) recoveryAvailable(ctx context.Context, account uint, row membership.UserAccessGroup) bool {
	if website.Recovery == nil || website.Groups == nil {
		return false
	}
	identity, err := website.Identity.ForAccount(ctx, account, 0)
	if err != nil || identity.TelegramID != row.BeneficiaryTelegramID {
		return false
	}
	group, err := website.Groups.Get(ctx, row.TargetID)
	if err != nil || group == nil || group.RecoveryTaskID == 0 {
		return false
	}
	identity.BotID = group.BotInstanceID
	return website.Recovery.Available(ctx, identity, row.TargetID) == nil
}
