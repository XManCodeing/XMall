// Package telegrammembershiprecovery assembles only task-bound membership recovery.
package telegrammembershiprecovery

import (
	"context"
	"fmt"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	domain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"strconv"

	bot "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/botapi"
	"github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
)

// New builds the cross-Bot recovery port from existing access platform dependencies.
// store must be registered/migrated by bootstrap; access retains its ordinary behavior.
// The returned port neither releases runtime fences nor starts business workers.
func New(store *routerecovery.Store, access *botapi.Access) (c.BotRouteRecoverer, error) {
	if store == nil || access == nil || access.Store == nil || access.Cipher == nil || access.API == nil || access.Query == nil {
		return nil, fmt.Errorf("membership recovery dependencies: %w", c.ErrInvalid)
	}
	return &routerecovery.BotRecovery{Store: store, Cipher: access.Cipher, Platforms: &botapi.RecoveryFactory{Access: access, Authorizer: store}}, nil
}

// Modules supplies ONLY membership evidence; callers must aggregate an independent
// support adapter. An empty current membership scope still checks persisted history.
type Modules struct {
	Store    *routerecovery.Store
	Recovery c.BotRouteRecoverer
}

// ContinueBot validates all snapshot current/history routes before executing membership
// work, then persists and returns one membership-only proof for parent aggregation.
func (modules *Modules) ContinueBot(ctx context.Context, request bot.ModuleRequest) ([]bot.ModuleProof, error) {
	if modules == nil || modules.Store == nil || modules.Recovery == nil || request.TaskID == 0 || request.ActorID == 0 {
		return nil, c.ErrInvalid
	}
	snapshot := request.Snapshot
	identity := c.BotRouteRecoveryRequest{ActorID: request.ActorID, TaskID: request.TaskID, OldBotID: snapshot.Old.BotID, NewBotID: snapshot.New.BotID, OldVersion: snapshot.ExpectedOld.Config, NewVersion: snapshot.ExpectedNew.Config, OldGeneration: snapshot.ExpectedOld.Generation, NewGeneration: snapshot.ExpectedNew.Generation}
	inventory, err := BotInventory(snapshot, identity)
	if err != nil {
		return nil, err
	}
	if err := modules.Store.CheckBotInventory(ctx, inventory); err != nil {
		return nil, err
	}
	for _, route := range inventory.Current {
		if route.Purpose != "membership" {
			continue
		}
		input := identity
		input.TargetID, input.OldChatID, input.NewChatID = route.TargetID, route.ChatID, route.ChatID
		if route.NewChatID != 0 {
			input.NewChatID = route.NewChatID
		}
		input.OldRouteVersion, input.NewRouteVersion = route.RouteVersion, route.RouteVersion+1
		if _, err := modules.Recovery.RecoverBotRoute(ctx, input); err != nil {
			return nil, err
		}
	}
	result, err := modules.Store.CompleteBotInventory(ctx, inventory)
	if err != nil {
		return nil, err
	}
	return []bot.ModuleProof{{Module: "membership", Receipt: result.CompletionReceipt, LocalComplete: result.Complete, Remaining: result.Remaining, Unknown: result.Unknown}}, nil
}

// NewBotAdmission adds current parent/RBAC authorization to dual-fenced admission.
// Callers keep New for legacy same-chat inventory; no global runtime is unlocked.
func NewBotAdmission(store *routerecovery.Store, access *botapi.Access, parents recovery.Store, authorize func(context.Context, uint, string) error, identity c.AccessAuthorizer) (*routerecovery.BotRecovery, error) {
	if store == nil || access == nil || access.Cipher == nil || access.API == nil || access.Query == nil || access.Lifecycle == nil || parents == nil || authorize == nil || identity == nil {
		return nil, c.ErrInvalid
	}
	authority := &BotAuthority{Store: store, Parents: parents, Authorize: authorize}
	factory := &botapi.RecoveryFactory{Access: access, Authorizer: authority}
	return &routerecovery.BotRecovery{Store: store, Cipher: access.Cipher, Platforms: factory, Admission: factory, Authority: authority, Authorizer: identity, Sponsor: authority.Sponsor}, nil
}

// BotAuthority validates current actor permissions and immutable route/scope snapshots.
type BotAuthority struct {
	Store     *routerecovery.Store
	Parents   recovery.Store
	Authorize func(context.Context, uint, string) error
}

// Sponsor resolves only system website authorization, never an administrator resume.
func (authority *BotAuthority) Sponsor(ctx context.Context, task uint) (uint, error) {
	if authority == nil || authority.Parents == nil || authority.Authorize == nil {
		return 0, c.ErrInvalid
	}
	detail, err := authority.Parents.Get(ctx, task)
	if err != nil {
		return 0, err
	}
	actor := detail.Task.ConfirmedActorID
	if actor == 0 {
		return 0, c.ErrInvalid
	}
	return actor, authority.Authorize(ctx, actor, "resume")
}

