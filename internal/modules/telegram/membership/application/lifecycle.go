package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"log/slog"
	"time"
)

// Lifecycle owns per-group expiry execution using existing access work and runtime fences.
// Now is injectable for deterministic tests; production leaves it nil.
type Lifecycle struct {
	Store    c.LifecycleRepository
	Routes   c.AccessRoutes
	Platform c.LifecyclePlatform
	Now      func() time.Time
}

// NewLifecycle validates required ports, returning an error rather than silently skipping work.
// routes MUST validate fresh can_restrict_members as well as membership target/current bot;
// the B6a invite-only route resolver is insufficient unless its lifecycle adapter adds this check.
func NewLifecycle(store c.LifecycleRepository, routes c.AccessRoutes, platform c.LifecyclePlatform) (*Lifecycle, error) {
	if store == nil || routes == nil || platform == nil {
		return nil, c.ErrInvalid
	}
	return &Lifecycle{Store: store, Routes: routes, Platform: platform}, nil
}

// ExecuteRecoveryOperation reuses the lifecycle state machine for an already claimed
// task-owned cleanup; its repository must prove destination membership before declaring
// the historical route inactive. No global worker or runtime authorization is bypassed.
func (service *Lifecycle) ExecuteRecoveryOperation(ctx context.Context, operation *d.AccessOperation) error {
	if operation == nil || operation.ID == 0 || operation.State != "creating" {
		return c.ErrInvalid
	}
	return service.execute(ctx, operation)
}

// now returns the injectable UTC business observation time, without changing paid source times.
func (service *Lifecycle) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

// Scan advances one bounded durable member page for bot; it never calls Telegram or scans orders.
func (service *Lifecycle) Scan(ctx context.Context, bot uint, limit int) (int, error) {
	return service.Store.ScanLifecycle(ctx, bot, limit, service.now())
}

// Run consumes at most limit phases under the current runtime fence; no detached worker is started.
func (service *Lifecycle) Run(ctx context.Context, bot uint, limit int) (int, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	for completed := 0; completed < limit; completed++ {
		token := make([]byte, 24)
		if _, err := rand.Read(token); err != nil {
			return completed, err
		}
		operation, err := service.Store.ClaimLifecycle(ctx, bot, hex.EncodeToString(token), service.now(), accessLeaseDuration)
		if errors.Is(err, c.ErrNotFound) {
			return completed, nil
		}
		if err != nil {
			return completed, err
		}
		if err := service.execute(ctx, operation); err != nil {
			return completed, err
		}
	}
	return limit, nil
}

// Observe persists trusted member evidence using the existing source and watermark guards.
func (service *Lifecycle) Observe(ctx context.Context, input c.AccessObservation) (bool, error) {
	return service.Store.ObserveAccess(ctx, input)
}

// Recover accepts only authenticated admin commands; it cannot declare a platform ban successful.
func (service *Lifecycle) Recover(ctx context.Context, input c.LifecycleRecovery) error {
	return service.Store.RecoverLifecycle(ctx, input, service.now())
}

// Tasks reads bounded durable work for an authorized admin adapter, excluding invitation secrets.
func (service *Lifecycle) Tasks(ctx context.Context, bot, after uint, limit int) ([]c.LifecycleTask, error) {
	return service.Store.ListLifecycle(ctx, bot, after, limit)
}

// prepare re-resolves route capabilities and runtime generation, then renews the task CAS lease.
// Every network operation follows this method outside any database transaction.
func (service *Lifecycle) prepare(ctx context.Context, operation *d.AccessOperation) (c.AccessRoute, c.AccessEligibility, error) {
	route, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return route, c.AccessEligibility{}, err
	}
	if route.TargetID != operation.TargetID || !sameAccessRoute(route, operation.BotID, operation.ChatID, operation.RouteVersion) {
		return route, c.AccessEligibility{}, c.ErrConflict
	}
	if err := checkAccessFence(ctx, route); err != nil {
		return route, c.AccessEligibility{}, err
	}
	eligible, err := service.Store.LifecycleEligibility(ctx, operation, service.now())
	if err != nil {
		return route, eligible, err
	}
	operation.Generation, operation.EntitlementVersion = route.Generation, eligible.Version
	if err := service.Store.RenewAccess(ctx, operation, service.now(), accessLeaseDuration); err != nil {
		return route, eligible, err
	}
	latest, err := service.Routes.MembershipRoute(ctx, operation.TargetID)
	if err != nil {
		return route, eligible, err
	}
	if latest != route {
		return route, eligible, c.ErrConflict
	}
	return route, eligible, checkAccessFence(ctx, route)
}

