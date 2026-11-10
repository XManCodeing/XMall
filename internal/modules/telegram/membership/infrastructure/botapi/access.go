package botapi

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

const accessDefaultTimeout = 10 * time.Second
const accessMaximumTimeout = 30 * time.Second

// Access keeps decrypted bot credentials inside infrastructure. Routes must
// re-resolve the current membership route; a database running flag alone never
// authorizes a request. Timeout bounds preflight and the single platform call.
type Access struct {
	Store                 r.Store
	Cipher                c.AccessCipher
	API                   r.MembershipAccessAPI
	Query                 r.MembershipQueryAPI
	Lifecycle             r.MembershipLifecycleAPI
	Routes                c.AccessRoutes
	Timeout               time.Duration
	recovery              *c.BotRouteRecoveryRequest
	recoveryAuthorizer    c.MembershipRecoveryAuthorizer
	groupTask, groupActor uint
	groupAuthorizer       c.GroupRecoveryAuthorization
}

var _ c.AccessPlatform = (*Access)(nil)

// CreateMembershipInvite creates exactly one approval-only invite for input.
// A malformed successful response remains unknown because creation may have run.
func (access *Access) CreateMembershipInvite(ctx context.Context, input c.AccessCreateInvite) c.AccessInviteResult {
	ctx, cancel := access.bounded(ctx)
	defer cancel()
	if access == nil || access.API == nil || !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 32 || !d.IsInviteTermValid(input.Term, input.ExpiresAt, time.Now()) {
		return c.AccessInviteResult{AccessAPIResult: accessResult(c.AccessFailed, "invalid_request")}
	}
	token, botID, result := access.prepare(ctx, input.Route)
	if result.Outcome != c.AccessSucceeded {
		return c.AccessInviteResult{AccessAPIResult: result}
	}
	// Expiry may have elapsed during route resolution; no request was sent yet.
	if !d.IsInviteTermValid(input.Term, input.ExpiresAt, time.Now()) {
		return c.AccessInviteResult{AccessAPIResult: accessResult(c.AccessFailed, "invalid_request")}
	}
	expireDate := input.ExpiresAt.Unix()
	if input.Term == d.InviteTermLongTerm {
		expireDate = 0
	}
	invite, err := access.API.CreateChatInviteLink(ctx, token, r.CreateChatInviteLinkRequest{
		ChatID: input.Route.ChatID, Name: input.Name, ExpireDate: expireDate,
	})
	result = classifyAccessError(err, false)
	if err == nil && (!validAccessLink(invite.InviteLink) || invite.Creator.ID != botID || !invite.CreatesJoinRequest || invite.IsRevoked || invite.IsPrimary || invite.MemberLimit != 0 || invite.ExpireDate != expireDate || (expireDate != 0 && invite.ExpireDate <= time.Now().Unix())) {
		result = accessResult(c.AccessUnknown, "invalid_response")
	}
	logAccessResult("create_invite", input.Route, result)
	response := c.AccessInviteResult{AccessAPIResult: result}
	if result.Outcome == c.AccessSucceeded {
		response.Link = invite.InviteLink
	}
	return response
}

// ApproveMembershipJoin approves input's trusted applicant once. Success does
// not imply a membership observation and never causes an automatic unban.
func (access *Access) ApproveMembershipJoin(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	return access.join(ctx, input, true)
}

// DeclineMembershipJoin declines input's trusted applicant once, returning only
// a fixed classification; uncertain rejection is never retried internally.
func (access *Access) DeclineMembershipJoin(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	return access.join(ctx, input, false)
}

// join checks input and a live fence before approving or declining. approve
// selects the sole external action; the result contains no upstream error text.
func (access *Access) join(ctx context.Context, input c.AccessJoinAction, approve bool) c.AccessAPIResult {
	ctx, cancel := access.bounded(ctx)
	defer cancel()
	if access == nil || access.API == nil || input.TelegramID <= 0 {
		return accessResult(c.AccessFailed, "invalid_request")
	}
	token, _, result := access.prepare(ctx, input.Route)
	if result.Outcome != c.AccessSucceeded {
		return result
	}
	operation := "decline_join"
	var err error
	if approve {
		operation = "approve_join"
		err = access.API.ApproveChatJoinRequest(ctx, token, input.Route.ChatID, input.TelegramID)
	} else {
		err = access.API.DeclineChatJoinRequest(ctx, token, input.Route.ChatID, input.TelegramID)
	}
	result = classifyAccessError(err, false)
	logAccessResult(operation, input.Route, result)
	return result
}