// AuthorizeMembershipRecovery checks both frozen runtime identities and full persisted
// parent mapping. Per-route destinations are authoritative; matching legacy endpoints
// remain compatible but conflicting immutable declarations are rejected.
func (authority *BotAuthority) AuthorizeMembershipRecovery(ctx context.Context, input c.BotRouteRecoveryRequest, route c.AccessRoute) error {
	if authority == nil || authority.Store == nil || authority.Parents == nil || authority.Authorize == nil || input.ActorID == 0 {
		return c.ErrInvalid
	}
	if err := authority.Authorize(ctx, input.ActorID, "resume"); err != nil {
		return err
	}
	detail, err := authority.Parents.Get(ctx, input.TaskID)
	if err != nil {
		return err
	}
	snapshot := detail.Snapshot
	if detail.Task.Kind != domain.BotReplace || detail.Task.ConfirmedAt == nil || detail.Task.ConfirmedActorID == 0 || (detail.Task.State != domain.Recovering && detail.Task.State != domain.NeedsReview && detail.Task.State != domain.PartialFailed) {
		return c.ErrConflict
	}
	if input.OldBotID != snapshot.Old.BotID || input.NewBotID != snapshot.New.BotID || input.OldVersion != snapshot.ExpectedOld.Config || input.NewVersion != snapshot.ExpectedNew.Config || input.OldGeneration != snapshot.ExpectedOld.Generation || input.NewGeneration != snapshot.ExpectedNew.Generation {
		return c.ErrConflict
	}
	expected, err := ResolveBotRoute(snapshot, input.TargetID)
	if err != nil {
		return err
	}
	if expected.Purpose != "membership" || input.OldChatID != expected.ChatID || input.NewChatID != expected.DestinationChat() || input.OldRouteVersion != expected.RouteVersion || input.NewRouteVersion != expected.RouteVersion+1 {
		return c.ErrConflict
	}
	contains := func(values []string, id string) bool {
		for _, value := range values {
			if value == id {
				return true
			}
		}
		return false
	}
	if !contains(snapshot.Scope.TargetIDs, strconv.FormatUint(uint64(input.TargetID), 10)) || !contains(snapshot.Scope.BotIDs, strconv.FormatUint(uint64(input.OldBotID), 10)) || !contains(snapshot.Scope.BotIDs, strconv.FormatUint(uint64(input.NewBotID), 10)) || !contains(snapshot.Scope.ChatIDs, strconv.FormatInt(input.OldChatID, 10)) || !contains(snapshot.Scope.ChatIDs, strconv.FormatInt(input.NewChatID, 10)) {
		return c.ErrConflict
	}
	inventory, err := BotInventory(snapshot, input)
	if err != nil {
		return err
	}
	if err := authority.Store.CheckBotInventory(ctx, inventory); err != nil {
		return err
	}
	return authority.Store.AuthorizeMembershipRecovery(ctx, input, route)
}

// ResolveBotRoute resolves one immutable target mapping, never a mutable group row.
// Explicit per-route destinations win only when any legacy endpoint agrees; an absent
// destination keeps the old chat except for a complete, matching legacy endpoint.
func ResolveBotRoute(snapshot domain.Snapshot, target uint) (domain.BotRoute, error) {
	var out domain.BotRoute
	if target == 0 {
		return out, c.ErrInvalid
	}
	count := 0
	for _, route := range snapshot.BotRoutes {
		if route.TargetID == target {
			out = route
			count++
		}
	}
	if count != 1 || out.ChatID >= 0 || out.RouteVersion == 0 || out.NewChatID > 0 {
		return domain.BotRoute{}, c.ErrConflict
	}
	if snapshot.Old.TargetID != 0 || snapshot.New.TargetID != 0 {
		if snapshot.Old.TargetID == 0 || snapshot.Old.TargetID != snapshot.New.TargetID || snapshot.Old.ChatID >= 0 || snapshot.New.ChatID >= 0 {
			return domain.BotRoute{}, c.ErrConflict
		}
		if snapshot.Old.TargetID == target {
			if snapshot.Old.ChatID != out.ChatID || (out.NewChatID != 0 && out.NewChatID != snapshot.New.ChatID) {
				return domain.BotRoute{}, c.ErrConflict
			}
			out.NewChatID = snapshot.New.ChatID
		}
	}
	return out, nil
}

// BotInventory converts every current and historical parent route without dropping
// non-membership scope. Canonical zero keeps legacy same-chat receipt hashes stable.
func BotInventory(snapshot domain.Snapshot, identity c.BotRouteRecoveryRequest) (c.BotRecoveryInventory, error) {
	out := c.BotRecoveryInventory{Identity: identity}
	destinations := map[int64]bool{}
	legacyFound := snapshot.Old.TargetID == 0 && snapshot.New.TargetID == 0
	for _, item := range snapshot.BotRoutes {
		route, err := ResolveBotRoute(snapshot, item.TargetID)
		if err != nil {
			return out, err
		}
		if route.TargetID == snapshot.Old.TargetID {
			legacyFound = true
		}
		destination := route.DestinationChat()
		if destinations[destination] || route.Purpose != "membership" && destination != route.ChatID {
			return out, c.ErrConflict
		}
		destinations[destination] = true
		if destination == route.ChatID {
			destination = 0
		}
		out.Current = append(out.Current, c.BotRecoveryRoute{TargetID: route.TargetID, ChatID: route.ChatID, NewChatID: destination, RouteVersion: route.RouteVersion, Purpose: route.Purpose})
	}
	if !legacyFound {
		return out, c.ErrConflict
	}
	for _, route := range snapshot.BotHistory {
		if route.NewChatID != 0 && route.NewChatID != route.ChatID {
			return out, c.ErrConflict
		}
		out.History = append(out.History, c.BotRecoveryRoute{TargetID: route.TargetID, ChatID: route.ChatID, RouteVersion: route.RouteVersion, Purpose: route.Purpose})
	}
	return out, nil
}

var _ bot.Modules = (*Modules)(nil)
