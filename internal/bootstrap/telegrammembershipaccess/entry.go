package telegrammembershipaccess

import (
	"context"
	"errors"
	"fmt"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"strconv"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershiphttp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/transport/http"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// Website maps authenticated account reads to the shared membership service; all network work remains in runtime.
type Website struct {
	Identity Identity
	Reader   membership.UserAccessReader
	Access   membership.AccessReader
	Invites  membership.AccessRepository
	Groups   groups.Store
	Recovery RecoveryAccess
}

// List returns a stable account-scoped page, including processing and identity-mismatch history without secrets.
func (website *Website) List(ctx context.Context, account, cursor uint, limit int) (membershiphttp.GroupPage, error) {
	if website.Reader == nil || account == 0 || limit < 1 || limit > 100 {
		return membershiphttp.GroupPage{}, membershiphttp.ErrAccessDenied
	}
	rows, err := website.Reader.ListUserAccess(ctx, account, cursor, limit, time.Now().UTC())
	if err != nil {
		return membershiphttp.GroupPage{}, err
	}
	page := membershiphttp.GroupPage{Items: make([]membershiphttp.GroupItem, 0, len(rows))}
	for _, row := range rows {
		if website.Groups != nil {
			group, err := website.Groups.Get(ctx, row.TargetID)
			if err != nil {
				return membershiphttp.GroupPage{}, err
			}
			if group != nil && group.RecoveryTaskID != 0 {
				row.CanRequest = website.recoveryAvailable(ctx, account, row)
			}
		}
		page.Items = append(page.Items, membershiphttp.GroupItem{TargetID: strconv.FormatUint(uint64(row.TargetID), 10), Title: row.Title, BeneficiaryTelegramID: strconv.FormatInt(row.BeneficiaryTelegramID, 10), Permanent: row.Permanent, ExpiresAt: row.ExpiresAt, State: row.State, MemberStatus: row.MemberStatus, CanRequest: row.CanRequest})
	}
	if len(rows) == limit {
		page.NextCursor = strconv.FormatUint(uint64(rows[len(rows)-1].TargetID), 10)
	}
	return page, nil
}

// Request derives the beneficiary from the current binding and resolves the target's actual bot before enqueueing.
// The submitted target never authorizes cross-account access; the shared service rechecks historical ownership.
func (website *Website) Request(ctx context.Context, account, target uint) (membershiphttp.InvitationResult, error) {
	return website.request(ctx, account, target, 0)
}

// request optionally limits a Bot entry to its own route; zero selects website routing.
func (website *Website) request(ctx context.Context, account, target, expectedBot uint) (membershiphttp.InvitationResult, error) {
	if target == 0 || website.Groups == nil || website.Access == nil {
		return membershiphttp.InvitationResult{}, membershiphttp.ErrAccessDenied
	}
	identity, err := website.Identity.ForAccount(ctx, account, expectedBot)
	if err != nil {
		return membershiphttp.InvitationResult{}, membershiphttp.ErrAccessDenied
	}
	group, err := website.Groups.Get(ctx, target)
	if err != nil || group == nil || group.Purpose != "membership" || (expectedBot != 0 && expectedBot != group.BotInstanceID) {
		return membershiphttp.InvitationResult{}, membershiphttp.ErrAccessDenied
	}
	identity.BotID = group.BotInstanceID
	var entry *membership.AccessEntry
	if group.RecoveryTaskID != 0 {
		// Bot notifications cannot borrow website recovery authorization or secrets.
		if expectedBot != 0 || website.Recovery == nil {
			return membershiphttp.InvitationResult{}, membership.ErrCapability
		}
		entry, err = website.Recovery.Entry(ctx, identity, target)
	} else {
		entry, err = website.Access.RequestMembershipInvite(ctx, identity, target)
	}
	if errors.Is(err, membership.ErrUntrustedIdentity) || errors.Is(err, membership.ErrNotFound) {
		return membershiphttp.InvitationResult{}, membershiphttp.ErrAccessDenied
	}
	if err != nil {
		return membershiphttp.InvitationResult{}, err
	}
	if entry == nil {
		return membershiphttp.InvitationResult{}, membership.ErrCapability
	}
	result := membershiphttp.InvitationResult{InviteID: entry.InviteID, State: publicInviteState(entry.InviteState)}
	if result.State == "available" {
		if website.Invites == nil {
			return membershiphttp.InvitationResult{}, membership.ErrCapability
		}
		invite, err := website.Invites.Invite(ctx, entry.InviteID)
		if err != nil || invite == nil || invite.AccountID != account || invite.TelegramID != identity.TelegramID || invite.TargetID != target || invite.State != "active" || !d.IsInviteUsableAt(*invite, time.Now().UTC()) {
			return membershiphttp.InvitationResult{}, membership.ErrCapability
		}
		result.InviteURL = entry.Link
		if invite.Term != d.InviteTermLongTerm {
			result.ExpiresAt = &invite.ExpiresAt
		}
	}
	return result, nil
}

// publicInviteState translates persisted execution state without conflating entitlement and actual admission.
func publicInviteState(state string) string {
	switch state {
	case "active":
		return "available"
	case "pending", "creating":
		return "processing"
	case "failed", "retryable":
		return "recovering"
	case "unknown":
		return "unknown"
	case "expired", "revoked":
		return state
	default:
		return "unavailable"
	}
}

// Bot delegates through the same account projection while independently enforcing the current identity generation.
type Bot struct {
	Website       *Website
	Deliveries    rt.InviteDeliveryReader
	DeliveryGuard rt.InviteDeliveryAuthorizer
}

// authorize checks the canonical current binding generation used by the shop; historical account access is insufficient.
func (bot *Bot) authorize(ctx context.Context, actor shop.Actor) error {
	if bot.Website == nil || actor.BotID == 0 || actor.TelegramID <= 0 || actor.UserID == 0 || actor.Generation == "" {
		return shop.ErrDenied
	}
	identity, err := bot.Website.Identity.ForAccount(ctx, actor.UserID, actor.BotID)
	if err != nil || identity.TelegramID != actor.TelegramID {
		return shop.ErrDenied
	}
	binding, err := bot.Website.Identity.Bindings.GetByUserProvider(actor.UserID, "telegram")
	if err != nil {
		return err
	}
	if binding == nil || fmt.Sprintf("%d:%d", binding.ID, actor.UserID) != actor.Generation {
		return shop.ErrDenied
	}
	return nil
}

// List reads only current-Bot/current-beneficiary entries, using bounded account pages to avoid cross-Bot disclosure.
// The returned cursor always names the last displayed target, matching the shop's signed-panel pagination contract.
func (bot *Bot) List(ctx context.Context, actor shop.Actor, cursor uint, limit int) (shop.MembershipPage, error) {
	if err := bot.authorize(ctx, actor); err != nil {
		return shop.MembershipPage{}, err
	}
	if limit < 1 || limit > 100 || bot.Website.Groups == nil {
		return shop.MembershipPage{}, shop.ErrDenied
	}
	result := shop.MembershipPage{Items: []shop.MembershipGroup{}}
	for scanned := 0; scanned < 20; scanned++ {
		page, err := bot.Website.List(ctx, actor.UserID, cursor, limit)
		if err != nil {
			return shop.MembershipPage{}, err
		}
		for _, row := range page.Items {
			target, err := strconv.ParseUint(row.TargetID, 10, strconv.IntSize)
			if err != nil || uint(target) <= cursor {
				return shop.MembershipPage{}, membership.ErrInvalid
			}
			group, err := bot.Website.Groups.Get(ctx, uint(target))
			if err != nil {
				return shop.MembershipPage{}, err
			}
			if group == nil || group.BotInstanceID != actor.BotID || group.Purpose != "membership" || row.BeneficiaryTelegramID != strconv.FormatInt(actor.TelegramID, 10) {
				continue
			}
			if group.RecoveryTaskID != 0 {
				row.CanRequest = false
			}
			result.Items = append(result.Items, shop.MembershipGroup{TargetID: row.TargetID, Title: row.Title, BeneficiaryTelegramID: row.BeneficiaryTelegramID, Permanent: row.Permanent, ExpiresAt: row.ExpiresAt, State: row.State, MemberStatus: row.MemberStatus, CanRequest: row.CanRequest})
			if len(result.Items) == limit {
				result.NextCursor = row.TargetID
				return result, nil
			}
		}
		if page.NextCursor == "" {
			return result, nil
		}
		next, err := strconv.ParseUint(page.NextCursor, 10, strconv.IntSize)
		if err != nil || uint(next) <= cursor {
			return shop.MembershipPage{}, membership.ErrInvalid
		}
		cursor = uint(next)
	}
	// Do not pretend a bounded partial scan is a complete empty membership list.
	return shop.MembershipPage{}, shop.ErrUnavailable
}

// Request requires an explicit signed shop action and revalidates the binding before delegating to the same durable queue.
func (bot *Bot) Request(ctx context.Context, actor shop.Actor, target uint) (shop.MembershipInvitation, error) {
	if err := bot.authorize(ctx, actor); err != nil {
		return shop.MembershipInvitation{}, err
	}
	result, err := bot.Website.request(ctx, actor.UserID, target, actor.BotID)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	out := shop.MembershipInvitation{State: result.State, InviteURL: result.InviteURL, ExpiresAt: result.ExpiresAt}
	if out.State != "available" {
		return out, nil
	}
	preparer, ok := bot.Website.Access.(interface {
		PrepareInviteDelivery(context.Context, membership.AccessIdentity, uint) (*rt.InviteDeliveryRef, error)
	})
	if !ok || bot.Deliveries == nil {
		return shop.MembershipInvitation{}, shop.ErrUnavailable
	}
	status, err := bot.Deliveries.InviteDelivery(ctx, actor.BotID, result.InviteID)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	// An uncertain or in-flight notification is not permission to send it again.
	if status.State == "unknown" || status.State == "sending" || status.State == "queued" {
		return shop.MembershipInvitation{State: "unknown"}, nil
	}
	out.InviteDelivery, err = preparer.PrepareInviteDelivery(ctx, membership.AccessIdentity{AccountID: actor.UserID, TelegramID: actor.TelegramID, BotID: actor.BotID}, result.InviteID)
	if err == nil {
		invite, readErr := bot.Website.Invites.Invite(ctx, result.InviteID)
		if readErr != nil {
			return shop.MembershipInvitation{}, readErr
		}
		if invite == nil {
			return shop.MembershipInvitation{}, shop.ErrUnavailable
		}
		out.Permanent = invite.Term == d.InviteTermLongTerm
	}
	return out, err
}

// GetExisting reads an already persisted invitation for the current authenticated actor.
// It never creates platform work; returned links still require send-time authorization.
func (bot *Bot) GetExisting(ctx context.Context, actor shop.Actor, target uint) (shop.MembershipInvitation, error) {
	if err := bot.authorize(ctx, actor); err != nil {
		return shop.MembershipInvitation{}, err
	}
	if target == 0 || bot.Website.Groups == nil || bot.Website.Invites == nil || bot.Deliveries == nil {
		return shop.MembershipInvitation{}, shop.ErrUnavailable
	}
	group, err := bot.Website.Groups.Get(ctx, target)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	if group == nil || group.BotInstanceID != actor.BotID || group.Purpose != "membership" || group.RecoveryTaskID != 0 {
		return shop.MembershipInvitation{}, shop.ErrDenied
	}
	reader, ok := bot.Website.Access.(interface {
		ExistingMembershipInvite(context.Context, membership.AccessIdentity, uint) (*membership.AccessEntry, error)
		PrepareInviteDelivery(context.Context, membership.AccessIdentity, uint) (*rt.InviteDeliveryRef, error)
	})
	if !ok {
		return shop.MembershipInvitation{}, shop.ErrUnavailable
	}
	identity := membership.AccessIdentity{AccountID: actor.UserID, TelegramID: actor.TelegramID, BotID: actor.BotID}
	entry, err := reader.ExistingMembershipInvite(ctx, identity, target)
	if errors.Is(err, membership.ErrNotFound) {
		return shop.MembershipInvitation{State: "unavailable"}, nil
	}
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	if entry == nil {
		return shop.MembershipInvitation{}, shop.ErrUnavailable
	}
	result := shop.MembershipInvitation{State: publicInviteState(entry.InviteState)}
	// This DTO describes the invitation, not why entitlement is inactive.
	// The account/order projection supplies expired versus revoked membership facts.
	if !entry.Active || entry.InviteID == 0 {
		return shop.MembershipInvitation{State: "unavailable"}, nil
	}
	if result.State != "available" || entry.Link == "" {
		return result, nil
	}
	status, err := bot.Deliveries.InviteDelivery(ctx, actor.BotID, entry.InviteID)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	if status.State == "unknown" || status.State == "queued" || status.State == "sending" {
		return shop.MembershipInvitation{State: "unknown"}, nil
	}
	invite, err := bot.Website.Invites.Invite(ctx, entry.InviteID)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	if invite == nil || invite.TargetID != target {
		return shop.MembershipInvitation{}, shop.ErrDenied
	}
	result.InviteDelivery, err = reader.PrepareInviteDelivery(ctx, identity, entry.InviteID)
	if err != nil {
		return shop.MembershipInvitation{}, err
	}
	result.InviteURL = entry.Link
	result.Permanent = invite.Term == d.InviteTermLongTerm
	if !result.Permanent {
		result.ExpiresAt = &invite.ExpiresAt
	}
	return result, nil
}

// Targets resolves only this order's immutable membership targets for the current actor.
// Historical order visibility never grants invitation access. No invitation is requested.
func (bot *Bot) Targets(ctx context.Context, actor shop.Actor, order, owner uint) ([]shop.MembershipGroup, error) {
	if err := bot.authorize(ctx, actor); err != nil {
		return nil, err
	}
	if order == 0 || owner != actor.UserID || bot.Website.Reader == nil || bot.Website.Groups == nil {
		return nil, shop.ErrDenied
	}
	targets, err := bot.orderHistory(ctx, actor, order)
	if err != nil {
		return nil, err
	}
	if len(targets) > 100 {
		return nil, shop.ErrUnavailable
	}
	result := make([]shop.MembershipGroup, 0, len(targets))
	seen := make(map[uint]bool, len(targets))
	for _, target := range targets {
		if seen[target.TargetID] {
			continue
		}
		seen[target.TargetID] = true
		row, err := bot.orderTarget(ctx, actor, target)
		if err != nil {
			return nil, err
		}
		if row != nil {
			result = append(result, *row)
		}
	}
	return result, nil
}

// orderHistory prefers the optional snapshot projection and keeps old reader adapters compatible.
// Both paths authorize through the repository; missing history never invents a migration reminder.
func (bot *Bot) orderHistory(ctx context.Context, actor shop.Actor, order uint) ([]membership.OrderMembershipTarget, error) {
	if reader, ok := bot.Website.Reader.(membership.OrderMembershipHistoryReader); ok {
		return reader.OrderMembershipHistory(ctx, actor.UserID, actor.TelegramID, order)
	}
	reader, ok := bot.Website.Reader.(membership.OrderAccessReader)
	if !ok {
		return nil, shop.ErrUnavailable
	}
	targets, err := reader.OrderMembershipTargets(ctx, actor.UserID, actor.TelegramID, order)
	if err != nil {
		return nil, err
	}
	out := make([]membership.OrderMembershipTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, membership.OrderMembershipTarget{TargetID: target})
	}
	return out, nil
}

