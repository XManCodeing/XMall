package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"time"
)

// LifecyclePlatform executes only scoped membership expiry operations outside transactions.
// Implementations must check the runtime fence; unknown bans must never be blindly retried.
type LifecyclePlatform interface {
	GetMembershipMember(context.Context, AccessJoinAction) AccessMemberResult
	BanMembershipMember(context.Context, AccessJoinAction) AccessAPIResult
	// UnbanMembershipMember MUST send only_if_banned=true and never remove a current member.
	UnbanMembershipMember(context.Context, AccessJoinAction) AccessAPIResult
}

// LifecycleRepository reuses access operations, member observations and ledger transactions.
// All identities are historical member IDs, not the account's current binding.
type LifecycleRepository interface {
	ScanLifecycle(context.Context, uint, int, time.Time) (int, error)
	ClaimLifecycle(context.Context, uint, string, time.Time, time.Duration) (*d.AccessOperation, error)
	RenewAccess(context.Context, *d.AccessOperation, time.Time, time.Duration) error
	LifecycleEligibility(context.Context, *d.AccessOperation, time.Time) (AccessEligibility, error)
	FinishLifecycle(context.Context, *d.AccessOperation, LifecycleResult, time.Time) error
	ListLifecycle(context.Context, uint, uint, int) ([]LifecycleTask, error)
	RecoverLifecycle(context.Context, LifecycleRecovery, time.Time) error
	ObserveAccess(context.Context, AccessObservation) (bool, error)
}

// LifecycleResult records a single phase without claiming ban success from kicked observations.
// BanConfirmed is set only for an acknowledged ban response from this operation.
type LifecycleResult struct {
	AccessAPIResult
	Phase        string
	BanOutcome   string
	UnbanOutcome string
	BanConfirmed bool
	ReviewReason string
	Member       *AccessMemberResult
	ObservedAt   time.Time
}