// RevokeMembershipInvite revokes link once on route. A matching revoked link
// owned by this bot is required for success; member queries cannot prove revoke.
func (access *Access) RevokeMembershipInvite(ctx context.Context, route c.AccessRoute, link string) c.AccessAPIResult {
	ctx, cancel := access.bounded(ctx)
	defer cancel()
	if access == nil || access.API == nil || !validAccessLink(link) {
		return accessResult(c.AccessFailed, "invalid_request")
	}
	token, botID, result := access.prepare(ctx, route)
	if result.Outcome != c.AccessSucceeded {
		return result
	}
	invite, err := access.API.RevokeChatInviteLink(ctx, token, route.ChatID, link)
	result = classifyAccessError(err, false)
	if err == nil && (invite.InviteLink != link || invite.Creator.ID != botID || !invite.IsRevoked) {
		result = accessResult(c.AccessUnknown, "invalid_response")
	}
	logAccessResult("revoke_invite", route, result)
	return result
}

// GetMembershipMember queries input's exact identity. Restricted membership
// uses IsMember; kicked is returned unchanged and never triggers unban.
func (access *Access) GetMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessMemberResult {
	ctx, cancel := access.bounded(ctx)
	defer cancel()
	if access == nil || access.Query == nil || input.TelegramID <= 0 {
		return c.AccessMemberResult{AccessAPIResult: accessResult(c.AccessFailed, "invalid_request")}
	}
	token, _, result := access.prepare(ctx, input.Route)
	if result.Outcome != c.AccessSucceeded {
		return c.AccessMemberResult{AccessAPIResult: result}
	}
	member, err := access.Query.GetChatMember(ctx, token, input.Route.ChatID, input.TelegramID)
	result = classifyAccessError(err, true)
	isMember, valid := accessMemberStatus(member.Status, member.IsMember)
	if err == nil && (member.User.ID != input.TelegramID || !valid) {
		result = accessResult(c.AccessRetryable, "invalid_response")
	}
	logAccessResult("get_member", input.Route, result)
	response := c.AccessMemberResult{AccessAPIResult: result}
	if result.Outcome == c.AccessSucceeded {
		response.Status, response.IsMember = member.Status, isMember
	}
	return response
}

// prepare validates route against fresh resolution, reads the current instance,
// decrypts its credential and checks the worker fence immediately before I/O.
// All failures here are known not to have executed the requested platform action.
func (access *Access) prepare(ctx context.Context, route c.AccessRoute) (string, int64, c.AccessAPIResult) {
	if access.groupAuthorizer != nil {
		return access.prepareGroupRecovery(ctx, route)
	}
	if access.recovery != nil {
		return access.prepareRecovery(ctx, route)
	}
	deny := func(class string) (string, int64, c.AccessAPIResult) {
		result := accessResult(c.AccessRetryable, class)
		logAccessResult("preflight", route, result)
		return "", 0, result
	}
	if access.Store == nil || access.Cipher == nil || access.Routes == nil {
		return deny("unavailable")
	}
	if route.TargetID == 0 || route.BotID == 0 || route.ChatID >= 0 || route.Generation == 0 || route.RouteVersion == 0 {
		return "", 0, accessResult(c.AccessFailed, "invalid_route")
	}
	// A resolver may probe Telegram; never enter it without worker ownership.
	if err := r.CheckExecutionFence(ctx, route.BotID, route.Generation); err != nil {
		return deny("execution_fence")
	}
	instance, err := access.Store.Get(ctx, route.BotID)
	if err != nil {
		return deny("instance_unavailable")
	}
	if instance == nil || instance.ID != route.BotID || instance.TelegramID <= 0 || instance.Generation != route.Generation || !instance.Enabled || instance.Mode != "internal" || instance.State != "running" {
		return deny("inactive_instance")
	}
	current, err := access.Routes.MembershipRoute(ctx, route.TargetID)
	if err != nil {
		return deny("route_unavailable")
	}
	if current != route {
		return deny("stale_route")
	}
	token, err := access.Cipher.Open(instance.TokenCipher)
	if err != nil || strings.TrimSpace(token) == "" {
		return deny("credential_unavailable")
	}
	if err := r.CheckExecutionFence(ctx, route.BotID, route.Generation); err != nil {
		return deny("execution_fence")
	}
	if ctx.Err() != nil {
		return deny("execution_fence")
	}
	return token, instance.TelegramID, accessResult(c.AccessSucceeded, "")
}