// execute runs a single phase, preserving acknowledged ban provenance even if renewal races.
// Post-call ownership failure leaves a durable unknown/read-only recovery path, never repeats ban.
func (service *Lifecycle) execute(ctx context.Context, operation *d.AccessOperation) error {
	route, eligible, err := service.prepare(ctx, operation)
	if err != nil {
		result := lifecycleResult("retryable", operation.LifecyclePhase, "unavailable")
		if errors.Is(err, c.ErrConflict) || errors.Is(err, c.ErrUntrustedIdentity) {
			result = lifecycleResult("needs_review", operation.LifecyclePhase, "route_changed")
		}
		if errors.Is(err, c.ErrPaymentEvidence) {
			result = lifecycleResult("retryable", operation.LifecyclePhase, "payment_pending")
		}
		return service.finish(ctx, operation, result)
	}
	slog.InfoContext(ctx, "membership_lifecycle_started", "operation_id", operation.ID, "phase", operation.LifecyclePhase)
	if operation.LifecyclePhase == "ban" || operation.LifecyclePhase == "unban" {
		return service.effect(ctx, operation, route, eligible)
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	observedAt := service.now()
	member := service.Platform.GetMembershipMember(callCtx, c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID})
	cancel()
	if member.Outcome != c.AccessSucceeded {
		return service.finish(ctx, operation, lifecycleResult("retryable", operation.LifecyclePhase, "unavailable"))
	}
	latest, current, err := service.prepare(ctx, operation)
	if errors.Is(err, c.ErrPaymentEvidence) {
		return service.finish(ctx, operation, lifecycleResult("retryable", operation.LifecyclePhase, "payment_pending"))
	}
	if err != nil || latest != route {
		return service.finish(ctx, operation, lifecycleResult("needs_review", operation.LifecyclePhase, "route_changed"))
	}
	result := service.decide(operation, current, member)
	result.Member, result.ObservedAt = &member, observedAt
	return service.finish(ctx, operation, result)
}

// lifecycleResult creates a phase conclusion using fixed, secret-free review categories.
func lifecycleResult(state, phase, reason string) c.LifecycleResult {
	return c.LifecycleResult{AccessAPIResult: c.AccessAPIResult{Outcome: state, ErrorClass: reason}, Phase: phase, ReviewReason: reason}
}

// decide interprets actual member facts, never attributing an unknown kicked status to this task.
// Renewal only restores eligibility/reapplication; paid timestamps and entitlement durations stay untouched.
func (service *Lifecycle) decide(operation *d.AccessOperation, eligible c.AccessEligibility, member c.AccessMemberResult) c.LifecycleResult {
	if member.Status == "administrator" || member.Status == "creator" {
		return lifecycleResult("needs_review", operation.LifecyclePhase, "administrator_member")
	}
	if member.Status != "member" && member.Status != "restricted" && member.Status != "left" && member.Status != "kicked" {
		return lifecycleResult("needs_review", operation.LifecyclePhase, "invalid_response")
	}
	present := member.Status == "member" || member.Status == "restricted" && member.IsMember
	if operation.BanBlocked {
		return lifecycleResult("needs_review", operation.LifecyclePhase, "disciplinary_ban")
	}
	if operation.LifecyclePhase == "unknown_ban" {
		// Kicked is a present-state observation, not proof that our timed-out ban ran.
		return lifecycleResult("needs_review", "unknown_ban", "ban_attribution_unknown")
	}
	if member.Status == "kicked" {
		if operation.UnbanOutcome == c.AccessSucceeded {
			return lifecycleResult("needs_review", operation.LifecyclePhase, "external_ban")
		}
		if !operation.BanConfirmed {
			return lifecycleResult("needs_review", operation.LifecyclePhase, "external_ban")
		}
		if operation.LifecyclePhase == "unknown_unban" {
			return lifecycleResult("needs_review", "unknown_unban", "unban_unknown")
		}
		return lifecycleResult("pending", "unban", "")
	}
	if operation.LifecyclePhase == "unknown_unban" || operation.LifecyclePhase == "verify" || operation.BanConfirmed {
		reason := "removed"
		if eligible.Active {
			reason = "admission_restored"
		}
		if present && !eligible.Active {
			return lifecycleResult("needs_review", "verify", "member_without_entitlement")
		}
		return lifecycleResult("succeeded", "done", reason)
	}
	if eligible.Active {
		return lifecycleResult("succeeded", "done", "entitlement_active")
	}
	if !present {
		return lifecycleResult("succeeded", "not_required", "already_left")
	}
	return lifecycleResult("pending", "ban", "")
}

