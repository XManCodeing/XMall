package contract

import (
	"context"
	"errors"
)

// ErrCapabilityPending means authorization cannot yet be established. It never
// wraps ErrCapability: callers may retry but must not authorize the operation.
var ErrCapabilityPending = errors.New("telegram group capability check pending")

// CapabilityPending carries a safe retry delay without exposing API credentials
// or platform response text. RetryAfterSeconds is optional; zero uses caller policy.
type CapabilityPending struct {
	RetryAfterSeconds int
}

// Error returns the safe, stable description of the pending authorization.
func (e *CapabilityPending) Error() string { return ErrCapabilityPending.Error() }

// Unwrap identifies pending checks without classifying them as a permanent denial.
func (e *CapabilityPending) Unwrap() error { return ErrCapabilityPending }

// RetrySeconds returns the platform's retry delay in seconds, or zero if unknown.
func (e *CapabilityPending) RetrySeconds() int { return max(0, e.RetryAfterSeconds) }

// SupportMemberProbe checks a specific actor, never enumerates group members.
// Every current member is a support operator; no separate staff allowlist exists.
type SupportMemberProbe interface {
	SupportMember(context.Context, uint, int64, int64) (bool, error)
}

// SupportCapability classifies a validated target snapshot without authorizing it.
// view supplies cached evidence and identity supplies the live runtime generation.
// Temporary absence of evidence returns pending; known revocation remains denied.
func SupportCapability(view View, identity Identity) error {
	observation := view.Observation
	// Explicit negative evidence wins even when it is older than the freshness TTL.
	switch observation.State {
	case "left", "inaccessible", "missing_permissions", "authentication_error":
		return ErrCapability
	}
	switch observation.ErrorClass {
	case "authentication", "permission_denied", "resource_inaccessible", "anonymous_admin", "route_changed", "instance_stopped":
		return ErrCapability
	}
	if observation.MemberStatus == "left" || observation.MemberStatus == "kicked" {
		return ErrCapability
	}
	if observation.RouteVersion != 0 && observation.RouteVersion != view.RouteVersion {
		return ErrCapability
	}
	// Failed probes do not contain capability booleans. Do not mistake their
	// zero values for a successful observation of missing permissions.
	switch observation.ErrorClass {
	case "network", "rate_limited", "platform", "instance_unavailable", "generation_changed", "event_pending":
		return &CapabilityPending{RetryAfterSeconds: observation.RetryAfterSeconds}
	case "":
	default:
		return ErrCapability
	}
	if view.Checking || observation.State == "unknown" || observation.State == "rate_limited" {
		return &CapabilityPending{RetryAfterSeconds: observation.RetryAfterSeconds}
	}
	if observation.State != "ready" || len(view.MissingCapabilities) != 0 {
		return ErrCapability
	}
	if !view.Fresh || observation.Generation != identity.Generation || observation.BotTelegramID != identity.TelegramID {
		return ErrCapabilityPending
	}
	return nil
}

// SupportRoute is a fresh, generation-bound authorization snapshot, not a
// durable routing instruction. Re-resolve it immediately before external I/O.
type SupportRoute struct {
	TargetID     uint
	BotID        uint
	ChatID       int64
	RouteVersion uint64
	Generation   uint64
}
