package contract

import "context"

// CreateChatInviteLinkRequest identifies one physical chat and an optional expiry
// (Unix seconds, zero means omitted). Name is limited to 32 Unicode characters.
// All links require approval; member_limit is deliberately not exposed.
type CreateChatInviteLinkRequest struct {
	ChatID     int64
	Name       string
	ExpireDate int64
}

// MembershipAccessAPI is an optional capability, not part of the base API.
// The caller owns lease renewal, generation/route/entitlement fencing before
// each call and persistence of its outcome. Implementations never retry calls.
// InviteLink contains a secret URL: persist protected and never log it.
type MembershipAccessAPI interface {
	// CreateChatInviteLink uses token and request to return a structured approval
	// link. An uncertain error must not trigger an unconditional create retry.
	CreateChatInviteLink(ctx context.Context, token string, request CreateChatInviteLinkRequest) (InviteLink, error)
	// RevokeChatInviteLink revokes this bot's link in chatID, returning its state.
	// Member lookup cannot establish whether an uncertain revocation succeeded.
	RevokeChatInviteLink(ctx context.Context, token string, chatID int64, inviteLink string) (InviteLink, error)
	// ApproveChatJoinRequest approves userID in chatID; nil confirms approval,
	// not a separately observed membership. Requires can_invite_users.
	ApproveChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error
	// DeclineChatJoinRequest declines userID in chatID; nil confirms rejection.
	// Member lookup cannot establish whether an uncertain rejection succeeded.
	DeclineChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error
}

// MembershipLifecycleAPI is optional: base runtimes need not implement removal.
// Calls never retry ambiguous effects; unban must always use only_if_banned=true.
type MembershipLifecycleAPI interface {
	// BanChatMember removes userID in chatID using token; Telegram may delete historical messages.
	BanChatMember(ctx context.Context, token string, chatID, userID int64) error
	// UnbanChatMember restores admission only when banned; it never removes a present member.
	UnbanChatMember(ctx context.Context, token string, chatID, userID int64) error
}

// MembershipQueryAPI is a separate read-only optional capability. Errors from
// both capabilities implement APIError and Kind() string: Uncertain takes
// precedence over retryability; 429 carries RetrySeconds. Raw errors are hidden.
type MembershipQueryAPI interface {
	// GetChatMember queries userID in chatID using token and returns the platform
	// status, including restricted.is_member. Requires admin for reliable lookup
	// of other users; it neither enumerates members nor proves invite outcomes.
	GetChatMember(ctx context.Context, token string, chatID, userID int64) (ChatMember, error)
}