// bounded derives a deadline from ctx and the configured timeout, clamping it to
// the hard maximum. The caller must cancel the returned context after use.
func (access *Access) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := accessDefaultTimeout
	if access != nil && access.Timeout > 0 {
		timeout = access.Timeout
	}
	if timeout > accessMaximumTimeout {
		timeout = accessMaximumTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// classifyAccessError converts err to fixed safe outcomes. Uncertainty outranks
// rate limits and retry hints. readOnly permits safe retry of untyped query errors.
func classifyAccessError(err error, readOnly bool) c.AccessAPIResult {
	if err == nil {
		return accessResult(c.AccessSucceeded, "")
	}
	var platformError r.APIError
	if errors.As(err, &platformError) {
		if platformError.Uncertain() {
			return accessResult(c.AccessUnknown, "uncertain")
		}
		if platformError.StatusCode() == 429 {
			result := accessResult(c.AccessRetryable, "rate_limited")
			delay := platformError.RetrySeconds()
			if delay < 1 {
				delay = 5
			}
			if int64(delay) > int64((1<<63-1)/int64(time.Second)) {
				result.RetryAfter = time.Duration(1<<63 - 1)
			} else {
				result.RetryAfter = time.Duration(delay) * time.Second
			}
			return result
		}
		if platformError.StatusCode() >= 400 && platformError.StatusCode() < 500 {
			return accessResult(c.AccessFailed, "platform_rejected")
		}
		var classified interface{ Kind() string }
		if errors.As(err, &classified) {
			switch classified.Kind() {
			case "retryable":
				return accessResult(c.AccessRetryable, "platform_retryable")
			case "refused":
				return accessResult(c.AccessFailed, "platform_rejected")
			}
		}
	}
	if readOnly {
		return accessResult(c.AccessRetryable, "query_unavailable")
	}
	return accessResult(c.AccessUnknown, "uncertain")
}

// accessResult builds a result from internal constants, never platform text.
func accessResult(outcome, class string) c.AccessAPIResult {
	return c.AccessAPIResult{Outcome: outcome, ErrorClass: class}
}

// accessMemberStatus maps status and restricted's flag to membership and validity.
func accessMemberStatus(status string, restrictedMember bool) (bool, bool) {
	switch status {
	case "creator", "administrator", "member":
		return true, true
	case "restricted":
		return restrictedMember, true
	case "left", "kicked":
		return false, true
	default:
		return false, false
	}
}

// validAccessLink accepts only Telegram invite URLs, never help pages, user
// profiles or escaped/credential-bearing URLs. raw is never emitted to logs.
func validAccessLink(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "t.me" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	secret := ""
	switch {
	case strings.HasPrefix(parsed.Path, "/+"):
		secret = strings.TrimPrefix(parsed.Path, "/+")
	case strings.HasPrefix(parsed.Path, "/joinchat/"):
		secret = strings.TrimPrefix(parsed.Path, "/joinchat/")
	}
	if len(secret) < 8 || len(secret) > 128 {
		return false
	}
	for _, character := range secret {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

// logAccessResult records operation identity and fixed classifications only.
// route contains no invite/token; successful operations are debug-level events.
func logAccessResult(operation string, route c.AccessRoute, result c.AccessAPIResult) {
	attributes := []any{"operation", operation, "bot_id", route.BotID, "target_id", route.TargetID, "route_version", route.RouteVersion, "outcome", result.Outcome, "error_class", result.ErrorClass}
	if result.Outcome == c.AccessSucceeded {
		slog.Debug("telegram_membership_access", attributes...)
	} else {
		slog.Warn("telegram_membership_access", attributes...)
	}
}
