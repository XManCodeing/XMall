package telegrammembershiprecovery

import (
	"context"
	"strconv"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/botapi"
	"github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// GroupAuthority reads real confirmed parent snapshots and rechecks current RBAC.
// Authorize must be the same trusted recovery action authorization used by HTTP.
type GroupAuthority struct {
	Parents   recovery.Store
	Authorize func(context.Context, uint, string) error
}

// Actor resolves the current system-continuation sponsor from the confirmed parent.
// It is NOT the administrator executing resume: RecoverRoute requires request.ActorID.
// Website/inbox actions use this sponsor only after checking their present RBAC again.
func (authority *GroupAuthority) Actor(ctx context.Context, task uint) (uint, error) {
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
	if err := authority.Authorize(ctx, actor, "resume"); err != nil {
		return 0, err
	}
	return actor, nil
}

// AuthorizeMembershipGroupRecovery validates exact parent/scope/route and current
// RBAC. Generation and group fence are independently checked by the member store.
func (authority *GroupAuthority) AuthorizeMembershipGroupRecovery(ctx context.Context, task, actor uint, route c.AccessRoute) error {
	if authority == nil || authority.Parents == nil || authority.Authorize == nil || actor == 0 {
		return c.ErrInvalid
	}
	if err := authority.Authorize(ctx, actor, "resume"); err != nil {
		return err
	}
	detail, err := authority.Parents.Get(ctx, task)
	if err != nil {
		return err
	}
	snapshot := detail.Snapshot
	state := detail.Task.State
	if detail.Task.Kind != d.GroupReplace || detail.Task.ConfirmedAt == nil || detail.Task.ConfirmedActorID == 0 || (state != d.Recovering && state != d.NeedsReview && state != d.PartialFailed) {
		return c.ErrConflict
	}
	old := route.ChatID == snapshot.Old.ChatID && route.RouteVersion == snapshot.ExpectedOld.Route
	current := route.ChatID == snapshot.New.ChatID && route.RouteVersion == snapshot.ExpectedOld.Route+1
	if route.TargetID != snapshot.Old.TargetID || route.TargetID != snapshot.New.TargetID || route.BotID != snapshot.Old.BotID || route.BotID != snapshot.New.BotID || route.Generation != snapshot.ExpectedNew.Generation || (!old && !current) {
		return c.ErrConflict
	}
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	if !contains(snapshot.Scope.TargetIDs, strconv.FormatUint(uint64(route.TargetID), 10)) || !contains(snapshot.Scope.BotIDs, strconv.FormatUint(uint64(route.BotID), 10)) || !contains(snapshot.Scope.ChatIDs, strconv.FormatInt(route.ChatID, 10)) {
		return c.ErrConflict
	}
	return nil
}

// NewGroup builds a real executable same-Bot different-chat recovery adapter. Wire
// its RecoverRoute into grouprecovery.Membership; Entry is authenticated website-only
// and ReceiveJoin must receive a trusted persisted inbox event, not public JSON.
func NewGroup(store *routerecovery.Store, access *botapi.Access, parents recovery.Store, authorize func(context.Context, uint, string) error, identity c.AccessAuthorizer) (*routerecovery.GroupRecovery, error) {
	if store == nil || access == nil || access.Cipher == nil || access.API == nil || access.Query == nil || access.Lifecycle == nil || parents == nil || authorize == nil || identity == nil {
		return nil, c.ErrInvalid
	}
	authority := &GroupAuthority{Parents: parents, Authorize: authorize}
	return &routerecovery.GroupRecovery{Store: store, Cipher: access.Cipher, Platforms: &botapi.RecoveryFactory{Access: access}, Authority: authority, Actor: authority.Actor, Authorizer: identity}, nil
}