// LifecycleTask is the authorized admin read model, excluding leases and secrets.
type LifecycleTask struct {
	CleanupCycleID         uint       `json:"cleanup_cycle_id,string"`
	MemberOutcome          string     `json:"member_outcome" gorm:"-"`
	InviteCleanup          string     `json:"invite_cleanup" gorm:"-"`
	InviteCleanupTotal     uint       `json:"invite_cleanup_total" gorm:"-"`
	InviteCleanupCompleted uint       `json:"invite_cleanup_completed" gorm:"-"`
	InviteCleanupFailed    uint       `json:"invite_cleanup_failed" gorm:"-"`
	InviteCleanupUnknown   uint       `json:"invite_cleanup_unknown" gorm:"-"`
	ID                     uint       `json:"id,string"`
	TargetID               uint       `json:"target_id,string"`
	BotID                  uint       `json:"bot_id,string"`
	ChatID                 int64      `json:"chat_id,string"`
	TelegramID             int64      `json:"telegram_id,string"`
	RouteVersion           uint64     `json:"route_version,string"`
	EntitlementVersion     uint64     `json:"entitlement_version,string"`
	Version                uint64     `json:"version,string"`
	State                  string     `json:"state"`
	LifecyclePhase         string     `json:"phase"`
	BanOutcome             string     `json:"ban_outcome"`
	UnbanOutcome           string     `json:"unban_outcome"`
	BanConfirmed           bool       `json:"ban_confirmed"`
	BanBlocked             bool       `json:"ban_blocked"`
	ReviewReason           string     `json:"review_reason"`
	ErrorClass             string     `json:"error_class"`
	MemberStatus           string     `json:"member_status"`
	MemberIsMember         bool       `json:"member_is_member"`
	ObservedAt             time.Time  `json:"observed_at"`
	NextAttemptAt          time.Time  `json:"next_attempt_at"`
	Attempt                uint       `json:"attempt"`
	LastAttemptAt          *time.Time `json:"last_attempt_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// LifecycleRecovery is an authenticated admin command. Recheck only reads platform state;
// retry is allowed only for definite failures, never unknown bans. Block-ban prevents auto unban.
type LifecycleRecovery struct {
	BotID           uint
	ID              uint
	ExpectedVersion uint64
	ActorAdminID    uint
	Action          string // recheck, retry, block_ban
}

// BotRouteRecoveryRequest binds a stable target to explicit old/new runtime fences.
// Same-chat replacement is valid only when the two Bot identities differ.
type BotRouteRecoveryRequest struct {
	ActorID                          uint `json:",omitempty"` // Current authenticated execution actor; zero is reserved for sponsor-bound website reads.
	TaskID, TargetID                 uint
	OldBotID, NewBotID               uint
	OldChatID, NewChatID             int64
	OldRouteVersion, NewRouteVersion uint64
	OldVersion, NewVersion           uint64
	OldGeneration, NewGeneration     uint64
}

// BotRouteRecoveryResult is a durable target receipt, never a scan-only success.
// Remaining/Unknown retain both fences. CompletionReceipt is required for release.
type BotRouteRecoveryResult struct {
	TaskID, TargetID   uint
	RouteVersion       uint64
	Remaining, Unknown uint64
	Complete           bool
	CompletionReceipt  string
}

// BotRouteRecoverer performs resumable membership recovery without changing ledger
// sources, expiry, orders, contacts or ordinary execution authorization.
type BotRouteRecoverer interface {
	RecoverBotRoute(context.Context, BotRouteRecoveryRequest) (BotRouteRecoveryResult, error)
}

// RecoveryAccessPlatform deliberately excludes creation, admission and bans.
// Unavailable old authority leaves liabilities, never fabricated cleanup.
type RecoveryAccessPlatform interface {
	RevokeMembershipInvite(context.Context, AccessRoute, string) AccessAPIResult
	GetMembershipMember(context.Context, AccessJoinAction) AccessMemberResult
}

// MembershipRecoveryAuthorizer revalidates both task-owned durable runtime fences,
// all real generation exits and exact old/new routes. It never enables workers.
type MembershipRecoveryAuthorizer interface {
	AuthorizeMembershipRecovery(context.Context, BotRouteRecoveryRequest, AccessRoute) error
}

// RecoveryAdmissionFactory creates task-bound access/lifecycle capabilities without
// starting either runtime; implementations reject approvals without a trusted source.
type RecoveryAdmissionFactory interface {
	MembershipBotAdmission(BotRouteRecoveryRequest, MembershipRecoveryAuthorizer) (AccessPlatform, LifecyclePlatform)
}

// RecoveryPlatformFactory binds every platform call to the immutable task snapshot.
type RecoveryPlatformFactory interface {
	MembershipRecoveryPlatform(BotRouteRecoveryRequest) RecoveryAccessPlatform
}

// GroupRecoveryAuthorization is supplied by bootstrap after loading the real parent,
// verifying live actor RBAC and exact scope/snapshot. Each platform call rechecks it.
type GroupRecoveryAuthorization interface {
	AuthorizeMembershipGroupRecovery(context.Context, uint, uint, AccessRoute) error
}

// GroupRecoveryPlatformFactory provides ordinary validated API implementations only
// behind a task-bound parent authorizer; it never releases group or runtime fences.
type GroupRecoveryPlatformFactory interface {
	MembershipGroupPlatform(uint, uint, GroupRecoveryAuthorization) (AccessPlatform, LifecyclePlatform)
}

// BotRecoveryRoute is the complete immutable current/history inventory, not just
// membership targets. History purpose may be empty and is resolved from stored target.
type BotRecoveryRoute struct {
	NewChatID    int64 `json:",omitempty"` // Zero preserves legacy same-chat semantics; server supplies immutable parent destination.
	TargetID     uint
	ChatID       int64
	RouteVersion uint64
	Purpose      string
}

// BotRecoveryInventory binds all routes and historical routes to the dual runtime snapshot.
type BotRecoveryInventory struct {
	Identity         BotRouteRecoveryRequest
	Current, History []BotRecoveryRoute
}