// orderTarget projects one authorized target without requesting an invitation.
// A different current Bot or beneficiary yields no entry, never all memberships.
func (bot *Bot) orderTarget(ctx context.Context, actor shop.Actor, history membership.OrderMembershipTarget) (*shop.MembershipGroup, error) {
	target := history.TargetID
	if target == 0 {
		return nil, membership.ErrInvalid
	}
	group, err := bot.Website.Groups.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	if group == nil || group.BotInstanceID != actor.BotID || group.Purpose != "membership" {
		return nil, nil
	}
	rows, err := bot.Website.Reader.ListUserAccess(ctx, actor.UserID, target-1, 1, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].TargetID != target || rows[0].BeneficiaryTelegramID != actor.TelegramID {
		return nil, nil
	}
	row := rows[0]
	result := &shop.MembershipGroup{TargetID: strconv.FormatUint(uint64(target), 10), Title: row.Title,
		BeneficiaryTelegramID: strconv.FormatInt(row.BeneficiaryTelegramID, 10), Permanent: row.Permanent,
		ExpiresAt: row.ExpiresAt, State: row.State, MemberStatus: row.MemberStatus,
		CanRequest: row.CanRequest && group.RecoveryTaskID == 0}
	seen := make(map[membership.OrderMembershipRoute]bool)
	for _, route := range history.Routes {
		// An incomplete legacy snapshot cannot prove replacement. Never infer it
		// from another order, previous invitations, or global target history.
		if route.ChatID >= 0 || route.BotInstanceID == 0 || route.RouteVersion == 0 || seen[route] {
			continue
		}
		seen[route] = true
		if route.BotInstanceID != group.BotInstanceID || route.ChatID != group.TelegramChatID || route.RouteVersion != group.RouteVersion {
			result.Replaced = true
			result.HistoricalChats = append(result.HistoricalChats, fmt.Sprintf("%d (routeVersion=%d)", route.ChatID, route.RouteVersion))
		}
	}
	return result, nil
}

// AuthorizeInviteDelivery delegates only to the installed membership guard, never to website retrieval.
func (bot *Bot) AuthorizeInviteDelivery(ctx context.Context, id uint, message rt.Message) error {
	if bot.DeliveryGuard == nil {
		return shop.ErrDenied
	}
	return bot.DeliveryGuard.AuthorizeInviteDelivery(ctx, id, message)
}