// effect rechecks membership immediately before writes, then checks route and entitlement again.
// unban is allowed only after an acknowledged own ban and always uses only_if_banned at the adapter.
func (service *Lifecycle) effect(ctx context.Context, operation *d.AccessOperation, route c.AccessRoute, eligible c.AccessEligibility) error {
	if operation.LifecyclePhase == "ban" && eligible.Active {
		return service.finish(ctx, operation, lifecycleResult("succeeded", "done", "entitlement_active"))
	}
	if operation.LifecyclePhase == "unban" && (!operation.BanConfirmed || operation.BanBlocked) {
		return service.finish(ctx, operation, lifecycleResult("needs_review", "unban", "ban_attribution_unknown"))
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	member := service.Platform.GetMembershipMember(callCtx, c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID})
	cancel()
	if member.Outcome != c.AccessSucceeded {
		return service.finish(ctx, operation, lifecycleResult("retryable", operation.LifecyclePhase, "unavailable"))
	}
	if member.Status == "administrator" || member.Status == "creator" {
		return service.finish(ctx, operation, lifecycleResult("needs_review", operation.LifecyclePhase, "administrator_member"))
	}
	if operation.LifecyclePhase == "ban" {
		if member.Status == "kicked" {
			return service.finish(ctx, operation, lifecycleResult("needs_review", "ban", "external_ban"))
		}
		if member.Status == "left" || member.Status == "restricted" && !member.IsMember {
			return service.finish(ctx, operation, lifecycleResult("succeeded", "done", "already_left"))
		}
		if member.Status != "member" && member.Status != "restricted" {
			return service.finish(ctx, operation, lifecycleResult("needs_review", "ban", "invalid_response"))
		}
	}
	if operation.LifecyclePhase == "unban" && member.Status != "kicked" {
		result := lifecycleResult("pending", "verify", "ban_no_longer_present")
		return service.finish(ctx, operation, result)
	}
	latest, current, err := service.prepare(ctx, operation)
	if errors.Is(err, c.ErrPaymentEvidence) {
		return service.finish(ctx, operation, lifecycleResult("retryable", operation.LifecyclePhase, "payment_pending"))
	}
	if err != nil || latest != route {
		return service.finish(ctx, operation, lifecycleResult("needs_review", operation.LifecyclePhase, "route_changed"))
	}
	if operation.LifecyclePhase == "ban" && current.Active {
		return service.finish(ctx, operation, lifecycleResult("succeeded", "done", "entitlement_active"))
	}
	callCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
	action := c.AccessJoinAction{Route: route, TelegramID: operation.TelegramID}
	var outcome c.AccessAPIResult
	if operation.LifecyclePhase == "ban" {
		outcome = service.Platform.BanMembershipMember(callCtx, action)
	} else {
		outcome = service.Platform.UnbanMembershipMember(callCtx, action)
	}
	cancel()
	result := lifecycleEffectResult(operation, outcome)
	afterRoute, _, afterErr := service.prepare(ctx, operation)
	if afterErr != nil || afterRoute != route {
		result.Outcome, result.ReviewReason = "needs_review", "route_or_lease_changed"
	}
	return service.finish(ctx, operation, result)
}

// lifecycleEffectResult maps explicit platform outcomes into separately persisted ban/unban steps.
// A successful ban always schedules only-if-banned unban, including expired users, for future renewals.
func lifecycleEffectResult(operation *d.AccessOperation, outcome c.AccessAPIResult) c.LifecycleResult {
	phase := operation.LifecyclePhase
	result := lifecycleResult("needs_review", phase, "platform_failed")
	result.AccessAPIResult = outcome
	switch outcome.Outcome {
	case c.AccessSucceeded:
		result.Outcome, result.ReviewReason = "pending", ""
		if phase == "ban" {
			result.Phase, result.BanConfirmed = "unban", true
		} else {
			result.Phase = "verify"
		}
	case c.AccessRetryable:
		result.Outcome = "retryable"
	case c.AccessFailed:
		result.Outcome = "needs_review"
	default:
		outcome.Outcome = "unknown"
		result.Outcome, result.Phase, result.ReviewReason = "pending", "unknown_"+phase, "result_unknown"
	}
	if phase == "ban" {
		result.BanOutcome = outcome.Outcome
	} else {
		result.UnbanOutcome = outcome.Outcome
	}
	return result
}

// finish records detached bounded evidence even after cancellation, without making further API calls.
func (service *Lifecycle) finish(ctx context.Context, operation *d.AccessOperation, result c.LifecycleResult) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := service.Store.FinishLifecycle(finishCtx, operation, result, service.now())
	slog.InfoContext(finishCtx, "membership_lifecycle_finished", "operation_id", operation.ID, "phase", result.Phase, "state", result.Outcome, "persisted", err == nil)
	return err
}
