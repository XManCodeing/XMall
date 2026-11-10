package contract

import "context"

// GroupRecoveryRequest binds module recovery to the owning task and both physical routes.
// Implementations must preserve original order identities and entitlement expiry; route
// replacement never grants a new term or manufactures successful old-route cleanup.
type GroupRecoveryRequest struct {
	TaskID          uint
	ActorID         uint // Trusted backend administrator for this execution attempt.
	TargetID        uint
	BotID           uint
	OldChatID       int64
	NewChatID       int64
	OldRouteVersion uint64
	NewRouteVersion uint64
	Generation      uint64
	Purpose         string
}

// GroupRecoveryResult is explicit completion evidence, not an optimistic work count.
// Verified means all source records were enumerated under the recovery fence and every
// required destination mapping/access operation is durable. Remaining and Unknown include
// old-route invitations, deliveries and cleanup; zero work alone is never proof.
// Support must verify topic mappings and buffered deliveries; membership must verify
// original entitlement/order identity and expiry, plus old-route access liabilities.
type GroupRecoveryResult struct {
	TaskID       uint
	TargetID     uint
	RouteVersion uint64
	Verified     bool
	// EntryReady proves only a fenced membership entry switch, not old-route cleanup.
	// Remaining retains historical liabilities; Unknown must still be zero for release.
	EntryReady bool
	// Checkpointed certifies that the bounded attempt and all external outcomes
	// are durable. With Unknown == 0, Remaining work may be resumed safely.
	// It never grants release and must be false for unrecorded/ambiguous calls.
	Checkpointed bool
	Remaining    uint64
	Unknown      uint64
}

// GroupRecoverer is the narrow, optional module-specific adapter. RecoverGroup executes
// outside recovery-store transactions and must use TaskID as a durable idempotency key.
// Missing adapters must fail closed. Any error may have side effects and is never retried
// automatically; unknown outcomes require explicit reconciliation, not another invocation.
type GroupRecoverer interface {
	RecoverGroup(context.Context, GroupRecoveryRequest) (GroupRecoveryResult, error)
}
